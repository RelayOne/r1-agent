package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RelayOne/r1-agent/internal/r1env"
)

// ------------------------------------------------------------------
// Phase 2: semantic scan (LLM per section × pattern)
// ------------------------------------------------------------------

// phase2Result summarizes a full Phase 2 pass. The heavy artefacts
// (per-pattern markdown files, semantic-report.md) live on disk;
// these counters feed the orchestrator's "no findings → skip 3+4"
// check and the human-readable completion banner.
type phase2Result struct {
	CallsMade     int // total (section × pattern) LLM calls actually dispatched
	FindingsCount int // aggregate non-"None." findings across all calls
	Timeouts      int // calls whose context expired (2-minute per-call ceiling)
	Errors        int // calls that returned non-empty but marked as error
}

// semanticPattern is one of the 20 patterns in semantic-patterns.md.
// Body is the full pattern description (paragraph-scale) used as the
// LLM prompt. Slug is derived from Name and used for filesystem
// paths (audit/scans/<section>/<slug>.md).
type semanticPattern struct {
	Name string
	Slug string
	Body string
}

// runPhase2 issues one LLM call per (section × pattern) pair up to
// cfg.MaxSections × cfg.MaxPatterns. Calls fan out across cfg.Workers
// concurrent goroutines. Each individual call is bounded by a 2-min
// context; a timeout on an individual call DOES NOT abort the phase
// — we record it and continue. The phase always produces at least a
// partial audit/semantic-report.md, even on ctx cancellation.
func runPhase2(ctx context.Context, cfg *scanRepairConfig, p1 *phase1Result) (*phase2Result, error) {
	res := &phase2Result{}
	if p1 == nil || len(p1.Sections) == 0 {
		// Nothing to scan. Still write an empty report so Phase 3
		// doesn't trip on a missing file.
		return res, writeEmptySemanticReport(cfg.Repo, "no sections produced by Phase 1")
	}

	patterns, err := parseSemanticPatterns(cfg.SemanticFile)
	if err != nil {
		return res, writeEmptySemanticReport(cfg.Repo, fmt.Sprintf("could not parse semantic-patterns.md: %v", err))
	}
	// Apply caps. A cap of 0 means "unlimited" (operator override).
	sections := p1.Sections
	if cfg.MaxSections > 0 && len(sections) > cfg.MaxSections {
		sections = sections[:cfg.MaxSections]
	}
	if cfg.MaxPatterns > 0 && len(patterns) > cfg.MaxPatterns {
		patterns = patterns[:cfg.MaxPatterns]
	}
	total := len(sections) * len(patterns)
	fmt.Printf("  Dispatching %d semantic calls (%d sections × %d patterns, %d workers)\n",
		total, len(sections), len(patterns), cfg.Workers)
	if total == 0 {
		return res, writeEmptySemanticReport(cfg.Repo, "caps produced zero calls")
	}

	// Worker-pool dispatch: a fixed number of goroutines drain a job
	// channel. atomic counters are aggregated into res at the end so
	// we don't need a mutex on the shared struct.
	type job struct {
		section string
		pattern semanticPattern
	}
	jobs := make(chan job, total)
	var wg sync.WaitGroup
	var calls, findings, timeouts, errs int64
	startedAt := time.Now()

	// Resolve the semantic caller: tests inject a mock, prod uses the
	// real worker-model invocation.
	call := cfg.semanticCaller
	if call == nil {
		call = func(ctx context.Context, dir, prompt string) string {
			return workerSemanticCall(ctx, cfg, prompt)
		}
	}

	for w := 0; w < cfg.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				// Per-call ceiling: 2 minutes. Honors outer
				// cancellation — if ctx is already dead, the Deadline
				// derived from it will reflect that.
				cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				prompt := buildSemanticPrompt(j.section, j.pattern)
				reply := call(cctx, cfg.Repo, prompt)
				cancel()
				atomic.AddInt64(&calls, 1)
				if cctx.Err() == context.DeadlineExceeded {
					atomic.AddInt64(&timeouts, 1)
				}
				if reply == "" {
					atomic.AddInt64(&errs, 1)
				}
				hits := writeSemanticFinding(cfg.Repo, j.section, j.pattern, reply)
				atomic.AddInt64(&findings, int64(hits))
			}
		}()
	}

	for _, s := range sections {
		for _, p := range patterns {
			jobs <- job{section: s, pattern: p}
		}
	}
	close(jobs)
	wg.Wait()

	res.CallsMade = int(atomic.LoadInt64(&calls))
	res.FindingsCount = int(atomic.LoadInt64(&findings))
	res.Timeouts = int(atomic.LoadInt64(&timeouts))
	res.Errors = int(atomic.LoadInt64(&errs))
	elapsed := time.Since(startedAt)

	// Assemble the semantic-report aggregate. Always write SOMETHING,
	// even if the phase collected zero findings or saw every call
	// time out.
	if err := writeSemanticReport(cfg.Repo, res, len(sections), len(patterns), elapsed); err != nil {
		return res, err
	}
	return res, nil
}

