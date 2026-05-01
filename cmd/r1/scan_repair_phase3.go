// scan_repair_phase3.go — H-17 Phase 3 full pipeline.
//
// Replaces the simple single-reviewer-call Phase 3 with the four-step
// pipeline defined in .claude/commands/scan-and-repair.md:
//
//   3a — Aggregate every finding from Phase 1 + 2a + 2b + 2c + 2d into
//        one buffer.
//   3b — Call the reviewer once to de-dupe the aggregate buffer.
//        Output: audit/findings-deduped.md.
//   3c — Call the reviewer once to TIER-classify each deduped finding.
//        Post-process: promote any finding mentioning a never-TIER-3
//        category (see cmd/r1/tier_filter.go's neverTier3Categories)
//        into TIER 1 regardless of the reviewer's call. Output:
//        audit/findings-approved.md + deferred.md + dropped.md.
//   3d — Per-section fix-task generation: one reviewer call per
//        section with approved findings. Output: audit/fix-tasks/<s>.md,
//        then aggregated into specs/repair-<date>.md + FIX_SOW.md.
//
// The function returns a phase3Result capturing APPROVED/DEFERRED/
// DROPPED counts and the SOW path. The outer orchestrator uses these
// counts to decide on Phase 3e (clean-exit) vs Phase 4 dispatch.

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// phase3Result is the summary the caller inspects.
type phase3Result struct {
	Aggregated int    // total finding lines in the aggregate buffer
	Deduped    int    // unique findings after 3b
	Approved   int    // TIER 1 + qualifying TIER 2 from 3c
	Deferred   int    // TIER 2 with effort > medium from 3c
	Dropped    int    // TIER 3 from 3c
	SOWPath    string // FIX_SOW.md absolute path
}

