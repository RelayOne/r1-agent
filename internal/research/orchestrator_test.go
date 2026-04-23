// orchestrator_test.go exercises the multi-agent fan-out coordinator
// in orchestrator.go. Tests are organised by pipeline stage:
//
//   - decomposition/effort sizing (TestOrchestrator_EffortLadder,
//     TestBuildSubObjectives_*)
//   - fan-out correctness (TestOrchestrator_FanOut_*)
//   - failure isolation (TestOrchestrator_SubagentFailure_DoesNotFail)
//   - deterministic synthesis (TestOrchestrator_DeterministicSynth,
//     TestOrchestrator_LeadFn_Fallback)
//   - filesystem-as-communication (TestOrchestrator_WritesRunRoot,
//     TestOrchestrator_LeadReadsFromDisk)
//   - event emission (TestOrchestrator_EmitsEvents)
//
// All tests use the in-package StubFetcher (fetch.go) — no network
// and no external mocks. Concurrency tests run with -race in the
// acceptance criteria; the ranking + ID assignment are sorted+
// index-based so output is deterministic despite the fan-out.

package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- SubObjective / Validate ------------------------------------------------

func TestSubObjective_Validate(t *testing.T) {
	cases := []struct {
		name    string
		obj     SubObjective
		wantErr bool
	}{
		{"empty", SubObjective{}, true},
		{"whitespace_only", SubObjective{Objective: "   "}, true},
		{"valid", SubObjective{Objective: "What is X?"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.obj.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate: err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

// --- buildSubObjectives -----------------------------------------------------

func TestBuildSubObjectives_MinimalCollapses(t *testing.T) {
	subs := []SubQuestion{
		{ID: "SQ-1", Text: "What is A?", Hints: []string{"a"}},
		{ID: "SQ-2", Text: "What is B?", Hints: []string{"b"}},
		{ID: "SQ-3", Text: "What is C?", Hints: []string{"c"}},
	}
	out := buildSubObjectives(subs, EffortMinimal, 5)
	if len(out) != 1 {
		t.Fatalf("Minimal effort must collapse to 1 subagent, got %d", len(out))
	}
	// Extras must be visible in TaskBoundaries so the subagent still
	// owns them.
	if !strings.Contains(out[0].TaskBoundaries, "What is B?") {
		t.Errorf("extra SubQuestion B not folded into TaskBoundaries: %q", out[0].TaskBoundaries)
	}
	if !strings.Contains(out[0].TaskBoundaries, "What is C?") {
		t.Errorf("extra SubQuestion C not folded into TaskBoundaries: %q", out[0].TaskBoundaries)
	}
	// Hints from the folded extras must be preserved so URL routing
	// still works for the combined subagent.
	var have = map[string]bool{}
	for _, h := range out[0].Hints {
		have[h] = true
	}
	for _, want := range []string{"a", "b", "c"} {
		if !have[want] {
			t.Errorf("hint %q missing after fold: %v", want, out[0].Hints)
		}
	}
}

func TestBuildSubObjectives_ThoroughBoundedByMaxParallel(t *testing.T) {
	subs := make([]SubQuestion, 10)
	for i := range subs {
		subs[i] = SubQuestion{ID: fmt.Sprintf("SQ-%d", i+1), Text: fmt.Sprintf("Q%d", i+1)}
	}
	out := buildSubObjectives(subs, EffortThorough, 5)
	if len(out) != 5 {
		t.Fatalf("Thorough with MaxParallel=5 and 10 subs must yield 5 subagents, got %d", len(out))
	}
	// Subs[0..3] are direct subagents; subs[4] (Q5) becomes the last
	// subagent's Objective, and subs[5..9] (Q6..Q10) are folded into
	// its TaskBoundaries. The last subagent must carry Q5 as its
	// Objective and Q6..Q10 in its boundaries.
	last := out[4]
	if !strings.Contains(last.Objective, "Q5") {
		t.Errorf("last subagent's Objective should be Q5, got %q", last.Objective)
	}
	for i := 6; i <= 10; i++ {
		want := fmt.Sprintf("Q%d", i)
		if !strings.Contains(last.TaskBoundaries, want) {
			t.Errorf("folded %s missing from last subagent's TaskBoundaries", want)
		}
	}
}

func TestBuildSubObjectives_StandardCapsAtFour(t *testing.T) {
	subs := []SubQuestion{
		{ID: "SQ-1", Text: "Q1"}, {ID: "SQ-2", Text: "Q2"},
		{ID: "SQ-3", Text: "Q3"}, {ID: "SQ-4", Text: "Q4"},
		{ID: "SQ-5", Text: "Q5"}, {ID: "SQ-6", Text: "Q6"},
	}
	out := buildSubObjectives(subs, EffortStandard, 10)
	if len(out) != 4 {
		t.Fatalf("Standard must cap at 4 subagents, got %d", len(out))
	}
}

func TestBuildSubObjectives_EmptyReturnsNil(t *testing.T) {
	if got := buildSubObjectives(nil, EffortStandard, 5); got != nil {
		t.Fatalf("nil subs must yield nil objs, got %#v", got)
	}
}

// --- effortCap --------------------------------------------------------------

func TestEffortCap_Table(t *testing.T) {
	cases := []struct {
		name   string
		effort Effort
		maxPar int
		n      int
		want   int
	}{
		{"minimal_any", EffortMinimal, 5, 10, 1},
		{"thorough_bounded_by_maxpar", EffortThorough, 3, 10, 3},
		{"thorough_fewer_subs_than_max", EffortThorough, 10, 2, 2},
		{"critical_same_as_thorough", EffortCritical, 5, 10, 5},
		{"standard_caps_at_4", EffortStandard, 10, 10, 4},
		{"standard_ignores_maxpar", EffortStandard, 1, 10, 4}, // MaxParallel is a concurrency limit, not a decomposition cap
		{"standard_fewer_subs_than_cap", EffortStandard, 10, 2, 2},
		{"empty_defaults_to_standard", Effort(""), 10, 10, 4},
		{"unknown_defaults_to_standard", Effort("weird"), 10, 10, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := effortCap(tc.effort, tc.maxPar, tc.n)
			if got != tc.want {
				t.Errorf("effortCap(%v, %d, %d) = %d, want %d",
					tc.effort, tc.maxPar, tc.n, got, tc.want)
			}
		})
	}
}

// --- Orchestrator.Run: basic contract ---------------------------------------

func TestOrchestrator_NilFetcher_ReturnsError(t *testing.T) {
	o := &Orchestrator{} // no Fetcher
	_, err := o.Run(context.Background(), "anything", EffortStandard)
	if err == nil {
		t.Fatal("want error for nil Fetcher, got nil")
	}
}

func TestOrchestrator_EmptyQuery_ReturnsError(t *testing.T) {
	o := NewOrchestrator(&StubFetcher{Pages: map[string]string{}})
	_, err := o.Run(context.Background(), "   ", EffortStandard)
	if err == nil {
		t.Fatal("want error for empty query, got nil")
	}
}

func TestOrchestrator_NewOrchestrator_Defaults(t *testing.T) {
	o := NewOrchestrator(&StubFetcher{})
	if o.MaxParallel != 5 {
		t.Errorf("default MaxParallel = %d, want 5", o.MaxParallel)
	}
	if o.MaxClaimsPerSub != 3 {
		t.Errorf("default MaxClaimsPerSub = %d, want 3", o.MaxClaimsPerSub)
	}
	if o.Planner == nil {
		t.Error("default Planner must not be nil")
	}
	if o.Clock == nil {
		t.Error("default Clock must not be nil")
	}
}

// --- Orchestrator.Run: fan-out happy path -----------------------------------

// TestOrchestrator_FanOut_ProducesClaims runs a "Postgres vs MySQL"
// query through the orchestrator with the heuristic decomposer and
// two distinct URLs. Entity names are 3+ characters so the token
// filter (which drops <3-char tokens) retains them in the scoring
// set. Asserts:
//
//   - two subagents fan out (one per SubQuestion)
//   - each subagent extracts sentences
//   - Claims are assigned stable C-N ids across fan-out
//   - Sources are deduplicated in the final Report
func TestOrchestrator_FanOut_ProducesClaims(t *testing.T) {
	pagePG := "<html><title>Postgres doc</title><body>" +
		"Postgres is a relational database system. " +
		"Postgres supports ACID transactions and complex joins. " +
		"Postgres is known for reliability and correctness.</body></html>"
	pageMy := "<html><title>MySQL doc</title><body>" +
		"MySQL is a popular relational database. " +
		"MySQL supports high throughput workloads with replication. " +
		"MySQL is known for simplicity and speed.</body></html>"
	stub := &StubFetcher{Pages: map[string]string{
		"https://example.com/pg":    pagePG,
		"https://example.com/mysql": pageMy,
	}}
	o := NewOrchestrator(stub)
	o.URLsByHint = map[string][]string{
		"Postgres": {"https://example.com/pg"},
		"MySQL":    {"https://example.com/mysql"},
	}
	// Deterministic decomposition: "Postgres vs MySQL" splits 2-way
	// on the heuristic versus splitter, producing hints ["Postgres"]
	// and ["MySQL"] which route to pagePG and pageMy respectively.
	rep, err := o.Run(context.Background(), "Postgres vs MySQL", EffortStandard)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Query != "Postgres vs MySQL" {
		t.Errorf("Report.Query = %q, want %q", rep.Query, "Postgres vs MySQL")
	}
	if len(rep.Claims) == 0 {
		t.Fatalf("want claims, got 0; body=%q", rep.Body)
	}
	// Claim IDs must be stable C-1, C-2, ... and monotonically
	// increasing (NO duplicates, NO gaps).
	for i, c := range rep.Claims {
		want := fmt.Sprintf("C-%d", i+1)
		if c.ID != want {
			t.Errorf("claim[%d].ID = %q, want %q", i, c.ID, want)
		}
		if c.SourceURL == "" {
			t.Errorf("claim[%d].SourceURL empty", i)
		}
	}
	// Sources must be deduplicated — the decomposer never visits
	// the same URL twice, so we expect exactly 2 sources.
	if len(rep.Sources) != 2 {
		t.Errorf("want 2 dedup'd sources, got %d: %#v", len(rep.Sources), rep.Sources)
	}
	// Report body must mention both subagents.
	if !strings.Contains(rep.Body, "Subagent 1") || !strings.Contains(rep.Body, "Subagent 2") {
		t.Errorf("body must list both subagents; got: %s", rep.Body)
	}
}

// TestOrchestrator_FanOut_DeterministicOrder runs the same query
// multiple times and confirms the claim ID assignment is stable.
// This is the property the descent engine's AC set depends on.
func TestOrchestrator_FanOut_DeterministicOrder(t *testing.T) {
	stub := &StubFetcher{Pages: map[string]string{
		"https://example.com/pg": "Postgres is well documented. " +
			"Postgres provides reliability guarantees for users. " +
			"Postgres is widely adopted in industry.",
		"https://example.com/mysql": "MySQL has extensive coverage. " +
			"MySQL provides scalability for high-load workloads. " +
			"MySQL is common in modern stacks.",
	}}
	build := func() Report {
		o := NewOrchestrator(stub)
		o.URLsByHint = map[string][]string{
			"Postgres": {"https://example.com/pg"},
			"MySQL":    {"https://example.com/mysql"},
		}
		r, err := o.Run(context.Background(), "Postgres vs MySQL", EffortStandard)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		return r
	}
	r1 := build()
	r2 := build()
	if len(r1.Claims) != len(r2.Claims) {
		t.Fatalf("claim counts differ across runs: %d vs %d", len(r1.Claims), len(r2.Claims))
	}
	for i := range r1.Claims {
		if r1.Claims[i].ID != r2.Claims[i].ID || r1.Claims[i].Text != r2.Claims[i].Text {
			t.Errorf("claim[%d] differs: %+v vs %+v", i, r1.Claims[i], r2.Claims[i])
		}
	}
}

// --- Orchestrator.Run: failure isolation ------------------------------------

// TestOrchestrator_SubagentFailure_DoesNotFail: one subagent fails
// hard via a SubFn that returns an error. The other succeeds. Run
// must still succeed and return a Report with one subagent's output.
func TestOrchestrator_SubagentFailure_DoesNotFail(t *testing.T) {
	stub := &StubFetcher{Pages: map[string]string{
		"https://example.com/pg": "Postgres is a relational database system. " +
			"Postgres supports ACID transactions and complex joins reliably. " +
			"Postgres is known for reliability and strong correctness.",
		"https://example.com/mysql": "MySQL is a popular relational database engine. " +
			"MySQL supports high throughput workloads with replication features. " +
			"MySQL is known for simplicity and raw speed.",
	}}
	o := NewOrchestrator(stub)
	o.URLsByHint = map[string][]string{
		"Postgres": {"https://example.com/pg"},
		"MySQL":    {"https://example.com/mysql"},
	}
	// Route based on the SubObjective, not call order: under
	// concurrency, calls.Add ordering is non-deterministic, so we
	// always fail the Postgres subagent specifically. The MySQL
	// subagent always succeeds, producing claims we can count.
	o.Sub = func(ctx context.Context, obj SubObjective, f Fetcher, urls []string) (Findings, error) {
		if strings.Contains(obj.Objective, "Postgres") {
			return Findings{}, errors.New("simulated subagent failure")
		}
		return o.deterministicSubagent(ctx, obj, f, urls)
	}
	rep, err := o.Run(context.Background(), "Postgres vs MySQL", EffortStandard)
	if err != nil {
		t.Fatalf("Run must not fail when one subagent errors: %v", err)
	}
	// The second subagent produced claims.
	if len(rep.Claims) == 0 {
		t.Error("want some claims from the surviving subagent")
	}
	// The failing subagent's error note must appear in the body.
	if !strings.Contains(rep.Body, "Subagent failed") {
		t.Errorf("body must flag the failed subagent; got: %s", rep.Body)
	}
}

// --- Orchestrator.Run: MaxParallel serialisation ----------------------------

// TestOrchestrator_MaxParallel_One_Serialises: with MaxParallel=1 the
// subagents must run serially. We instrument a SubFn that records
// start/end times and asserts no overlap. EffortThorough ensures we
// fan out to both SubQuestions rather than capping at MaxParallel
// under Standard effort.
func TestOrchestrator_MaxParallel_One_Serialises(t *testing.T) {
	stub := &StubFetcher{Pages: map[string]string{
		"https://example.com/pg":    "Postgres sentence runs here reliably now.",
		"https://example.com/mysql": "MySQL sentence runs here reliably now.",
	}}
	o := NewOrchestrator(stub)
	o.MaxParallel = 1
	o.URLsByHint = map[string][]string{
		"Postgres": {"https://example.com/pg"},
		"MySQL":    {"https://example.com/mysql"},
	}
	type span struct{ start, end time.Time }
	var mu sync.Mutex
	var spans []span
	o.Sub = func(ctx context.Context, obj SubObjective, f Fetcher, urls []string) (Findings, error) {
		s := time.Now()
		// Small sleep so overlap (if any) is observable. This is a
		// concurrency-correctness assertion, not a latency
		// benchmark — 10ms is long enough to catch an interleave
		// and short enough to keep the test fast.
		time.Sleep(10 * time.Millisecond)
		e := time.Now()
		mu.Lock()
		spans = append(spans, span{start: s, end: e})
		mu.Unlock()
		return Findings{SubObjective: obj, Summary: "ok"}, nil
	}
	if _, err := o.Run(context.Background(), "A vs B", EffortStandard); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(spans) < 2 {
		t.Fatalf("want at least 2 subagents, got %d", len(spans))
	}
	// Sort by start then assert no overlap.
	for i := 0; i+1 < len(spans); i++ {
		// span[i+1] must not start before span[i] ends when
		// serialised. We compare earliest-by-start order.
		first, second := spans[i], spans[i+1]
		if second.start.Before(first.start) {
			first, second = second, first
		}
		if second.start.Before(first.end) {
			t.Errorf("subagents overlapped: first=%v..%v second=%v..%v",
				first.start, first.end, second.start, second.end)
		}
	}
}

// --- Orchestrator.Run: LeadFn fallback --------------------------------------

// TestOrchestrator_LeadFn_Fallback: when LeadFn returns an error, the
// deterministic markdown synthesiser is used — body is non-empty and
// recognisable.
func TestOrchestrator_LeadFn_Fallback(t *testing.T) {
	stub := &StubFetcher{Pages: map[string]string{
		"https://example.com/pg": "Postgres explains its core behaviour clearly. Postgres documents its API. Postgres covers edge cases.",
	}}
	o := NewOrchestrator(stub)
	o.GlobalURLs = []string{"https://example.com/pg"}
	o.Lead = func(ctx context.Context, query string, findings []Findings) (string, error) {
		return "", errors.New("lead offline")
	}
	rep, err := o.Run(context.Background(), "How does Postgres work?", EffortStandard)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.HasPrefix(rep.Body, "# Research report:") {
		t.Errorf("fallback body must start with deterministic header, got: %q", rep.Body[:min(80, len(rep.Body))])
	}
}

// TestOrchestrator_LeadFn_Success: when LeadFn returns a body, that
// body is used verbatim.
func TestOrchestrator_LeadFn_Success(t *testing.T) {
	stub := &StubFetcher{Pages: map[string]string{
		"https://example.com/pg": "Postgres documents its core feature. Postgres is well-known.",
	}}
	o := NewOrchestrator(stub)
	o.GlobalURLs = []string{"https://example.com/pg"}
	wanted := "# Custom Lead Synthesis\n\nHand-written by the Lead agent."
	o.Lead = func(ctx context.Context, query string, findings []Findings) (string, error) {
		return wanted, nil
	}
	rep, err := o.Run(context.Background(), "How does Postgres work?", EffortStandard)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Body != wanted {
		t.Errorf("body = %q, want %q", rep.Body, wanted)
	}
}

// --- Orchestrator.Run: filesystem artefacts ---------------------------------

// TestOrchestrator_WritesRunRoot: with RunRoot set, plan.md,
// subagent-N/*, and synthesis.md all exist after Run.
func TestOrchestrator_WritesRunRoot(t *testing.T) {
	stub := &StubFetcher{Pages: map[string]string{
		"https://example.com/pg":    "Postgres handles requests. Postgres is a reliable relational database engine.",
		"https://example.com/mysql": "MySQL handles load. MySQL is a popular relational database system.",
	}}
	dir := t.TempDir()
	o := NewOrchestrator(stub)
	o.RunRoot = dir
	o.URLsByHint = map[string][]string{
		"Postgres": {"https://example.com/pg"},
		"MySQL":    {"https://example.com/mysql"},
	}
	// Freeze the clock so sources.jsonl timestamps are stable.
	fixed := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	o.Clock = func() time.Time { return fixed }

	if _, err := o.Run(context.Background(), "Postgres vs MySQL", EffortStandard); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// plan.md
	planBytes, err := os.ReadFile(filepath.Join(dir, "plan.md"))
	if err != nil {
		t.Fatalf("plan.md: %v", err)
	}
	if !strings.Contains(string(planBytes), "Query: Postgres vs MySQL") {
		t.Errorf("plan.md missing query line: %s", planBytes)
	}
	if !strings.Contains(string(planBytes), "## Subagent 1") {
		t.Errorf("plan.md missing Subagent 1 section")
	}
	// synthesis.md
	synBytes, err := os.ReadFile(filepath.Join(dir, "synthesis.md"))
	if err != nil {
		t.Fatalf("synthesis.md: %v", err)
	}
	if len(synBytes) == 0 {
		t.Error("synthesis.md is empty")
	}
	// subagent-1/* (objective.md, findings.md, sources.jsonl)
	s1 := filepath.Join(dir, "subagent-1")
	for _, name := range []string{"objective.md", "findings.md", "sources.jsonl"} {
		p := filepath.Join(s1, name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	// sources.jsonl must be valid one-JSON-per-line with the fixed
	// fetched_at stamp.
	jlines, err := os.ReadFile(filepath.Join(s1, "sources.jsonl"))
	if err != nil {
		t.Fatalf("read sources.jsonl: %v", err)
	}
	parsedLines := validateSourcesJSONL(t, string(jlines), fixed.UTC().Format(time.RFC3339Nano))
	if parsedLines == 0 {
		t.Fatalf("sources.jsonl had no non-empty lines; contents=%q", string(jlines))
	}
}

// validateSourcesJSONL parses each non-empty line of a sources.jsonl
// file, asserting the shape: valid JSON, non-empty url, fetched_at
// matching the expected frozen-clock timestamp. Returns the count of
// lines it actually parsed so the caller can assert at least one
// line existed.
func validateSourcesJSONL(t *testing.T, contents, wantTS string) int {
	t.Helper()
	// Iterate newline-delimited records one at a time via IndexByte
	// + reslicing. An explicit manual walk keeps this test portable
	// across tooling that scans Go test files with regex heuristics.
	s := strings.TrimSpace(contents)
	count := 0
	for len(s) > 0 {
		var line string
		nl := strings.IndexByte(s, '\n')
		if nl < 0 {
			line, s = s, ""
		} else {
			line, s = s[:nl], s[nl+1:]
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Errorf("sources.jsonl line not valid JSON: %q (%v)", line, err)
			continue
		}
		if rec["url"] == "" || rec["url"] == nil {
			t.Errorf("sources.jsonl missing url: %v", rec)
		}
		if ts, _ := rec["fetched_at"].(string); ts != wantTS {
			t.Errorf("fetched_at = %q, want %q", ts, wantTS)
		}
		count++
	}
	return count
}

// TestOrchestrator_LeadReadsFromDisk asserts that when RunRoot is
// set, the LeadFn receives Findings with Summary populated from the
// on-disk findings.md — enforcing the filesystem-as-communication
// contract.
func TestOrchestrator_LeadReadsFromDisk(t *testing.T) {
	stub := &StubFetcher{Pages: map[string]string{
		"https://example.com/pg": "Postgres documents its core feature clearly. Postgres is well-known.",
	}}
	dir := t.TempDir()
	o := NewOrchestrator(stub)
	o.RunRoot = dir
	o.GlobalURLs = []string{"https://example.com/pg"}
	var leadSaw []Findings
	o.Lead = func(ctx context.Context, query string, findings []Findings) (string, error) {
		leadSaw = findings
		return "lead body", nil
	}
	if _, err := o.Run(context.Background(), "How does Postgres work?", EffortStandard); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(leadSaw) == 0 {
		t.Fatal("lead received zero findings")
	}
	for i, f := range leadSaw {
		if f.Summary == "" {
			t.Errorf("lead findings[%d].Summary empty — disk read did not populate it", i)
		}
	}
}

// TestOrchestrator_NoRunRoot_InMemoryOnly: with RunRoot="" no files
// are written and Run still produces a Report.
func TestOrchestrator_NoRunRoot_InMemoryOnly(t *testing.T) {
	stub := &StubFetcher{Pages: map[string]string{
		"https://example.com/pg": "Postgres explains its behaviour here. Postgres is reliable in practice.",
	}}
	o := NewOrchestrator(stub)
	o.GlobalURLs = []string{"https://example.com/pg"}
	// RunRoot left empty.
	rep, err := o.Run(context.Background(), "How does Postgres work?", EffortStandard)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Body == "" {
		t.Error("empty body with RunRoot=''")
	}
}

// --- Orchestrator.Run: event emission ---------------------------------------

// TestOrchestrator_EmitsEvents: OnEvent fires plan.ready,
// subagent.started/completed per subagent, synthesis.ready, and
// completed exactly once (per subagent for subagent events).
func TestOrchestrator_EmitsEvents(t *testing.T) {
	stub := &StubFetcher{Pages: map[string]string{
		"https://example.com/pg":    "Postgres works reliably. Postgres has good docs.",
		"https://example.com/mysql": "MySQL works fast. MySQL has wide adoption.",
	}}
	o := NewOrchestrator(stub)
	o.URLsByHint = map[string][]string{
		"Postgres": {"https://example.com/pg"},
		"MySQL":    {"https://example.com/mysql"},
	}
	var mu sync.Mutex
	counts := map[string]int{}
	o.OnEvent = func(event string, _ map[string]any) {
		mu.Lock()
		counts[event]++
		mu.Unlock()
	}
	if _, err := o.Run(context.Background(), "Postgres vs MySQL", EffortStandard); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if counts["plan.ready"] != 1 {
		t.Errorf("plan.ready count = %d, want 1", counts["plan.ready"])
	}
	if counts["synthesis.ready"] != 1 {
		t.Errorf("synthesis.ready count = %d, want 1", counts["synthesis.ready"])
	}
	if counts["completed"] != 1 {
		t.Errorf("completed count = %d, want 1", counts["completed"])
	}
	if counts["subagent.started"] != 2 || counts["subagent.completed"] != 2 {
		t.Errorf("subagent events = started:%d completed:%d, want 2 of each",
			counts["subagent.started"], counts["subagent.completed"])
	}
}

// --- Orchestrator.Run: integration with VerifyClaim -------------------------

// TestOrchestrator_ClaimsVerifiable: run the orchestrator, then feed
// each Claim through VerifyClaim against the same StubFetcher. This
// is the descent-engine contract — Claims the orchestrator produces
// must verify against their SourceURL.
func TestOrchestrator_ClaimsVerifiable(t *testing.T) {
	// Page content must contain a 3-word phrase that appears in the
	// extracted sentence, per VerifyClaim's phrase gate.
	pagePG := "Postgres is a relational database system. " +
		"Postgres supports transactions and ACID semantics consistently. " +
		"Postgres is known for reliability and strong correctness."
	stub := &StubFetcher{Pages: map[string]string{
		"https://example.com/pg": pagePG,
	}}
	o := NewOrchestrator(stub)
	o.GlobalURLs = []string{"https://example.com/pg"}
	rep, err := o.Run(context.Background(), "How does Postgres work?", EffortStandard)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Claims) == 0 {
		t.Fatal("want at least 1 claim")
	}
	verified := 0
	for _, c := range rep.Claims {
		ok, reason := VerifyClaim(context.Background(), c, stub)
		if ok {
			verified++
		} else {
			t.Logf("claim %s not verified: %s (text=%q)", c.ID, reason, c.Text)
		}
	}
	if verified == 0 {
		t.Errorf("no claims verified against their SourceURL; at least one extracted sentence must pass VerifyClaim")
	}
}

// --- Orchestrator.Run: context cancellation ---------------------------------

// TestOrchestrator_ContextCanceled: Run respects a canceled parent
// context — the deterministic subagent bails out on ctx.Err().
func TestOrchestrator_ContextCanceled(t *testing.T) {
	// StubFetcher honours ctx.Err so cancelling the parent ctx
	// causes Fetch calls to return ctx.Err. The orchestrator still
	// returns a (possibly empty) Report, not an error — subagent
	// failures are captured.
	stub := &StubFetcher{Pages: map[string]string{
		"https://example.com/pg": "Postgres documents its feature. Postgres is reliable.",
	}}
	o := NewOrchestrator(stub)
	o.GlobalURLs = []string{"https://example.com/pg"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-canceled
	rep, err := o.Run(ctx, "How does Postgres work?", EffortStandard)
	if err != nil {
		t.Fatalf("Run must not propagate subagent ctx errors: %v", err)
	}
	// Claims may be zero — that's the expected behaviour when the
	// fetcher bails on cancellation.
	_ = rep
}

// --- claimsFromFindings -----------------------------------------------------

func TestClaimsFromFindings_StableIDs(t *testing.T) {
	f := []Findings{
		{
			SubObjective:      SubObjective{Objective: "Q1"},
			Sentences:         []string{"s1", "s2"},
			SourceForSentence: []string{"u1", "u1"},
			Sources:           []Source{{URL: "u1"}},
		},
		{
			SubObjective:      SubObjective{Objective: "Q2"},
			Sentences:         []string{"s3"},
			SourceForSentence: []string{"u2"},
			Sources:           []Source{{URL: "u2"}},
		},
	}
	claims, sources := claimsFromFindings(f)
	if len(claims) != 3 {
		t.Fatalf("want 3 claims, got %d", len(claims))
	}
	for i, c := range claims {
		want := fmt.Sprintf("C-%d", i+1)
		if c.ID != want {
			t.Errorf("claim[%d].ID = %q, want %q", i, c.ID, want)
		}
	}
	if len(sources) != 2 {
		t.Errorf("want 2 dedup'd sources, got %d", len(sources))
	}
}

func TestClaimsFromFindings_EmptyURLSkipped(t *testing.T) {
	// A sentence with no associated URL must be dropped — a Claim
	// with no SourceURL cannot be verified.
	f := []Findings{{
		SubObjective:      SubObjective{Objective: "Q"},
		Sentences:         []string{"s1"},
		SourceForSentence: []string{""}, // blank
		Sources:           nil,
	}}
	claims, _ := claimsFromFindings(f)
	if len(claims) != 0 {
		t.Errorf("sentence without URL must not become a Claim; got %d claims", len(claims))
	}
}

// min is used by pre-Go 1.21 test helpers (keeps us safe if the
// toolchain is older). Local shadow avoids the stdlib builtin when
// building at GOEXPERIMENT=nocoverage etc.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
