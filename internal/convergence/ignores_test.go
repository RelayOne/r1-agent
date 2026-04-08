package convergence

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIgnoreList_LoadMissing(t *testing.T) {
	dir := t.TempDir()
	list, err := LoadIgnores(dir)
	if err != nil {
		t.Fatalf("LoadIgnores: %v", err)
	}
	if list == nil || len(list.Entries) != 0 {
		t.Errorf("expected empty list, got %+v", list)
	}
	if list.Version != 1 {
		t.Errorf("version = %d", list.Version)
	}
}

func TestIgnoreList_AddRequiresReason(t *testing.T) {
	list := &IgnoreList{Version: 1}
	err := list.Add(IgnoreEntry{RuleID: "no-secrets", File: "foo.go"})
	if err == nil {
		t.Error("expected error for missing reason")
	}

	err = list.Add(IgnoreEntry{RuleID: "no-secrets", File: "foo.go", Reason: "test fixture"})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(list.Entries) != 1 {
		t.Errorf("entries = %d", len(list.Entries))
	}
}

func TestIgnoreList_AddRequiresRuleIDAndFile(t *testing.T) {
	list := &IgnoreList{Version: 1}
	if err := list.Add(IgnoreEntry{Reason: "x"}); err == nil {
		t.Error("expected error for missing RuleID")
	}
	if err := list.Add(IgnoreEntry{Reason: "x", RuleID: "r1"}); err == nil {
		t.Error("expected error for missing File")
	}
}

func TestIgnoreList_DeduplicatesEntries(t *testing.T) {
	list := &IgnoreList{Version: 1}
	e := IgnoreEntry{RuleID: "r1", File: "foo.go", LineStart: 10, Pattern: "xxx", Reason: "fine"}
	list.Add(e)
	list.Add(e)
	list.Add(e)
	if len(list.Entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(list.Entries))
	}
}

func TestIgnoreList_SaveAndReload(t *testing.T) {
	dir := t.TempDir()
	list, _ := LoadIgnores(dir)
	list.Add(IgnoreEntry{RuleID: "no-secrets", File: "test/fixtures/creds.go", Reason: "test data", ProposedBy: "vp-eng", ApprovedBy: "cto"})
	if err := list.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded, err := LoadIgnores(dir)
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(reloaded.Entries) != 1 {
		t.Fatalf("entries = %d", len(reloaded.Entries))
	}
	got := reloaded.Entries[0]
	if got.RuleID != "no-secrets" || got.ApprovedBy != "cto" {
		t.Errorf("entry roundtrip broken: %+v", got)
	}
}

func TestIgnoreList_Matches_ExactFile(t *testing.T) {
	list := &IgnoreList{Version: 1}
	list.Add(IgnoreEntry{RuleID: "r1", File: "src/foo.go", Reason: "x"})

	if !list.Matches(Finding{RuleID: "r1", File: "src/foo.go"}) {
		t.Error("exact match should hit")
	}
	if list.Matches(Finding{RuleID: "r1", File: "src/bar.go"}) {
		t.Error("different file should not hit")
	}
	if list.Matches(Finding{RuleID: "r2", File: "src/foo.go"}) {
		t.Error("different rule should not hit")
	}
}

func TestIgnoreList_Matches_GlobPattern(t *testing.T) {
	list := &IgnoreList{Version: 1}
	list.Add(IgnoreEntry{RuleID: "r1", File: "*.generated.ts", Reason: "codegen"})

	if !list.Matches(Finding{RuleID: "r1", File: "src/foo.generated.ts"}) {
		t.Error("glob should match basename")
	}
	if list.Matches(Finding{RuleID: "r1", File: "src/foo.ts"}) {
		t.Error("non-matching glob should miss")
	}
}

func TestIgnoreList_Matches_LineRange(t *testing.T) {
	list := &IgnoreList{Version: 1}
	list.Add(IgnoreEntry{RuleID: "r1", File: "foo.go", LineStart: 10, LineEnd: 20, Reason: "x"})

	if !list.Matches(Finding{RuleID: "r1", File: "foo.go", Line: 15}) {
		t.Error("line 15 should match range 10-20")
	}
	if list.Matches(Finding{RuleID: "r1", File: "foo.go", Line: 25}) {
		t.Error("line 25 should miss range 10-20")
	}
}