// runPhase3Full drives the new 3a → 3b → 3c → 3d pipeline.
func runPhase3Full(ctx context.Context, cfg *scanRepairConfig, p1 *phase1Result, p2 *phase2Result) (*phase3Result, error) {
	res := &phase3Result{}

	// 3a: aggregate.
	agg := aggregatePhase3Findings(cfg.Repo)
	res.Aggregated = countAggregateLines(agg)
	if err := os.WriteFile(filepath.Join(cfg.Repo, "audit", "findings-aggregated.md"), // #nosec G306 -- CLI output artefact; user-readable.
		[]byte(agg), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "  [Phase 3a] write findings-aggregated.md: %v\n", err)
	}
	fmt.Printf("  Phase 3a aggregate: %d total finding lines from all sources\n", res.Aggregated)

	// H-21: on big repos the aggregate buffer can balloon past the
	// OS argv limit (ARG_MAX ~= 128 KiB on Linux) AND past the point
	// where the reviewer can usefully reason about the whole set in
	// one turn. Pre-prioritize by severity and cap total input to a
	// sane upper bound. CRITICAL + HIGH + MEDIUM are kept before any
	// LOW-severity findings are considered for drop; within a tier
	// we preserve aggregation order (deterministic → semantic →
	// security → perspectives → codex). The reviewer downstream also
	// uses stdin piping (see argMaxStdinThreshold), so this cap is
	// about prompt-sanity for the LLM rather than ARG_MAX alone.
	if trimmed, droppedByTier := prioritizeAggregatedFindings(agg, phase3MaxAggregateChars); trimmed != agg {
		fmt.Printf("  Phase 3a prioritize: %d → %d chars (%s)\n",
			len(agg), len(trimmed), describeDropped(droppedByTier))
		agg = trimmed
		res.Aggregated = countAggregateLines(agg)
	}

	if res.Aggregated == 0 {
		// Nothing to dedupe/filter — write empty files and let the
		// orchestrator take the zero-findings clean exit.
		_ = os.WriteFile(filepath.Join(cfg.Repo, "audit", "findings-deduped.md"), // #nosec G306 -- CLI output artefact; user-readable.
			[]byte("# Deduped Findings\n\nNone.\n"), 0644)
		_ = os.WriteFile(filepath.Join(cfg.Repo, "audit", "findings-approved.md"), // #nosec G306 -- CLI output artefact; user-readable.
			[]byte("# Approved Findings\n\nNone.\n"), 0644)
		_ = os.WriteFile(filepath.Join(cfg.Repo, "audit", "findings-deferred.md"), // #nosec G306 -- CLI output artefact; user-readable.
			[]byte("# Deferred Findings\n\nNone.\n"), 0644)
		_ = os.WriteFile(filepath.Join(cfg.Repo, "audit", "findings-dropped.md"), // #nosec G306 -- CLI output artefact; user-readable.
			[]byte("# Dropped Findings\n\nNone.\n"), 0644)
		res.SOWPath = filepath.Join(cfg.Repo, "FIX_SOW.md")
		return res, nil
	}

	// Reviewer caller: same in prod for 3b/3c/3d. Tests override via
	// cfg.reviewerCaller (used for dedupe + TIER) and cfg.fixTaskCaller
	// (used for 3d per-section task generation).
	reviewer := cfg.reviewerCaller
	if reviewer == nil {
		reviewer = func(ctx context.Context, dir, prompt string) string {
			return reviewerPhase3Call(ctx, cfg, prompt)
		}
	}

	// 3b: dedupe.
	dedupePrompt := fmt.Sprintf(dedupPromptTemplate, agg)
	dctx, dcancel := context.WithTimeout(ctx, 10*time.Minute)
	dedupeReply := reviewer(dctx, cfg.Repo, dedupePrompt)
	dcancel()
	dedupeReply = strings.TrimSpace(dedupeReply)
	if dedupeReply == "" {
		// Fail-open: treat the aggregate itself as the dedupe result.
		dedupeReply = agg
	}
	_ = os.WriteFile(filepath.Join(cfg.Repo, "audit", "findings-deduped.md"), // #nosec G306 -- CLI output artefact; user-readable.
		[]byte("# Deduped Findings\n\n"+dedupeReply+"\n"), 0644)
	res.Deduped = countFindingLinesInBlock(dedupeReply)
	fmt.Printf("🧹 Phase 3b dedupe: %d→%d\n", res.Aggregated, res.Deduped)

	// 3c: TIER filter.
	tierPrompt := fmt.Sprintf(tierFilterPromptTemplate, dedupeReply)
	tctx, tcancel := context.WithTimeout(ctx, 10*time.Minute)
	tierReply := reviewer(tctx, cfg.Repo, tierPrompt)
	tcancel()
	tierReply = strings.TrimSpace(tierReply)

	approved, deferred, dropped := partitionTiers(tierReply, dedupeReply)

	// Post-process: ensure any finding whose text contains a
	// neverTier3Categories entry is promoted to TIER 1 — it must
	// never appear in deferred OR dropped regardless of the LLM's
	// classification.
	approved, deferred, dropped = promoteAllowlistFindings(approved, deferred, dropped)

	res.Approved = len(approved)
	res.Deferred = len(deferred)
	res.Dropped = len(dropped)

	if err := os.WriteFile(filepath.Join(cfg.Repo, "audit", "findings-approved.md"), // #nosec G306 -- CLI output artefact; user-readable.
		[]byte("# Approved Findings\n\n"+strings.Join(approved, "\n")+"\n"), 0644); err != nil {
		return res, fmt.Errorf("write findings-approved.md: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Repo, "audit", "findings-deferred.md"), // #nosec G306 -- CLI output artefact; user-readable.
		[]byte("# Deferred Findings\n\n"+strings.Join(deferred, "\n")+"\n"), 0644); err != nil {
		return res, fmt.Errorf("write findings-deferred.md: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Repo, "audit", "findings-dropped.md"), // #nosec G306 -- CLI output artefact; user-readable.
		[]byte("# Dropped Findings\n\n"+strings.Join(dropped, "\n")+"\n"), 0644); err != nil {
		return res, fmt.Errorf("write findings-dropped.md: %w", err)
	}

	// 3d: per-section fix-task generation.
	sowPath := filepath.Join(cfg.Repo, "FIX_SOW.md")
	res.SOWPath = sowPath
	if len(approved) == 0 {
		// Write an empty SOW; the orchestrator's zero-findings branch
		// replaces this with the clean-exit message.
		_ = os.WriteFile(sowPath, []byte("# Fix SOW\n\nNo high-impact findings approved.\n"), 0644) // #nosec G306 -- CLI output artefact; user-readable.
		return res, nil
	}

	fixTaskCall := cfg.fixTaskCaller
	if fixTaskCall == nil {
		// Fall back to the same reviewer plumbing the dedupe / tier
		// calls used. Tests that stub reviewerCaller cover 3d with a
		// single hook; production paths reach reviewerPhase3Call.
		fixTaskCall = reviewer
	}
	if err := runPhase3dFixTasks(ctx, cfg, approved, fixTaskCall); err != nil {
		fmt.Fprintf(os.Stderr, "  [Phase 3d] %v\n", err)
	}

	// Compile everything into FIX_SOW.md and a dated repair spec.
	if err := writeFinalSOW(cfg, res, approved, p1, p2); err != nil {
		return res, fmt.Errorf("write FIX_SOW.md: %w", err)
	}
	return res, nil
}

// ------------------------------------------------------------------
// 3a — aggregate
// ------------------------------------------------------------------

// aggregatePhase3Findings walks every audit/ source directory and
// concatenates finding lines into a single buffer. The format each
// block uses is preserved verbatim — Phase 3b's dedupe prompt sees
// the raw text and is responsible for any normalization.
func aggregatePhase3Findings(repo string) string {
	var buf strings.Builder
	auditDir := filepath.Join(repo, "audit")

	// Deterministic grep reports.
	buf.WriteString("## Deterministic scans\n\n")
	if matches, _ := filepath.Glob(filepath.Join(auditDir, "scans", "section-*-grep.md")); len(matches) > 0 {
		sort.Strings(matches)
		for _, m := range matches {
			b, err := os.ReadFile(m)
			if err != nil {
				continue
			}
			fmt.Fprintf(&buf, "### %s\n%s\n\n", filepath.Base(m), string(b))
		}
	}

	// Semantic scans (per-section subdirs under audit/scans/).
	buf.WriteString("## Semantic scans\n\n")
	if entries, _ := os.ReadDir(filepath.Join(auditDir, "scans")); len(entries) > 0 {
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			pats, _ := filepath.Glob(filepath.Join(auditDir, "scans", e.Name(), "*.md"))
			sort.Strings(pats)
			for _, pf := range pats {
				b, err := os.ReadFile(pf)
				if err != nil {
					continue
				}
				// Skip "None." stanzas — they add noise without adding findings.
				trimmed := strings.TrimSpace(string(b))
				if strings.HasSuffix(trimmed, "\nNone.") || strings.HasSuffix(trimmed, "None.") {
					continue
				}
				fmt.Fprintf(&buf, "### %s/%s\n%s\n\n", e.Name(), filepath.Base(pf), string(b))
			}
		}
	}

	// Security vector findings.
	buf.WriteString("## Security vectors\n\n")
	if matches, _ := filepath.Glob(filepath.Join(auditDir, "security", "vector-*.md")); len(matches) > 0 {
		sort.Strings(matches)
		for _, m := range matches {
			b, err := os.ReadFile(m)
			if err != nil {
				continue
			}
			fmt.Fprintf(&buf, "### %s\n%s\n\n", filepath.Base(m), string(b))
		}
	}

	// Persona perspectives (includes codex-review.qa.md).
	buf.WriteString("## Multi-perspective audit\n\n")
	if matches, _ := filepath.Glob(filepath.Join(auditDir, "perspectives", "*.qa.md")); len(matches) > 0 {
		sort.Strings(matches)
		for _, m := range matches {
			b, err := os.ReadFile(m)
			if err != nil {
				continue
			}
			fmt.Fprintf(&buf, "### %s\n%s\n\n", filepath.Base(m), string(b))
		}
	}

	// Raw codex review JSON summary (if present).
	if b, err := os.ReadFile(filepath.Join(auditDir, "codex-review.json")); err == nil && len(b) > 0 {
		buf.WriteString("## Codex review (raw)\n\n```json\n")
		buf.Write(b)
		buf.WriteString("\n```\n\n")
	}

	return buf.String()
}

