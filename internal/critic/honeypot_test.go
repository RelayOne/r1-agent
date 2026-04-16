package critic

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestHoneypotPool_AddAndPick(t *testing.T) {
	p := NewHoneypotPool()
	p.Add(Honeypot{ID: "h1", Class: "factual", Prompt: "capital of France?", ExpectedAnswer: "Paris"})
	p.Add(Honeypot{ID: "h2", Class: "factual", Prompt: "3+4?", ExpectedAnswer: "7"})
	p.Add(Honeypot{ID: "h3", Class: "code", Prompt: "go fmt output for bad-formatted source", ExpectedAnswer: "cleaned"})

	if p.Len() != 3 {
		t.Errorf("Len=%d want 3", p.Len())
	}
	classes := p.Classes()
	if len(classes) != 2 {
		t.Errorf("Classes=%d want 2", len(classes))
	}
	h, ok := p.Pick("factual", 0)
	if !ok {
		t.Fatal("Pick factual returned !ok")
	}
	if h.Class != "factual" {
		t.Errorf("picked class=%q want factual", h.Class)
	}
	h2, ok := p.Pick("factual", 1)
	if !ok {
		t.Fatal("Pick factual idx 1 returned !ok")
	}
	if h2.ID == h.ID {
		t.Error("different seeds should pick different honeypots when pool has multiple")
	}
}

func TestHoneypotPool_PickEmptyClass(t *testing.T) {
	p := NewHoneypotPool()
	if _, ok := p.Pick("nonexistent", 0); ok {
		t.Error("empty class should return !ok")
	}
}

func TestHoneypotPool_ReplaceKeepsConsistent(t *testing.T) {
	p := NewHoneypotPool()
	p.Add(Honeypot{ID: "h1", Class: "factual", Prompt: "old"})
	p.Add(Honeypot{ID: "h1", Class: "factual", Prompt: "new"})
	if p.Len() != 1 {
		t.Errorf("Len=%d want 1 (replace not append)", p.Len())
	}
	h, _ := p.Get("h1")
	if h.Prompt != "new" {
		t.Errorf("prompt=%q want new", h.Prompt)
	}
}

func TestHoneypotPool_ClassChangeOnReplace(t *testing.T) {
	p := NewHoneypotPool()
	p.Add(Honeypot{ID: "h1", Class: "factual"})
	p.Add(Honeypot{ID: "h1", Class: "reasoning"}) // class changed
	// Old class should no longer index it.
	if _, ok := p.Pick("factual", 0); ok {
		t.Error("factual should be empty after class change")
	}
	if _, ok := p.Pick("reasoning", 0); !ok {
		t.Error("reasoning should have h1")
	}
}

func TestEvaluate_PassesOnMatch(t *testing.T) {
	h := Honeypot{
		ID: "h1", Prompt: "?", ExpectedAnswer: "Paris",
	}
	disp := func(_ context.Context, _ Honeypot) (string, error) {
		return " Paris ", nil // whitespace + same case
	}
	eval, err := Evaluate(context.Background(), h, disp)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !eval.Passed {
		t.Errorf("expected passed, got score=%v", eval.Score)
	}
}

func TestEvaluate_FailsOnMismatch(t *testing.T) {
	h := Honeypot{ID: "h1", ExpectedAnswer: "Paris"}
	disp := func(_ context.Context, _ Honeypot) (string, error) {
		return "Lyon", nil
	}
	eval, _ := Evaluate(context.Background(), h, disp)
	if eval.Passed {
		t.Error("expected fail on Lyon vs Paris")
	}
}

func TestEvaluate_CustomChecker(t *testing.T) {
	// Checker that accepts any answer containing "paris".
	h := Honeypot{
		ID:      "h1",
		Checker: func(a string) float64 { if strings.Contains(strings.ToLower(a), "paris") { return 1.0 }; return 0 },
		Tolerance: 0.9,
	}
	disp := func(_ context.Context, _ Honeypot) (string, error) {
		return "The capital is Paris in France", nil
	}
	eval, _ := Evaluate(context.Background(), h, disp)
	if !eval.Passed {
		t.Errorf("expected passed via custom Checker, got score=%v", eval.Score)
	}
}

func TestEvaluate_DispatcherError(t *testing.T) {
	h := Honeypot{ID: "h1"}
	disp := func(_ context.Context, _ Honeypot) (string, error) {
		return "", errors.New("timeout")
	}
	_, err := Evaluate(context.Background(), h, disp)
	if err == nil {
		t.Error("expected dispatcher error to propagate")
	}
}

func TestPeriodicSnapshotter_FiresWithJitter(t *testing.T) {
	var fired int
	snap := func(_ context.Context) error {
		fired++
		return nil
	}
	s := NewPeriodicSnapshotter(10*time.Millisecond, 5*time.Millisecond, snap)
	// Deterministic RNG for test.
	var seq uint64
	s.SetRNG(func() uint64 {
		seq++
		return seq * 1000
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	s.Start(ctx)
	<-ctx.Done()
	s.Stop()
	if fired == 0 {
		t.Error("expected snapshots to fire within 100ms window at 10ms base interval")
	}
}

func TestPeriodicSnapshotter_ZeroBaseNoop(t *testing.T) {
	var fired int
	s := NewPeriodicSnapshotter(0, 0, func(_ context.Context) error {
		fired++
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	s.Start(ctx)
	<-ctx.Done()
	if fired != 0 {
		t.Errorf("base=0 should never fire, got %d", fired)
	}
}

func TestPeriodicSnapshotter_StopAndRestart(t *testing.T) {
	s := NewPeriodicSnapshotter(5*time.Millisecond, 0, func(_ context.Context) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	s.Stop()
	// Restart should work without panic (stop channel
	// recreated internally).
	s.Start(ctx)
	s.Stop()
}

func TestNormalizeForMatch_CaseAndWhitespace(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"  Paris  ", "paris"},
		{"PARIS\n", "paris"},
		{"par\t is", "par is"},
	}
	for _, c := range cases {
		if got := normalizeForMatch(c.in); got != c.want {
			t.Errorf("normalize(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
