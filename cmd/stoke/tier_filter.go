// tier_filter.go — H-19: TIER filter for simple-loop Final review convergence.
//
// Problem: the Final-review loop in simpleLoopCmd can burn all its
// max-rounds cycles bouncing minor TIER-3 gaps back and forth between
// the codex reviewer and the CC worker. Each round the reviewer finds
// fresh style preferences / missing docstrings / pattern migrations;
// the worker dutifully fixes them; the reviewer finds NEW minor things.
// This is the "AI reviewer approving AI worker" feedback loop. Users
// approve anything the loop presents, so the loop itself has to be
// opinionated about what's worth fixing.
//
// Fix: after N rounds (default 5) of not-converging Final review, we
// invoke the reviewer one more time in a classifier role. The classifier
// buckets every remaining gap into TIER 1/2/3 (same taxonomy as
// scan-repair Phase 3c), measures whether gaps are TIER-3-dominated AND
// recurring across prior rounds, and emits one of three decisions:
//
//   - continue               — loop normally (gaps are real defects)
//   - drop-tier3-continue    — drop TIER-3 noise, keep iterating on TIER-1/2
//   - drop-tier3-complete    — all remaining gaps were noise; declare complete
//
// A hardcoded allowlist promotes any gap mentioning security/auth/data-
// loss/crash/race-condition to TIER 1 regardless of the reviewer's
// classification. This is the critical guardrail — the filter MUST NEVER
// drop a real vulnerability just because the reviewer misclassified it.
//
// The filter call is fail-open: on any error (rate-limit, timeout,
// malformed JSON, empty response), we return Decision="continue" so the
// outer loop keeps iterating the old way. Under NO circumstance do we
// drop gaps on filter failure — that would be a silent regression.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// tierFilterResult captures the classifier's verdict on a round's gap
// list. Counts are post-allowlist-promotion (so Tier1Count includes
// any gaps the reviewer called TIER-3 but that matched the never-TIER-3
// allowlist). Tier3Dropped is the subset of original TIER-3 gaps that
// the filter decided to drop; its contents are logged for audit.
type tierFilterResult struct {
	Tier1Count   int
	Tier2Count   int
	Tier3Count   int
	Tier3Dropped []string // gap text that the filter decided to drop
	Recurring    bool     // TIER-3 gaps similar to prior rounds' gaps?
	Decision     string   // "continue" | "drop-tier3-continue" | "drop-tier3-complete"
	Rationale    string   // one-line explanation from the classifier
	// RemainingGaps is the post-filter gap list: TIER-1 + TIER-2 only
	// when a drop decision was taken, otherwise the original gaps. The
	// caller passes this to the next fix round.
	RemainingGaps []string
}

// neverTier3Categories is the hardcoded allowlist: any gap whose text
// contains any of these substrings (case-insensitive) MUST be
// classified TIER 1, regardless of what the reviewer LLM returned.
// This is the non-filterable safety net from the H-19 spec.
//
// Kept as an exported package-level var so future edits are trivial —
// when a new class of critical defect shows up in the field, add the
// keyword here and every downstream callsite inherits the promotion.
var neverTier3Categories = []string{
	// Authentication / authorization
	"auth bypass",
	"auth missing",
	"authorization gap",
	"missing auth",
	"broken auth",
	"missing authentication",
	"missing authorization",
	"csrf",
	"missing rate limit", // on auth endpoints — conservative: promote all
	"cors wildcard",      // credentialed endpoints — conservative: promote all
	"cors *",

	// Injection
	"sql injection",
	"xss",
	"cross-site scripting",
	"command injection",
	"path traversal",
	"ssrf",
	"xxe",
	"ldap injection",
	"shell injection",

	// Secrets
	"secret exposure",
	"credential leak",
	"hardcoded key",
	"hardcoded secret",
	"hardcoded password",
	"hardcoded token",
	"api key in",
	"leaked credential",

	// Data integrity
	"data loss",
	"data corruption",
	"race condition",

	// Crash / panic
	"unhandled panic",
	"uncaught exception",
	"null dereference",
	"nil dereference",
	"segfault",
	"crash",
}