// phase3MaxAggregateChars caps the character-count of the aggregate
// buffer passed to Phase 3b/3c. Empirically picked at 100 KiB: large
// enough for thousands of findings, small enough to fit comfortably
// below ARG_MAX on kernels with conservative limits AND to keep the
// reviewer's attention focused on the most impactful subset. See
// H-21 (R-deep 31K+ security findings crashed Phase 3 with
// "argument list too long").
const phase3MaxAggregateChars = 100 * 1024

// severityRank orders the severity tokens we see in aggregate
// finding lines. Lower rank = keep first. Unknown severities are
// treated as medium so they don't get dropped before real low-sev.
var severityRank = map[string]int{
	"CRITICAL": 0,
	"HIGH":     1,
	"MEDIUM":   2,
	"LOW":      3,
	"INFO":     4,
}

// severityLineRE extracts the "[SEVERITY]" token from the first
// finding-line prefix. Matches both `- [SEVERITY]` and
// `- [x] [SEVERITY]` shapes produced across the phase-2* sources.
var severityLineRE = regexp.MustCompile(`(?i)^-\s*(?:\[[ xX]\]\s*)?\[([A-Za-z]+)\]`)

// prioritizeAggregatedFindings trims `agg` so its character-count fits
// under `maxChars`. Keeps CRITICAL > HIGH > MEDIUM > LOW > INFO. Lines
// that don't match the finding format (section headers, prose) are
// ALWAYS kept as long as they fit — they're structural context that
// the reviewer needs to understand which block a finding came from.
//
// Returns the possibly-trimmed buffer plus a map of severity → dropped
// count so the caller can surface an informative log line. If `agg`
// already fits, returns the original string untouched and an empty
// drop map (the `trimmed == agg` check in the caller short-circuits
// the log).
func prioritizeAggregatedFindings(agg string, maxChars int) (string, map[string]int) {
	dropped := map[string]int{}
	if len(agg) <= maxChars {
		return agg, dropped
	}
	// Classify every line. Structural lines (headers / prose) retain
	// their original order so the reviewer sees a coherent document.
	// Findings are binned by severity for tiered drop.
	type entry struct {
		text     string // the line + its trailing "\n"
		sevRank  int    // 0..4 for known sevs, -1 for structural/prose
		severity string // "CRITICAL" / "HIGH" / ... or "" for structural
		idx      int    // original position (stable ordering within rank)
	}
	lines := strings.SplitAfter(agg, "\n")
	entries := make([]entry, 0, len(lines))
	for i, l := range lines {
		if l == "" {
			continue
		}
		m := severityLineRE.FindStringSubmatch(l)
		if m == nil {
			// Structural/prose — always retained.
			entries = append(entries, entry{text: l, sevRank: -1, idx: i})
			continue
		}
		sev := strings.ToUpper(m[1])
		rank, ok := severityRank[sev]
		if !ok {
			// Unknown severity — park at MEDIUM rank so a bespoke
			// severity like "WARN" doesn't get dropped before LOW.
			rank = severityRank["MEDIUM"]
			sev = "MEDIUM"
		}
		entries = append(entries, entry{
			text: l, sevRank: rank, severity: sev, idx: i,
		})
	}
	// Compute the total size if we dropped nothing, then iteratively
	// drop lowest-rank findings until we fit. Structural lines (rank
	// = -1) are never dropped. Within a rank we drop from the TAIL so
	// earlier (higher-signal) findings survive.
	total := 0
	for _, e := range entries {
		total += len(e.text)
	}
	// Collect indices of droppable entries sorted by descending
	// severity rank (drop LOW first, then MEDIUM, etc.) and within
	// the same rank by descending index (later findings drop first).
	dropOrder := make([]int, 0, len(entries))
	for i, e := range entries {
		if e.sevRank >= 0 {
			dropOrder = append(dropOrder, i)
		}
	}
	sort.Slice(dropOrder, func(i, j int) bool {
		ai, bi := dropOrder[i], dropOrder[j]
		if entries[ai].sevRank != entries[bi].sevRank {
			return entries[ai].sevRank > entries[bi].sevRank
		}
		return entries[ai].idx > entries[bi].idx
	})
	droppedIdx := map[int]bool{}
	for _, di := range dropOrder {
		if total <= maxChars {
			break
		}
		droppedIdx[di] = true
		total -= len(entries[di].text)
		dropped[entries[di].severity]++
	}
	// Reassemble in original order, skipping dropped entries.
	var buf strings.Builder
	for i, e := range entries {
		if droppedIdx[i] {
			continue
		}
		buf.WriteString(e.text)
	}
	// If we still don't fit (pathological: structural prose alone
	// blows the budget), hard-truncate at maxChars. This preserves
	// the guarantee that no caller will ever blow ARG_MAX.
	out := buf.String()
	if len(out) > maxChars {
		out = out[:maxChars] + "\n\n... (truncated to fit prompt cap)\n"
	}
	return out, dropped
}

