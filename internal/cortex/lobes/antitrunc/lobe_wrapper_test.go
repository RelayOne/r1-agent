package antitrunclobe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/cortex"
)

// historyOf wraps a list of plain assistant strings into the
// cortex.LobeInput shape AntiTruncLobe.Run consumes. Mirrors the
// agentloop.Message construction the production cortex emits but is
// scoped to assistant-only text content blocks because that is the
// subset the Detector consumes.
func historyOf(turns ...string) []agentloop.Message {
	out := make([]agentloop.Message, 0, len(turns))
	for _, t := range turns {
		out = append(out, agentloop.Message{
			Role:    "assistant",
			Content: []agentloop.ContentBlock{{Type: "text", Text: t}},
		})
	}
	return out
}

// criticalNotes returns the snapshot of every SevCritical Note in the
// workspace. The cortex.Workspace API exposes UnresolvedCritical which
// returns the same shape (it filters by Severity == SevCritical AND no
// resolving Note); for our tests no resolution Notes are ever published
// so the two are equivalent.
func criticalNotes(ws *cortex.Workspace) []cortex.Note {
	return ws.UnresolvedCritical()
}

func TestLobe_NoFindings_NoNotes(t *testing.T) {
	ws := cortex.NewWorkspace(nil, nil)
	l := NewAntiTruncLobe(ws, "", "")
	if err := l.Run(context.Background(), cortex.LobeInput{
		History: historyOf("build green; tests pass"),
	}); err != nil {
		t.Fatal(err)
	}
	if got := len(ws.Snapshot()); got != 0 {
		t.Errorf("expected zero Notes, got %d: %+v", got, ws.Snapshot())
	}
}

func TestLobe_TruncationPhrase_OneCriticalNote(t *testing.T) {
	ws := cortex.NewWorkspace(nil, nil)
	l := NewAntiTruncLobe(ws, "", "")
	if err := l.Run(context.Background(), cortex.LobeInput{
		History: historyOf("i'll stop here for now"),
	}); err != nil {
		t.Fatal(err)
	}
	notes := criticalNotes(ws)
	if len(notes) != 1 {
		t.Fatalf("expected 1 critical Note, got %d: %+v", len(notes), notes)
	}
	if notes[0].LobeID != "antitrunc" {
		t.Errorf("LobeID = %q, want antitrunc", notes[0].LobeID)
	}
	if !strings.Contains(notes[0].Title, "premature_stop_let_me") {
		t.Errorf("Title missing phrase ID: %q", notes[0].Title)
	}
}

func TestLobe_UncheckedPlan_OneCriticalNote(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(plan, []byte("- [x] one\n- [ ] two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := cortex.NewWorkspace(nil, nil)
	l := NewAntiTruncLobe(ws, plan, "")
	if err := l.Run(context.Background(), cortex.LobeInput{}); err != nil {
		t.Fatal(err)
	}
	notes := criticalNotes(ws)
	if len(notes) != 1 {
		t.Fatalf("expected 1 critical Note on partial plan, got %d", len(notes))
	}
	if !strings.Contains(notes[0].Body, "1/2 unchecked") {
		t.Errorf("Body missing count: %q", notes[0].Body)
	}
}

func TestLobe_FalseCompletionRecentCommit_OneCriticalNote(t *testing.T) {
	ws := cortex.NewWorkspace(nil, nil)
	l := NewAntiTruncLobe(ws, "", "").WithGitLog(func(n int) ([]string, error) {
		return []string{"spec 12 done"}, nil
	})
	if err := l.Run(context.Background(), cortex.LobeInput{}); err != nil {
		t.Fatal(err)
	}
	notes := criticalNotes(ws)
	if len(notes) != 1 {
		t.Fatalf("expected 1 critical Note from commit-body, got %d", len(notes))
	}
	if !strings.Contains(notes[0].Title, "false completion") {
		t.Errorf("Title missing 'false completion': %q", notes[0].Title)
	}
}

func TestLobe_IDAndKind(t *testing.T) {
	l := NewAntiTruncLobe(nil, "", "")
	if l.ID() != "antitrunc" {
		t.Errorf("ID = %q, want antitrunc", l.ID())
	}
	if l.Kind() != cortex.KindDeterministic {
		t.Errorf("Kind = %v, want KindDeterministic", l.Kind())
	}
	if l.Description() == "" {
		t.Errorf("Description() returned empty")
	}
}

func TestLobe_NilWorkspace_NoOp(t *testing.T) {
	l := NewAntiTruncLobe(nil, "", "")
	// Must not panic, must not error, even with truncation phrase.
	err := l.Run(context.Background(), cortex.LobeInput{
		History: historyOf("i'll defer the rest"),
	})
	if err != nil {
		t.Errorf("nil-workspace run errored: %v", err)
	}
}

func TestLobe_AllFourSignals_FourNotes(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.md")
	os.WriteFile(plan, []byte("- [ ] open\n"), 0o644)
	sp := filepath.Join(dir, "feat.md")
	os.WriteFile(sp, []byte("<!-- STATUS: in-progress -->\n- [ ] go\n"), 0o644)

	ws := cortex.NewWorkspace(nil, nil)
	l := NewAntiTruncLobe(ws, plan, filepath.Join(dir, "feat*.md")).
		WithGitLog(func(n int) ([]string, error) { return []string{"spec 1 done"}, nil })

	err := l.Run(context.Background(), cortex.LobeInput{
		History: historyOf("i'll stop here"),
	})
	if err != nil {
		t.Fatal(err)
	}
	notes := criticalNotes(ws)
	if len(notes) < 4 {
		t.Errorf("expected at least 4 critical Notes (one per source), got %d", len(notes))
	}
}
