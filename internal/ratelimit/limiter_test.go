package ratelimit

import (
	"testing"
	"time"
)

func TestAllowBurst(t *testing.T) {
	l := NewLimiter(Config{
		RequestsPerMinute: 60,
		BurstSize:         3,
	})

	// Should allow burst of 3
	for i := 0; i < 3; i++ {
		if !l.Allow() {
			t.Errorf("request %d should be allowed", i)
		}
	}

	// 4th should be throttled
	if l.Allow() {
		t.Error("should throttle after burst")
	}
}

func TestAllowRefill(t *testing.T) {
	l := NewLimiter(Config{
		RequestsPerMinute: 6000, // 100/sec for fast test
		BurstSize:         1,
	})

	l.Allow() // drain
	if l.Allow() {
		t.Error("should be empty")
	}

	// Manually advance the refill time
	l.mu.Lock()
	l.lastRefill = time.Now().Add(-time.Second)
	l.mu.Unlock()

	if !l.Allow() {
		t.Error("should have refilled")
	}
}

func TestRetryAfter(t *testing.T) {
	l := NewLimiter(Config{
		RequestsPerMinute: 60,
		BurstSize:         10,
	})

	l.SetRetryAfterDuration(time.Hour)
	if l.Allow() {
		t.Error("should not allow during retry-after")
	}

	wait := l.Wait()
	if wait < time.Minute {
		t.Errorf("wait should be large, got %v", wait)
	}
}

func TestWaitReturnsZero(t *testing.T) {
	l := NewLimiter(Config{
		RequestsPerMinute: 60,
		BurstSize:         5,
	})

	wait := l.Wait()
	if wait != 0 {
		t.Errorf("should be 0 with tokens available, got %v", wait)
	}
}

func TestAllowN(t *testing.T) {
	l := NewLimiter(Config{
		RequestsPerMinute: 60,
		BurstSize:         5,
	})

	if !l.AllowN(3) {
		t.Error("should allow 3 of 5")
	}
	if l.AllowN(3) {
		t.Error("should not allow 3 more (only 2 left)")
	}
}

func TestMinInterval(t *testing.T) {
	l := NewLimiter(Config{
		RequestsPerMinute: 6000,
		BurstSize:         100,
		MinInterval:       100 * time.Millisecond,
	})

	l.Allow()
	if l.Allow() {
		t.Error("should enforce min interval")
	}
}

func TestForProvider(t *testing.T) {
	l := ForProvider("anthropic")
	if l.config.RequestsPerMinute != 60 {
		t.Errorf("expected 60 rpm, got %d", l.config.RequestsPerMinute)
	}

	l = ForProvider("unknown")
	if l.config.RequestsPerMinute != 60 {
		t.Error("unknown should fall back to anthropic defaults")
	}
}

func TestStats(t *testing.T) {
	l := NewLimiter(Config{
		RequestsPerMinute: 60,
		BurstSize:         2,
	})

	l.Allow()
	l.Allow()
	l.Allow() // throttled

	stats := l.GetStats()
	if stats.Allowed != 2 {
		t.Errorf("expected 2 allowed, got %d", stats.Allowed)
	}
	if stats.Throttled != 1 {
		t.Errorf("expected 1 throttled, got %d", stats.Throttled)
	}
}

func TestReset(t *testing.T) {
	l := NewLimiter(Config{
		RequestsPerMinute: 60,
		BurstSize:         2,
	})

	l.Allow()
	l.Allow()
	l.Reset()

	if !l.Allow() {
		t.Error("should allow after reset")
	}
	stats := l.GetStats()
	if stats.Allowed != 1 {
		t.Errorf("stats should reset, got %d allowed", stats.Allowed)
	}
}

func TestMultiLimiter(t *testing.T) {
	ml := NewMultiLimiter()
	ml.Add("requests", NewLimiter(Config{RequestsPerMinute: 60, BurstSize: 5}))
	ml.Add("tokens", NewLimiter(Config{RequestsPerMinute: 120, BurstSize: 10}))

	if !ml.Allow() {
		t.Error("should allow when all limiters have capacity")
	}

	s := ml.Summary()
	if s == "" {
		t.Error("summary should not be empty")
	}
}

func TestMultiLimiterWait(t *testing.T) {
	ml := NewMultiLimiter()
	l1 := NewLimiter(Config{RequestsPerMinute: 60, BurstSize: 1})
	l2 := NewLimiter(Config{RequestsPerMinute: 60, BurstSize: 5})
	ml.Add("tight", l1)
	ml.Add("loose", l2)

	ml.Allow() // drains l1

	wait := ml.Wait()
	// l1 should need to wait, l2 should be fine
	// wait should be > 0 from l1
	_ = wait // just verify no panic
}

func TestEmptyMultiLimiter(t *testing.T) {
	ml := NewMultiLimiter()
	if !ml.Allow() {
		t.Error("empty multi-limiter should allow")
	}
	s := ml.Summary()
	if s != "no limiters configured" {
		t.Errorf("unexpected: %s", s)
	}
}
