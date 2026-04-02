package subscriptions

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Provider identifies which execution engine a pool serves.
type Provider string

const (
	ProviderClaude Provider = "claude"
	ProviderCodex  Provider = "codex"
)

// PoolStatus tracks the operational state of a subscription pool.
type PoolStatus int

const (
	StatusIdle        PoolStatus = iota // available for work
	StatusBusy                          // running a task
	StatusThrottled                     // utilization > 80%
	StatusExhausted                     // utilization > 95%
	StatusCircuitOpen                   // consecutive failures, backed off
)

// Pool represents one subscription with its own auth and rate limits.
type Pool struct {
	ID          string
	Provider    Provider
	ConfigDir   string
	OAuthToken  string

	// Mutable state (protected by Manager.mu)
	Utilization         float64
	SevenDayUtilization float64
	ResetsAt            time.Time
	SevenDayResetsAt    time.Time
	LastPolled          time.Time
	Status              PoolStatus
	CurrentTask         string
	ConsecutiveFails    int
	CircuitBreakerUntil time.Time
}

// Manager coordinates multiple subscription pools with thread-safe access.
type Manager struct {
	mu    sync.Mutex
	pools []Pool
}

// NewManager creates a pool manager. Pools are copied to prevent external mutation.
func NewManager(pools []Pool) *Manager {
	copied := make([]Pool, len(pools))
	copy(copied, pools)
	return &Manager{pools: copied}
}

// Acquire atomically selects the least-loaded pool for the given provider,
// marks it busy, and returns a copy. Returns an error if no pool is available.
// The caller MUST call Release when done.
func (m *Manager) Acquire(provider Provider, taskID string) (Pool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	var candidates []int // indices into m.pools

	for i := range m.pools {
		p := &m.pools[i]
		if p.Provider != provider {
			continue
		}
		if p.Status == StatusBusy {
			continue
		}
		if p.Status == StatusCircuitOpen && now.Before(p.CircuitBreakerUntil) {
			continue
		}
		if p.Utilization > 95 {
			continue
		}
		candidates = append(candidates, i)
	}

	if len(candidates) == 0 {
		return Pool{}, errors.New("no available pool for " + string(provider))
	}

	// Smart selection for large pools (10+ accounts):
	// Priority: fewest consecutive fails -> least recently used -> lowest utilization
	sort.Slice(candidates, func(a, b int) bool {
		ia, ib := candidates[a], candidates[b]
		pa, pb := &m.pools[ia], &m.pools[ib]

		// 1. Prefer pools with zero consecutive fails
		if pa.ConsecutiveFails != pb.ConsecutiveFails {
			return pa.ConsecutiveFails < pb.ConsecutiveFails
		}
		// 2. Prefer least recently used (spread load across accounts)
		if !pa.LastPolled.Equal(pb.LastPolled) {
			return pa.LastPolled.Before(pb.LastPolled)
		}
		// 3. Lowest utilization
		if pa.Utilization != pb.Utilization {
			return pa.Utilization < pb.Utilization
		}
		// 4. Stable tiebreaker
		return pa.ID < pb.ID
	})

	best := candidates[0]
	m.pools[best].Status = StatusBusy
	m.pools[best].CurrentTask = taskID
	m.pools[best].LastPolled = time.Now() // LRU tracking

	// Return a copy so callers can't mutate internal state
	return m.pools[best], nil
}

// Release marks a pool as idle and updates state based on task outcome.
// rateLimited=true trips the circuit breaker after consecutive failures.
func (m *Manager) Release(poolID string, rateLimited bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := range m.pools {
		if m.pools[i].ID != poolID {
			continue
		}
		m.pools[i].CurrentTask = ""
		if rateLimited {
			m.pools[i].ConsecutiveFails++
			if m.pools[i].ConsecutiveFails >= 3 {
				m.pools[i].Status = StatusCircuitOpen
				m.pools[i].CircuitBreakerUntil = time.Now().Add(5 * time.Minute)
			} else {
				m.pools[i].Status = StatusThrottled
			}
		} else {
			m.pools[i].ConsecutiveFails = 0
			// Reclassify based on utilization
			switch {
			case m.pools[i].Utilization > 95:
				m.pools[i].Status = StatusExhausted
			case m.pools[i].Utilization > 80:
				m.pools[i].Status = StatusThrottled
			default:
				m.pools[i].Status = StatusIdle
			}
		}
		return
	}
}

