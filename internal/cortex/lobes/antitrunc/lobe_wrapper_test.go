package antitrunclobe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLobe_NoFindings_NoNotes(t *testing.T) {
	ws := NewWorkspace()
	l := NewAntiTruncLobe(ws, "", "")
	if err := l.Run(context.Background(), LobeInput{
		History: []string{"build green; tests pass"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := len(ws.Notes()); got != 0 {
		t.Errorf("expected zero Notes, got %d: %+v", got, ws.Notes())
	}
}

func TestLobe_TruncationPhrase_OneCriticalNote(t *testing.T) {
	ws := NewWorkspace()
	l := NewAntiTruncLobe(ws, "", "")
	if err := l.Run(context.Background(), LobeInput{
		History: []string{"i'll stop here for now"},
	}); err != nil {
		t.Fatal(err)
	}
	notes := ws.CriticalNotes()
	if len(notes) != 1 {
		t.Fatalf("expected 1 critical Note, got %d: %+v", len(notes), notes)
	}
	if notes[0].Source != "antitrunc" {
		t.Errorf("Source = %q, want antitrunc", notes[0].Source)
	}
	if !strings.Contains(notes[0].Text, "premature_stop_let_me") {
		t.Errorf("Text missing phrase ID: %q", notes[0].Text)
	}
}

func TestLobe_UncheckedPlan_OneCriticalNote(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(plan, []byte("- [x] one\n- [ ] two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := NewWorkspace()
	l := NewAntiTruncLobe(ws, plan, "")
	if err := l.Run(context.Background(), LobeInput{}); err != nil {
		t.Fatal(err)
	}
	notes := ws.CriticalNotes()
	if len(notes) != 1 {
		t.Fatalf("expected 1 critical Note on partial plan, got %d", len(notes))
	}
	if !strings.Contains(notes[0].Detail, "1/2 unchecked") {
		t.Errorf("Detail missing count: %q", notes[0].Detail)
	}
}

func TestLobe_FalseCompletionRecentCommit_OneCriticalNote(t *testing.T) {
	ws := NewWorkspace()
	l := NewAntiTruncLobe(ws, "", "").WithGitLog(func(n int) ([]string, error) {
		return []string{"spec 12 done"}, nil
	})
	if err := l.Run(context.Background(), LobeInput{}); err != nil {
		t.Fatal(err)
	}
	notes := ws.CriticalNotes()
	if len(notes) != 1 {
		t.Fatalf("expected 1 critical Note from commit-body, got %d", len(notes))
	}
	if !strings.Contains(notes[0].Text, "false completion") {
		t.Errorf("Text missing 'false completion': %q", notes[0].Text)
	}
}

func TestLobe_NameAndKind(t *testing.T) {
	l := NewAntiTruncLobe(nil, "", "")
	if l.Name() != "antitrunc" {
		t.Errorf("Name = %q, want antitrunc", l.Name())
	}
	if l.Kind() != KindDeterministic {
		t.Errorf("Kind = %v, want KindDeterministic", l.Kind())
	}
	if l.Kind().String() != "deterministic" {
		t.Errorf("Kind.String = %q, want deterministic", l.Kind().String())
	}
}

func TestLobe_NilWorkspace_NoOp(t *testing.T) {
	l := NewAntiTruncLobe(nil, "", "")
	// Must not panic, must not error, even with truncation phrase.
	err := l.Run(context.Background(), LobeInput{
		History: []string{"i'll defer the rest"},
	})
	if err != nil {
		t.Errorf("nil-workspace run errored: %v", err)
	}
}

func TestNoteSeverity_String(t *testing.T) {
	cases := map[NoteSeverity]string{
		SevInfo:     "info",
		SevWarning:  "warning",
		SevCritical: "critical",
	}
	for sev, want := range cases {
		if got := sev.String(); got != want {
			t.Errorf("String(%d) = %q, want %q", sev, got, want)
		}
	}
}

func TestWorkspace_PublishAndCriticalFilter(t *testing.T) {
	ws := NewWorkspace()
	ws.PublishNote(Note{Source: "x", Severity: SevInfo, Text: "info"})
	ws.PublishNote(Note{Source: "x", Severity: SevCritical, Text: "crit-1"})
	ws.PublishNote(Note{Source: "x", Severity: SevCritical, Text: "crit-2"})
	if got := len(ws.Notes()); got != 3 {
		t.Errorf("Notes len = %d, want 3", got)
	}
	if got := len(ws.CriticalNotes()); got != 2 {
		t.Errorf("CriticalNotes len = %d, want 2", got)
	}
}

func TestLobe_AllFourSignals_FourNotes(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.md")
	os.WriteFile(plan, []byte("- [ ] open\n"), 0o644)
	sp := filepath.Join(dir, "feat.md")
	os.WriteFile(sp, []byte("<!-- STATUS: in-progress -->\n- [ ] go\n"), 0o644)

	ws := NewWorkspace()
	l := NewAntiTruncLobe(ws, plan, filepath.Join(dir, "feat*.md")).
		WithGitLog(func(n int) ([]string, error) { return []string{"spec 1 done"}, nil })

	err := l.Run(context.Background(), LobeInput{
		History: []string{"i'll stop here"},
	})
	if err != nil {
		t.Fatal(err)
	}
	notes := ws.CriticalNotes()
	if len(notes) < 4 {
		t.Errorf("expected at least 4 critical Notes (one per source), got %d", len(notes))
	}
}
