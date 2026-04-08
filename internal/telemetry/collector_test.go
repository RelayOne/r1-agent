package telemetry

import (
	"strings"
	"testing"
	"time"
)

func TestRecord(t *testing.T) {
	c := New()
	c.Record(Event{Name: "tool.grep", Category: "tool", Duration: 100 * time.Millisecond, Success: true})

	if c.EventCount() != 1 {
		t.Errorf("expected 1 event, got %d", c.EventCount())
	}
}

func TestRecordDuration(t *testing.T) {
	c := New()
	c.RecordDuration("build", "verify", 2*time.Second, true)
	c.RecordDuration("test", "verify", 5*time.Second, false)

	s := c.Summary()
	if s.TotalEvents != 2 {
		t.Errorf("expected 2, got %d", s.TotalEvents)
	}
	if s.Successes != 1 {
		t.Errorf("expected 1 success, got %d", s.Successes)
	}
}

func TestTimer(t *testing.T) {
	c := New()
	timer := c.Timer("operation", "test")
	time.Sleep(5 * time.Millisecond)
	d := timer.Stop(true)

	if d < 5*time.Millisecond {
		t.Errorf("duration too short: %v", d)
	}
	if c.EventCount() != 1 {
		t.Error("timer should record event on stop")
	}
}

func TestSummary(t *testing.T) {
	c := New()
	c.Record(Event{Name: "a", Category: "cat1", Duration: 100 * time.Millisecond, Success: true, Tokens: 500, Cost: 0.01})
	c.Record(Event{Name: "b", Category: "cat1", Duration: 200 * time.Millisecond, Success: false, Tokens: 300, Cost: 0.005})
	c.Record(Event{Name: "c", Category: "cat2", Duration: 50 * time.Millisecond, Success: true})

	s := c.Summary()

	if s.TotalEvents != 3 {
		t.Errorf("expected 3 events, got %d", s.TotalEvents)
	}
	if s.SuccessRate < 0.6 || s.SuccessRate > 0.7 {
		t.Errorf("expected ~0.66 success rate, got %f", s.SuccessRate)
	}
	if s.TotalTokens != 800 {
		t.Errorf("expected 800 tokens, got %d", s.TotalTokens)
	}

	cat1 := s.ByCategory["cat1"]
	if cat1.Count != 2 {
		t.Errorf("expected 2 cat1 events, got %d", cat1.Count)
	}
}

func TestPercentiles(t *testing.T) {
	c := New()
	for i := 1; i <= 100; i++ {
		c.Record(Event{
			Category: "api",
			Duration: time.Duration(i) * time.Millisecond,
			Success:  true,
		})
	}

	p := c.Percentiles("api")
	if p == nil {
		t.Fatal("percentiles should not be nil")
	}

	if p["p50"] < 49*time.Millisecond || p["p50"] > 51*time.Millisecond {
		t.Errorf("p50 should be ~50ms, got %v", p["p50"])
	}
	if p["p90"] < 89*time.Millisecond || p["p90"] > 91*time.Millisecond {
		t.Errorf("p90 should be ~90ms, got %v", p["p90"])
	}
}

func TestPercentilesEmpty(t *testing.T) {
	c := New()
	if c.Percentiles("nonexistent") != nil {
		t.Error("empty category should return nil")
	}
}

func TestEvents(t *testing.T) {
	c := New()
	c.Record(Event{Category: "a"})
	c.Record(Event{Category: "b"})
	c.Record(Event{Category: "a"})

	all := c.Events("")
	if len(all) != 3 {
		t.Errorf("expected 3, got %d", len(all))
	}

	filtered := c.Events("a")
	if len(filtered) != 2 {
		t.Errorf("expected 2 'a' events, got %d", len(filtered))
	}
}

func TestClear(t *testing.T) {
	c := New()
	c.Record(Event{Category: "a"})
	c.Clear()
	if c.EventCount() != 0 {
		t.Error("should be empty after clear")
	}
}

func TestFormat(t *testing.T) {
	c := New()
	c.Record(Event{Name: "a", Category: "tool", Duration: 100 * time.Millisecond, Success: true, Tokens: 500})

	out := c.Format()
	if !strings.Contains(out, "1 events") {
		t.Error("should show event count")
	}
	if !strings.Contains(out, "100%") {
		t.Error("should show success rate")
	}
}

func TestSummaryEmpty(t *testing.T) {
	c := New()
	s := c.Summary()
	if s.TotalEvents != 0 {
		t.Error("empty should have 0 events")
	}
	if s.SuccessRate != 0 {
		t.Error("empty should have 0 success rate")
	}
}
