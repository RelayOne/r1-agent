package workflow

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub/builtin"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/skilltracker"
)

type closerFn func(ctx context.Context, stanceID, taskScope string) (int, error)

func newCloserWithTracker(t *testing.T) (*SkillScopeCloser, *skilltracker.Tracker, closerFn) {
	t.Helper()
	dir := t.TempDir()
	led, err := ledger.New(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	tr := skilltracker.New(led)
	c := NewSkillScopeCloser(tr)
	return c, tr, c.OnPhaseExit
}

func noteForCloser(t *testing.T, tr *skilltracker.Tracker, stanceID, skillRef, scope string) {
	t.Helper()
	if err := tr.NoteLoadInfo(builtin.LoadInfoNote{
		LoadID:     "ld-" + skillRef,
		StanceID:   stanceID,
		StanceRole: "cto",
		SkillRef:   skillRef,
		TaskScope:  scope,
		LoadedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("NoteLoadInfo: %v", err)
	}
}

func TestSkillScopeCloser_DropsAllSkillsInScope(t *testing.T) {
	_, tr, closeFn := newCloserWithTracker(t)
	noteForCloser(t, tr, "st-1", "alpha", "task-7")
	noteForCloser(t, tr, "st-1", "beta", "task-7")
	noteForCloser(t, tr, "st-1", "gamma", "task-other")

	dropped, err := closeFn(context.Background(), "st-1", "task-7")
	if err != nil {
		t.Fatalf("close-scope: %v", err)
	}
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2 (alpha + beta)", dropped)
	}
	if tr.Loaded("st-1", "alpha") || tr.Loaded("st-1", "beta") {
		t.Error("alpha + beta should be dropped")
	}
	if !tr.Loaded("st-1", "gamma") {
		t.Error("gamma (different scope) should remain")
	}
}

func TestSkillScopeCloser_IdempotentReClose(t *testing.T) {
	_, tr, closeFn := newCloserWithTracker(t)
	noteForCloser(t, tr, "st-1", "alpha", "task-7")
	first, err := closeFn(context.Background(), "st-1", "task-7")
	if err != nil || first != 1 {
		t.Errorf("first close: dropped=%d err=%v, want 1/nil", first, err)
	}
	second, err := closeFn(context.Background(), "st-1", "task-7")
	if err != nil {
		t.Errorf("second close errored: %v", err)
	}
	if second != 0 {
		t.Errorf("second close dropped %d, want 0 (idempotent)", second)
	}
}

func TestSkillScopeCloser_EmptyArgsIsNoop(t *testing.T) {
	_, tr, closeFn := newCloserWithTracker(t)
	noteForCloser(t, tr, "st-1", "alpha", "task-7")

	for _, args := range []struct{ stance, scope string }{
		{"", "task-7"},
		{"st-1", ""},
		{"", ""},
	} {
		dropped, err := closeFn(context.Background(), args.stance, args.scope)
		if err != nil {
			t.Errorf("empty args (%q,%q) errored: %v", args.stance, args.scope, err)
		}
		if dropped != 0 {
			t.Errorf("empty args (%q,%q) dropped %d, want 0", args.stance, args.scope, dropped)
		}
	}
	if !tr.Loaded("st-1", "alpha") {
		t.Error("empty-arg calls should not have dropped anything")
	}
}

func TestSkillScopeCloser_NilTrackerIsNoop(t *testing.T) {
	c := NewSkillScopeCloser(nil)
	if c.Active() {
		t.Error("nil-tracker closer should report Active()=false")
	}
	closeFn := c.OnPhaseExit
	dropped, err := closeFn(context.Background(), "st", "scope")
	if err != nil || dropped != 0 {
		t.Errorf("nil tracker: got dropped=%d err=%v, want 0/nil", dropped, err)
	}
}
