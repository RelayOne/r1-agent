package subscriptions

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestAcquireLeastLoaded(t *testing.T) {
	m := NewManager([]Pool{
		{ID: "c1", Provider: ProviderClaude, Utilization: 60},
		{ID: "c2", Provider: ProviderClaude, Utilization: 20},
		{ID: "c3", Provider: ProviderClaude, Utilization: 80},
	})

	p, err := m.Acquire(ProviderClaude, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "c2" {
		t.Errorf("acquired %q, want c2 (lowest utilization)", p.ID)
	}
}

func TestAcquireMarksBusy(t *testing.T) {
	m := NewManager([]Pool{
		{ID: "c1", Provider: ProviderClaude},
	})

	_, err := m.Acquire(ProviderClaude, "task-1")
	if err != nil {
		t.Fatal(err)
	}

	// Second acquire should fail -- pool is busy
	_, err = m.Acquire(ProviderClaude, "task-2")
	if err == nil {
		t.Error("expected error: pool should be busy")
	}
}

func TestAcquireRejectsExhausted(t *testing.T) {
	m := NewManager([]Pool{
		{ID: "c1", Provider: ProviderClaude, Utilization: 96},
	})

	_, err := m.Acquire(ProviderClaude, "task-1")
	if err == nil {
		t.Error("expected error: pool utilization > 95%")
	}
}

func TestReleaseRestoresAvailability(t *testing.T) {
	m := NewManager([]Pool{
		{ID: "c1", Provider: ProviderClaude},
	})

	p, _ := m.Acquire(ProviderClaude, "task-1")
	m.Release(p.ID, false)

	// Should be available again
	p2, err := m.Acquire(ProviderClaude, "task-2")
	if err != nil {
		t.Fatalf("pool should be available after release: %v", err)
	}
	if p2.CurrentTask != "task-2" {
		t.Errorf("currentTask=%q, want task-2", p2.CurrentTask)
	}
}

func TestCircuitBreakerAfterThreeFailures(t *testing.T) {
	m := NewManager([]Pool{
		{ID: "c1", Provider: ProviderClaude},
	})

	for i := 0; i < 3; i++ {
		p, err := m.Acquire(ProviderClaude, "task")
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		m.Release(p.ID, true) // rate limited
	}

	// Circuit should be open now
	_, err := m.Acquire(ProviderClaude, "task-4")
	if err == nil {
		t.Error("expected error: circuit breaker should be open")
	}
}

func TestCircuitBreakerResetsAfterTimeout(t *testing.T) {
	m := NewManager([]Pool{
		{ID: "c1", Provider: ProviderClaude},
	})

	// Trip the breaker
	for i := 0; i < 3; i++ {
		p, _ := m.Acquire(ProviderClaude, "task")
		m.Release(p.ID, true)
	}

	// Manually reset the timeout to the past
	m.mu.Lock()
	m.pools[0].CircuitBreakerUntil = time.Now().Add(-1 * time.Second)
	m.mu.Unlock()

	p, err := m.Acquire(ProviderClaude, "task-after-reset")
	if err != nil {
		t.Fatalf("should acquire after circuit breaker timeout: %v", err)
	}
	if p.ID != "c1" {
		t.Errorf("ID=%q", p.ID)
	}
}

func TestUpdateUtilizationThreadSafe(t *testing.T) {
	m := NewManager([]Pool{
		{ID: "c1", Provider: ProviderClaude},
		{ID: "c2", Provider: ProviderClaude},
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			m.UpdateUtilization("c1", float64(n), float64(n)/2, time.Now(), time.Now())
		}(i)
	}
	wg.Wait()

	// Should not panic or corrupt state
	snap := m.Snapshot()
	if len(snap) != 2 {
		t.Errorf("pools=%d", len(snap))
	}
}

func TestAcquireReleaseConcurrent(t *testing.T) {
	m := NewManager([]Pool{
		{ID: "c1", Provider: ProviderClaude},
		{ID: "c2", Provider: ProviderClaude},
		{ID: "c3", Provider: ProviderClaude},
		{ID: "c4", Provider: ProviderClaude},
	})

	var wg sync.WaitGroup
	acquired := make(chan string, 100)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			p, err := m.Acquire(ProviderClaude, fmt.Sprintf("task-%d", n))
			if err != nil {
				return // expected when all pools are busy
			}
			acquired <- p.ID
			time.Sleep(time.Millisecond)
			m.Release(p.ID, false)
		}(i)
	}
	wg.Wait()
	close(acquired)

	// Verify no pool was double-acquired by checking all are idle now
	snap := m.Snapshot()
	for _, p := range snap {
		if p.Status == StatusBusy {
			t.Errorf("pool %s still busy after all goroutines finished", p.ID)
		}
		if p.CurrentTask != "" {
			t.Errorf("pool %s has leftover task %q", p.ID, p.CurrentTask)
		}
	}
}

func TestProviderIsolation(t *testing.T) {
	m := NewManager([]Pool{
		{ID: "claude-1", Provider: ProviderClaude},
		{ID: "codex-1", Provider: ProviderCodex},
	})

	_, err := m.Acquire(ProviderCodex, "task-1")
	if err != nil {
		t.Fatal(err)
	}

	// Claude pool should still be available
	p, err := m.Acquire(ProviderClaude, "task-2")
	if err != nil {
		t.Fatalf("claude should be available: %v", err)
	}
	if p.ID != "claude-1" {
		t.Errorf("ID=%q", p.ID)
	}
}

func TestSnapshotReturnsCopy(t *testing.T) {
	m := NewManager([]Pool{{ID: "c1", Provider: ProviderClaude}})
	snap := m.Snapshot()
	snap[0].ID = "mutated"

	// Internal state should not be affected
	snap2 := m.Snapshot()
	if snap2[0].ID != "c1" {
		t.Error("Snapshot should return a copy, not a reference")
	}
}