// buildSemanticPrompt constructs the single-pattern prompt the user
// mandated. Format is load-bearing — Phase 3's reviewer consumes the
// per-pattern files and expects the "None." sentinel for zero-finding
// patterns.
func buildSemanticPrompt(sectionFile string, pattern semanticPattern) string {
	filesBlock, err := os.ReadFile(sectionFile)
	if err != nil {
		// The section file is produced by Phase 1's project-mapper.sh
		// — a missing file here means the shell-out failed silently.
		// Log once per prompt (the scan still proceeds with empty
		// file content so the worker can report "None.").
		fmt.Fprintf(os.Stderr, "  [Phase 2] read %s: %v\n", sectionFile, err)
	}
	return fmt.Sprintf(
		"Scan the files in %s for ONLY the pattern described below. "+
			"List every occurrence with file path + line number + one-line reason. "+
			"If zero findings, reply exactly: `None.`\n\n"+
			"## Pattern\n%s\n\n"+
			"## Files\n%s\n",
		filepath.Base(sectionFile), strings.TrimSpace(pattern.Body), string(filesBlock))
}

// workerSemanticCall routes a semantic-scan prompt through the
// production worker. Uses claudeReviewCall when the worker-model is
// a claude alias (opus/sonnet/haiku) — review mode gives us a
// text-only call with no tool-use turns, which is exactly what we
// want for pattern detection. Any other model string falls through
// to the generic claudeCall with the resolved --model override.
//
// This deliberately does NOT use the writerPair fallback; Phase 2 is
// pure prompt-in / prose-out, so a rate-limit here just wastes a
// call — cheaper to let the worker fail and move on than to swap in
// codex mid-scan.
func workerSemanticCall(ctx context.Context, cfg *scanRepairConfig, prompt string) string {
	select {
	case <-ctx.Done():
		return ""
	default:
	}
	model := cfg.WorkerModel
	// Recognize the short-form aliases the user can pass.
	alias := strings.ToLower(strings.TrimSpace(model))
	if alias == "opus" || alias == "sonnet" || alias == "haiku" {
		return claudeReviewCall(cfg.ClaudeBin, cfg.Repo, prompt, alias)
	}
	// Otherwise pass as-is through claudeReviewCall (non-empty model
	// string) so LiteLLM / direct vendor IDs flow cleanly.
	if model == "" {
		return claudeReviewCall(cfg.ClaudeBin, cfg.Repo, prompt, "")
	}
	return claudeReviewCall(cfg.ClaudeBin, cfg.Repo, prompt, model)
}

// writeSemanticFinding writes one per-(section,pattern) markdown
// file to audit/scans/<section-basename>/<pattern-slug>.md and
// returns the number of findings detected (0 when the reply is
// empty or the literal sentinel "None.").
func writeSemanticFinding(repo, sectionFile string, pattern semanticPattern, reply string) int {
	base := strings.TrimSuffix(filepath.Base(sectionFile), ".txt")
	outDir := filepath.Join(repo, "audit", "scans", base)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return 0
	}
	outPath := filepath.Join(outDir, pattern.Slug+".md")
	body := strings.TrimSpace(reply)
	if body == "" {
		body = "(no response — call failed or timed out)"
	}
	if err := os.WriteFile(outPath, []byte(fmt.Sprintf("# %s / %s\n\n%s\n", base, pattern.Name, body)), 0644); err != nil { // #nosec G306 -- CLI output artefact; user-readable.
		// A failed per-pattern write doesn't abort the whole phase —
		// other (section,pattern) goroutines are still running and
		// the aggregate report will just be missing this entry. Log
		// so the operator sees the failure.
		fmt.Fprintf(os.Stderr, "  [Phase 2] write %s: %v\n", outPath, err)
	}
	return countSemanticFindings(body)
}