func TestIgnoreList_Matches_PatternSubstring(t *testing.T) {
	list := &IgnoreList{Version: 1}
	list.Add(IgnoreEntry{RuleID: "r1", File: "foo.go", Pattern: "TEST_API_KEY", Reason: "fixture"})

	if !list.Matches(Finding{RuleID: "r1", File: "foo.go", Evidence: "found TEST_API_KEY=abc"}) {
		t.Error("pattern substring should match")
	}
	if list.Matches(Finding{RuleID: "r1", File: "foo.go", Evidence: "found REAL_KEY=abc"}) {
		t.Error("different pattern should miss")
	}
}

func TestApplyIgnores_FiltersAndRecomputesScore(t *testing.T) {
	list := &IgnoreList{Version: 1}
	list.Add(IgnoreEntry{RuleID: "no-secrets", File: "test/fixtures.go", Reason: "test fixture"})

	report := &Report{
		MissionID: "m1",
		Findings: []Finding{
			{RuleID: "no-secrets", File: "test/fixtures.go", Severity: SevBlocking, Description: "hardcoded key"},
			{RuleID: "no-secrets", File: "real/code.go", Severity: SevBlocking, Description: "real leak"},
			{RuleID: "other-rule", File: "foo.go", Severity: SevMinor},
		},
		Score:       0.65, // 2*blocking(0.15) + 1*minor(0.03) + overhead → 1.0-0.33 ≈ 0.67; the actual value isn't critical
		IsConverged: false,
	}

	filtered := ApplyIgnores(report, list)
	if len(filtered.Findings) != 2 {
		t.Errorf("filtered findings = %d, want 2", len(filtered.Findings))
	}
	// One blocking finding remains → still not converged
	if filtered.IsConverged {
		t.Error("should not be converged — one blocking finding remains")
	}
	// New score: 1.0 - 0.15(blocking) - 0.03(minor) = 0.82
	if filtered.Score < 0.8 || filtered.Score > 0.85 {
		t.Errorf("filtered score = %f, want ~0.82", filtered.Score)
	}
}

func TestApplyIgnores_ConvergesWhenAllBlockingFiltered(t *testing.T) {
	list := &IgnoreList{Version: 1}
	list.Add(IgnoreEntry{RuleID: "r1", File: "a.go", Reason: "x"})
	list.Add(IgnoreEntry{RuleID: "r2", File: "b.go", Reason: "x"})

	report := &Report{
		Findings: []Finding{
			{RuleID: "r1", File: "a.go", Severity: SevBlocking},
			{RuleID: "r2", File: "b.go", Severity: SevBlocking},
		},
	}
	filtered := ApplyIgnores(report, list)
	if !filtered.IsConverged {
		t.Error("should converge when all blocking findings are ignored")
	}
}

func TestApplyIgnores_NilListNoOp(t *testing.T) {
	report := &Report{Findings: []Finding{{RuleID: "r1", Severity: SevBlocking}}}
	if out := ApplyIgnores(report, nil); out != report {
		t.Error("nil ignore list should return the same report pointer")
	}
	empty := &IgnoreList{Version: 1}
	if out := ApplyIgnores(report, empty); out != report {
		t.Error("empty ignore list should return the same report pointer")
	}
}

func TestRepeatTracker_RecordAndTrigger(t *testing.T) {
	t2 := NewRepeatTracker()
	f := Finding{RuleID: "r1", File: "foo.go", Line: 10, Severity: SevBlocking}

	if n := t2.Record(f); n != 1 {
		t.Errorf("first record = %d", n)
	}
	if n := t2.Record(f); n != 2 {
		t.Errorf("second record = %d", n)
	}
	if n := t2.Record(f); n != 3 {
		t.Errorf("third record = %d", n)
	}
}

func TestRepeatTracker_ResetClearsCount(t *testing.T) {
	t2 := NewRepeatTracker()
	f := Finding{RuleID: "r1", File: "foo.go", Line: 10}
	t2.Record(f)
	t2.Record(f)
	t2.Reset(FlagSignature{RuleID: "r1", File: "foo.go", Line: 10})
	if n := t2.Count(FlagSignature{RuleID: "r1", File: "foo.go", Line: 10}); n != 0 {
		t.Errorf("count after reset = %d", n)
	}
}