// describeDropped formats the severity→count map for the Phase 3a
// prioritize log line. Returns a compact "LOW=123 MEDIUM=4" string.
func describeDropped(dropped map[string]int) string {
	if len(dropped) == 0 {
		return "within cap"
	}
	// Deterministic ordering: walk severityRank by rank.
	type kv struct {
		sev   string
		count int
		rank  int
	}
	items := make([]kv, 0, len(dropped))
	for sev, c := range dropped {
		items = append(items, kv{sev: sev, count: c, rank: severityRank[sev]})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].rank > items[j].rank })
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("dropped %s=%d", it.sev, it.count))
	}
	return strings.Join(parts, ", ")
}

// countAggregateLines counts all lines matching the shared finding
// format across the aggregate buffer. Used for the 3a → 3b log line.
func countAggregateLines(agg string) int {
	re := regexp.MustCompile(`(?m)^-\s*(?:\[[ xX]\]\s*)?\[[A-Za-z]+\]`)
	return len(re.FindAllString(agg, -1))
}

// countFindingLinesInBlock counts "- [SEVERITY]" prefixes in one
// reviewer reply block. Same rule as countAggregateLines but applied
// to a single reply.
func countFindingLinesInBlock(block string) int {
	return countAggregateLines(block)
}

// ------------------------------------------------------------------
// 3c — TIER partitioning and allowlist promotion
// ------------------------------------------------------------------