// countSemanticFindings approximates the number of distinct findings
// in a per-pattern reply. The exact format is free-form (the worker
// isn't given a strict schema) so we use a generous heuristic: lines
// starting with "- ", "* ", "1." etc. Empty / sentinel replies → 0.
func countSemanticFindings(body string) int {
	t := strings.TrimSpace(body)
	if t == "" || strings.EqualFold(t, "None.") || strings.EqualFold(t, "None") {
		return 0
	}
	n := 0
	for _, line := range strings.Split(t, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") ||
			strings.HasPrefix(trimmed, "* ") ||
			(len(trimmed) > 2 && trimmed[0] >= '0' && trimmed[0] <= '9' && (trimmed[1] == '.' || trimmed[1] == ')')) {
			n++
		}
	}
	// Non-empty reply with no list items: credit as 1 "finding"
	// (free-prose description of an issue).
	if n == 0 && !strings.EqualFold(t, "none.") {
		return 1
	}
	return n
}

// writeSemanticReport aggregates every per-pattern finding into a
// single markdown file the Phase 3 reviewer consumes. Timeouts /
// errors are surfaced in a banner so the reviewer can tell a
// "genuinely clean" result from "half our calls timed out".
func writeSemanticReport(repo string, res *phase2Result, numSections, numPatterns int, elapsed time.Duration) error {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# Semantic Audit Report\n\n")
	fmt.Fprintf(&buf, "Generated: %s\n\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&buf, "## Summary\n\n")
	fmt.Fprintf(&buf, "- Sections scanned: %d\n", numSections)
	fmt.Fprintf(&buf, "- Patterns per section: %d\n", numPatterns)
	fmt.Fprintf(&buf, "- Calls dispatched: %d\n", res.CallsMade)
	fmt.Fprintf(&buf, "- Findings: %d\n", res.FindingsCount)
	fmt.Fprintf(&buf, "- Timeouts: %d\n", res.Timeouts)
	fmt.Fprintf(&buf, "- Errors / empty replies: %d\n", res.Errors)
	fmt.Fprintf(&buf, "- Elapsed: %s\n\n", elapsed.Round(time.Second))

	if res.Timeouts > 0 {
		fmt.Fprintf(&buf, "⚠ %d call(s) timed out (2 min each). Coverage is partial.\n\n", res.Timeouts)
	}

	// Stream per-(section,pattern) files into the aggregate,
	// omitting the "None." results so the reviewer isn't buried in
	// empty stanzas.
	scansDir := filepath.Join(repo, "audit", "scans")
	entries, err := os.ReadDir(scansDir)
	if err != nil {
		// scansDir missing = zero sections produced findings; record
		// that in the report rather than silently showing an empty
		// section list.
		fmt.Fprintf(&buf, "_could not enumerate scans dir: %v_\n", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sectionDir := filepath.Join(scansDir, e.Name())
		patternFiles, gerr := filepath.Glob(filepath.Join(sectionDir, "*.md"))
		if gerr != nil {
			fmt.Fprintf(os.Stderr, "  [Phase 2] glob %s: %v\n", sectionDir, gerr)
			continue
		}
		sort.Strings(patternFiles)
		var sectionHasFindings bool
		var sectionBuf bytes.Buffer
		for _, pf := range patternFiles {
			b, err := os.ReadFile(pf)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  [Phase 2] read %s: %v\n", pf, err)
				continue
			}
			body := string(b)
			// Skip the "None." stanzas to keep the report focused on
			// actual findings. The raw files remain on disk for
			// operators who want full context.
			if strings.Contains(body, "\nNone.\n") || strings.HasSuffix(strings.TrimSpace(body), "None.") {
				continue
			}
			sectionBuf.WriteString(body)
			sectionBuf.WriteString("\n\n")
			sectionHasFindings = true
		}
		if sectionHasFindings {
			fmt.Fprintf(&buf, "## Section %s\n\n", e.Name())
			buf.Write(sectionBuf.Bytes())
		}
	}

	return os.WriteFile(filepath.Join(repo, "audit", "semantic-report.md"), buf.Bytes(), 0644) // #nosec G306 -- CLI output artefact; user-readable.
}

// writeEmptySemanticReport is called from the short-circuits in
// runPhase2 (no sections, no patterns, all timeouts). Keeps Phase 3
// from crashing on a missing file.
func writeEmptySemanticReport(repo, reason string) error {
	body := fmt.Sprintf("# Semantic Audit Report\n\nNo semantic scan ran: %s\n", reason)
	return os.WriteFile(filepath.Join(repo, "audit", "semantic-report.md"), []byte(body), 0644) // #nosec G306 -- CLI output artefact; user-readable.
}

// parseSemanticPatterns extracts the 20 patterns from
// .claude/scripts/semantic-patterns.md. Each pattern is a level-2
// heading (## N. NAME (severity)) followed by a free-prose paragraph.
// The slug is derived from the NAME, lowercased with non-alphanumerics
// replaced by "-". Robust against extra whitespace / trailing blank
// lines.
func parseSemanticPatterns(path string) ([]semanticPattern, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []semanticPattern
	var cur semanticPattern
	headingRE := regexp.MustCompile(`^##\s+\d+\.\s+([A-Z_][A-Z0-9_]*)`)
	var bodyBuf bytes.Buffer
	flush := func() {
		if cur.Name == "" {
			return
		}
		cur.Body = strings.TrimSpace(bodyBuf.String())
		if cur.Body != "" {
			out = append(out, cur)
		}
		cur = semanticPattern{}
		bodyBuf.Reset()
	}
	for _, line := range strings.Split(string(b), "\n") {
		if m := headingRE.FindStringSubmatch(line); m != nil {
			flush()
			cur.Name = m[1]
			cur.Slug = slugify(m[1])
			continue
		}
		if cur.Name != "" {
			bodyBuf.WriteString(line)
			bodyBuf.WriteByte('\n')
		}
	}
	flush()
	if len(out) == 0 {
		return nil, fmt.Errorf("no patterns found in %s", path)
	}
	return out, nil
}

// slugify lowercases and replaces non-alphanumeric runs with single
// dashes. Used for per-pattern filenames.
func slugify(name string) string {
	var out strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			out.WriteByte('-')
			lastDash = true
		}
	}
	s := strings.Trim(out.String(), "-")
	if s == "" {
		return "pattern"
	}
	return s
}

