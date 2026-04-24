// research_llm_decomposer_test.go covers the LLMDecomposer alone
// (stubbed provider) and its integration with ResearchExecutor via
// the fan-out path. The heuristic fallback + the filesystem write
// contract are exercised by TestResearchExecutor_FanOut_* below.

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/research"
	"github.com/RelayOne/r1/internal/stream"
)

// stubProvider is a minimal provider.Provider implementation that
// returns a canned JSON blob on Chat. It exists so the decomposer
// tests can exercise the parse path without standing up an HTTP
// client against a live API.
type stubProvider struct {
	// response is the text body to return as the sole "text" content
	// block. When response is empty the provider returns a zero-
	// content response so callers can exercise the empty-response
	// branch.
	response string

	// err, when non-nil, is returned directly from Chat so the
	// fallback branch is exercisable.
	err error

	// lastPrompt captures the prompt text for assertions.
	lastPrompt string
}

func (s *stubProvider) Name() string { return "stub" }

func (s *stubProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	// Capture the prompt text so tests can assert on its shape.
	if len(req.Messages) > 0 {
		// Messages[0].Content is a JSON array of content blocks; pull
		// the text of the first one.
		var blocks []map[string]any
		_ = json.Unmarshal(req.Messages[0].Content, &blocks)
		if len(blocks) > 0 {
			if t, ok := blocks[0]["text"].(string); ok {
				s.lastPrompt = t
			}
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	return &provider.ChatResponse{
		ID:    "stub-resp",
		Model: req.Model,
		Content: []provider.ResponseContent{
			{Type: "text", Text: s.response},
		},
		StopReason: "end_turn",
	}, nil
}

func (s *stubProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	return s.Chat(req)
}

// TestLLMDecomposer_Decompose_Minimal verifies Minimal effort yields
// exactly one sub-question regardless of how many the model returned.
func TestLLMDecomposer_Decompose_Minimal(t *testing.T) {
	sp := &stubProvider{
		response: `{"questions":[{"id":"SQ-1","question":"What is Go?","expected_source_type":"official-docs"}]}`,
	}
	d := NewLLMDecomposer(sp)
	got, err := d.Decompose(context.Background(), "What is Go?", EffortMinimal)
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 subq for Minimal, got %d: %#v", len(got), got)
	}
	if got[0].ID != "SQ-1" {
		t.Errorf("id = %q, want SQ-1", got[0].ID)
	}
	if got[0].Text != "What is Go?" {
		t.Errorf("text = %q, want 'What is Go?'", got[0].Text)
	}
	// Hints should carry the question text (baseline) plus the
	// expected_source_type tag.
	if len(got[0].Hints) < 1 {
		t.Errorf("expected at least one hint, got %#v", got[0].Hints)
	}
	// Prompt should mention "exactly 1" for Minimal.
	if !strings.Contains(sp.lastPrompt, "exactly 1") {
		t.Errorf("Minimal prompt should say 'exactly 1', got: %q", sp.lastPrompt)
	}
}

// TestLLMDecomposer_Decompose_Minimal_ClampsExcess ensures that even
// when the model over-produces, Minimal clamps to a single subq.
func TestLLMDecomposer_Decompose_Minimal_ClampsExcess(t *testing.T) {
	sp := &stubProvider{
		response: `{"questions":[
			{"id":"SQ-1","question":"Q1"},
			{"id":"SQ-2","question":"Q2"},
			{"id":"SQ-3","question":"Q3"}
		]}`,
	}
	d := NewLLMDecomposer(sp)
	got, err := d.Decompose(context.Background(), "big query", EffortMinimal)
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Minimal should clamp to 1, got %d", len(got))
	}
}

// TestLLMDecomposer_Decompose_Standard verifies Standard accepts
// 3..5 sub-questions and returns them as-is.
func TestLLMDecomposer_Decompose_Standard(t *testing.T) {
	sp := &stubProvider{
		response: `{"questions":[
			{"id":"SQ-1","question":"Go history","expected_source_type":"wikipedia"},
			{"id":"SQ-2","question":"Go concurrency","expected_source_type":"official-docs"},
			{"id":"SQ-3","question":"Go performance","expected_source_type":"benchmark"},
			{"id":"SQ-4","question":"Go ecosystem","expected_source_type":"community-survey"}
		]}`,
	}
	d := NewLLMDecomposer(sp)
	got, err := d.Decompose(context.Background(), "What is Go?", EffortStandard)
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	if len(got) < 3 || len(got) > 5 {
		t.Fatalf("want 3-5 subqs for Standard, got %d", len(got))
	}
	for i, sq := range got {
		if sq.ID == "" {
			t.Errorf("subq[%d] has empty ID", i)
		}
		if strings.TrimSpace(sq.Text) == "" {
			t.Errorf("subq[%d] has empty text", i)
		}
	}
	// Prompt should mention a range.
	if !strings.Contains(sp.lastPrompt, "3") || !strings.Contains(sp.lastPrompt, "5") {
		t.Errorf("Standard prompt should say 'between 3 and 5', got: %q", sp.lastPrompt)
	}
}