// simpleLoopTierFilterPromptTemplate is the classifier prompt. Filled
// in by applyTierFilter via fmt.Sprintf; the two format-verbs are the
// current-round gap list and the prior-rounds summary in that order.
//
// The prompt is deliberately opinionated: it tells the reviewer what
// counts as TIER 3 noise, lists the never-TIER-3 allowlist, and asks
// for a strict JSON response. Any deviation from the JSON shape is
// handled by the fail-open path (Decision=continue).
//
// Named with the simpleLoop prefix to avoid collision with the
// scan-repair Phase 3c prompt in scan_repair_prompts.go, which uses
// the same TIER taxonomy but for a different call site.
const simpleLoopTierFilterPromptTemplate = `You are the impact-effort filter for a simple-loop auto-code-fix
system that has iterated several rounds on a Statement of Work
without converging. Your job: decide whether the remaining gaps
are REAL defects or LOW-ROI NOISE that's preventing convergence.

## Never-TIER-3 categories (always TIER 1 — promote if you see one)
- auth bypass, auth missing, authorization gap
- SQL injection, XSS, command injection, path traversal, SSRF, XXE
- secret exposure, credential leak, hardcoded keys/passwords/tokens
- data loss risk, data corruption
- race condition affecting data integrity
- crash / unhandled panic / uncaught exception in production path
- missing rate limiting on auth endpoints
- CORS wildcarding on credentialed endpoints

## TIER taxonomy
TIER 1 (fix now): security, data loss, crashes, race conditions,
  scaling blockers, UX blockers, broken API contracts, missing auth.
TIER 2 (fix if effort <= medium): reliability on critical paths,
  test gaps on business logic, performance >500ms, type holes causing
  runtime errors, error handling gaps that swallow failures silently.
TIER 3 (DROP -- low-ROI noise): style preferences, aesthetic
  rewrites, DRY violations on 2-3 lines, missing docstrings on
  internal helpers, pattern migrations where current code works,
  library swaps for minor gains, naming preferences, theoretical
  improvements with no measurable user impact.

## Task
Classify each current gap into TIER 1/2/3. Then decide:
  - "continue" -- gaps are still dominated by TIER 1/2; loop should
    continue unchanged.
  - "drop-tier3-continue" -- >= 70%% of gaps are TIER 3 AND text-
    similar gaps appeared in prior rounds; drop TIER 3 and continue
    with TIER 1/2 only.
  - "drop-tier3-complete" -- all remaining gaps after the TIER 3
    drop are zero; loop can declare SIMPLE LOOP COMPLETE.

Output STRICT JSON with exactly these fields (no markdown fences,
no prose outside the object):
{
  "tier1": ["gap text", ...],
  "tier2": ["gap text", ...],
  "tier3": ["gap text", ...],
  "recurring": true|false,
  "decision": "continue" | "drop-tier3-continue" | "drop-tier3-complete",
  "rationale": "one-line explanation"
}

## Current round gaps
%s

## Prior rounds (for recurrence detection)
%s
`

// tierFilterReviewFunc is the reviewer callback used by applyTierFilter.
// Accepting it as a parameter lets tests stub the reviewer without
// spawning a real codex/CC subprocess. Production callers pass a
// closure that wraps reviewCall(dir, prompt).
type tierFilterReviewFunc func(ctx context.Context, prompt string) string