// ------------------------------------------------------------------
// Phase 3: Fix-SOW generation via the reviewer.
// ------------------------------------------------------------------

// runPhase3 feeds the combined deterministic + semantic reports to
// the reviewer and writes the reviewer's output to <repo>/FIX_SOW.md.
// The reviewer is a distinct model from the worker: this guards
// against single-model self-review confirmation bias and lets the
// operator plug a stronger judge model (opus, gpt-5, etc.) in even
// when the worker is a cheaper tier.
func runPhase3(ctx context.Context, cfg *scanRepairConfig, p1 *phase1Result, p2 *phase2Result) (string, error) {
	detPath := filepath.Join(cfg.Repo, "audit", "deterministic-report.md")
	semPath := filepath.Join(cfg.Repo, "audit", "semantic-report.md")
	detReport, err := os.ReadFile(detPath)
	if err != nil {
		// Phase 1 should always produce this; a missing file means
		// something went wrong earlier. Surface it loudly but
		// continue so the reviewer at least sees the semantic report.
		fmt.Fprintf(os.Stderr, "  [Phase 3] read %s: %v (continuing with empty deterministic report)\n", detPath, err)
	}
	semReport, err := os.ReadFile(semPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  [Phase 3] read %s: %v (continuing with empty semantic report)\n", semPath, err)
	}
	prompt := buildReviewerPrompt(string(detReport), string(semReport))

	// 15-minute ceiling on the reviewer call — it produces a fairly
	// large SOW document on a big audit, so we give it more room
	// than the per-semantic-call 2-min budget.
	cctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	caller := cfg.reviewerCaller
	if caller == nil {
		caller = func(ctx context.Context, dir, prompt string) string {
			return reviewerPhase3Call(ctx, cfg, prompt)
		}
	}
	reply := caller(cctx, cfg.Repo, prompt)
	reply = strings.TrimSpace(reply)
	if reply == "" {
		// Rescue: produce a minimal stub SOW so Phase 4 has something
		// to chew on. A genuine empty reply usually means a
		// transient reviewer failure; operators can re-run with
		// --fresh to retry.
		reply = stubFixSOW(p1, p2)
	}

	sowPath := filepath.Join(cfg.Repo, "FIX_SOW.md")
	if err := os.WriteFile(sowPath, []byte(reply), 0644); err != nil { // #nosec G306 -- CLI output artefact; user-readable.
		return "", fmt.Errorf("write FIX_SOW.md: %w", err)
	}
	return sowPath, nil
}

