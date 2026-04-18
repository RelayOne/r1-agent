// tier_filter_test.go — H-19 tests.
//
// Covers the 8 required scenarios from the task spec:
//
//  1. Filter not called when round < threshold (enforced at call site;
//     here we verify applyTierFilter with 0 gaps is a no-op).
//  2. TIER-3-dominated + recurring → drop-tier3-continue with allowlist
//     promotion preserving the SQL-injection gap as TIER 1.
//  3. TIER-3-dominated + recurring + all-dropped → drop-tier3-complete.
//  4. Real issues remaining → decision downgrades to "continue".
//  5. Never-TIER-3 allowlist: reviewer classified a CSRF gap as TIER 3,
//     filter post-process promotes it to TIER 1.
//  6. Reviewer errors (empty response) → fail-open continue with
//     original gaps preserved.
//  7. Malformed JSON → fail-open continue.
//  8. Unknown decision value → fail-open continue.

package main

import (
	"context"
	"strings"
	"testing"
)

// stubReviewFunc builds a tierFilterReviewFunc that returns a fixed
// body and records the call count so tests can assert invocation.
func stubReviewFunc(body string, callCount *int) tierFilterReviewFunc {
	return func(_ context.Context, _ string) string {
		if callCount != nil {
			*callCount++
		}
		return body
	}
}

// TestApplyTierFilter_NoGapsShortCircuits is the #1-ish case: if
// somehow we're called with zero gaps, the function declares complete
// without invoking the reviewer. (The call site gates on round >=
// threshold; this test confirms the function itself is defensive.)
func TestApplyTierFilter_NoGapsShortCircuits(t *testing.T) {
	calls := 0
	result, err := applyTierFilter(
		context.Background(),
		stubReviewFunc("SHOULD NOT BE CALLED", &calls),
		nil, // no current gaps
		nil,
		5,
		0.7,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("reviewer should not be called when gaps empty; calls=%d", calls)
	}
	if result.Decision != "drop-tier3-complete" {
		t.Fatalf("empty-gaps should be drop-tier3-complete, got %q", result.Decision)
	}
	if len(result.RemainingGaps) != 0 {
		t.Fatalf("empty-gaps result must have no remaining gaps; got %v", result.RemainingGaps)
	}
}