func TestRepeatTracker_SaveAndReload(t *testing.T) {
	dir := t.TempDir()
	t1 := NewRepeatTracker()
	t1.Record(Finding{RuleID: "r1", File: "a.go", Line: 5})
	t1.Record(Finding{RuleID: "r1", File: "a.go", Line: 5})
	if err := t1.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	t2, err := LoadRepeatTracker(dir)
	if err != nil {
		t.Fatalf("LoadRepeatTracker: %v", err)
	}
	if n := t2.Count(FlagSignature{RuleID: "r1", File: "a.go", Line: 5}); n != 2 {
		t.Errorf("reloaded count = %d", n)
	}
}

func TestRepeatTracker_RecordReportBlocks_Threshold(t *testing.T) {
	t3 := NewRepeatTracker()
	report := &Report{
		Findings: []Finding{
			{RuleID: "r1", File: "a.go", Line: 5, Severity: SevBlocking},
			{RuleID: "r2", File: "b.go", Line: 10, Severity: SevBlocking},
			{RuleID: "r3", File: "c.go", Severity: SevMinor}, // non-blocking — ignored
		},
	}

	// First pass: everything hits 1. Threshold of 2 means nothing triggers yet.
	triggered := t3.RecordReportBlocks(report, 2)
	if len(triggered) != 0 {
		t.Errorf("first pass should not trigger: %v", triggered)
	}

	// Second pass: both blocking findings hit 2 → both trigger.
	triggered = t3.RecordReportBlocks(report, 2)
	if len(triggered) != 2 {
		t.Errorf("second pass should trigger 2 findings, got %d", len(triggered))
	}
}

// --- Judge tests ---

// MockOverrideJudge is a deterministic test double for OverrideJudge. Its
// Propose always approves every finding (rubber-stamp), and Approve echoes
// the proposal as-is. Real judges are LLM-backed.
type MockOverrideJudge struct {
	approveAll bool
	denyAll    bool
	errOnPropose error
	errOnApprove error
}

func (m *MockOverrideJudge) Propose(ctx JudgeContext) (*JudgeProposal, error) {
	if m.errOnPropose != nil {
		return nil, m.errOnPropose
	}
	prop := &JudgeProposal{
		Rationale: "mock vp eng: all findings look like false positives given the passing build",
	}
	for _, f := range ctx.Findings {
		prop.Ignores = append(prop.Ignores, IgnoreEntry{
			RuleID:     f.RuleID,
			File:       f.File,
			LineStart:  f.Line,
			Reason:     "mock: " + f.Description,
			ProposedBy: "vp-eng",
		})
	}
	return prop, nil
}

func (m *MockOverrideJudge) Approve(ctx JudgeContext, proposal *JudgeProposal) (*JudgeDecision, error) {
	if m.errOnApprove != nil {
		return nil, m.errOnApprove
	}
	dec := &JudgeDecision{Rationale: "mock cto: agreed", Continuations: proposal.Continuations}
	if m.denyAll {
		dec.Denied = proposal.Ignores
		dec.Rationale = "mock cto: denied all"
		return dec, nil
	}
	for _, e := range proposal.Ignores {
		e.ApprovedBy = "cto"
		dec.Approved = append(dec.Approved, e)
	}
	return dec, nil
}

func TestRunOverrideFlow_HappyPath(t *testing.T) {
	judge := &MockOverrideJudge{approveAll: true}
	list := &IgnoreList{Version: 1}
	ctx := JudgeContext{
		MissionID: "m1",
		Findings: []Finding{
			{RuleID: "r1", File: "a.go", Line: 5, Severity: SevBlocking, Description: "flagged string"},
		},
		BuildPassed: true,
		TestsPassed: true,
	}
	decision, err := RunOverrideFlow(judge, list, ctx)
	if err != nil {
		t.Fatalf("RunOverrideFlow: %v", err)
	}
	if len(decision.Approved) != 1 {
		t.Errorf("approved = %d", len(decision.Approved))
	}
	if len(list.Entries) != 1 {
		t.Errorf("list should have 1 entry, got %d", len(list.Entries))
	}
	if list.Entries[0].ApprovedBy != "cto" {
		t.Error("entry should be stamped ApprovedBy=cto")
	}
	if list.Entries[0].ProposedBy != "vp-eng" {
		t.Error("entry should be stamped ProposedBy=vp-eng")
	}
}