// TestLLMDecomposer_Decompose_Standard_ClampsExcess ensures Standard
// clamps to 5 when the model over-produces.
func TestLLMDecomposer_Decompose_Standard_ClampsExcess(t *testing.T) {
	var qs []string
	for i := 1; i <= 9; i++ {
		qs = append(qs, fmt.Sprintf(`{"id":"SQ-%d","question":"Q%d"}`, i, i))
	}
	sp := &stubProvider{
		response: `{"questions":[` + strings.Join(qs, ",") + `]}`,
	}
	d := NewLLMDecomposer(sp)
	got, err := d.Decompose(context.Background(), "big query", EffortStandard)
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("Standard should clamp to 5, got %d", len(got))
	}
}

// TestLLMDecomposer_Decompose_Thorough verifies Thorough accepts up
// to 10 sub-questions.
func TestLLMDecomposer_Decompose_Thorough(t *testing.T) {
	var qs []string
	for i := 1; i <= 12; i++ {
		qs = append(qs, fmt.Sprintf(`{"id":"SQ-%d","question":"Q%d"}`, i, i))
	}
	sp := &stubProvider{
		response: `{"questions":[` + strings.Join(qs, ",") + `]}`,
	}
	d := NewLLMDecomposer(sp)
	got, err := d.Decompose(context.Background(), "survey the field", EffortThorough)
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("Thorough should clamp to 10, got %d", len(got))
	}
}

// TestLLMDecomposer_LLMFailure_FallsBack exercises the executor's
// fallback behavior — the LLM errors, the executor drops back to the
// HeuristicDecomposer via Planner.
func TestLLMDecomposer_LLMFailure_FallsBack(t *testing.T) {
	sp := &stubProvider{err: errors.New("simulated provider outage")}
	d := NewLLMDecomposer(sp)
	// Calling the decomposer directly should return the error so the
	// executor can decide to fall back.
	_, err := d.Decompose(context.Background(), "anything", EffortStandard)
	if err == nil {
		t.Fatal("expected error from LLMDecomposer on provider failure, got nil")
	}

	// Now exercise the executor-level fallback: ResearchExecutor
	// with a Decomposer whose Provider returns an error must fall
	// back to the heuristic Planner and still produce a Report.
	ex := NewResearchExecutor(&research.StubFetcher{Pages: map[string]string{
		"https://example/a": "Postgres is great. MySQL is also great.",
	}})
	ex.Decomposer = d
	// Use a "Postgres vs MySQL" query so the heuristic splitter yields
	// 2 sub-questions — verifying we actually fell back.
	d_, err := ex.Execute(context.Background(), Plan{
		Query: "Postgres vs MySQL",
		Extra: map[string]any{"urls": []string{"https://example/a"}},
	}, EffortStandard)
	if err != nil {
		t.Fatalf("Execute after fallback: %v", err)
	}
	rd, ok := d_.(ResearchDeliverable)
	if !ok {
		t.Fatalf("deliverable: unexpected type: %T", d_)
	}
	// The heuristic "X vs Y" split produces >1 subq, which yields
	// >=1 claim given the stubbed page is relevant.
	if rd.Report.Query != "Postgres vs MySQL" {
		t.Errorf("query lost: %q", rd.Report.Query)
	}
}

// TestLLMDecomposer_EmptyResponse_ReturnsError checks that a provider
// returning nothing produces an error rather than panicking.
func TestLLMDecomposer_EmptyResponse_ReturnsError(t *testing.T) {
	sp := &stubProvider{response: ""}
	d := NewLLMDecomposer(sp)
	_, err := d.Decompose(context.Background(), "q", EffortStandard)
	if err == nil {
		t.Fatal("expected error on empty response, got nil")
	}
}

