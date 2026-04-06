package hub

import (
	"sync"
	"time"
)

// Circuit breaker states.
const (
	CircuitClosed   = iota // normal operation
	CircuitOpen            // failing, reject calls
	CircuitHalfOpen        // testing recovery
)

// CircuitBreaker prevents cascading failures from broken subscribers.
// When a subscriber fails MaxFailures times, the breaker opens and
// stops dispatching to it until ResetTimeout elapses.
type CircuitBreaker struct {
	MaxFailures  int
	ResetTimeout time.Duration
	HalfOpenMax  int // max calls allowed in half-open state

	mu          sync.Mutex
	state       int
	failures    int
	lastFailure time.Time
	halfOpenCnt int
}

// Allow returns true if the circuit breaker permits a call.
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitClosed:
		return true
	case CircuitOpen:
		if time.Since(cb.lastFailure) >= cb.ResetTimeout {
			cb.state = CircuitHalfOpen
			cb.halfOpenCnt = 0
			return true
		}
		return false
	case CircuitHalfOpen:
		if cb.halfOpenCnt < cb.HalfOpenMax {
			cb.halfOpenCnt++
			return true
		}
		return false
	}
	return true
}

// RecordSuccess records a successful call and resets the breaker if needed.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures = 0
	cb.state = CircuitClosed
	cb.halfOpenCnt = 0
}

// RecordFailure records a failed call and opens the breaker if threshold is reached.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures++
	cb.lastFailure = time.Now()

	if cb.state == CircuitHalfOpen {
		cb.state = CircuitOpen
		return
	}

	if cb.failures >= cb.MaxFailures {
		cb.state = CircuitOpen
	}
}

// State returns the current circuit state.
func (cb *CircuitBreaker) State() int {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}
