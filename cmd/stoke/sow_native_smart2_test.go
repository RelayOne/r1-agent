package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/plan"
	"github.com/ericmacdougall/stoke/internal/provider"
	"github.com/ericmacdougall/stoke/internal/stream"
	"github.com/ericmacdougall/stoke/internal/wisdom"
)

// --- mock provider for wisdom + cross-review tests ---

type mockChatProvider struct {
	name     string
	response string
	err      error
	lastReq  provider.ChatRequest
	calls    int
}

func (m *mockChatProvider) Name() string { return m.name }
func (m *mockChatProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	m.calls++
	m.lastReq = req
	if m.err != nil {
		return nil, m.err
	}
	return &provider.ChatResponse{
		Content: []provider.ResponseContent{{Type: "text", Text: m.response}},
	}, nil
}
func (m *mockChatProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	return m.Chat(req)
}

// --- Smart 8: wisdom tests ---

func TestCaptureSessionWisdom_RecordsLearnings(t *testing.T) {
	resp := `{"learnings":[
  {"category":"pattern","description":"this project puts handlers under cmd/*/http","file":"cmd/api/http/routes.go"},
  {"category":"gotcha","description":"go.sum must be committed for lint to pass"},
  {"category":"decision","description":"token refresh uses a 10-minute leeway"}
]}`
	prov := &mockChatProvider{response: resp}
	store := wisdom.NewStore()
	session := plan.Session{ID: "S1", Title: "Auth"}
	n, err := CaptureSessionWisdom(nil, session, nil, nil, store, prov, "m")
	if err != nil {
		t.Fatalf("CaptureSessionWisdom: %v", err)
	}
	if n != 3 {
		t.Errorf("captured = %d, want 3", n)
	}
	learnings := store.Learnings()
	if len(learnings) != 3 {
		t.Errorf("store has %d learnings, want 3", len(learnings))
	}
}

func TestCaptureSessionWisdom_StripsMarkdownFences(t *testing.T) {
	resp := "```json\n" + `{"learnings":[{"category":"pattern","description":"x"}]}` + "\n```"
	prov := &mockChatProvider{response: resp}
	store := wisdom.NewStore()
	n, err := CaptureSessionWisdom(nil, plan.Session{ID: "S1"}, nil, nil, store, prov, "m")
	if err != nil {
		t.Fatalf("fence strip failed: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 learning, got %d", n)
	}
}

func TestCaptureSessionWisdom_NilStore_NoOp(t *testing.T) {
	prov := &mockChatProvider{response: `{"learnings":[{"category":"pattern","description":"x"}]}`}
	n, err := CaptureSessionWisdom(nil, plan.Session{}, nil, nil, nil, prov, "m")
	if err != nil || n != 0 {
		t.Errorf("nil store should be no-op: n=%d err=%v", n, err)
	}
	if prov.calls != 0 {
		t.Error("nil store should not call the provider")
	}
}

func TestCaptureSessionWisdom_NilProvider_NoOp(t *testing.T) {
	store := wisdom.NewStore()
	n, err := CaptureSessionWisdom(nil, plan.Session{}, nil, nil, store, nil, "m")
	if err != nil || n != 0 {
		t.Errorf("nil provider should be no-op: n=%d err=%v", n, err)
	}
}

func TestCaptureSessionWisdom_PropagatesError(t *testing.T) {
	prov := &mockChatProvider{err: errors.New("429")}
	store := wisdom.NewStore()
	_, err := CaptureSessionWisdom(nil, plan.Session{}, nil, nil, store, prov, "m")
	if err == nil {
		t.Error("expected error from provider to propagate")
	}
}

func TestCaptureSessionWisdom_DropsEmptyDescription(t *testing.T) {
	prov := &mockChatProvider{response: `{"learnings":[
  {"category":"pattern","description":""},
  {"category":"pattern","description":"valid one"}
]}`}
	store := wisdom.NewStore()
	n, _ := CaptureSessionWisdom(nil, plan.Session{}, nil, nil, store, prov, "m")
	if n != 1 {
		t.Errorf("expected 1 (dropped the empty one), got %d", n)
	}
}