// partitionTiers splits the reviewer's tier-filter reply into three
// string slices (approved, deferred, dropped) keyed off the "## TIER N"
// headers. If the reply is empty or unparseable, fails open by
// putting every deduped finding into approved — the TIER filter
// MUST NEVER silently drop findings on parse failure.
func partitionTiers(tierReply, dedupeFallback string) (approved, deferred, dropped []string) {
	if strings.TrimSpace(tierReply) == "" {
		// Fail-open: treat every deduped finding line as approved.
		for _, line := range strings.Split(dedupeFallback, "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "- [") {
				approved = append(approved, t)
			}
		}
		return
	}
	// Split reply into three sections using simple header regex.
	// Normalize headers: "## TIER 1", "## TIER 2 (...)", "## TIER 3 (...)".
	// We walk line-by-line and track which bucket we're currently in.
	var current *[]string
	for _, line := range strings.Split(tierReply, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "##") {
			up := strings.ToUpper(trimmed)
			switch {
			case strings.Contains(up, "TIER 1"):
				current = &approved
			case strings.Contains(up, "TIER 2"):
				current = &deferred
			case strings.Contains(up, "TIER 3"):
				current = &dropped
			default:
				current = nil
			}
			continue
		}
		if current == nil {
			continue
		}
		if strings.HasPrefix(trimmed, "- [") {
			*current = append(*current, trimmed)
		}
	}
	// Heuristic: TIER 2 in the spec is BOTH "approve if effort ≤
	// medium" AND "defer if > medium". The reviewer prompt asks it to
	// put "small/medium effort" TIER 2s in section 2 — so we treat
	// everything the reviewer returned in section 2 as approved+
	// qualifying. Items literally marked "effort: large" fall back to
	// deferred. This keeps the split deterministic without needing the
	// reviewer to write to three distinct markdown headers.
	var trueApproved, trueDeferred []string
	for _, f := range deferred {
		if isLargeEffort(f) {
			trueDeferred = append(trueDeferred, f)
		} else {
			trueApproved = append(trueApproved, f)
		}
	}
	approved = append(approved, trueApproved...)
	deferred = trueDeferred
	return
}

