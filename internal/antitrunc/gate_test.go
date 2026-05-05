package antitrunc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGate_CleanOutput_NoFindings exercises the happy path: a clean
// assistant turn, no plan/spec, no commits — the gate must allow
// end_turn (return "").
func TestGate_CleanOutput_NoFindings(t *testing.T) {
	g := &Gate{}
	got := g.CheckOutput([]Message{
		{Role: "user", Text: "do the work"},
		{Role: "assistant", Text: "Here is the implementation. Tests pass."},
	})
	if got != "" {
		t.Errorf("expected clean gate, got: %s", got)
	}
}

// TestGate_TruncationPhrase_RefusesEndTurn covers the §"Layer 2" hot
// path: a phrase hit alone refuses end_turn even with no plan/spec.
func TestGate_TruncationPhrase_RefusesEndTurn(t *testing.T) {
	g := &Gate{}
	got := g.CheckOutput([]Message{
		{Role: "user", Text: "do everything"},
		{Role: "assistant", Text: "ok, i'll stop here for the day"},
	})
	if got == "" {
		t.Fatal("expected gate to refuse, got empty")
	}
	if !strings.Contains(got, "[ANTI-TRUNCATION]") {
		t.Errorf("missing [ANTI-TRUNCATION] prefix: %s", got)
	}
	if !strings.Contains(got, "premature_stop_let_me") {
		t.Errorf("missing phrase ID: %s", got)
	}
}

// TestGate_UncheckedPlan_RefusesEndTurn covers the plan signal: even
// with clean assistant text, an unchecked plan must refuse end_turn.
func TestGate_UncheckedPlan_RefusesEndTurn(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.md")
	body := "# plan\n- [x] one\n- [ ] two\n- [ ] three\n"
	if err := os.WriteFile(plan, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &Gate{PlanPath: plan}
	got := g.CheckOutput([]Message{
		{Role: "assistant", Text: "the build is green"},
	})
	if got == "" {
		t.Fatal("expected gate to refuse on unchecked plan, got empty")
	}
	if !strings.Contains(got, "2/3 plan items unchecked") {
		t.Errorf("missing count: %s", got)
	}
}

// TestGate_FullyCheckedPlan_Allows verifies the gate passes when all
// plan boxes are checked (no false positive).
func TestGate_FullyCheckedPlan_Allows(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(plan, []byte("- [x] one\n- [x] two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &Gate{PlanPath: plan}
	got := g.CheckOutput([]Message{{Role: "assistant", Text: "done"}})
	if got != "" {
		t.Errorf("expected clean gate on fully-checked plan, got: %s", got)
	}
}

// TestGate_SpecInProgressUnchecked_RefusesEndTurn covers the spec
// signal: a spec marked STATUS:in-progress with unchecked items
// blocks end_turn.
func TestGate_SpecInProgressUnchecked_RefusesEndTurn(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "foo.md")
	body := "<!-- STATUS: in-progress -->\n# foo\n- [ ] x\n- [ ] y\n"
	if err := os.WriteFile(sp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &Gate{SpecPaths: []string{sp}}
	got := g.CheckOutput([]Message{{Role: "assistant", Text: "ok"}})
	if got == "" {
		t.Fatal("expected gate to refuse on in-progress spec with unchecked")
	}
	if !strings.Contains(got, "unchecked items") {
		t.Errorf("missing unchecked detail: %s", got)
	}
}

// TestGate_SpecDone_Ignored verifies a spec NOT marked in-progress
// is ignored (no false positive when status is "ready" or "done").
func TestGate_SpecDone_Ignored(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "ready.md")
	body := "<!-- STATUS: ready -->\n- [ ] something\n"
	if err := os.WriteFile(sp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &Gate{SpecPaths: []string{sp}}
	got := g.CheckOutput([]Message{{Role: "assistant", Text: "ok"}})
	if got != "" {
		t.Errorf("expected clean gate (spec not in-progress), got: %s", got)
	}
}

// TestGate_FalseCompletionInCommit_NeedsCorroboration covers the
// multi-signal corroboration rule: a false-completion commit body
// alone does NOT block (anti-false-positive); but combined with
// unchecked plan items it does.
func TestGate_FalseCompletionInCommit_NeedsCorroboration(t *testing.T) {
	// Case A: false-completion alone -> allow (corroboration missing).
	g := &Gate{
		CommitLookbackFn: func(n int) ([]string, error) {
			return []string{"spec 9 done — merging"}, nil
		},
	}
	got := g.CheckOutput([]Message{{Role: "assistant", Text: "ok"}})
	if got != "" {
		t.Errorf("expected gate to require corroboration, got refusal: %s", got)
	}

	// Case B: false-completion + unchecked plan -> refuse.
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(plan, []byte("- [ ] open\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g.PlanPath = plan
	got = g.CheckOutput([]Message{{Role: "assistant", Text: "ok"}})
	if got == "" {
		t.Fatal("expected gate to refuse on corroborated false-completion")
	}
	if !strings.Contains(got, "false completion") {
		t.Errorf("missing false-completion detail: %s", got)
	}
}

// TestGate_AdvisoryMode_DoesNotBlock verifies the operator override
// path: when Advisory=true, findings are forwarded to AdvisoryFn but
// CheckOutput returns "" so the loop is not blocked.
func TestGate_AdvisoryMode_DoesNotBlock(t *testing.T) {
	var captured []Finding
	g := &Gate{
		Advisory:   true,
		AdvisoryFn: func(f Finding) { captured = append(captured, f) },
	}
	got := g.CheckOutput([]Message{
		{Role: "assistant", Text: "i'll stop here"},
	})
	if got != "" {
		t.Errorf("advisory mode must not block, got: %s", got)
	}
	if len(captured) == 0 {
		t.Error("advisory mode must still detect and forward findings")
	}
}

// TestGate_CommitLookbackError_NotFatal verifies a CommitLookbackFn
// that returns an error doesn't crash the gate; other signals still
// work.
func TestGate_CommitLookbackError_NotFatal(t *testing.T) {
	g := &Gate{
		CommitLookbackFn: func(n int) ([]string, error) {
			return nil, errors.New("git not available")
		},
	}
	got := g.CheckOutput([]Message{{Role: "assistant", Text: "ok"}})
	if got != "" {
		t.Errorf("commit-fn error must not produce a refusal, got: %s", got)
	}
}

// TestGate_LastAssistantText verifies the gate scans only the LAST
// assistant message, not historic ones (otherwise old phrases haunt
// every turn).
func TestGate_LastAssistantText(t *testing.T) {
	g := &Gate{}
	// Old assistant turn has truncation phrase, latest is clean.
	got := g.CheckOutput([]Message{
		{Role: "assistant", Text: "i'll stop here"},
		{Role: "user", Text: "no, continue"},
		{Role: "assistant", Text: "tests pass; build green"},
	})
	if got != "" {
		t.Errorf("gate scanned historic turn, got: %s", got)
	}
}

// TestSpecInProgress_Variants exercises the marker tolerance.
func TestSpecInProgress_Variants(t *testing.T) {
	cases := map[string]bool{
		"STATUS: in-progress":           true,
		"<!-- STATUS:in-progress -->":   true,
		"STATUS: IN-PROGRESS":           true,
		"status: in_progress":           true,
		"<!-- STATUS: ready -->":        false,
		"STATUS: done":                  false,
		"":                              false,
	}
	for in, want := range cases {
		if got := specInProgress(in); got != want {
			t.Errorf("specInProgress(%q) = %v, want %v", in, got, want)
		}
	}
}
