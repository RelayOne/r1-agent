package metrics

import (
	"sync"
	"testing"
	"time"
)

func TestCounter(t *testing.T) {
	c := &Counter{}
	if c.Value() != 0 {
		t.Errorf("initial value should be 0, got %d", c.Value())
	}
	c.Inc()
	c.Inc()
	c.Add(3)
	if c.Value() != 5 {
		t.Errorf("expected 5, got %d", c.Value())
	}
}

func TestCounter_Concurrent(t *testing.T) {
	c := &Counter{}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Inc()
		}()
	}
	wg.Wait()
	if c.Value() != 100 {
		t.Errorf("expected 100, got %d", c.Value())
	}
}

func TestGauge(t *testing.T) {
	g := &Gauge{}
	g.Set(10)
	if g.Value() != 10 {
		t.Errorf("expected 10, got %d", g.Value())
	}
	g.Inc()
	g.Inc()
	g.Dec()
	if g.Value() != 11 {
		t.Errorf("expected 11, got %d", g.Value())
	}
}

func TestGauge_Concurrent(t *testing.T) {
	g := &Gauge{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); g.Inc() }()
		go func() { defer wg.Done(); g.Dec() }()
	}
	wg.Wait()
	if g.Value() != 0 {
		t.Errorf("expected 0 after equal inc/dec, got %d", g.Value())
	}
}

func TestTimer(t *testing.T) {
	tm := &Timer{}
	tm.Record(100 * time.Millisecond)
	tm.Record(200 * time.Millisecond)
	tm.Record(300 * time.Millisecond)

	snap := tm.Snapshot()
	if snap.Count != 3 {
		t.Errorf("count: expected 3, got %d", snap.Count)
	}
	if snap.Min != 100*time.Millisecond {
		t.Errorf("min: expected 100ms, got %v", snap.Min)
	}
	if snap.Max != 300*time.Millisecond {
		t.Errorf("max: expected 300ms, got %v", snap.Max)
	}
	if snap.Avg != 200*time.Millisecond {
		t.Errorf("avg: expected 200ms, got %v", snap.Avg)
	}
}

func TestTimer_Since(t *testing.T) {
	tm := &Timer{}
	start := time.Now()
	time.Sleep(time.Millisecond)
	tm.Since(start)
	snap := tm.Snapshot()
	if snap.Count != 1 {
		t.Errorf("expected 1 observation")
	}
	if snap.Min < time.Millisecond {
		t.Errorf("expected >= 1ms, got %v", snap.Min)
	}
}

func TestTimer_Concurrent(t *testing.T) {
	tm := &Timer{}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(d time.Duration) {
			defer wg.Done()
			tm.Record(d)
		}(time.Duration(i) * time.Millisecond)
	}
	wg.Wait()
	snap := tm.Snapshot()
	if snap.Count != 100 {
		t.Errorf("expected 100 observations, got %d", snap.Count)
	}
}

func TestCostAccumulator(t *testing.T) {
	c := &CostAccumulator{}
	c.Add(0.05)
	c.Add(0.10)
	total, count := c.Total()
	if count != 2 {
		t.Errorf("expected 2 entries, got %d", count)
	}
	if total < 0.14 || total > 0.16 {
		t.Errorf("expected ~0.15, got %f", total)
	}
}

func TestCostAccumulator_Concurrent(t *testing.T) {
	c := &CostAccumulator{}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Add(0.01)
		}()
	}
	wg.Wait()
	total, count := c.Total()
	if count != 100 {
		t.Errorf("expected 100, got %d", count)
	}
	if total < 0.99 || total > 1.01 {
		t.Errorf("expected ~1.0, got %f", total)
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	r.Counter("tasks.total").Add(5)
	r.Gauge("workers.active").Set(3)
	r.Timer("task.duration").Record(100 * time.Millisecond)
	r.Cost("api.spend").Add(0.05)

	snap := r.Snapshot()
	if snap.Counters["tasks.total"] != 5 {
		t.Errorf("counter: expected 5, got %d", snap.Counters["tasks.total"])
	}
	if snap.Gauges["workers.active"] != 3 {
		t.Errorf("gauge: expected 3, got %d", snap.Gauges["workers.active"])
	}
	if snap.Timers["task.duration"].Count != 1 {
		t.Errorf("timer count: expected 1, got %d", snap.Timers["task.duration"].Count)
	}
	if snap.Costs["api.spend"].Count != 1 {
		t.Errorf("cost count: expected 1, got %d", snap.Costs["api.spend"].Count)
	}
}

func TestRegistry_GetOrCreate(t *testing.T) {
	r := NewRegistry()
	c1 := r.Counter("test")
	c2 := r.Counter("test")
	if c1 != c2 {
		t.Error("same name should return same counter")
	}
	g1 := r.Gauge("test")
	g2 := r.Gauge("test")
	if g1 != g2 {
		t.Error("same name should return same gauge")
	}
}

func TestRegistry_Concurrent(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Counter("c").Inc()
			r.Gauge("g").Inc()
			r.Timer("t").Record(time.Millisecond)
			r.Cost("$").Add(0.01)
		}()
	}
	wg.Wait()
	snap := r.Snapshot()
	if snap.Counters["c"] != 100 {
		t.Errorf("expected 100, got %d", snap.Counters["c"])
	}
}

func TestDefaultRegistry(t *testing.T) {
	// DefaultRegistry should be initialized
	if DefaultRegistry == nil {
		t.Fatal("DefaultRegistry should not be nil")
	}
	DefaultRegistry.Counter("test.default").Inc()
	if DefaultRegistry.Counter("test.default").Value() != 1 {
		t.Error("default registry should track values")
	}
}