func TestBuildWisdomContext_IncludesAll(t *testing.T) {
	session := plan.Session{
		ID:          "S1",
		Title:       "Test",
		Description: "session desc",
		Tasks: []plan.Task{
			{ID: "T1", Description: "do x", Files: []string{"a.go"}},
			{ID: "T2", Description: "do y"},
		},
	}
	results := []plan.TaskExecResult{
		{TaskID: "T1", Success: true},
		{TaskID: "T2", Success: false},
	}
	acceptance := []plan.AcceptanceResult{
		{CriterionID: "AC1", Description: "build", Passed: true},
		{CriterionID: "AC2", Description: "test", Passed: false},
	}
	blob := buildWisdomContext(session, results, acceptance)
	for _, want := range []string{"S1", "Test", "session desc", "T1", "do x", "a.go", "T2", "ok", "failed", "AC1", "PASS", "AC2", "FAIL"} {
		if !strings.Contains(blob, want) {
			t.Errorf("context missing %q:\n%s", want, blob)
		}
	}
}

func TestSaveWisdom_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := wisdom.NewStore()
	store.Record("S1", wisdom.Learning{Category: wisdom.Pattern, Description: "a pattern"})
	store.Record("S1", wisdom.Learning{Category: wisdom.Gotcha, Description: "a gotcha"})

	if err := SaveWisdom(dir, "test-sow", store); err != nil {
		t.Fatalf("SaveWisdom: %v", err)
	}
	loaded, err := LoadWisdom(dir, "test-sow")
	if err != nil {
		t.Fatalf("LoadWisdom: %v", err)
	}
	if len(loaded.Learnings()) != 2 {
		t.Errorf("expected 2 learnings, got %d", len(loaded.Learnings()))
	}
}

func TestLoadWisdom_MissingFile_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	store, err := LoadWisdom(dir, "nonexistent")
	if err != nil {
		t.Fatalf("missing file should return empty, not error: %v", err)
	}
	if len(store.Learnings()) != 0 {
		t.Error("missing file should yield empty store")
	}
}

func TestSaveWisdom_SanitizesSowID(t *testing.T) {
	dir := t.TempDir()
	store := wisdom.NewStore()
	store.Record("X", wisdom.Learning{Category: wisdom.Pattern, Description: "x"})
	// SOW IDs with slashes should be sanitized so we don't accidentally
	// create sub-directories.
	if err := SaveWisdom(dir, "sow/with/slashes", store); err != nil {
		t.Fatalf("SaveWisdom: %v", err)
	}
	path := wisdomPathForSOW(dir, "sow/with/slashes")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("sanitized path should exist: %v", err)
	}
}

func TestBuildSOWNativePrompts_InjectsWisdom(t *testing.T) {
	store := wisdom.NewStore()
	store.Record("S0", wisdom.Learning{
		Category:    wisdom.Pattern,
		Description: "handlers under cmd/api/http",
	})
	sow := &plan.SOW{ID: "w", Name: "W"}
	session := plan.Session{ID: "S1", Title: "t"}
	task := plan.Task{ID: "T1", Description: "do a thing"}

	sys, _ := buildSOWNativePrompts(sow, session, task, nil, 0, nil, store)
	if !strings.Contains(sys, "handlers under cmd/api/http") {
		t.Errorf("system prompt should include injected wisdom:\n%s", sys)
	}
}

// --- Smart 9: cross-review tests ---