// TestLLMDecomposer_UnparseableResponse_ReturnsError covers the
// "model returned garbage" path.
func TestLLMDecomposer_UnparseableResponse_ReturnsError(t *testing.T) {
	sp := &stubProvider{response: "this is not json at all"}
	d := NewLLMDecomposer(sp)
	_, err := d.Decompose(context.Background(), "q", EffortStandard)
	if err == nil {
		t.Fatal("expected error on unparseable response, got nil")
	}
}

// TestLLMDecomposer_NilProvider_ReturnsError verifies the nil-safety.
func TestLLMDecomposer_NilProvider_ReturnsError(t *testing.T) {
	d := &LLMDecomposer{Provider: nil}
	_, err := d.Decompose(context.Background(), "q", EffortStandard)
	if err == nil {
		t.Fatal("expected error on nil Provider, got nil")
	}
}

// TestLLMDecomposer_MaxSubQuestionsCap verifies the explicit cap.
func TestLLMDecomposer_MaxSubQuestionsCap(t *testing.T) {
	sp := &stubProvider{
		response: `{"questions":[
			{"id":"SQ-1","question":"Q1"},
			{"id":"SQ-2","question":"Q2"},
			{"id":"SQ-3","question":"Q3"},
			{"id":"SQ-4","question":"Q4"}
		]}`,
	}
	d := &LLMDecomposer{Provider: sp, MaxSubQuestions: 2}
	got, err := d.Decompose(context.Background(), "q", EffortThorough)
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("MaxSubQuestions=2 should clamp, got %d", len(got))
	}
}

// TestResearchExecutor_FanOut_WritesFiles verifies the SessionDir
// fan-out path: N files land on disk, one per sub-question, each
// containing a parseable subagentFinding.
func TestResearchExecutor_FanOut_WritesFiles(t *testing.T) {
	tmp := t.TempDir()
	stub := &research.StubFetcher{Pages: map[string]string{
		"https://example/a": "Postgres offers strong ACID guarantees across all transactions. " +
			"Postgres supports JSON columns. Postgres has extensive indexing.",
		"https://example/b": "MySQL offers strong performance on read-heavy workloads. " +
			"MySQL supports replication. MySQL has a large ecosystem.",
	}}
	sp := &stubProvider{response: `{"questions":[
		{"id":"SQ-1","question":"What is Postgres?"},
		{"id":"SQ-2","question":"What is MySQL?"}
	]}`}
	ex := NewResearchExecutor(stub)
	ex.Decomposer = NewLLMDecomposer(sp)
	ex.SessionDir = tmp
	ex.MaxParallel = 2

	p := Plan{
		ID:    "sess-42",
		Query: "Postgres vs MySQL",
		Extra: map[string]any{
			"urls": []string{"https://example/a", "https://example/b"},
		},
	}
	d, err := ex.Execute(context.Background(), p, EffortStandard)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Session directory: <SessionDir>/<Plan.ID>/
	sessDir := filepath.Join(tmp, "sess-42")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", sessDir, err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 finding files, got %d: %v", len(entries), entries)
	}

	// Every file must be valid JSON and parse into subagentFinding.
	found := map[string]subagentFinding{}
	for _, e := range entries {
		raw, rerr := os.ReadFile(filepath.Join(sessDir, e.Name()))
		if rerr != nil {
			t.Fatalf("ReadFile(%s): %v", e.Name(), rerr)
		}
		var f subagentFinding
		if jerr := json.Unmarshal(raw, &f); jerr != nil {
			t.Fatalf("parse %s: %v — raw: %s", e.Name(), jerr, raw)
		}
		found[e.Name()] = f
	}
	// The two expected file names.
	for _, name := range []string{"SQ-1.json", "SQ-2.json"} {
		if _, ok := found[name]; !ok {
			t.Errorf("missing expected file %s (got: %v)", name, keysOf(found))
		}
	}

	rd, ok := d.(ResearchDeliverable)
	if !ok {
		t.Fatalf("deliverable: unexpected type: %T", d)
	}
	if rd.Report.Query != p.Query {
		t.Errorf("query dropped: %q", rd.Report.Query)
	}
}