// buildReviewerPrompt constructs the reviewer prompt mandated by the
// spec. Format is load-bearing — tests assert on the markers present
// in the prompt so a drift here will fail the prompt-construction
// test.
func buildReviewerPrompt(detReport, semReport string) string {
	return fmt.Sprintf(
		"You are a tech-lead generating a build-ready SOW from audit findings.\n"+
			"Read the deterministic + semantic audit reports below and produce a SOW\n"+
			"that enumerates every fix as a discrete task. Each task must:\n"+
			"  - have an ID (T1, T2, ...)\n"+
			"  - name the file(s) it touches\n"+
			"  - state the fix succinctly in 1-3 sentences\n"+
			"  - set severity (critical / high / medium / low)\n\n"+
			"Output as valid markdown with sections per severity, tasks ordered by\n"+
			"severity then by file. Do not invent findings. If a finding is trivial\n"+
			"(whitespace / typo), batch related trivial fixes into one task.\n\n"+
			"## Deterministic Report\n\n%s\n\n## Semantic Report\n\n%s\n",
		detReport, semReport)
}

// reviewerPhase3Call routes the reviewer prompt through the
// production reviewer path. We honor cfg.Reviewer: "codex" goes
// through codexCall (with the read-only sandbox), "cc-opus" /
// "cc-sonnet" / "claude" go through claudeReviewCall so the
// operator can use Claude as the judge when codex isn't available.
func reviewerPhase3Call(ctx context.Context, cfg *scanRepairConfig, prompt string) string {
	select {
	case <-ctx.Done():
		return ""
	default:
	}
	switch strings.ToLower(cfg.Reviewer) {
	case "cc-opus":
		return claudeReviewCall(cfg.ClaudeBin, cfg.Repo, prompt, "opus")
	case "cc-sonnet":
		return claudeReviewCall(cfg.ClaudeBin, cfg.Repo, prompt, "sonnet")
	case "cc", "claude":
		return claudeReviewCall(cfg.ClaudeBin, cfg.Repo, prompt, "")
	default:
		// Codex (default). Uses the read-only sandbox so a rogue
		// codex session can't accidentally overwrite source files.
		return codexCall(cfg.CodexBin, cfg.Repo, prompt)
	}
}

// stubFixSOW produces a minimal SOW when the reviewer returns
// nothing. It writes enough structure for Phase 4's runners to parse
// without crashing and surfaces the counts from Phase 1/2 so the
// operator sees WHY the reviewer had nothing to say.
func stubFixSOW(p1 *phase1Result, p2 *phase2Result) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# Fix SOW (reviewer call failed — stub)\n\n")
	fmt.Fprintf(&buf, "The reviewer returned no output. Audit counters:\n\n")
	fmt.Fprintf(&buf, "- Deterministic findings: %d\n", p1.DeterministicFindings)
	fmt.Fprintf(&buf, "- Security findings: %d\n", p1.SecurityFindings)
	fmt.Fprintf(&buf, "- Semantic findings: %d\n\n", p2.FindingsCount)
	fmt.Fprintf(&buf, "## critical\n\n")
	fmt.Fprintf(&buf, "- [ ] T1: Re-run `stoke scan-repair --fresh` after the reviewer recovers.\n")
	fmt.Fprintf(&buf, "  - files: (none)\n")
	fmt.Fprintf(&buf, "  - fix: The automatic reviewer call returned empty output. Retry the audit.\n\n")
	return buf.String()
}

// ------------------------------------------------------------------
// Phase 4: execute FIX_SOW via the existing runners.
// ------------------------------------------------------------------