func TestUnmarshalCrossReview(t *testing.T) {
	raw := `{
  "approved": false,
  "score": 40,
  "summary": "broken",
  "concerns": [
    {"severity": "blocking", "file": "a.go", "line": 12, "description": "null deref"},
    {"severity": "minor", "description": "naming"}
  ]
}`
	var out crossReviewResult
	if err := unmarshalCrossReview([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Approved {
		t.Error("should not be approved")
	}
	if len(out.Concerns) != 2 {
		t.Errorf("concerns = %d", len(out.Concerns))
	}
	if out.Concerns[0].Severity != "blocking" {
		t.Errorf("first concern severity = %q", out.Concerns[0].Severity)
	}
}

func TestRunCrossModelReview_NoProvider_ReturnsNil(t *testing.T) {
	cfg := sowNativeConfig{RepoRoot: t.TempDir()}
	result := runCrossModelReview(context.Background(), plan.Session{ID: "S1"}, cfg)
	if result != nil {
		t.Error("nil provider should yield nil result")
	}
}

func TestRunCrossModelReview_NoDiff_ReturnsNil(t *testing.T) {
	// Set up a repo with no unstaged changes
	dir := t.TempDir()
	initEmptyGitRepo(t, dir)
	prov := &mockChatProvider{response: `{"approved":true,"score":95,"summary":"fine","concerns":[]}`}
	cfg := sowNativeConfig{RepoRoot: dir, ReviewProvider: prov, Model: "m"}
	result := runCrossModelReview(context.Background(), plan.Session{ID: "S1"}, cfg)
	// Empty diff → nil result.
	if result != nil {
		t.Errorf("empty diff should yield nil result, got %+v", result)
	}
	if prov.calls != 0 {
		t.Error("no diff means no LLM call")
	}
}

func TestRunCrossModelReview_WithDiff_CallsProvider(t *testing.T) {
	dir := t.TempDir()
	initEmptyGitRepo(t, dir)
	// Modify the seeded file so `git diff HEAD` picks it up (untracked
	// files don't show in diff HEAD without --intent-to-add).
	os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("changed\n"), 0o644)

	prov := &mockChatProvider{response: `{"approved":true,"score":90,"summary":"fine","concerns":[]}`}
	cfg := sowNativeConfig{RepoRoot: dir, ReviewProvider: prov, Model: "m"}
	result := runCrossModelReview(context.Background(), plan.Session{ID: "S1", Title: "test"}, cfg)
	if result == nil {
		t.Fatal("expected review result")
	}
	if !result.Approved {
		t.Error("review should be approved")
	}
	if prov.calls != 1 {
		t.Errorf("expected 1 provider call, got %d", prov.calls)
	}
}

// --- Smart 10: scope gate tests ---

func TestCheckScopeViolations_NoDeclaredScope_Passes(t *testing.T) {
	session := plan.Session{ID: "S1"}
	touched := []string{"random/file.go", "another.md"}
	violations := checkScopeViolations(session, touched)
	if len(violations) != 0 {
		t.Errorf("no declared scope should allow any file, got %v", violations)
	}
}

func TestCheckScopeViolations_ExactMatch(t *testing.T) {
	session := plan.Session{
		ID:      "S1",
		Outputs: []string{"src/main.go"},
		Tasks: []plan.Task{
			{ID: "T1", Files: []string{"src/lib.go"}},
		},
	}
	// Both declared files are allowed
	violations := checkScopeViolations(session, []string{"src/main.go", "src/lib.go"})
	if len(violations) != 0 {
		t.Errorf("declared files should not violate: %v", violations)
	}
	// An undeclared file is flagged
	violations = checkScopeViolations(session, []string{"src/secret.go"})
	if len(violations) != 1 || violations[0] != "src/secret.go" {
		t.Errorf("expected violation for src/secret.go, got %v", violations)
	}
}

func TestCheckScopeViolations_DirectoryPrefix(t *testing.T) {
	// Declaring "src/auth/" as an output should allow any file under it
	session := plan.Session{
		ID:      "S1",
		Outputs: []string{"src/auth/"},
	}
	violations := checkScopeViolations(session, []string{"src/auth/token.go", "src/auth/middleware.go"})
	if len(violations) != 0 {
		t.Errorf("directory prefix should allow children: %v", violations)
	}
	// But files outside the prefix are still flagged
	violations = checkScopeViolations(session, []string{"src/other/file.go"})
	if len(violations) != 1 {
		t.Errorf("outside-prefix file should violate: %v", violations)
	}
}

func TestCheckScopeViolations_AllowsStokeDir(t *testing.T) {
	session := plan.Session{ID: "S1", Outputs: []string{"src/main.go"}}
	// .stoke/ is always allowed (state files, caches)
	violations := checkScopeViolations(session, []string{".stoke/sow-state.json", "src/main.go"})
	if len(violations) != 0 {
		t.Errorf(".stoke/ should be allowed: %v", violations)
	}
}

func TestCheckScopeViolations_SortsOutput(t *testing.T) {
	session := plan.Session{ID: "S1", Outputs: []string{"declared.go"}}
	touched := []string{"z.go", "a.go", "m.go"}
	violations := checkScopeViolations(session, touched)
	sorted := make([]string, len(violations))
	copy(sorted, violations)
	sort.Strings(sorted)
	if !reflect.DeepEqual(violations, sorted) {
		t.Errorf("violations not sorted: %v", violations)
	}
}

// --- Smart 11: parallel wave builder tests ---

func TestBuildParallelWaves_AllDisjoint(t *testing.T) {
	tasks := []plan.Task{
		{ID: "T1", Files: []string{"a.go"}},
		{ID: "T2", Files: []string{"b.go"}},
		{ID: "T3", Files: []string{"c.go"}},
	}
	waves := buildParallelWaves(tasks)
	if len(waves) != 1 {
		t.Errorf("expected 1 wave for 3 disjoint tasks, got %d", len(waves))
	}
	if len(waves[0]) != 3 {
		t.Errorf("wave should have all 3 tasks, got %d", len(waves[0]))
	}
}

func TestBuildParallelWaves_FileConflictSeparatesWaves(t *testing.T) {
	tasks := []plan.Task{
		{ID: "T1", Files: []string{"a.go"}},
		{ID: "T2", Files: []string{"a.go", "b.go"}}, // conflicts with T1
	}
	waves := buildParallelWaves(tasks)
	if len(waves) != 2 {
		t.Errorf("expected 2 waves when files conflict, got %d", len(waves))
	}
}

func TestBuildParallelWaves_DependenciesForceOrder(t *testing.T) {
	tasks := []plan.Task{
		{ID: "T1", Files: []string{"a.go"}},
		{ID: "T2", Files: []string{"b.go"}, Dependencies: []string{"T1"}},
	}
	waves := buildParallelWaves(tasks)
	if len(waves) != 2 {
		t.Errorf("expected 2 waves due to dependency, got %d", len(waves))
	}
	if waves[0][0] != 0 || waves[1][0] != 1 {
		t.Errorf("wave order wrong: %v", waves)
	}
}

func TestBuildParallelWaves_NoFilesRunAlone(t *testing.T) {
	tasks := []plan.Task{
		{ID: "T1"}, // no files
		{ID: "T2", Files: []string{"b.go"}},
		{ID: "T3", Files: []string{"c.go"}},
	}
	waves := buildParallelWaves(tasks)
	// T1 should be alone; T2+T3 can share a wave (or be separate, not critical).
	// At minimum the no-files task must be in its own wave.
	for _, w := range waves {
		if len(w) == 1 && w[0] == 0 {
			return // found T1 alone
		}
	}
	t.Errorf("T1 (no files) should be in its own wave: %v", waves)
}

func TestBuildParallelWaves_Empty(t *testing.T) {
	waves := buildParallelWaves(nil)
	if len(waves) != 0 {
		t.Errorf("empty input should produce empty waves, got %d", len(waves))
	}
}

func TestBuildParallelWaves_CyclicDeps_ForcesSequential(t *testing.T) {
	// Classic cycle: T1 -> T2 -> T1
	tasks := []plan.Task{
		{ID: "T1", Files: []string{"a.go"}, Dependencies: []string{"T2"}},
		{ID: "T2", Files: []string{"b.go"}, Dependencies: []string{"T1"}},
	}
	waves := buildParallelWaves(tasks)
	// The algorithm force-places tasks one at a time to break the
	// cycle. We should see 2 waves with 1 task each.
	if len(waves) != 2 {
		t.Errorf("cycle should resolve to sequential waves, got %d waves: %v", len(waves), waves)
	}
	// Both tasks must be placed eventually.
	placed := 0
	for _, w := range waves {
		placed += len(w)
	}
	if placed != 2 {
		t.Errorf("all 2 tasks should be placed, got %d", placed)
	}
}

// --- Helpers ---

func initEmptyGitRepo(t *testing.T, dir string) {
	t.Helper()
	// Use the worktree EnsureRepo helper since it's already tested.
	// But we can't import it here (import cycle). Do it manually.
	mustCmd := func(name string, args ...string) {
		if out, err := runCmdHelper(dir, name, args...); err != nil {
			t.Fatalf("%s %v: %v: %s", name, args, err, out)
		}
	}
	mustCmd("git", "init")
	mustCmd("git", "config", "user.name", "Test")
	mustCmd("git", "config", "user.email", "test@example.com")
	os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o644)
	mustCmd("git", "add", ".")
	mustCmd("git", "commit", "-m", "init", "--no-gpg-sign")
}

func runCmdHelper(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ensure json package is referenced by the test file so imports don't
// get auto-removed.
var _ = json.RawMessage(nil)

// ensure fmt is referenced
var _ = fmt.Sprint
