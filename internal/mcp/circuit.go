// Package mcp — circuit.go — per-server circuit breaker with
// state machine: closed -> open -> half_open -> closed (or
// half_open -> open on failed probe). See specs/mcp-client.md
// §Circuit Breaker for the authoritative state machine + config
// constants.
//
// Intentionally stdlib-only (sync + time). No bus imports — the
// OnStateChange callback is the wire-up point for MCP-8 (registry).

package mcp

import (
	"sync"
	"time"
)

// CircuitState enumerates the three states of the breaker. Zero
// value is StateClosed, matching the "healthy on construction"
// invariant.
type CircuitState int

const (
	// StateClosed — calls flow normally. Failures increment the
	// counter; a success resets it. When the counter reaches the
	// configured threshold the breaker transitions to StateOpen.
	StateClosed CircuitState = iota

	// StateOpen — all calls are short-circuited with
	// ErrCircuitOpen (returned via Allow). When the cooldown
	// elapses the next Allow call transitions to StateHalfOpen.
	StateOpen

	// StateHalfOpen — exactly one probe call is permitted at a
	// time. Success transitions back to StateClosed (cooldown
	// reset to initial). Failure transitions back to StateOpen
	// with cooldown multiplied by CooldownFactor (capped at
	// MaxCooldown). While a probe is in-flight, other Allow
	// calls return ErrCircuitOpen.
	StateHalfOpen
)

// String renders the state for logs / event payloads. Lowercase
// matches the spec's event-payload convention (`mcp.circuit.state`
// → {from, to} string values).
func (s CircuitState) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// CircuitConfig captures the breaker's tunable parameters. Any
// zero-valued field falls back to the documented default.
type CircuitConfig struct {
	// FailureThreshold — consecutive failures in StateClosed
	// before transitioning to StateOpen. Default 5.
	FailureThreshold int

	// InitialCooldown — time the breaker stays open before the
	// first half-open probe is permitted. Default 10s. Also the
	// value cooldown resets to on success.
	InitialCooldown time.Duration

	// CooldownFactor — multiplier applied to cooldown on each
	// failed probe (half_open -> open). Default 2.0.
	CooldownFactor float64

	// MaxCooldown — cap for the cooldown window after repeated
	// half_open failures. Default 5m.
	MaxCooldown time.Duration
}

// CircuitInfo is a read-only snapshot of the breaker's state
// passed to the OnStateChange callback. Fields are copied at
// publish time so callback implementations need not hold locks.
type CircuitInfo struct {
	Name     string
	Failures int
	Cooldown time.Duration
	OpenedAt time.Time
}

// Circuit is a per-server breaker. Every public method takes
// c.mu before reading or mutating state, so the struct is safe
// to share across goroutines.
type Circuit struct {
	name string

	mu               sync.Mutex
	state            CircuitState
	failures         int
	openedAt         time.Time
	cooldown         time.Duration
	cooldownFactor   float64
	maxCooldown      time.Duration
	halfOpenInFlight bool

	// Configured ceilings / initial values (const after New).
	failureThreshold int
	initialCooldown  time.Duration

	// OnStateChange, if non-nil, is invoked (outside the lock)
	// on every state transition. Registry (MCP-8) wires this to
	// the bus emitter for `mcp.circuit.state`.
	OnStateChange func(from, to CircuitState, info CircuitInfo)

	// now is injected for deterministic tests; defaults to
	// time.Now.
	now func() time.Time
}

// NewCircuit builds a breaker in StateClosed with zero failures.
// Defaults fill in any zero-valued config field.
func NewCircuit(name string, cfg CircuitConfig) *Circuit {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.InitialCooldown <= 0 {
		cfg.InitialCooldown = 10 * time.Second
	}
	if cfg.CooldownFactor <= 1.0 {
		cfg.CooldownFactor = 2.0
	}
	if cfg.MaxCooldown <= 0 {
		cfg.MaxCooldown = 5 * time.Minute
	}
	if cfg.InitialCooldown > cfg.MaxCooldown {
		cfg.InitialCooldown = cfg.MaxCooldown
	}
	return &Circuit{
		name:             name,
		state:            StateClosed,
		cooldown:         cfg.InitialCooldown,
		cooldownFactor:   cfg.CooldownFactor,
		maxCooldown:      cfg.MaxCooldown,
		failureThreshold: cfg.FailureThreshold,
		initialCooldown:  cfg.InitialCooldown,
		now:              time.Now,
	}
}

