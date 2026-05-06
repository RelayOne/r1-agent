package concern

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub/builtin"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/skilltracker"
)

func newTestTracker(t *testing.T) *skilltracker.Tracker {
	t.Helper()
	dir := t.TempDir()
	led, err := ledger.New(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	return skilltracker.New(led)
}

func note(t *testing.T, tr *skilltracker.Tracker, stanceID, skillRef, loadID string, tokens int, loadedAt time.Time) {
	t.Helper()
	if err := tr.NoteLoadInfo(builtin.LoadInfoNote{
		LoadID:     loadID,
		StanceID:   stanceID,
		StanceRole: "cto",
		SkillRef:   skillRef,
		TaskScope:  "scope-default",
		LoadedAt:   loadedAt,
		Tokens:     tokens,
	}); err != nil {
		t.Fatalf("NoteLoadInfo(%s): %v", skillRef, err)
	}
}

func TestSkillCompactor_NoOpWhenWithinBudget(t *testing.T) {
	tr := newTestTracker(t)
	now := time.Now()
	note(t, tr, "st-1", "alpha", "ld-1", 500, now)
	c := NewSkillCompactor(tr, 1000, nil)
	got, err := c.EvictForBudget(context.Background(), "st-1", 500)
	if err != nil {
		t.Fatalf("EvictForBudget: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil eviction list when within budget, got %v", got)
	}
	if !tr.Loaded("st-1", "alpha") {
		t.Error("alpha should still be loaded")
	}
}

func TestSkillCompactor_DropsLRUUntilBudget(t *testing.T) {
	tr := newTestTracker(t)
	t0 := time.Now()
	// Loaded oldest → newest; LRU drops alpha first.
	note(t, tr, "st-1", "alpha", "ld-a", 600, t0)
	note(t, tr, "st-1", "beta", "ld-b", 400, t0.Add(time.Second))
	note(t, tr, "st-1", "gamma", "ld-g", 300, t0.Add(2*time.Second))

	c := NewSkillCompactor(tr, 1000, nil) // budget 1000, currently 1300
	got, err := c.EvictForBudget(context.Background(), "st-1", 1300)
	if err != nil {
		t.Fatalf("EvictForBudget: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 evict (drop alpha frees 600 → 700 ≤ 1000), got %d: %v", len(got), got)
	}
	if got[0].SkillRef != "alpha" {
		t.Errorf("evicted = %s, want alpha (LRU)", got[0].SkillRef)
	}
	if got[0].TokensFreed != 600 {
		t.Errorf("TokensFreed = %d, want 600", got[0].TokensFreed)
	}
	if tr.Loaded("st-1", "alpha") {
		t.Error("alpha should be evicted from tracker")
	}
	if !tr.Loaded("st-1", "beta") || !tr.Loaded("st-1", "gamma") {
		t.Error("beta + gamma should remain loaded")
	}
}

func TestSkillCompactor_DropsMultipleWhenSingleNotEnough(t *testing.T) {
	tr := newTestTracker(t)
	t0 := time.Now()
	note(t, tr, "st-1", "alpha", "ld-a", 200, t0)
	note(t, tr, "st-1", "beta", "ld-b", 200, t0.Add(time.Second))
	note(t, tr, "st-1", "gamma", "ld-g", 200, t0.Add(2*time.Second))

	c := NewSkillCompactor(tr, 100, nil) // budget 100, currently 600 → drop 500 worth
	got, err := c.EvictForBudget(context.Background(), "st-1", 600)
	if err != nil {
		t.Fatalf("EvictForBudget: %v", err)
	}
	if len(got) < 3 {
		t.Errorf("expected ≥3 evictions to free 500 (3×200=600>500), got %d: %v", len(got), got)
	}
}

func TestSkillCompactor_NilTrackerIsNoop(t *testing.T) {
	c := NewSkillCompactor(nil, 100, nil)
	got, err := c.EvictForBudget(context.Background(), "st-1", 9999)
	if err != nil || got != nil {
		t.Errorf("nil tracker: got=%v err=%v, want nil/nil", got, err)
	}
}

func TestSkillCompactor_ZeroBudgetIsNoop(t *testing.T) {
	tr := newTestTracker(t)
	note(t, tr, "st-1", "alpha", "ld-a", 100, time.Now())
	c := NewSkillCompactor(tr, 0, nil)
	got, err := c.EvictForBudget(context.Background(), "st-1", 9999)
	if err != nil {
		t.Fatalf("EvictForBudget: %v", err)
	}
	if got != nil {
		t.Errorf("zero budget: expected nil, got %v", got)
	}
	if !tr.Loaded("st-1", "alpha") {
		t.Error("alpha should remain loaded under zero-budget no-op")
	}
}

func TestSkillCompactor_EmptyStanceIsNoop(t *testing.T) {
	tr := newTestTracker(t)
	c := NewSkillCompactor(tr, 100, nil)
	got, err := c.EvictForBudget(context.Background(), "st-empty", 9999)
	if err != nil {
		t.Errorf("expected nil err for unknown stance, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil list for unknown stance, got %v", got)
	}
}