// TestApplyTierFilter_Tier3DominatedRecurringContinue is scenario #2:
// 5 gaps where 4 are missing-docstring TIER 3 and 1 is SQL injection.
// Prior rounds contain similar docstring gaps, so the filter recurrence
// check passes. Outcome: drop-tier3-continue, the SQL injection gap
// remains (kept via never-TIER-3 allowlist).
func TestApplyTierFilter_Tier3DominatedRecurringContinue(t *testing.T) {
	reviewBody := `{
  "tier1": ["SQL injection in user search endpoint"],
  "tier2": [],
  "tier3": [
    "missing docstring on internal helper foo",
    "missing docstring on internal helper bar",
    "missing docstring on internal helper baz",
    "missing docstring on internal helper qux"
  ],
  "recurring": true,
  "decision": "drop-tier3-continue",
  "rationale": "4/5 are docstring noise already seen in round 2"
}`
	current := []string{
		"SQL injection in user search endpoint",
		"missing docstring on internal helper foo",
		"missing docstring on internal helper bar",
		"missing docstring on internal helper baz",
		"missing docstring on internal helper qux",
	}
	prior := [][]string{
		{"missing docstring on internal helper foo",
			"missing docstring on internal helper bar"},
		{"missing docstring on internal helper baz"},
	}

	result, err := applyTierFilter(
		context.Background(),
		stubReviewFunc(reviewBody, nil),
		current, prior, 5, 0.7,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Decision != "drop-tier3-continue" {
		t.Fatalf("decision=%q want drop-tier3-continue; rationale=%s",
			result.Decision, result.Rationale)
	}
	if result.Tier1Count != 1 {
		t.Fatalf("Tier1Count=%d want 1 (SQL injection promoted/kept)", result.Tier1Count)
	}
	if result.Tier3Count != 4 {
		t.Fatalf("Tier3Count=%d want 4", result.Tier3Count)
	}
	if len(result.Tier3Dropped) != 4 {
		t.Fatalf("Tier3Dropped=%v want 4 dropped", result.Tier3Dropped)
	}
	if len(result.RemainingGaps) != 1 || !strings.Contains(result.RemainingGaps[0], "SQL injection") {
		t.Fatalf("RemainingGaps=%v want single SQL-injection gap", result.RemainingGaps)
	}
	if !result.Recurring {
		t.Fatalf("Recurring should be true given shared docstring keywords in prior rounds")
	}
}

// TestApplyTierFilter_Tier3DominatedRecurringAllDropped is scenario #3:
// every gap is style / aesthetic TIER 3 and all are recurring. After
// the drop there's nothing left, so the filter signals
// drop-tier3-complete.
func TestApplyTierFilter_Tier3DominatedRecurringAllDropped(t *testing.T) {
	reviewBody := `{
  "tier1": [],
  "tier2": [],
  "tier3": [
    "prefer arrow function syntax over function keyword",
    "rename variable counter to count",
    "extract duplicated 3-line helper into utility"
  ],
  "recurring": true,
  "decision": "drop-tier3-complete",
  "rationale": "all remaining gaps are style preferences already rejected in prior rounds"
}`
	current := []string{
		"prefer arrow function syntax over function keyword",
		"rename variable counter to count",
		"extract duplicated 3-line helper into utility",
	}
	prior := [][]string{
		{"prefer arrow function syntax over function keyword"},
		{"rename variable counter to count",
			"extract duplicated 3-line helper into utility"},
	}

	result, err := applyTierFilter(
		context.Background(),
		stubReviewFunc(reviewBody, nil),
		current, prior, 5, 0.7,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Decision != "drop-tier3-complete" {
		t.Fatalf("decision=%q want drop-tier3-complete; rationale=%s",
			result.Decision, result.Rationale)
	}
	if len(result.RemainingGaps) != 0 {
		t.Fatalf("RemainingGaps=%v want empty", result.RemainingGaps)
	}
	if len(result.Tier3Dropped) != 3 {
		t.Fatalf("Tier3Dropped=%v want all 3 dropped", result.Tier3Dropped)
	}
}

// TestApplyTierFilter_RealIssuesRemainingContinue is scenario #4:
// 5 gaps, 3 security/auth TIER 1 + 2 style TIER 3. Reviewer correctly
// returns "continue"; filter must pass it through. Also checks the
// tier3Share threshold gate: 2/5 = 40% is below 70%, so even if the
// reviewer had said drop, we'd downgrade to continue.
func TestApplyTierFilter_RealIssuesRemainingContinue(t *testing.T) {
	reviewBody := `{
  "tier1": [
    "auth bypass on /admin/users",
    "missing authentication on /api/config",
    "SQL injection in login flow"
  ],
  "tier2": [],
  "tier3": [
    "rename helper from get to fetch",
    "prefer const over let in test file"
  ],
  "recurring": false,
  "decision": "continue",
  "rationale": "3 TIER-1 security issues dominate"
}`
	current := []string{
		"auth bypass on /admin/users",
		"missing authentication on /api/config",
		"SQL injection in login flow",
		"rename helper from get to fetch",
		"prefer const over let in test file",
	}
	prior := [][]string{
		{"rename helper from get to fetch"},
	}

	result, err := applyTierFilter(
		context.Background(),
		stubReviewFunc(reviewBody, nil),
		current, prior, 5, 0.7,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Decision != "continue" {
		t.Fatalf("decision=%q want continue", result.Decision)
	}
	if result.Tier1Count != 3 {
		t.Fatalf("Tier1Count=%d want 3", result.Tier1Count)
	}
	if len(result.Tier3Dropped) != 0 {
		t.Fatalf("Tier3Dropped=%v want empty when decision=continue", result.Tier3Dropped)
	}
	if len(result.RemainingGaps) != len(current) {
		t.Fatalf("RemainingGaps=%v want all 5 preserved", result.RemainingGaps)
	}
}

// TestApplyTierFilter_NeverTier3Allowlist is scenario #5: the reviewer
// LLM wrongly classifies "missing CSRF check on /api/admin" as TIER 3.
// Post-processing MUST promote it to TIER 1 because CSRF is on the
// never-TIER-3 allowlist. Result: decision downgrades from
// drop-tier3-complete to drop-tier3-continue (since TIER 1 is
// non-empty after promotion) and the CSRF gap survives.
func TestApplyTierFilter_NeverTier3Allowlist(t *testing.T) {
	reviewBody := `{
  "tier1": [],
  "tier2": [],
  "tier3": [
    "missing CSRF check on /api/admin",
    "inconsistent naming: fooBar vs foo_bar"
  ],
  "recurring": true,
  "decision": "drop-tier3-complete",
  "rationale": "both are style in my view"
}`
	current := []string{
		"missing CSRF check on /api/admin",
		"inconsistent naming: fooBar vs foo_bar",
	}
	prior := [][]string{
		{"missing CSRF check on /api/admin",
			"inconsistent naming: fooBar vs foo_bar"},
	}

	result, err := applyTierFilter(
		context.Background(),
		stubReviewFunc(reviewBody, nil),
		current, prior, 5, 0.7,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Tier1Count != 1 {
		t.Fatalf("Tier1Count=%d want 1 (CSRF promoted from TIER 3)", result.Tier1Count)
	}
	// CSRF was promoted so the filter can't declare complete. Should
	// have downgraded to drop-tier3-continue.
	if result.Decision != "drop-tier3-continue" {
		t.Fatalf("decision=%q want drop-tier3-continue after CSRF promotion; rationale=%s",
			result.Decision, result.Rationale)
	}
	// Remaining gaps must include the CSRF gap.
	if len(result.RemainingGaps) != 1 || !strings.Contains(result.RemainingGaps[0], "CSRF") {
		t.Fatalf("RemainingGaps=%v want only the CSRF gap", result.RemainingGaps)
	}
}

// TestApplyTierFilter_ReviewerEmptyFailOpen is scenario #6: the
// reviewer returns an empty string (rate-limit, killed, whatever). The
// filter MUST return Decision=continue with the ORIGINAL gaps intact
// so the outer loop keeps iterating. Also returns a non-nil error so
// the caller can log it.
func TestApplyTierFilter_ReviewerEmptyFailOpen(t *testing.T) {
	current := []string{
		"something the reviewer should classify",
		"another thing",
	}
	result, err := applyTierFilter(
		context.Background(),
		stubReviewFunc("", nil), // empty body
		current, nil, 5, 0.7,
	)
	if err == nil {
		t.Fatalf("expected non-nil error on empty reviewer response")
	}
	if result.Decision != "continue" {
		t.Fatalf("decision=%q want continue on reviewer failure", result.Decision)
	}
	if len(result.RemainingGaps) != len(current) {
		t.Fatalf("RemainingGaps=%v want original gaps preserved on fail-open",
			result.RemainingGaps)
	}
	for i := range current {
		if result.RemainingGaps[i] != current[i] {
			t.Fatalf("RemainingGaps[%d]=%q want %q", i, result.RemainingGaps[i], current[i])
		}
	}
}

// TestApplyTierFilter_MalformedJSONFailOpen is scenario #7: the
// reviewer returns prose without any JSON. Same fail-open contract as
// #6: decision=continue, gaps preserved.
func TestApplyTierFilter_MalformedJSONFailOpen(t *testing.T) {
	current := []string{"gap-a", "gap-b"}
	result, err := applyTierFilter(
		context.Background(),
		stubReviewFunc("I think this is fine, continue the loop.", nil),
		current, nil, 5, 0.7,
	)
	if err == nil {
		t.Fatalf("expected non-nil error on garbage response")
	}
	if result.Decision != "continue" {
		t.Fatalf("decision=%q want continue on malformed JSON", result.Decision)
	}
	if len(result.RemainingGaps) != 2 {
		t.Fatalf("RemainingGaps=%v want 2 (originals preserved)", result.RemainingGaps)
	}
}

// TestApplyTierFilter_UnknownDecisionFailOpen is scenario #8: JSON
// parses cleanly but the decision field is a value we don't recognize
// (e.g. the reviewer made one up). Same fail-open contract — NEVER
// drop gaps on an unrecognized decision.
func TestApplyTierFilter_UnknownDecisionFailOpen(t *testing.T) {
	reviewBody := `{
  "tier1": [],
  "tier2": [],
  "tier3": ["something"],
  "recurring": false,
  "decision": "ship-it-anyway",
  "rationale": "I vibe with it"
}`
	current := []string{"something"}
	result, err := applyTierFilter(
		context.Background(),
		stubReviewFunc(reviewBody, nil),
		current, nil, 5, 0.7,
	)
	if err == nil {
		t.Fatalf("expected non-nil error on unknown decision")
	}
	if result.Decision != "continue" {
		t.Fatalf("decision=%q want continue on unknown decision", result.Decision)
	}
	if len(result.RemainingGaps) != 1 || result.RemainingGaps[0] != "something" {
		t.Fatalf("RemainingGaps=%v want original preserved on fail-open", result.RemainingGaps)
	}
}

// TestApplyTierFilter_JSONFencedRecognized verifies the parser strips
// markdown code fences when the reviewer helpfully wraps its JSON. No
// fail-open should be triggered in this path — the decision must land
// correctly.
func TestApplyTierFilter_JSONFencedRecognized(t *testing.T) {
	reviewBody := "```json\n{\n  \"tier1\": [],\n  \"tier2\": [],\n  \"tier3\": [\"naming\"],\n  \"recurring\": true,\n  \"decision\": \"drop-tier3-complete\",\n  \"rationale\": \"all style\"\n}\n```"
	prior := [][]string{{"naming preference helper"}} // recurrence keyword match
	result, err := applyTierFilter(
		context.Background(),
		stubReviewFunc(reviewBody, nil),
		[]string{"naming preference on helper"}, prior, 5, 0.7,
	)
	if err != nil {
		t.Fatalf("unexpected error on fenced JSON: %v", err)
	}
	if result.Decision != "drop-tier3-complete" {
		t.Fatalf("decision=%q want drop-tier3-complete; rationale=%s",
			result.Decision, result.Rationale)
	}
}

// TestApplyTierFilter_DropWithoutPriorRoundsDowngrades is a safety
// test for the recurrence gate: reviewer says drop-tier3-* with
// recurring:true but no prior rounds exist to corroborate. We
// downgrade to continue because recurrence can't be verified.
func TestApplyTierFilter_DropWithoutPriorRoundsDowngrades(t *testing.T) {
	reviewBody := `{
  "tier1": [],
  "tier2": [],
  "tier3": ["style-1", "style-2", "style-3"],
  "recurring": true,
  "decision": "drop-tier3-complete",
  "rationale": "all style"
}`
	result, err := applyTierFilter(
		context.Background(),
		stubReviewFunc(reviewBody, nil),
		[]string{"style-1", "style-2", "style-3"},
		nil, // no prior rounds!
		5, 0.7,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Decision != "continue" {
		t.Fatalf("decision=%q want continue when no prior rounds to verify recurrence",
			result.Decision)
	}
	if len(result.RemainingGaps) != 3 {
		t.Fatalf("RemainingGaps=%v want originals preserved", result.RemainingGaps)
	}
}

// TestApplyTierFilter_BelowThresholdDowngrades verifies the
// dominanceThreshold gate: reviewer wants drop but TIER 3 share is
// below the threshold. Filter should downgrade to continue regardless
// of what the reviewer said.
func TestApplyTierFilter_BelowThresholdDowngrades(t *testing.T) {
	// 2 TIER-3 out of 5 total = 40% share, below 70% threshold.
	reviewBody := `{
  "tier1": ["real security issue A", "real security issue B", "real security issue C"],
  "tier2": [],
  "tier3": ["style-1", "style-2"],
  "recurring": true,
  "decision": "drop-tier3-continue",
  "rationale": "wants to drop style"
}`
	prior := [][]string{{"style-1 keyword", "style-2 keyword"}}
	result, err := applyTierFilter(
		context.Background(),
		stubReviewFunc(reviewBody, nil),
		[]string{"real security issue A", "real security issue B", "real security issue C",
			"style-1", "style-2"},
		prior, 5, 0.7,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Decision != "continue" {
		t.Fatalf("decision=%q want continue (40%% tier3 < 70%% threshold)", result.Decision)
	}
}

// TestIsNeverTier3 exercises the allowlist matcher against each
// category plus a few negative cases. Case-insensitivity and separator
// normalization ("SQL-injection" / "sql_injection") must both work.
func TestIsNeverTier3(t *testing.T) {
	cases := []struct {
		gap  string
		want bool
	}{
		{"SQL injection in login", true},
		{"SQL-injection in login", true},     // hyphen normalized
		{"sql_injection vector", true},        // underscore normalized
		{"Auth bypass on /admin", true},
		{"missing CSRF check", true},
		{"hardcoded API key in config.js", true},
		{"data loss risk in purge path", true},
		{"race condition on writes", true},
		{"unhandled panic in worker", true},
		{"missing rate limit on /login", true},
		{"CORS wildcard on /api with credentials", true},
		// Negatives
		{"missing JSDoc on internal helper", false},
		{"prefer arrow function over function keyword", false},
		{"rename variable counter to count", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isNeverTier3(c.gap); got != c.want {
			t.Errorf("isNeverTier3(%q)=%v want %v", c.gap, got, c.want)
		}
	}
}

// TestParseTierFilterJSON covers the common LLM response shapes:
// bare JSON, code-fenced JSON, JSON with a prose preface. Malformed
// shapes (no object at all) return an error.
func TestParseTierFilterJSON(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"bare", `{"tier1":[],"tier2":[],"tier3":[],"recurring":false,"decision":"continue","rationale":"x"}`, false},
		{"fenced", "```json\n{\"tier1\":[],\"tier2\":[],\"tier3\":[],\"recurring\":false,\"decision\":\"continue\",\"rationale\":\"x\"}\n```", false},
		{"fenced-bare", "```\n{\"tier1\":[],\"tier2\":[],\"tier3\":[],\"recurring\":false,\"decision\":\"continue\",\"rationale\":\"x\"}\n```", false},
		{"prose-preface", "Here is my analysis:\n\n{\"tier1\":[],\"tier2\":[],\"tier3\":[],\"recurring\":false,\"decision\":\"continue\",\"rationale\":\"x\"}\n\nEnd.", false},
		{"no-object", "I don't think this is a real problem.", true},
		{"unterminated", "{\"tier1\":[],", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseTierFilterJSON(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, c.wantErr)
			}
		})
	}
}

// TestExtractGapsFromReview covers the bullet / numbered / issue:
// heuristic and the single-line fallback.
func TestExtractGapsFromReview(t *testing.T) {
	in := `The following problems remain:
- missing CSRF check on /api/admin
- SQL injection in search endpoint
1. missing docstring on helper foo
2) missing docstring on helper bar
Issue: hardcoded token in config.ts
plain prose line that isn't a bullet should be skipped`
	gaps := extractGapsFromReview(in)
	if len(gaps) != 5 {
		t.Fatalf("len(gaps)=%d want 5; gaps=%v", len(gaps), gaps)
	}
	wantSubstrings := []string{
		"CSRF", "SQL injection", "docstring on helper foo",
		"docstring on helper bar", "hardcoded token",
	}
	for i, want := range wantSubstrings {
		if !strings.Contains(gaps[i], want) {
			t.Errorf("gaps[%d]=%q want substring %q", i, gaps[i], want)
		}
	}
}

// TestExtractGapsFromReview_SingleLineFallback confirms that a review
// with no bullet markers returns the whole text as one gap (so the
// classifier still has material).
func TestExtractGapsFromReview_SingleLineFallback(t *testing.T) {
	in := "Just one long finding without any bullets here."
	gaps := extractGapsFromReview(in)
	if len(gaps) != 1 || gaps[0] != in {
		t.Fatalf("gaps=%v want single full-text gap", gaps)
	}
	// Empty input returns empty.
	if gaps := extractGapsFromReview(""); len(gaps) != 0 {
		t.Fatalf("empty review should yield no gaps; got %v", gaps)
	}
}

// TestGapKeywords sanity-checks the tokenizer used by recurrence
// detection. Short words and stoplist words are suppressed; real
// tokens survive.
func TestGapKeywords(t *testing.T) {
	got := gapKeywords("MISSING: docstring on helper foo")
	// "MISSING" and "on" drop (stoplist / too short); others survive.
	if !containsAll(got, []string{"docstring", "helper"}) {
		t.Fatalf("keywords=%v missing expected tokens", got)
	}
	for _, stop := range []string{"the", "and", "missing"} {
		for _, g := range got {
			if g == stop {
				t.Fatalf("stoplist word %q leaked into keywords %v", stop, got)
			}
		}
	}
}

func containsAll(have []string, want []string) bool {
	set := map[string]bool{}
	for _, h := range have {
		set[h] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}