// runPhase4 shells out to the stoke binary (self-invocation) to
// execute the generated FIX_SOW.md. We use a subprocess rather than
// calling sowCmd / simpleLoopCmd directly for two reasons:
//  1. those functions parse their OWN flags and call os.Exit on
//     failure — embedding them would require plumbing a clean-exit
//     path through their internals,
//  2. a subprocess naturally propagates ctrl-C and gives us a clean
//     exit code to surface.
//
// The FIX_SOW.md path is threaded through --file. All existing
// runner hardening fires because it lives inside the runners; we
// don't re-implement any of H-4/6/7/10/11 here.
func runPhase4(ctx context.Context, cfg *scanRepairConfig, sowPath string) error {
	if cfg.StokeBin == "" {
		return fmt.Errorf("stoke binary path unknown — cannot invoke Phase 4 runner")
	}
	args := buildPhase4Args(cfg, sowPath)
	if len(args) == 0 {
		return fmt.Errorf("unknown mode %q", cfg.Mode)
	}
	cmd := exec.CommandContext(ctx, cfg.StokeBin, args...) // #nosec G204 -- Stoke self-invocation or dev-tool binary with Stoke-generated args.
	cmd.Dir = cfg.Repo
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	fmt.Printf("  invoking: %s %s\n", cfg.StokeBin, strings.Join(args, " "))
	return cmd.Run()
}

// buildPhase4Args constructs the argv the Phase 4 subprocess uses
// based on cfg.Mode. Extracted from runPhase4 so the mode-routing
// logic is testable without spawning a subprocess. Returns nil for
// an unknown mode so the caller can surface a clear error.
//
// H-22: forwards provider flags (--native-*, --reasoning-*, --runner,
// --workers) to sow mode so a LiteLLM / native-API run of scan-repair
// doesn't die with "no provider configured" the moment it reaches the
// sub-invocation. Forwards simple-loop-specific flags (--claude-model,
// --reviewer, --fix-mode, --max-rounds, --tier-filter-*) to simple-
// loop mode for the same reason.
func buildPhase4Args(cfg *scanRepairConfig, sowPath string) []string {
	switch cfg.Mode {
	case "sow":
		args := []string{"sow",
			"--repo", cfg.Repo,
			"--file", sowPath,
			"--per-task-worktree",
		}
		// H-22: forward provider plumbing. `--native-model` is the
		// old positional flag for the sow runner; prefer the
		// explicit `cfg.NativeModel` if the operator set it and
		// fall back to `cfg.WorkerModel` for backward compat.
		nativeModel := cfg.NativeModel
		if nativeModel == "" {
			nativeModel = cfg.WorkerModel
		}
		if nativeModel != "" {
			args = append(args, "--native-model", nativeModel)
		}
		if cfg.Runner != "" {
			args = append(args, "--runner", cfg.Runner)
		}
		if cfg.NativeAPIKey != "" {
			args = append(args, "--native-api-key", cfg.NativeAPIKey)
		}
		if cfg.NativeBaseURL != "" {
			args = append(args, "--native-base-url", cfg.NativeBaseURL)
		}
		if cfg.ReasoningAPIKey != "" {
			args = append(args, "--reasoning-api-key", cfg.ReasoningAPIKey)
		}
		if cfg.ReasoningBaseURL != "" {
			args = append(args, "--reasoning-base-url", cfg.ReasoningBaseURL)
		}
		if cfg.ReasoningModel != "" {
			args = append(args, "--reasoning-model", cfg.ReasoningModel)
		}
		if cfg.Workers > 0 {
			args = append(args, "--workers", fmt.Sprintf("%d", cfg.Workers))
		}
		return args
	case "simple-loop":
		args := []string{"simple-loop",
			"--repo", cfg.Repo,
			"--file", sowPath,
			"--claude-bin", cfg.ClaudeBin,
			"--codex-bin", cfg.CodexBin,
			"--reviewer", cfg.Reviewer,
		}
		// The simple-loop worker model override is documented as
		// "sonnet/opus"; pass it through only when it looks like a
		// simple alias (no "/" path separator that would imply a
		// full model ID meant for LiteLLM routing). An explicit
		// --claude-model at the scan-repair layer wins over the
		// auto-derived --worker-model.
		claudeModel := cfg.ClaudeModel
		if claudeModel == "" && cfg.WorkerModel != "" && !strings.Contains(cfg.WorkerModel, "/") {
			claudeModel = cfg.WorkerModel
		}
		if claudeModel != "" {
			args = append(args, "--claude-model", claudeModel)
		}
		// H-22: simple-loop-specific passthroughs. Only forward when
		// set — simple-loop's defaults are sensible and we don't
		// want to override unless the operator explicitly chose.
		if cfg.FixMode != "" {
			args = append(args, "--fix-mode", cfg.FixMode)
		}
		if cfg.MaxRounds > 0 {
			args = append(args, "--max-rounds", fmt.Sprintf("%d", cfg.MaxRounds))
		}
		if cfg.TierFilterAfter >= 0 {
			args = append(args, "--tier-filter-after", fmt.Sprintf("%d", cfg.TierFilterAfter))
		}
		if cfg.TierFilterThresh >= 0 {
			args = append(args, "--tier-filter-threshold", fmt.Sprintf("%g", cfg.TierFilterThresh))
		}
		return args
	default:
		return nil
	}
}

