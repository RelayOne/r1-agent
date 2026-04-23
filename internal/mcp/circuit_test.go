package mcp

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a monotonic clock whose value only advances when
// the test asks it to. Safe for concurrent reads / writes via
// its own lock so -race stays clean.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Unix(1_700_000_000, 0)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

// newTestCircuit wires a circuit to a fake clock and records
// transitions for assertions.
type recordedTransition struct {
	from CircuitState
	to   CircuitState
	info CircuitInfo
}

func newTestCircuit(t *testing.T, cfg CircuitConfig) (*Circuit, *fakeClock, func() []recordedTransition) {
	t.Helper()
	clk := newFakeClock()
	c := NewCircuit("test", cfg)
	c.now = clk.Now

	var (
		mu          sync.Mutex
		transitions []recordedTransition
	)
	c.OnStateChange = func(from, to CircuitState, info CircuitInfo) {
		mu.Lock()
		defer mu.Unlock()
		transitions = append(transitions, recordedTransition{from, to, info})
	}
	get := func() []recordedTransition {
		mu.Lock()
		defer mu.Unlock()
		cp := make([]recordedTransition, len(transitions))
		copy(cp, transitions)
		return cp
	}
	return c, clk, get
}

// ---------------------------------------------------------------
// Basics: defaults + string formatting + State accessor.
// ---------------------------------------------------------------

func TestCircuitDefaults(t *testing.T) {
	c := NewCircuit("defaults", CircuitConfig{})
	if got := c.failureThreshold; got != 5 {
		t.Errorf("default failureThreshold = %d, want 5", got)
	}
	if got := c.initialCooldown; got != 10*time.Second {
		t.Errorf("default initialCooldown = %v, want 10s", got)
	}
	if got := c.cooldownFactor; got != 2.0 {
		t.Errorf("default cooldownFactor = %v, want 2.0", got)
	}
	if got := c.maxCooldown; got != 5*time.Minute {
		t.Errorf("default maxCooldown = %v, want 5m", got)
	}
	if got := c.State(); got != StateClosed {
		t.Errorf("new circuit state = %v, want closed", got)
	}
	if got := c.Name(); got != "defaults" {
		t.Errorf("Name() = %q, want %q", got, "defaults")
	}
}

func TestCircuitStateString(t *testing.T) {
	cases := map[CircuitState]string{
		StateClosed:      "closed",
		StateOpen:        "open",
		StateHalfOpen:    "half_open",
		CircuitState(99): "unknown",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", s, got, want)
		}
	}
}

// ---------------------------------------------------------------
// Closed -> Open on N consecutive failures.
// ---------------------------------------------------------------

func TestCircuitClosedToOpenAtThreshold(t *testing.T) {
	c, _, trans := newTestCircuit(t, CircuitConfig{
		FailureThreshold: 3,
		InitialCooldown:  time.Second,
	})
	// 2 failures below threshold — still closed.
	c.OnFailure()
	c.OnFailure()
	if got := c.State(); got != StateClosed {
		t.Fatalf("after 2 failures state=%v, want closed", got)
	}
	if len(trans()) != 0 {
		t.Fatalf("unexpected transitions before threshold: %+v", trans())
	}

	// 3rd failure trips to open.
	c.OnFailure()
	if got := c.State(); got != StateOpen {
		t.Fatalf("after 3 failures state=%v, want open", got)
	}
	tr := trans()
	if len(tr) != 1 || tr[0].from != StateClosed || tr[0].to != StateOpen {
		t.Fatalf("expected closed->open transition, got %+v", tr)
	}
	if tr[0].info.Failures != 3 {
		t.Fatalf("transition info.Failures = %d, want 3", tr[0].info.Failures)
	}

	// Allow short-circuits while open.
	if err := c.Allow(); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("Allow() in open state = %v, want ErrCircuitOpen", err)
	}
}

// ---------------------------------------------------------------
// Success resets failure counter before threshold.
// ---------------------------------------------------------------

func TestCircuitSuccessResetsFailures(t *testing.T) {
	c, _, trans := newTestCircuit(t, CircuitConfig{FailureThreshold: 3})
	c.OnFailure()
	c.OnFailure()
	c.OnSuccess()
	// Still closed, no transition fired (already closed).
	if got := c.State(); got != StateClosed {
		t.Fatalf("after success state=%v, want closed", got)
	}
	if len(trans()) != 0 {
		t.Fatalf("unexpected transitions: %+v", trans())
	}
	// Needs a fresh 3 consecutive failures to trip.
	c.OnFailure()
	c.OnFailure()
	if got := c.State(); got != StateClosed {
		t.Fatalf("2 failures after reset should stay closed, got %v", got)
	}
	c.OnFailure()
	if got := c.State(); got != StateOpen {
		t.Fatalf("3 failures after reset should open, got %v", got)
	}
}

// ---------------------------------------------------------------
// Open -> HalfOpen after cooldown elapses (via Allow).
// ---------------------------------------------------------------