// AcquireExcluding is like Acquire but skips the specified pool IDs.
// Used for pool rotation after rate limiting.
func (m *Manager) AcquireExcluding(provider Provider, taskID string, exclude map[string]bool) (Pool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	var candidates []int

	for i := range m.pools {
		p := &m.pools[i]
		if p.Provider != provider { continue }
		if exclude[p.ID] { continue }
		if p.Status == StatusBusy { continue }
		if p.Status == StatusCircuitOpen && now.Before(p.CircuitBreakerUntil) { continue }
		if p.Utilization > 95 { continue }
		candidates = append(candidates, i)
	}

	if len(candidates) == 0 {
		return Pool{}, errors.New("no alternative pool available for " + string(provider))
	}

	sort.Slice(candidates, func(a, b int) bool {
		ia, ib := candidates[a], candidates[b]
		pa, pb := &m.pools[ia], &m.pools[ib]
		if pa.ConsecutiveFails != pb.ConsecutiveFails {
			return pa.ConsecutiveFails < pb.ConsecutiveFails
		}
		if !pa.LastPolled.Equal(pb.LastPolled) {
			return pa.LastPolled.Before(pb.LastPolled)
		}
		if pa.Utilization != pb.Utilization {
			return pa.Utilization < pb.Utilization
		}
		return pa.ID < pb.ID
	})

	best := candidates[0]
	m.pools[best].Status = StatusBusy
	m.pools[best].CurrentTask = taskID
	m.pools[best].LastPolled = now // LRU tracking
	return m.pools[best], nil
}

// WaitForPool blocks until a pool becomes available or context expires.
// Checks every 10 seconds. Used when all pools are rate-limited.
func (m *Manager) WaitForPool(ctx context.Context, provider Provider, taskID string) (Pool, error) {
	// Try immediately first
	pool, err := m.Acquire(provider, taskID)
	if err == nil {
		return pool, nil
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return Pool{}, fmt.Errorf("all %s pools exhausted, timed out waiting: %w", provider, ctx.Err())
		case <-ticker.C:
			pool, err := m.Acquire(provider, taskID)
			if err == nil {
				return pool, nil
			}
		}
	}
}

// PoolCount returns the number of pools for a provider.
func (m *Manager) PoolCount(provider Provider) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for i := range m.pools {
		if m.pools[i].Provider == provider { count++ }
	}
	return count
}

// LeastLoaded returns a copy of the best available pool without acquiring it.
// Used for dry-run / preview. For real execution, use Acquire/Release.
func (m *Manager) LeastLoaded(provider Provider) (Pool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var best *Pool
	for i := range m.pools {
		p := &m.pools[i]
		if p.Provider != provider || p.Status == StatusBusy {
			continue
		}
		if best == nil || p.Utilization < best.Utilization {
			best = p
		}
	}
	if best == nil {
		return Pool{}, errors.New("no available pool for " + string(provider))
	}
	return *best, nil
}

// UpdateUtilization updates a pool's utilization data from the OAuth endpoint.
// Thread-safe: called by the poller goroutine.
func (m *Manager) UpdateUtilization(poolID string, fiveHour, sevenDay float64, fiveHourReset, sevenDayReset time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := range m.pools {
		if m.pools[i].ID != poolID {
			continue
		}
		m.pools[i].Utilization = fiveHour
		m.pools[i].SevenDayUtilization = sevenDay
		m.pools[i].ResetsAt = fiveHourReset
		m.pools[i].SevenDayResetsAt = sevenDayReset
		m.pools[i].LastPolled = time.Now()

		// Reclassify status if not busy
		if m.pools[i].Status != StatusBusy {
			switch {
			case fiveHour > 95:
				m.pools[i].Status = StatusExhausted
			case fiveHour > 80:
				m.pools[i].Status = StatusThrottled
			default:
				m.pools[i].Status = StatusIdle
			}
		}
		return
	}
}

// Snapshot returns a copy of all pool states for display.
func (m *Manager) Snapshot() []Pool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Pool, len(m.pools))
	copy(out, m.pools)
	return out
}

func (s PoolStatus) String() string {
	switch s {
	case StatusIdle:        return "idle"
	case StatusBusy:        return "busy"
	case StatusThrottled:   return "throttled"
	case StatusExhausted:   return "exhausted"
	case StatusCircuitOpen: return "circuit-open"
	default:                return fmt.Sprintf("unknown(%d)", int(s))
	}
}
