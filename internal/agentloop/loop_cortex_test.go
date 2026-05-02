package agentloop

import (
	"testing"
)

// fakeCortexHook is a minimal CortexHook used to exercise the
// composition logic in Config.defaults() without depending on
// internal/cortex (which would create an import cycle anyway, since
// cortex imports agentloop).
type fakeCortexHook struct {
	midReturn string
	endReturn string
}

func (f *fakeCortexHook) MidturnNote(msgs []Message, turn int) string {
	return f.midReturn
}
func (f *fakeCortexHook) PreEndTurnGate(msgs []Message) string {
	return f.endReturn
}

func TestCortexMidturnCompositionBoth(t *testing.T) {
	cfg := Config{
		Cortex:         &fakeCortexHook{midReturn: "X"},
		MidturnCheckFn: func(msgs []Message, turn int) string { return "Y" },
	}
	cfg.defaults()
	got := cfg.MidturnCheckFn(nil, 0)
	if got != "X\n\nY" {
		t.Fatalf("got %q, want %q", got, "X\n\nY")
	}
}

func TestCortexMidturnCompositionCortexOnly(t *testing.T) {
	cfg := Config{Cortex: &fakeCortexHook{midReturn: "X"}}
	cfg.defaults()
	got := cfg.MidturnCheckFn(nil, 0)
	if got != "X" {
		t.Fatalf("got %q, want %q", got, "X")
	}
}

func TestCortexMidturnCompositionOperatorOnly(t *testing.T) {
	cfg := Config{
		Cortex:         &fakeCortexHook{midReturn: ""},
		MidturnCheckFn: func(msgs []Message, turn int) string { return "Y" },
	}
	cfg.defaults()
	got := cfg.MidturnCheckFn(nil, 0)
	if got != "Y" {
		t.Fatalf("got %q, want %q", got, "Y")
	}
}

func TestCortexMidturnCompositionEmpty(t *testing.T) {
	cfg := Config{Cortex: &fakeCortexHook{}}
	cfg.defaults()
	got := cfg.MidturnCheckFn(nil, 0)
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestCortexPreEndTurnShortCircuits(t *testing.T) {
	operatorCalled := false
	cfg := Config{
		Cortex: &fakeCortexHook{endReturn: "BLOCK"},
		PreEndTurnCheckFn: func(msgs []Message) string {
			operatorCalled = true
			return "operator"
		},
	}
	cfg.defaults()
	got := cfg.PreEndTurnCheckFn(nil)
	if got != "BLOCK" {
		t.Fatalf("got %q, want BLOCK", got)
	}
	if operatorCalled {
		t.Fatalf("operator called despite cortex short-circuit")
	}
}

func TestCortexPreEndTurnFallsThrough(t *testing.T) {
	cfg := Config{
		Cortex:            &fakeCortexHook{endReturn: ""},
		PreEndTurnCheckFn: func(msgs []Message) string { return "operator" },
	}
	cfg.defaults()
	got := cfg.PreEndTurnCheckFn(nil)
	if got != "operator" {
		t.Fatalf("got %q, want operator", got)
	}
}

func TestNoCortexNoChange(t *testing.T) {
	cfg := Config{
		MidturnCheckFn: func(msgs []Message, turn int) string { return "Y" },
	}
	cfg.defaults()
	got := cfg.MidturnCheckFn(nil, 0)
	if got != "Y" {
		t.Fatalf("got %q, want Y", got)
	}
}

// TestDefaultsIdempotent verifies double-application of defaults()
// does not double-wrap the hook composition. Without the
// defaultsApplied guard, calling defaults() twice would wrap the
// closure twice and yield "X\n\nX\n\nY".
func TestDefaultsIdempotent(t *testing.T) {
	cfg := Config{
		Cortex:         &fakeCortexHook{midReturn: "X"},
		MidturnCheckFn: func(msgs []Message, turn int) string { return "Y" },
	}
	cfg.defaults()
	cfg.defaults() // second call must be a no-op
	got := cfg.MidturnCheckFn(nil, 0)
	if got != "X\n\nY" {
		t.Fatalf("got %q, want %q (defaults() not idempotent)", got, "X\n\nY")
	}
}
