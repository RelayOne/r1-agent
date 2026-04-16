package agentloop

import (
	"errors"
	"testing"
	"time"
)

func TestStepCounter_DefaultLimits(t *testing.T) {
	s := NewStepCounter()
	// Light class default is 25; 25 increments should succeed,
	// 26th should fail.
	for i := 1; i <= 25; i++ {
		n, err := s.Increment("n1", NodeClassLight)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if n != i {
			t.Errorf("step %d: count=%d", i, n)
		}
	}
	_, err := s.Increment("n1", NodeClassLight)
	if !errors.Is(err, ErrStepLimitExceeded) {
		t.Fatalf("26th step should exceed, got %v", err)
	}
}

func TestStepCounter_SetLimit(t *testing.T) {
	s := NewStepCounter()
	s.SetLimit(NodeClassLight, 5)
	for i := 1; i <= 5; i++ {
		if _, err := s.Increment("n1", NodeClassLight); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	if _, err := s.Increment("n1", NodeClassLight); !errors.Is(err, ErrStepLimitExceeded) {
		t.Fatalf("want exceed, got %v", err)
	}
}

func TestStepCounter_PerNodeIndependent(t *testing.T) {
	s := NewStepCounter()
	s.SetLimit(NodeClassLight, 2)
	_, _ = s.Increment("a", NodeClassLight)
	_, _ = s.Increment("a", NodeClassLight)
	if _, err := s.Increment("a", NodeClassLight); err == nil {
		t.Error("a should be at limit")
	}
	// Different node should be independent.
	if _, err := s.Increment("b", NodeClassLight); err != nil {
		t.Errorf("b should be fresh, got %v", err)
	}
}

func TestStepCounter_Reset(t *testing.T) {
	s := NewStepCounter()
	s.SetLimit(NodeClassLight, 2)
	_, _ = s.Increment("a", NodeClassLight)
	_, _ = s.Increment("a", NodeClassLight)
	s.Reset("a")
	if _, err := s.Increment("a", NodeClassLight); err != nil {
		t.Errorf("after Reset, Increment should succeed, got %v", err)
	}
}

func TestStepCounter_UnknownClassFallsToXL(t *testing.T) {
	// Unknown class shouldn't hard-fail; falls through to the
	// most permissive ceiling.
	s := NewStepCounter()
	for i := 1; i <= 100; i++ {
		if _, err := s.Increment("n", "made-up-class"); err != nil {
			t.Fatalf("step %d should not exceed (XL fallback): %v", i, err)
		}
	}
}

func TestTokenRateDetector_FiresOnSpike(t *testing.T) {
	d := NewTokenRateDetector()
	// Seed a small baseline: 3 × 100 tokens.
	for i := 0; i < 3; i++ {
		if d.Observe(100) {
			t.Errorf("baseline step %d fired alert prematurely", i)
		}
	}
	// Spike to 500 (5× baseline) — should fire.
	if !d.Observe(500) {
		t.Error("5x spike should trigger alert")
	}
	if !d.HasAlert() {
		t.Error("HasAlert should persist after fire")
	}
}

func TestTokenRateDetector_QuietIsNoAlert(t *testing.T) {
	d := NewTokenRateDetector()
	for i := 0; i < 10; i++ {
		if d.Observe(100 + i) { // mild variation
			t.Errorf("step %d fired false positive", i)
		}
	}
	if d.HasAlert() {
		t.Error("no alert on stable workload")
	}
}

func TestTokenRateDetector_Reset(t *testing.T) {
	d := NewTokenRateDetector()
	for i := 0; i < 3; i++ {
		_ = d.Observe(100)
	}
	_ = d.Observe(1000)
	if !d.HasAlert() {
		t.Fatal("expected alert before reset")
	}
	d.Reset()
	if d.HasAlert() {
		t.Error("Reset should clear alert")
	}
}

func TestTokenRateDetector_OnlyFiresOnce(t *testing.T) {
	d := NewTokenRateDetector()
	for i := 0; i < 3; i++ {
		_ = d.Observe(100)
	}
	first := d.Observe(1000)
	second := d.Observe(1100)
	if !first {
		t.Error("first breach should fire")
	}
	if second {
		t.Error("second breach should not re-fire until Reset")
	}
}

func TestCircularOutputDetector_DetectsRepetition(t *testing.T) {
	d := NewCircularOutputDetector()
	for i := 0; i < 2; i++ {
		if d.Observe("same output") {
			t.Errorf("iteration %d shouldn't fire yet", i)
		}
	}
	if !d.Observe("same output") {
		t.Error("3rd identical output should fire")
	}
}

func TestCircularOutputDetector_DifferentOutputsDontFire(t *testing.T) {
	d := NewCircularOutputDetector()
	for i := 0; i < 6; i++ {
		out := string(rune('a' + i))
		if d.Observe(out) {
			t.Errorf("unique output %d fired false positive", i)
		}
	}
}

func TestReasoningThrashDetector(t *testing.T) {
	d := NewReasoningThrashDetector()
	// Two consecutive text-only turns shouldn't fire.
	if d.Observe(false) {
		t.Error("first text-only shouldn't fire")
	}
	if d.Observe(false) {
		t.Error("second text-only shouldn't fire")
	}
	// Third fires.
	if !d.Observe(false) {
		t.Error("third consecutive text-only should fire")
	}
}

func TestReasoningThrashDetector_ToolCallResetsStreak(t *testing.T) {
	d := NewReasoningThrashDetector()
	_ = d.Observe(false)
	_ = d.Observe(false)
	if d.Observe(true) {
		t.Error("tool call shouldn't fire")
	}
	// After reset, need 3 fresh text-only turns to fire.
	if d.Observe(false) {
		t.Error("post-reset first text-only shouldn't fire")
	}
}

func TestHeartbeatMonitor_Stale(t *testing.T) {
	h := NewHeartbeatMonitor()
	h.stalenessWindow = 50 * time.Millisecond // tight window for test
	h.Pulse("a")
	if h.Stale("a") {
		t.Error("fresh pulse shouldn't be stale")
	}
	time.Sleep(80 * time.Millisecond)
	if !h.Stale("a") {
		t.Error("80ms after pulse should be stale (window=50ms)")
	}
}

func TestHeartbeatMonitor_UnknownNotStale(t *testing.T) {
	h := NewHeartbeatMonitor()
	if h.Stale("never-pulsed") {
		t.Error("unknown task should not be stale (not started ≠ stale)")
	}
}