func TestCircuitOpenToHalfOpenAfterCooldown(t *testing.T) {
	c, clk, trans := newTestCircuit(t, CircuitConfig{
		FailureThreshold: 1,
		InitialCooldown:  time.Second,
	})
	c.OnFailure() // trips to open
	if got := c.State(); got != StateOpen {
		t.Fatalf("state=%v, want open", got)
	}

	// Before cooldown elapses — still blocked, stays open.
	clk.Advance(500 * time.Millisecond)
	if err := c.Allow(); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("Allow pre-cooldown = %v, want ErrCircuitOpen", err)
	}
	if got := c.State(); got != StateOpen {
		t.Fatalf("state pre-cooldown = %v, want open", got)
	}

	// Elapse cooldown; next Allow promotes to half_open and admits probe.
	clk.Advance(600 * time.Millisecond)
	if err := c.Allow(); err != nil {
		t.Fatalf("Allow after cooldown = %v, want nil", err)
	}
	if got := c.State(); got != StateHalfOpen {
		t.Fatalf("state after cooldown = %v, want half_open", got)
	}
	tr := trans()
	if len(tr) != 2 {
		t.Fatalf("expected 2 transitions, got %d: %+v", len(tr), tr)
	}
	if tr[1].from != StateOpen || tr[1].to != StateHalfOpen {
		t.Fatalf("expected open->half_open, got %+v", tr[1])
	}
}

// ---------------------------------------------------------------
// HalfOpen -> Closed on successful probe (cooldown resets).
// ---------------------------------------------------------------

func TestCircuitHalfOpenToClosedOnSuccess(t *testing.T) {
	c, clk, trans := newTestCircuit(t, CircuitConfig{
		FailureThreshold: 1,
		InitialCooldown:  time.Second,
		CooldownFactor:   3.0,
		MaxCooldown:      time.Minute,
	})
	// Trip and advance time to move into half_open.
	c.OnFailure()
	clk.Advance(time.Second)
	if err := c.Allow(); err != nil {
		t.Fatalf("Allow into half_open = %v", err)
	}
	// Force an artificially high cooldown to confirm reset-to-initial.
	c.mu.Lock()
	c.cooldown = 30 * time.Second
	c.mu.Unlock()

	c.OnSuccess()
	if got := c.State(); got != StateClosed {
		t.Fatalf("state after successful probe = %v, want closed", got)
	}
	if got := c.Cooldown(); got != time.Second {
		t.Fatalf("cooldown after success = %v, want 1s (initial)", got)
	}
	// New Allow should flow normally.
	if err := c.Allow(); err != nil {
		t.Fatalf("Allow in reset-closed = %v", err)
	}
	tr := trans()
	// closed->open, open->half_open, half_open->closed.
	if len(tr) != 3 {
		t.Fatalf("expected 3 transitions, got %d: %+v", len(tr), tr)
	}
	if tr[2].from != StateHalfOpen || tr[2].to != StateClosed {
		t.Fatalf("final transition = %+v, want half_open->closed", tr[2])
	}
}

// ---------------------------------------------------------------
// HalfOpen -> Open on failed probe with exponential cooldown.
// ---------------------------------------------------------------

func TestCircuitHalfOpenToOpenFailedProbeExponential(t *testing.T) {
	c, clk, trans := newTestCircuit(t, CircuitConfig{
		FailureThreshold: 1,
		InitialCooldown:  time.Second,
		CooldownFactor:   2.0,
		MaxCooldown:      time.Minute,
	})
	c.OnFailure() // closed -> open (cooldown=1s)
	clk.Advance(time.Second)
	if err := c.Allow(); err != nil {
		t.Fatalf("Allow into half_open = %v", err)
	}
	c.OnFailure() // half_open -> open, cooldown=2s
	if got := c.State(); got != StateOpen {
		t.Fatalf("state=%v, want open", got)
	}
	if got := c.Cooldown(); got != 2*time.Second {
		t.Fatalf("cooldown after 1st failed probe = %v, want 2s", got)
	}

	// Probe again — cooldown should be 4s.
	clk.Advance(2 * time.Second)
	if err := c.Allow(); err != nil {
		t.Fatalf("Allow into half_open (2nd time) = %v", err)
	}
	c.OnFailure()
	if got := c.Cooldown(); got != 4*time.Second {
		t.Fatalf("cooldown after 2nd failed probe = %v, want 4s", got)
	}

	// And once more — 8s.
	clk.Advance(4 * time.Second)
	if err := c.Allow(); err != nil {
		t.Fatalf("Allow into half_open (3rd time) = %v", err)
	}
	c.OnFailure()
	if got := c.Cooldown(); got != 8*time.Second {
		t.Fatalf("cooldown after 3rd failed probe = %v, want 8s", got)
	}

	// Check that we saw the half_open -> open transitions.
	count := 0
	for _, tr := range trans() {
		if tr.from == StateHalfOpen && tr.to == StateOpen {
			count++
		}
	}
	if count != 3 {
		t.Fatalf("half_open->open transition count = %d, want 3", count)
	}
}