// TestResearchExecutor_FanOut_FallbackWhenLLMFails verifies that
// when the LLM decomposer errors, the fan-out path STILL runs using
// the heuristic fallback — and STILL writes finding files to disk.
func TestResearchExecutor_FanOut_FallbackWhenLLMFails(t *testing.T) {
	tmp := t.TempDir()
	stub := &research.StubFetcher{Pages: map[string]string{
		"https://example/a": "Postgres rocks. MySQL rocks. Redis rocks.",
	}}
	sp := &stubProvider{err: errors.New("nope")}
	ex := NewResearchExecutor(stub)
	ex.Decomposer = NewLLMDecomposer(sp)
	ex.SessionDir = tmp

	p := Plan{
		ID:    "sess-7",
		Query: "Postgres vs MySQL", // heuristic yields 2 subqs
		Extra: map[string]any{"urls": []string{"https://example/a"}},
	}
	_, err := ex.Execute(context.Background(), p, EffortStandard)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	sessDir := filepath.Join(tmp, "sess-7")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) < 2 {
		t.Errorf("want >=2 finding files from heuristic fallback, got %d", len(entries))
	}
}

// TestResearchExecutor_FanOut_ReadsFromFilesystem proves the
// lead-reads-from-filesystem contract: after fan-out, the in-memory
// Synthesize call receives answers reconstructed from disk. We
// assert the Synthesize callback observed sub-question text matching
// what the subagent wrote.
func TestResearchExecutor_FanOut_ReadsFromFilesystem(t *testing.T) {
	tmp := t.TempDir()
	stub := &research.StubFetcher{Pages: map[string]string{
		"https://x/1": "Goroutines are lightweight. Channels coordinate goroutines. Select statements multiplex channels.",
	}}
	sp := &stubProvider{response: `{"questions":[
		{"id":"SQ-1","question":"What are goroutines?"}
	]}`}
	ex := NewResearchExecutor(stub)
	ex.Decomposer = NewLLMDecomposer(sp)
	ex.SessionDir = tmp

	var observed []research.SubQuestionAnswer
	ex.Synthesize = func(_ context.Context, q string, answers []research.SubQuestionAnswer) string {
		observed = answers
		return "body"
	}

	p := Plan{
		ID:    "s1",
		Query: "goroutines lightweight concurrency",
		Extra: map[string]any{"urls": []string{"https://x/1"}},
	}
	_, err := ex.Execute(context.Background(), p, EffortMinimal)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(observed) != 1 {
		t.Fatalf("want 1 answer, got %d", len(observed))
	}
	if observed[0].Question.Text != "What are goroutines?" {
		t.Errorf("Synthesize received wrong question: %q", observed[0].Question.Text)
	}
	// The sentences should have been reconstructed from the finding
	// file, not invented — so they must match what's on disk.
	diskPath := filepath.Join(tmp, "s1", "SQ-1.json")
	raw, _ := os.ReadFile(diskPath)
	var diskF subagentFinding
	_ = json.Unmarshal(raw, &diskF)
	if len(diskF.Sentences) != len(observed[0].Sentences) {
		t.Errorf("sentence count mismatch: disk=%d observed=%d",
			len(diskF.Sentences), len(observed[0].Sentences))
	}
}

// TestResearchExecutor_ProviderOnly_BuildsDecomposer verifies that
// setting Provider alone auto-constructs an LLMDecomposer.
func TestResearchExecutor_ProviderOnly_BuildsDecomposer(t *testing.T) {
	stub := &research.StubFetcher{Pages: map[string]string{
		"https://x": "Body body body. More body.",
	}}
	sp := &stubProvider{response: `{"questions":[
		{"id":"SQ-1","question":"What?"}
	]}`}
	ex := NewResearchExecutor(stub)
	ex.Provider = sp

	d, err := ex.Execute(context.Background(), Plan{
		Query: "what",
		Extra: map[string]any{"urls": []string{"https://x"}},
	}, EffortMinimal)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	rd, ok := d.(ResearchDeliverable)
	if !ok {
		t.Fatalf("deliverable: unexpected type: %T", d)
	}
	if rd.Report.Query != "what" {
		t.Errorf("query: %q", rd.Report.Query)
	}
}

// TestSanitizeID spot-checks the slug helper — used to derive a path
// from a Plan.ID or Plan.Query.
func TestSanitizeID(t *testing.T) {
	cases := map[string]string{
		"":              "",
		"clean-id_1.2":  "clean-id_1.2",
		"hello world":   "hello-world",
		"   spaced   ":  "spaced",
		"! weird//id !": "weird-id",
	}
	for in, want := range cases {
		if got := sanitizeID(in); got != want {
			t.Errorf("sanitizeID(%q) = %q, want %q", in, got, want)
		}
	}
}

// keysOf is a small helper for test diagnostics.
func keysOf(m map[string]subagentFinding) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