// ------------------------------------------------------------------
// Shared helpers.
// ------------------------------------------------------------------

// cleanAuditArtifacts wipes the artefacts a prior scan-repair run
// produced. Deliberately narrow: never touches plan files, specs,
// or source. Used by --fresh.
func cleanAuditArtifacts(repo string) error {
	paths := []string{
		filepath.Join(repo, "audit"),
		filepath.Join(repo, "FIX_SOW.md"),
	}
	for _, p := range paths {
		if err := os.RemoveAll(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	return nil
}

// ensureClaudeScripts verifies that .claude/scripts/ exists in the
// target repo. When missing, we try to bootstrap by running the
// local setup.sh against the target. Fail-closed: if both the
// scripts directory AND the setup.sh attempt fail, we error out
// rather than silently skipping Phase 1 — the spec explicitly calls
// for this behavior.
func ensureClaudeScripts(repo string) error {
	scriptsDir := filepath.Join(repo, ".claude", "scripts")
	if fi, err := os.Stat(scriptsDir); err == nil && fi.IsDir() {
		return nil
	}
	// Try setup.sh from $STOKE_HOME or the running stoke binary's repo.
	candidates := []string{}
	if home := r1env.Get("R1_HOME", "STOKE_HOME"); home != "" {
		candidates = append(candidates, filepath.Join(home, "setup.sh"))
	}
	if exe, err := os.Executable(); err == nil {
		// Walk up from bin/stoke until we find setup.sh.
		dir := filepath.Dir(exe)
		for i := 0; i < 5; i++ {
			candidates = append(candidates, filepath.Join(dir, "setup.sh"))
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	// Last resort: the stoke source checkout on disk (not guaranteed
	// to exist in a deployed environment but covers the dev case).
	candidates = append(candidates, "/home/eric/repos/stoke/setup.sh")

	var lastErr error
	for _, c := range candidates {
		if _, err := os.Stat(c); err != nil {
			continue
		}
		fmt.Printf("  .claude/scripts missing — running %s in %s\n", c, repo)
		cmd := exec.Command("bash", c) // #nosec G204 -- Stoke self-invocation or dev-tool binary with Stoke-generated args.
		cmd.Dir = repo
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			lastErr = err
			continue
		}
		if fi, err := os.Stat(scriptsDir); err == nil && fi.IsDir() {
			return nil
		}
	}
	if lastErr != nil {
		return fmt.Errorf(".claude/scripts missing and setup.sh bootstrap failed: %w", lastErr)
	}
	return fmt.Errorf(".claude/scripts missing and no setup.sh found on any candidate path")
}

// runShell executes a bash -lc command in dir with a timeout. Returns
// the combined stdout+stderr and an error for non-zero exit (or
// timeout). Used by Phase 1 for project-mapper / deterministic-scan /
// security scripts; each call is capped at 5 minutes per the spec.
func runShell(ctx context.Context, dir, cmdline string, timeout time.Duration) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "bash", "-lc", cmdline) // #nosec G204 -- Stoke self-invocation or dev-tool binary with Stoke-generated args.
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

// shellQuote wraps a path in single quotes and escapes any embedded
// single quotes. Good-enough for the paths we feed to bash -lc in
// runShell; the paths come from os.MkdirTemp / filepath.Glob and
// never contain newlines.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