// ---------------------------------------------------------------
// Cooldown is capped at MaxCooldown.
// ---------------------------------------------------------------

func TestCircuitCooldownCapsAtMax(t *testing.T) {
	c, clk, _ := newTestCircuit(t, CircuitConfig{
		FailureThreshold: 1,
		InitialCooldown:  time.Second,
		CooldownFactor:   10.0,
		MaxCooldown:      5 * time.Second,
	})
	c.OnFailure() // cooldown=1s, open
	// First failed probe: 1s * 10 = 10s, capped to 5s.
	clk.Advance(time.Second)
	if err := c.Allow(); err != nil {
		t.Fatalf("Allow = %v", err)
	}
	c.OnFailure()
	if got := c.Cooldown(); got != 5*time.Second {
		t.Fatalf("cooldown = %v, want capped at 5s", got)
	}
	// Second failed probe: stays capped.
	clk.Advance(5 * time.Second)
	if err := c.Allow(); err != nil {
		t.Fatalf("Allow (2) = %v", err)
	}
	c.OnFailure()
	if got := c.Cooldown(); got != 5*time.Second {
		t.Fatalf("cooldown (2) = %v, want still capped at 5s", got)
	}
}

// ---------------------------------------------------------------
// Concurrent probe serialization: only one in-flight at a time.
// ---------------------------------------------------------------

func TestCircuitHalfOpenConcurrentProbeSerialization(t *testing.T) {
	c, clk, _ := newTestCircuit(t, CircuitConfig{
		FailureThreshold: 1,
		InitialCooldown:  time.Millisecond,
	})
	c.OnFailure()
	clk.Advance(time.Second)

	const goroutines = 32
	var (
		wg       sync.WaitGroup
		start    = make(chan struct{})
		admitted int32
		blocked  int32
	)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			err := c.Allow()
			if err == nil {
				atomic.AddInt32(&admitted, 1)
			} else if errors.Is(err, ErrCircuitOpen) {
				atomic.AddInt32(&blocked, 1)
			} else {
				t.Errorf("unexpected Allow err: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&admitted); got != 1 {
		t.Fatalf("admitted probes = %d, want exactly 1", got)
	}
	if got := atomic.LoadInt32(&blocked); got != goroutines-1 {
		t.Fatalf("blocked probes = %d, want %d", got, goroutines-1)
	}
	if got := c.State(); got != StateHalfOpen {
		t.Fatalf("state after concurrent Allow = %v, want half_open", got)
	}

	// Completing the probe re-opens the gate. A fresh failure
	// should bounce back to open and clear halfOpenInFlight.
	c.OnFailure()
	if got := c.State(); got != StateOpen {
		t.Fatalf("state after probe failure = %v, want open", got)
	}
	c.mu.Lock()
	inFlight := c.halfOpenInFlight
	c.mu.Unlock()
	if inFlight {
		t.Fatal("halfOpenInFlight should be cleared after failed probe")
	}
}

// ---------------------------------------------------------------
// Allow in StateClosed is a fast-path no-op (no transitions).
// ---------------------------------------------------------------

func TestCircuitAllowClosedFastPath(t *testing.T) {
	c, _, trans := newTestCircuit(t, CircuitConfig{})
	for i := 0; i < 100; i++ {
		if err := c.Allow(); err != nil {
			t.Fatalf("Allow iter %d = %v", i, err)
		}
	}
	if len(trans()) != 0 {
		t.Fatalf("closed-state Allow should not emit transitions: %+v", trans())
	}
}

// ---------------------------------------------------------------
// Nil callback is safe.
// ---------------------------------------------------------------

func TestCircuitNilCallbackSafe(t *testing.T) {
	c := NewCircuit("nocb", CircuitConfig{FailureThreshold: 1, InitialCooldown: time.Millisecond})
	// Does not panic with no callback set.
	c.OnFailure()
	if got := c.State(); got != StateOpen {
		t.Fatalf("state = %v, want open", got)
	}
	c.OnSuccess()
	if got := c.State(); got != StateClosed {
		t.Fatalf("state = %v, want closed", got)
	}
}

// ---------------------------------------------------------------
// OnFailure in StateOpen bumps counter but no spurious transitions.
// ---------------------------------------------------------------

func TestCircuitOnFailureInOpenIsNoopTransitionWise(t *testing.T) {
	c, _, trans := newTestCircuit(t, CircuitConfig{
		FailureThreshold: 1,
		InitialCooldown:  time.Second,
	})
	c.OnFailure() // -> open (1 transition)
	c.OnFailure() // stays open, but counter bumps
	c.OnFailure() // stays open

	tr := trans()
	if len(tr) != 1 {
		t.Fatalf("expected 1 transition (closed->open), got %d: %+v", len(tr), tr)
	}
	c.mu.Lock()
	failures := c.failures
	c.mu.Unlock()
	if failures != 3 {
		t.Fatalf("failures = %d, want 3", failures)
	}
}