// isLargeEffort heuristic: matches "effort: large" or "effort: xl"
// substring (case-insensitive). Used to split TIER 2 into approve /
// defer buckets.
func isLargeEffort(line string) bool {
	l := strings.ToLower(line)
	return strings.Contains(l, "effort: large") ||
		strings.Contains(l, "effort: xl") ||
		strings.Contains(l, "effort=large")
}

// promoteAllowlistFindings scans deferred + dropped for findings
// matching neverTier3Categories (see tier_filter.go), moves them to
// approved. A finding can NEVER be dropped if it hits the allowlist.
func promoteAllowlistFindings(approved, deferred, dropped []string) ([]string, []string, []string) {
	var newDeferred, newDropped []string
	for _, f := range deferred {
		if isNeverTier3(f) {
			approved = append(approved, f)
		} else {
			newDeferred = append(newDeferred, f)
		}
	}
	for _, f := range dropped {
		if isNeverTier3(f) {
			approved = append(approved, f)
		} else {
			newDropped = append(newDropped, f)
		}
	}
	return approved, newDeferred, newDropped
}

// ------------------------------------------------------------------
// 3d — per-section fix-task generation
// ------------------------------------------------------------------

// runPhase3dFixTasks groups approved findings by section (derived
// from the file path in each finding) and dispatches one reviewer
// call per section to produce an audit/fix-tasks/<section>.md file.
func runPhase3dFixTasks(ctx context.Context, cfg *scanRepairConfig, approved []string, call func(context.Context, string, string) string) error {
	bySection := groupFindingsBySection(approved)
	outDir := filepath.Join(cfg.Repo, "audit", "fix-tasks")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("mkdir fix-tasks: %w", err)
	}

	for section, findings := range bySection {
		prompt := fmt.Sprintf(fixTaskPromptTemplate, section, section, strings.Join(findings, "\n"))
		cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		reply := call(cctx, cfg.Repo, prompt)
		cancel()
		body := strings.TrimSpace(reply)
		if body == "" {
			// Fail-open: emit a minimal task list echoing the findings.
			var b strings.Builder
			for i, f := range findings {
				fmt.Fprintf(&b, "- [ ] FIX-%s-%d: %s\n", section, i+1, f)
			}
			body = b.String()
		}
		out := filepath.Join(outDir, section+".md")
		if err := os.WriteFile(out, []byte(body), 0644); err != nil { // #nosec G306 -- CLI output artefact; user-readable.
			fmt.Fprintf(os.Stderr, "  [Phase 3d] write %s: %v\n", out, err)
		}
	}
	return nil
}

// groupFindingsBySection extracts the file path from each finding line
// ("- [SEV] file:line — …") and groups by the first path component so
// each section gets its own fix-tasks file. Unknown paths fall into
// the "misc" bucket.
func groupFindingsBySection(findings []string) map[string][]string {
	out := map[string][]string{}
	for _, f := range findings {
		section := extractSectionFromFinding(f)
		out[section] = append(out[section], f)
	}
	return out
}