// applyTierFilter is the H-19 entrypoint. It builds the classifier
// prompt from the current gaps + prior-rounds gaps, dispatches to the
// reviewer, parses the JSON verdict, applies the never-TIER-3 allowlist
// post-hoc, and returns a tierFilterResult with the final decision.
//
// Fail-open semantics: any error path (empty reviewer response,
// malformed JSON, invalid decision value, reviewer rate-limit) returns
// Decision="continue" with the ORIGINAL gaps preserved in
// RemainingGaps. This is the whole point of fail-open — the filter
// must never cause the outer loop to silently drop real findings.
//
// The filter is called AT MOST ONCE per Final-review fix loop (the
// simple-loop gates it behind `round >= tierFilterAfter`), so the
// overhead is one extra reviewer turn on failing SOWs and zero on
// happy-path SOWs that converge before the threshold.
func applyTierFilter(
	ctx context.Context,
	review tierFilterReviewFunc,
	currentGaps []string,
	priorRoundsGaps [][]string,
	round int,
	dominanceThreshold float64,
) (*tierFilterResult, error) {
	// Fail-open result template. We mutate fields as the happy path
	// proceeds; on any error we `return failOpen, err` with the
	// original gaps preserved.
	failOpen := &tierFilterResult{
		Decision:      "continue",
		Rationale:     "filter declined; defaulting to continue",
		RemainingGaps: append([]string{}, currentGaps...),
	}

	if len(currentGaps) == 0 {
		// Nothing to classify — the outer loop should have taken the
		// "reviewer approved" branch. Be defensive and declare complete.
		failOpen.Decision = "drop-tier3-complete"
		failOpen.Rationale = "no gaps to classify"
		failOpen.RemainingGaps = nil
		return failOpen, nil
	}

	prompt := fmt.Sprintf(simpleLoopTierFilterPromptTemplate,
		formatGapsForPrompt(currentGaps),
		formatPriorRoundsForPrompt(priorRoundsGaps),
	)

	raw := strings.TrimSpace(review(ctx, prompt))
	if raw == "" {
		failOpen.Rationale = "reviewer returned empty response; fail-open"
		return failOpen, fmt.Errorf("tier filter: empty reviewer response")
	}

	parsed, err := parseTierFilterJSON(raw)
	if err != nil {
		failOpen.Rationale = "malformed reviewer JSON; fail-open: " + err.Error()
		return failOpen, err
	}

	// Snapshot pre-promotion share so the threshold check reflects the
	// reviewer's own classification of noise. If we measured share on
	// the post-promotion buckets, promoting a safety item would
	// mechanically push TIER 3 below 70% even when the reviewer
	// correctly saw the set as noise-dominated — we'd stop dropping
	// noise every time the allowlist fires, which defeats the point.
	rawTotal := len(parsed.Tier1) + len(parsed.Tier2) + len(parsed.Tier3)
	rawTier3Share := 0.0
	if rawTotal > 0 {
		rawTier3Share = float64(len(parsed.Tier3)) / float64(rawTotal)
	}

	// Promote any gaps matching the never-TIER-3 allowlist from
	// whatever bucket the reviewer put them in up to TIER 1. Do this
	// on the RAW parse output before we make drop decisions — otherwise
	// the allowlist wouldn't save a misclassified SQL-injection gap.
	promoted := promoteAllowlistGaps(parsed)

	result := &tierFilterResult{
		Tier1Count:    len(promoted.Tier1),
		Tier2Count:    len(promoted.Tier2),
		Tier3Count:    len(promoted.Tier3),
		Recurring:     promoted.Recurring,
		Decision:      promoted.Decision,
		Rationale:     promoted.Rationale,
		RemainingGaps: append([]string{}, currentGaps...),
	}

	// Validate the reviewer's decision. If it's not one of the three
	// expected values, fail-open to continue. A bogus decision is
	// treated the same as malformed JSON — we won't drop gaps on it.
	switch result.Decision {
	case "continue", "drop-tier3-continue", "drop-tier3-complete":
		// ok
	default:
		failOpen.Rationale = "reviewer returned unknown decision '" + result.Decision + "'; fail-open"
		return failOpen, fmt.Errorf("tier filter: unknown decision %q", result.Decision)
	}

	// Independent cross-check of the drop decision. Even if the
	// reviewer says "drop-tier3-*", we only honour it when BOTH of
	// these hold:
	//   (a) TIER 3 is actually >= dominanceThreshold of the total
	//       in the RAW reviewer classification (rawTier3Share) — this
	//       way the allowlist promoting a safety item from TIER 3 to
	//       TIER 1 doesn't mechanically kill a legitimate drop.
	//   (b) We have prior rounds to compare against (recurring==true
	//       is the reviewer's call, but we re-check via text similarity
	//       below so a stale reviewer can't drop on the first round).
	// If either check fails, downgrade to "continue" — better to loop
	// one more round than to drop real gaps.
	recurringVerified := result.Recurring && hasRecurringGaps(promoted.Tier3, priorRoundsGaps)

	if result.Decision == "drop-tier3-continue" || result.Decision == "drop-tier3-complete" {
		if rawTier3Share < dominanceThreshold || !recurringVerified {
			// Reviewer wanted to drop but the math doesn't support it
			// (either not dominated or not recurring). Downgrade.
			result.Decision = "continue"
			result.Rationale = fmt.Sprintf(
				"filter declined drop: rawTier3Share=%.2f (threshold=%.2f) recurringVerified=%v; %s",
				rawTier3Share, dominanceThreshold, recurringVerified, result.Rationale)
			result.Tier3Dropped = nil
			result.Recurring = recurringVerified
			return result, nil
		}
		// Drop is legitimate. Record which gaps got dropped.
		result.Tier3Dropped = append([]string{}, promoted.Tier3...)
		result.Recurring = recurringVerified
		result.RemainingGaps = append(append([]string{}, promoted.Tier1...), promoted.Tier2...)

		// drop-tier3-complete requires ZERO remaining after drop; if
		// there's anything left in TIER 1/2, downgrade to
		// drop-tier3-continue so the caller still iterates.
		if result.Decision == "drop-tier3-complete" && len(result.RemainingGaps) > 0 {
			result.Decision = "drop-tier3-continue"
			result.Rationale = "reviewer said complete but TIER-1/2 remain; downgraded to continue; " + result.Rationale
		}
		// Inverse: drop-tier3-continue with ZERO remaining after drop
		// is effectively complete; promote so the caller exits.
		if result.Decision == "drop-tier3-continue" && len(result.RemainingGaps) == 0 {
			result.Decision = "drop-tier3-complete"
		}
	}
	return result, nil
}