// Name returns the breaker's name (as supplied to NewCircuit).
func (c *Circuit) Name() string { return c.name }

// State returns the current state. Takes c.mu for a consistent
// read; callers should treat the value as a snapshot.
func (c *Circuit) State() CircuitState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Cooldown returns the current cooldown window. Useful for event
// payloads. Consistent-read via c.mu.
func (c *Circuit) Cooldown() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cooldown
}

// Allow returns nil when a call is permitted, or ErrCircuitOpen
// when it is blocked. In StateOpen, the elapsed-cooldown check
// may transition the breaker to StateHalfOpen and permit a probe.
// In StateHalfOpen, Allow permits exactly one in-flight probe at
// a time via halfOpenInFlight.
func (c *Circuit) Allow() error {
	c.mu.Lock()
	var (
		publish bool
		from    CircuitState
		to      CircuitState
		info    CircuitInfo
	)
	switch c.state {
	case StateClosed:
		c.mu.Unlock()
		return nil
	case StateOpen:
		if c.now().Sub(c.openedAt) < c.cooldown {
			c.mu.Unlock()
			return ErrCircuitOpen
		}
		// Cooldown elapsed — transition to half_open and admit
		// the caller as the sole probe.
		from, to = c.state, StateHalfOpen
		c.state = StateHalfOpen
		c.halfOpenInFlight = true
		publish = true
		info = c.snapshotLocked()
		c.mu.Unlock()
	case StateHalfOpen:
		if c.halfOpenInFlight {
			c.mu.Unlock()
			return ErrCircuitOpen
		}
		c.halfOpenInFlight = true
		c.mu.Unlock()
		return nil
	default:
		c.mu.Unlock()
		return nil
	}
	if publish {
		c.fireStateChange(from, to, info)
	}
	return nil
}

// OnSuccess records a successful call. Regardless of prior state
// the breaker transitions to StateClosed with failures=0 and
// cooldown reset to InitialCooldown. Publishes a state-change if
// the prior state was not already StateClosed.
func (c *Circuit) OnSuccess() {
	c.mu.Lock()
	from := c.state
	c.failures = 0
	c.cooldown = c.initialCooldown
	c.halfOpenInFlight = false
	c.openedAt = time.Time{}
	c.state = StateClosed
	info := c.snapshotLocked()
	c.mu.Unlock()

	if from != StateClosed {
		c.fireStateChange(from, StateClosed, info)
	}
}

// OnFailure records a failed call. In StateClosed, increments
// the failure counter and trips to StateOpen on threshold reach.
// In StateHalfOpen, transitions back to StateOpen with cooldown
// multiplied by CooldownFactor (capped at MaxCooldown) and
// clears halfOpenInFlight. In StateOpen this is a no-op on the
// state machine but bumps the counter so telemetry reflects the
// attempted call.
func (c *Circuit) OnFailure() {
	c.mu.Lock()
	var (
		publish bool
		from    CircuitState
		to      CircuitState
		info    CircuitInfo
	)
	switch c.state {
	case StateClosed:
		c.failures++
		if c.failures >= c.failureThreshold {
			from, to = StateClosed, StateOpen
			c.state = StateOpen
			c.openedAt = c.now()
			publish = true
		}
	case StateHalfOpen:
		from, to = StateHalfOpen, StateOpen
		c.state = StateOpen
		c.halfOpenInFlight = false
		c.openedAt = c.now()
		// Exponential back-off on failed probe, capped.
		next := time.Duration(float64(c.cooldown) * c.cooldownFactor)
		if next > c.maxCooldown {
			next = c.maxCooldown
		}
		if next <= 0 {
			next = c.initialCooldown
		}
		c.cooldown = next
		c.failures++
		publish = true
	case StateOpen:
		c.failures++
	}
	info = c.snapshotLocked()
	c.mu.Unlock()

	if publish {
		c.fireStateChange(from, to, info)
	}
}

// snapshotLocked copies the fields the OnStateChange callback
// consumes. Must be called with c.mu held.
func (c *Circuit) snapshotLocked() CircuitInfo {
	return CircuitInfo{
		Name:     c.name,
		Failures: c.failures,
		Cooldown: c.cooldown,
		OpenedAt: c.openedAt,
	}
}

// fireStateChange invokes the callback outside the lock so the
// callback may itself call back into the Circuit without
// deadlocking. A nil callback is a no-op.
func (c *Circuit) fireStateChange(from, to CircuitState, info CircuitInfo) {
	cb := c.OnStateChange
	if cb == nil {
		return
	}
	cb(from, to, info)
}