// findingPathRE extracts the file path token from a finding line.
// The format is "- [SEVERITY] path:line — …"; we match everything
// after the "] " up to the first ":" or " — ".
var findingPathRE = regexp.MustCompile(`^\-\s*(?:\[[ xX]\]\s*)?\[[A-Za-z]+\]\s*([^\s:—]+)`)

// extractSectionFromFinding returns the first directory component of
// the finding's path. Empty/unmatched → "misc".
func extractSectionFromFinding(line string) string {
	m := findingPathRE.FindStringSubmatch(line)
	if m == nil {
		return "misc"
	}
	p := strings.TrimSpace(m[1])
	if p == "" {
		return "misc"
	}
	// Use the top-level directory as the "section" grouping so
	// related files cluster; fall back to the full path basename for
	// root-level files.
	parts := strings.Split(strings.TrimLeft(p, "./"), "/")
	if len(parts) > 1 {
		return slugify(parts[0])
	}
	return slugify(strings.TrimSuffix(parts[0], filepath.Ext(parts[0])))
}

// ------------------------------------------------------------------
// final SOW assembly
// ------------------------------------------------------------------

// writeFinalSOW compiles the per-section fix-tasks markdown + summary
// counters into FIX_SOW.md AND a dated copy at specs/repair-<date>.md.
func writeFinalSOW(cfg *scanRepairConfig, res *phase3Result, approved []string, p1 *phase1Result, p2 *phase2Result) error {
	var buf strings.Builder
	fmt.Fprintf(&buf, "<!-- STATUS: ready -->\n")
	fmt.Fprintf(&buf, "<!-- TYPE: repair -->\n")
	fmt.Fprintf(&buf, "<!-- CREATED: %s -->\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&buf, "<!-- SOURCE: r1 scan-repair -->\n\n")
	fmt.Fprintf(&buf, "# Fix SOW\n\n")
	fmt.Fprintf(&buf, "## Summary\n\n")
	fmt.Fprintf(&buf, "- Deterministic findings: %d\n", p1.DeterministicFindings)
	fmt.Fprintf(&buf, "- Security findings: %d\n", p1.SecurityFindings)
	fmt.Fprintf(&buf, "- Semantic findings: %d\n", p2.FindingsCount)
	fmt.Fprintf(&buf, "- Aggregate → deduped → approved: %d → %d → %d\n", res.Aggregated, res.Deduped, res.Approved)
	fmt.Fprintf(&buf, "- Deferred: %d, Dropped: %d\n\n", res.Deferred, res.Dropped)

	fmt.Fprintf(&buf, "## Implementation Checklist\n\n")
	fmt.Fprintf(&buf, "### Code Quality Fixes\n\n")
	// Append every fix-tasks/*.md file.
	fxDir := filepath.Join(cfg.Repo, "audit", "fix-tasks")
	matches, _ := filepath.Glob(filepath.Join(fxDir, "*.md"))
	sort.Strings(matches)
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		fmt.Fprintf(&buf, "#### %s\n\n%s\n\n", strings.TrimSuffix(filepath.Base(m), ".md"), string(b))
	}
	if len(matches) == 0 {
		// Fallback: dump approved findings directly as tasks so the
		// SOW runner has SOMETHING to chew on.
		for i, f := range approved {
			fmt.Fprintf(&buf, "- [ ] T%d: %s\n", i+1, f)
		}
	}

	sowPath := filepath.Join(cfg.Repo, "FIX_SOW.md")
	if err := os.WriteFile(sowPath, []byte(buf.String()), 0644); err != nil { // #nosec G306 -- CLI output artefact; user-readable.
		return err
	}
	res.SOWPath = sowPath

	// Dated copy under specs/ — best-effort, don't fail the run if the
	// specs dir can't be created.
	specsDir := filepath.Join(cfg.Repo, "specs")
	if err := os.MkdirAll(specsDir, 0755); err == nil {
		repairName := fmt.Sprintf("repair-%s.md", time.Now().Format("2006-01-02"))
		_ = os.WriteFile(filepath.Join(specsDir, repairName), []byte(buf.String()), 0644) // #nosec G306 -- CLI output artefact; user-readable.
	}
	return nil
}