// tierFilterJSON is the strict shape we expect back from the reviewer.
// Extra fields are ignored; missing fields default to zero values,
// which the caller treats as "fail-open continue".
type tierFilterJSON struct {
	Tier1     []string `json:"tier1"`
	Tier2     []string `json:"tier2"`
	Tier3     []string `json:"tier3"`
	Recurring bool     `json:"recurring"`
	Decision  string   `json:"decision"`
	Rationale string   `json:"rationale"`
}

// parseTierFilterJSON extracts the classifier's JSON object from the
// reviewer's raw response. Handles the common LLM behaviors of
// wrapping JSON in ```json ... ``` fences or prefixing prose before
// the object. Returns the first parseable object; if none is found,
// returns an error and the caller falls back to "continue".
func parseTierFilterJSON(raw string) (*tierFilterJSON, error) {
	// Strip markdown code fences if present. Handles ```json and bare ```.
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```JSON")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)

	// Find the first { ... } block. Reviewers sometimes include a
	// preface like "Here's the classification:" before the object.
	start := strings.Index(trimmed, "{")
	if start < 0 {
		return nil, fmt.Errorf("no JSON object in reviewer response")
	}
	// Find the matching closing brace by counting depth.
	depth := 0
	end := -1
	for i := start; i < len(trimmed); i++ {
		switch trimmed[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i
				break
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("unterminated JSON object in reviewer response")
	}

	body := trimmed[start : end+1]
	var out tierFilterJSON
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}
	return &out, nil
}

// promoteAllowlistGaps moves any gap mentioning a never-TIER-3
// category out of TIER 2 or TIER 3 and into TIER 1. Returns a copy
// with the corrected buckets. The allowlist check is substring-based
// and case-insensitive; this is deliberately generous because false
// positives promote noise to TIER 1 (at worst: one extra iteration)
// while false negatives drop a real vulnerability (catastrophic).
func promoteAllowlistGaps(in *tierFilterJSON) *tierFilterJSON {
	out := &tierFilterJSON{
		Tier1:     append([]string{}, in.Tier1...),
		Recurring: in.Recurring,
		Decision:  in.Decision,
		Rationale: in.Rationale,
	}
	for _, g := range in.Tier2 {
		if isNeverTier3(g) {
			out.Tier1 = append(out.Tier1, g)
		} else {
			out.Tier2 = append(out.Tier2, g)
		}
	}
	for _, g := range in.Tier3 {
		if isNeverTier3(g) {
			out.Tier1 = append(out.Tier1, g)
		} else {
			out.Tier3 = append(out.Tier3, g)
		}
	}
	return out
}

// isNeverTier3 reports whether gap text contains any allowlist
// substring. Case-insensitive; punctuation-insensitive (normalizes
// common separators to spaces so "SQL-injection" matches "sql injection").
func isNeverTier3(gap string) bool {
	normalized := strings.ToLower(gap)
	// Normalize common separators so "SQL-injection", "sql_injection",
	// and "SQL injection" all match the same allowlist entry.
	normalized = strings.NewReplacer("-", " ", "_", " ", "/", " ").Replace(normalized)
	for _, cat := range neverTier3Categories {
		if strings.Contains(normalized, cat) {
			return true
		}
	}
	return false
}

// hasRecurringGaps reports whether any of the TIER-3 gaps currently
// flagged also appear (with textual similarity) in prior rounds.
// Used as an independent check on the reviewer's recurring:true claim
// so the filter can't drop gaps on the very first round just because
// the reviewer hallucinated a recurrence. Empty priorRounds => false.
//
// Similarity is keyword-overlap based: we tokenize each gap down to
// the non-trivial keywords (words >= 4 chars, lowercased, filtered
// against a small stoplist), and count a gap as "recurring" if >= 2
// of its keywords appear in any prior gap's keyword set. This is
// noisy by design — the filter fails-open on ambiguity.
func hasRecurringGaps(currentTier3 []string, priorRounds [][]string) bool {
	if len(currentTier3) == 0 || len(priorRounds) == 0 {
		return false
	}
	// Flatten prior rounds into a single keyword index. We don't need
	// per-round granularity — presence in ANY prior round is enough.
	priorKeywords := map[string]bool{}
	for _, round := range priorRounds {
		for _, gap := range round {
			for _, kw := range gapKeywords(gap) {
				priorKeywords[kw] = true
			}
		}
	}
	if len(priorKeywords) == 0 {
		return false
	}
	// A tier-3 gap "recurs" when its keyword set overlaps prior
	// rounds' keywords by either:
	//   - >=2 distinct keywords (strong signal); OR
	//   - >=1 keyword when the gap itself only had 1 keyword to begin
	//     with (short gap texts like "naming" can't clear the 2-bar
	//     without being unfairly penalized for being terse).
	// Either rule firing on ANY tier-3 gap is enough to call the set
	// "recurring" — we already have the reviewer's recurring:true as a
	// gate, this is just guarding against total hallucination.
	for _, gap := range currentTier3 {
		kws := gapKeywords(gap)
		if len(kws) == 0 {
			continue
		}
		hit := 0
		for _, kw := range kws {
			if priorKeywords[kw] {
				hit++
			}
		}
		if hit >= 2 {
			return true
		}
		if hit >= 1 && len(kws) <= 2 {
			return true
		}
	}
	return false
}

// gapKeywordSplitter matches runs of word characters so we can
// tokenize free-form gap text. Compiled once at package init.
var gapKeywordSplitter = regexp.MustCompile(`[A-Za-z][A-Za-z0-9]+`)

// gapStoplist suppresses words that appear in nearly every gap and
// therefore carry no signal about recurrence. Kept short so the
// keyword match still has teeth.
var gapStoplist = map[string]bool{
	"tier": true, "tier1": true, "tier2": true, "tier3": true,
	"missing": true, "gap": true, "issue": true, "fix": true,
	"the": true, "and": true, "for": true, "with": true, "this": true,
	"that": true, "from": true, "into": true, "code": true, "file": true,
	"line": true, "should": true, "could": true, "would": true,
}

func gapKeywords(gap string) []string {
	words := gapKeywordSplitter.FindAllString(gap, -1)
	var out []string
	for _, w := range words {
		lw := strings.ToLower(w)
		if len(lw) < 4 || gapStoplist[lw] {
			continue
		}
		out = append(out, lw)
	}
	return out
}

// formatGapsForPrompt renders a gap list as a numbered bullet
// block for the classifier prompt. Keeps each gap on its own line
// so the reviewer can reference them by text when producing JSON.
func formatGapsForPrompt(gaps []string) string {
	if len(gaps) == 0 {
		return "(no gaps)"
	}
	var b strings.Builder
	for i, g := range gaps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, strings.TrimSpace(g))
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatPriorRoundsForPrompt summarizes the prior rounds' gaps into a
// compact per-round block for the prompt. Bounded per round and in
// total so a pathological history doesn't explode the prompt.
func formatPriorRoundsForPrompt(priorRounds [][]string) string {
	if len(priorRounds) == 0 {
		return "(no prior rounds)"
	}
	const maxPerRound = 10
	const maxRounds = 10
	var b strings.Builder
	start := 0
	if len(priorRounds) > maxRounds {
		start = len(priorRounds) - maxRounds
		fmt.Fprintf(&b, "(showing last %d of %d rounds)\n", maxRounds, len(priorRounds))
	}
	for i, round := range priorRounds[start:] {
		fmt.Fprintf(&b, "Round %d:\n", start+i+1)
		if len(round) == 0 {
			b.WriteString("  (none)\n")
			continue
		}
		shown := round
		truncated := false
		if len(shown) > maxPerRound {
			shown = shown[:maxPerRound]
			truncated = true
		}
		for _, g := range shown {
			fmt.Fprintf(&b, "  - %s\n", strings.TrimSpace(g))
		}
		if truncated {
			fmt.Fprintf(&b, "  ... (+%d more)\n", len(round)-maxPerRound)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// extractGapsFromReview is a best-effort parser that pulls discrete
// gap entries out of a free-form reviewer response so we can feed them
// to applyTierFilter. Looks for bullets ("- ", "* "), numbered lines
// ("1. ", "1) "), and "Issue:" prefixes. Returns the lines verbatim
// (trimmed), preserving order. If nothing matches the bullet heuristic,
// returns the whole response as a single gap so the classifier still
// has something to work with.
//
// Kept here (not in simple_loop.go) because it's tightly coupled to
// the tier-filter flow and lives alongside the classifier logic.
func extractGapsFromReview(review string) []string {
	var out []string
	for _, line := range strings.Split(review, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Bullet / numbered / issue-prefix heuristics — same set as
		// splitReviewIntoIssues in simple_loop.go.
		isBullet := strings.HasPrefix(trimmed, "- ") ||
			strings.HasPrefix(trimmed, "* ") ||
			strings.HasPrefix(strings.ToLower(trimmed), "issue:") ||
			(len(trimmed) > 2 && trimmed[0] >= '0' && trimmed[0] <= '9' &&
				(trimmed[1] == '.' || trimmed[1] == ')'))
		if !isBullet {
			continue
		}
		// Strip the bullet prefix for cleaner classifier input.
		cleaned := trimmed
		for _, prefix := range []string{"- ", "* "} {
			if strings.HasPrefix(cleaned, prefix) {
				cleaned = cleaned[len(prefix):]
				break
			}
		}
		if len(cleaned) > 2 && cleaned[0] >= '0' && cleaned[0] <= '9' &&
			(cleaned[1] == '.' || cleaned[1] == ')') {
			cleaned = strings.TrimSpace(cleaned[2:])
		}
		if strings.HasPrefix(strings.ToLower(cleaned), "issue:") {
			cleaned = strings.TrimSpace(cleaned[len("issue:"):])
		}
		if cleaned != "" {
			out = append(out, cleaned)
		}
	}
	if len(out) == 0 {
		// No bullets — treat the whole response as one gap rather than
		// returning empty (which would make the classifier think we're
		// done). TrimSpace so a short response doesn't produce "".
		t := strings.TrimSpace(review)
		if t != "" {
			out = []string{t}
		}
	}
	return out
}

// assessPriorReviewQuality returns a one-line verdict describing the
// shape of findings the reviewer has produced across prior rounds. The
// self-aware reviewer loop uses this verdict in its adaptive preamble —
// "your reviews have been [X]" primes the reviewer to match the right
// bar for this round.
//
// Possible verdicts:
//   - "insufficient-data": fewer than 2 rounds observed
//   - "polish-only":       every finding reads like polish (style, config
//                          nits, could-also-add, naming, documentation)
//   - "real-issues":       at least one finding in the window names a
//                          blocker (build fail, test fail, crash, security)
//   - "mixed":             both categories present in the window
//
// Window: looks at the most recent `assessWindow` rounds (default 3) so
// a long run doesn't drown out recent quality signal.
func assessPriorReviewQuality(priorRoundsGaps [][]string) string {
	const assessWindow = 3
	if len(priorRoundsGaps) < 2 {
		return "insufficient-data"
	}
	start := len(priorRoundsGaps) - assessWindow
	if start < 0 {
		start = 0
	}
	window := priorRoundsGaps[start:]

	// Polish markers — substrings that flag "style/nice-to-have" findings.
	polishMarkers := []string{
		"naming", "nit", "could also", "consider", "prefer", "minor",
		"suggest", "idiomatic", "convention", "formatting", "style",
		"could be more", "documentation", "docs", "comment", "typo",
		"alias", "rename", "refactor to", "extract", "dry",
	}
	// Blocker markers — explicit "this breaks" language.
	blockerMarkers := []string{
		"fails", "broken", "crash", "throws", "error", "missing",
		"does not exist", "doesn't exist", "not implemented",
		"build fails", "tests fail", "test failure", "compile error",
		"type error", "security", "injection", "bypass", "leak",
		"segfault", "panic", "undefined behavior",
	}

	sawPolish, sawBlocker := false, false
	for _, round := range window {
		for _, finding := range round {
			lower := strings.ToLower(finding)
			for _, m := range blockerMarkers {
				if strings.Contains(lower, m) {
					sawBlocker = true
					break
				}
			}
			for _, m := range polishMarkers {
				if strings.Contains(lower, m) {
					sawPolish = true
					break
				}
			}
		}
	}

	switch {
	case sawBlocker && sawPolish:
		return "mixed"
	case sawBlocker:
		return "real-issues"
	case sawPolish:
		return "polish-only"
	default:
		// No markers matched — treat as "real" to be conservative.
		// The default path where the reviewer is returning prose
		// without recognizable keywords shouldn't auto-skip.
		return "real-issues"
	}
}
