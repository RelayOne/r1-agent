package subscriptions

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestInitiateDrain(t *testing.T) {
	dm := NewDrainManager(30*time.Second, nil)
	dm.InitiateDrain("pool-1", "scale down")

	if !dm.IsDraining("pool-1") {
		t.Error("pool-1 should be draining")
	}
	if dm.IsDraining("pool-2") {
		t.Error("pool-2 should not be draining")
	}
	if dm.IsComplete("pool-1") {
		t.Error("pool-1 should not be complete yet")
	}
}

func TestCheckDrainsGracePeriod(t *testing.T) {
	dm := NewDrainManager(50*time.Millisecond, nil)
	dm.InitiateDrain("pool-1", "test")

	// Not complete yet
	completed := dm.CheckDrains(time.Now())
	if len(completed) != 0 {
		t.Error("should not be complete immediately")
	}

	// After grace period
	time.Sleep(60 * time.Millisecond)
	completed = dm.CheckDrains(time.Now())
	if len(completed) != 1 || completed[0] != "pool-1" {
		t.Errorf("expected pool-1 completed, got %v", completed)
	}
	if !dm.IsComplete("pool-1") {
		t.Error("pool-1 should be complete")
	}
}

func TestForceDrain(t *testing.T) {
	dm := NewDrainManager(time.Hour, nil) // very long grace period
	dm.InitiateDrain("pool-1", "test")
	dm.ForceDrain("pool-1")

	if !dm.IsComplete("pool-1") {
		t.Error("forced drain should be immediately complete")
	}
}

func TestDrainCallback(t *testing.T) {
	var mu sync.Mutex
	var called []string
	cb := func(poolID string, forced bool) {
		mu.Lock()
		defer mu.Unlock()
		label := poolID
		if forced {
			label += ":forced"
		}
		called = append(called, label)
	}

	dm := NewDrainManager(10*time.Millisecond, cb)
	dm.InitiateDrain("p1", "test")
	dm.ForceDrain("p2")

	time.Sleep(20 * time.Millisecond)
	dm.CheckDrains(time.Now())

	mu.Lock()
	defer mu.Unlock()
	if len(called) != 2 {
		t.Errorf("expected 2 callbacks, got %d: %v", len(called), called)
	}
}

func TestClearCompleted(t *testing.T) {
	dm := NewDrainManager(time.Hour, nil)
	dm.InitiateDrain("p1", "test")
	dm.ForceDrain("p1")
	dm.InitiateDrain("p2", "test")

	cleared := dm.ClearCompleted()
	if cleared != 1 {
		t.Errorf("expected 1 cleared, got %d", cleared)
	}
	if dm.IsDraining("p1") {
		t.Error("p1 should be removed after clear")
	}
	if !dm.IsDraining("p2") {
		t.Error("p2 should still be draining")
	}
}

func TestActiveDrains(t *testing.T) {
	dm := NewDrainManager(time.Hour, nil)
	if dm.ActiveDrains() != 0 {
		t.Error("expected 0 active drains initially")
	}

	dm.InitiateDrain("p1", "test")
	dm.InitiateDrain("p2", "test")
	if dm.ActiveDrains() != 2 {
		t.Errorf("expected 2, got %d", dm.ActiveDrains())
	}

	dm.ForceDrain("p1")
	if dm.ActiveDrains() != 1 {
		t.Errorf("expected 1 after force, got %d", dm.ActiveDrains())
	}
}

func TestDrainAllWithTimeout(t *testing.T) {
	dm := NewDrainManager(time.Hour, nil) // very long grace

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	dm.DrainAll(ctx, []string{"p1", "p2"}, "shutdown")

	// Context cancelled should force drain
	if !dm.IsComplete("p1") {
		t.Error("p1 should be force-completed after context cancel")
	}
	if !dm.IsComplete("p2") {
		t.Error("p2 should be force-completed after context cancel")
	}
}

func TestDrainAllGraceful(t *testing.T) {
	dm := NewDrainManager(20*time.Millisecond, nil)

	ctx := context.Background()
	dm.DrainAll(ctx, []string{"p1"}, "scale down")

	if !dm.IsComplete("p1") {
		t.Error("p1 should be complete after DrainAll")
	}
}