func TestRunOverrideFlow_CtoDeniesAll(t *testing.T) {
	judge := &MockOverrideJudge{denyAll: true}
	list := &IgnoreList{Version: 1}
	ctx := JudgeContext{
		Findings: []Finding{{RuleID: "r1", File: "a.go", Severity: SevBlocking}},
	}
	decision, err := RunOverrideFlow(judge, list, ctx)
	if err != nil {
		t.Fatalf("RunOverrideFlow: %v", err)
	}
	if len(decision.Approved) != 0 {
		t.Errorf("expected no approvals on deny-all")
	}
	if len(decision.Denied) != 1 {
		t.Errorf("expected 1 denied entry")
	}
	if len(list.Entries) != 0 {
		t.Errorf("ignore list should be untouched")
	}
}

func TestRunOverrideFlow_NoFindings_NoOp(t *testing.T) {
	judge := &MockOverrideJudge{approveAll: true}
	list := &IgnoreList{Version: 1}
	decision, err := RunOverrideFlow(judge, list, JudgeContext{})
	if err != nil {
		t.Fatalf("RunOverrideFlow: %v", err)
	}
	if len(decision.Approved) != 0 {
		t.Errorf("no findings → no approvals")
	}
}

func TestRunOverrideFlow_ProposeError(t *testing.T) {
	judge := &MockOverrideJudge{errOnPropose: errors.New("429 rate limited")}
	list := &IgnoreList{Version: 1}
	_, err := RunOverrideFlow(judge, list, JudgeContext{Findings: []Finding{{RuleID: "r1", File: "a.go"}}})
	if err == nil {
		t.Error("expected error from propose")
	}
}

func TestRunOverrideFlow_ApproveError(t *testing.T) {
	judge := &MockOverrideJudge{errOnApprove: errors.New("500 internal")}
	list := &IgnoreList{Version: 1}
	_, err := RunOverrideFlow(judge, list, JudgeContext{Findings: []Finding{{RuleID: "r1", File: "a.go"}}})
	if err == nil {
		t.Error("expected error from approve")
	}
}

func TestRunOverrideFlow_NilJudge(t *testing.T) {
	_, err := RunOverrideFlow(nil, &IgnoreList{}, JudgeContext{Findings: []Finding{{RuleID: "r1"}}})
	if err == nil {
		t.Error("expected error for nil judge")
	}
}

func TestBuildJudgeContextBlob_ContainsKey(t *testing.T) {
	ctx := JudgeContext{
		MissionID:    "m1",
		BuildPassed:  true,
		TestsPassed:  true,
		LintPassed:   false,
		SOWCriteria:  []string{"build compiles", "all tests pass"},
		Findings:     []Finding{{RuleID: "no-secrets", File: "a.go", Line: 5, Description: "hardcoded key"}},
		FileSnippets: map[string]string{"a.go": "const KEY = \"abc\""},
	}
	blob := buildJudgeContextBlob(ctx)
	for _, want := range []string{"m1", "Build:  PASS", "Tests:  PASS", "Lint:   FAIL", "build compiles", "no-secrets", "a.go:5", "hardcoded key", "const KEY"} {
		if !contains(blob, want) {
			t.Errorf("blob missing %q:\n%s", want, blob)
		}
	}
}

// contains avoids importing strings in test helpers that already import it.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func TestIgnoreList_SavePermissions(t *testing.T) {
	dir := t.TempDir()
	list := &IgnoreList{Version: 1}
	list.Add(IgnoreEntry{RuleID: "r1", File: "a.go", Reason: "x"})
	if err := list.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(IgnoreListPath(dir))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Perms: 0o600 (owner read/write only) — these are CTO sign-offs
	// and shouldn't be world-readable.
	if info.Mode().Perm() != 0o600 {
		t.Errorf("ignore list perm = %o, want 600", info.Mode().Perm())
	}
}

func TestValidPatternRegex_InvalidReturnsNil(t *testing.T) {
	if validPatternRegex("") != nil {
		t.Error("empty pattern should return nil")
	}
	if validPatternRegex("[invalid") != nil {
		t.Error("invalid regex should return nil")
	}
	if validPatternRegex("valid.*") == nil {
		t.Error("valid regex should return non-nil")
	}
}

func TestRepeatTrackerPath(t *testing.T) {
	got := repeatTrackerPath("/tmp/proj")
	want := filepath.Join("/tmp/proj", ".stoke", "convergence-repeats.json")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}
