package pools

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/ericmacdougall/stoke/internal/provider"
)

// Strategy selects which member of a ProviderPool serves the next call.
//
// The three shipped strategies cover the common operational shapes
// without pulling in weight-table configuration that the full
// spec-13 providerpool package will own:
//
//   - StrategyRoundRobin: cycle through members in order; ignore weight.
//   - StrategyWeighted:   pick proportionally to each member's Weight
//                         field using a stateless counter modulo the
//                         sum of weights. Deterministic per call index
//                         so tests can assert the exact sequence.
//   - StrategyFailover:   always return the first healthy member; only
//                         advance when the prior member has tripped its
//                         circuit via MarkUnhealthy.
//
// The zero value is StrategyRoundRobin so an operator who constructs
// a pool with `&ProviderPool{Members: ...}` gets sensible defaults
// without reading the docs.
type Strategy int

const (
	// StrategyRoundRobin cycles through members one at a time. Default.
	StrategyRoundRobin Strategy = iota
	// StrategyWeighted picks members proportionally to their Weight.
	StrategyWeighted
	// StrategyFailover sticks with the first healthy member and only
	// advances when MarkUnhealthy flips its flag.
	StrategyFailover
)

// String returns the strategy name for diagnostics and log lines. Use
// it in structured-log fields rather than format-specifier %d.
func (s Strategy) String() string {
	switch s {
	case StrategyRoundRobin:
		return "round-robin"
	case StrategyWeighted:
		return "weighted"
	case StrategyFailover:
		return "failover"
	default:
		return fmt.Sprintf("strategy(%d)", int(s))
	}
}

// ProviderMember is one entry in a ProviderPool: a concrete
// provider.Provider plus the routing metadata the pool needs to decide
// when to hand it out. The struct is value-friendly but callers should
// pass pointers so MarkHealthy / MarkUnhealthy flips propagate.
type ProviderMember struct {
	// Provider is the live client. Required.
	Provider provider.Provider
	// Weight governs StrategyWeighted selection. Ignored by other
	// strategies. A Weight of 0 means "never pick via weighted"; a
	// member with Weight=0 is still selectable via round-robin and
	// failover so operators can park a cold-spare in the pool.
	Weight int
	// healthy is 1 when the member is eligible, 0 when a recent call
	// marked it unhealthy. Stored as int32 for atomic access so the
	// hot path in Next does not contend on the pool mutex.
	healthy int32
}

// IsHealthy reports whether the member is currently eligible. Writers
// must use MarkHealthy / MarkUnhealthy so the atomic stays consistent.
func (m *ProviderMember) IsHealthy() bool {
	return atomic.LoadInt32(&m.healthy) != 0
}

// MarkHealthy flips the member's eligibility flag on.
func (m *ProviderMember) MarkHealthy() {
	atomic.StoreInt32(&m.healthy, 1)
}

// MarkUnhealthy flips the member's eligibility flag off. Used after a
// call errors in a way the caller wants to treat as "skip this member
// until something external flips it back" (rate limit, 401, etc.).
func (m *ProviderMember) MarkUnhealthy() {
	atomic.StoreInt32(&m.healthy, 0)
}

// Name returns the underlying provider's Name or "<nil>" for an empty
// member. Exposed so diagnostics can list pool contents without
// nil-checking the inner Provider at every call site.
func (m *ProviderMember) Name() string {
	if m == nil || m.Provider == nil {
		return "<nil>"
	}
	return m.Provider.Name()
}

// ProviderPool is a round-robin / weighted / failover wrapper over a
// slice of provider.Provider. It is the minimum primitive the rest of
// the codebase needs to treat "a set of interchangeable LLM backends"
// as a single Provider-shaped object; the richer capability-negotiation
// pool lives in spec-13's future internal/providerpool/ package.
//
// Zero-value usage: not supported — construct via NewProviderPool so
// the counter and mutex are initialised together and the member list
// is validated exactly once.
type ProviderPool struct {
	strategy Strategy
	members  []*ProviderMember

	// counter drives StrategyRoundRobin and StrategyWeighted selection
	// without locking. uint64 so a very long-running process doesn't
	// wrap inside a single session.
	counter uint64

	mu sync.RWMutex
}

// ErrNoProvider is returned by Next when the pool has no members or
// every member is unhealthy. Callers should treat this as a terminal
// condition for the current request, not a retriable error — the pool
// has already walked the whole member list.
var ErrNoProvider = errors.New("provider pool: no healthy providers")

// ErrEmptyPool is returned by NewProviderPool when the caller passes
// zero members. An empty pool is almost certainly a config bug so
// we fail at construction rather than at first Next().
var ErrEmptyPool = errors.New("provider pool: cannot build with zero members")

// NewProviderPool validates the member list and returns a pool with
// every member marked healthy. Caller retains ownership of the
// *ProviderMember pointers and may flip MarkHealthy / MarkUnhealthy
// independently — the pool re-reads the flag on every Next().
func NewProviderPool(strategy Strategy, members []*ProviderMember) (*ProviderPool, error) {
	if len(members) == 0 {
		return nil, ErrEmptyPool
	}
	for i, m := range members {
		if m == nil {
			return nil, fmt.Errorf("provider pool: member %d is nil", i)
		}
		if m.Provider == nil {
			return nil, fmt.Errorf("provider pool: member %d (weight=%d) has nil Provider", i, m.Weight)
		}
		if m.Weight < 0 {
			return nil, fmt.Errorf("provider pool: member %d (%s) has negative weight %d", i, m.Provider.Name(), m.Weight)
		}
		// Mark every member healthy at construction; callers who want
		// a member cold-started disabled should MarkUnhealthy after
		// the pool is built.
		m.MarkHealthy()
	}
	if strategy == StrategyWeighted && totalWeight(members) == 0 {
		return nil, fmt.Errorf("provider pool: weighted strategy requires at least one member with Weight > 0")
	}
	return &ProviderPool{
		strategy: strategy,
		members:  members,
	}, nil
}

// Strategy returns the selection strategy the pool was built with.
func (p *ProviderPool) Strategy() Strategy { return p.strategy }

// Len returns the total number of members, healthy or not.
func (p *ProviderPool) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.members)
}

// Members returns a snapshot of the member list. Callers receive a
// fresh slice but the *ProviderMember pointers alias the pool's
// storage — MarkHealthy / MarkUnhealthy on a returned member affects
// the live pool, which is the whole point.
func (p *ProviderPool) Members() []*ProviderMember {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*ProviderMember, len(p.members))
	copy(out, p.members)
	return out
}

// Next returns the next provider to serve a call, honoring the pool's
// strategy and skipping unhealthy members. Safe for concurrent use.
//
// Returns ErrNoProvider if every member is unhealthy. Callers that
// want "any provider, even unhealthy" should iterate Members()
// directly.
func (p *ProviderPool) Next() (*ProviderMember, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.members) == 0 {
		return nil, ErrNoProvider
	}

	switch p.strategy {
	case StrategyFailover:
		return p.nextFailover()
	case StrategyWeighted:
		return p.nextWeighted()
	default:
		// StrategyRoundRobin is the zero value; any unknown strategy
		// falls through here too. That is the safe default: a new
		// strategy constant not yet handled acts like round-robin
		// instead of panicking in production.
		return p.nextRoundRobin()
	}
}

// nextRoundRobin advances the counter and returns the next healthy
// member, probing up to len(members) slots before giving up. Must be
// called with p.mu held (RLock is sufficient; counter is atomic).
func (p *ProviderPool) nextRoundRobin() (*ProviderMember, error) {
	n := len(p.members)
	for attempt := 0; attempt < n; attempt++ {
		idx := int(atomic.AddUint64(&p.counter, 1)-1) % n // #nosec G115 -- negative-result wrap handled explicitly below.
		if idx < 0 { // guard against wrap on 32-bit int coercion
			idx += n
		}
		m := p.members[idx]
		if m.IsHealthy() {
			return m, nil
		}
	}
	return nil, ErrNoProvider
}

// nextWeighted picks a member proportionally to Weight by stepping a
// stateless counter through the cumulative-weight band. Unhealthy
// members and Weight=0 members are skipped; if every healthy member
// has Weight=0 the function falls back to round-robin across them so
// the caller still gets a provider instead of ErrNoProvider.
func (p *ProviderPool) nextWeighted() (*ProviderMember, error) {
	total := 0
	for _, m := range p.members {
		if m.IsHealthy() && m.Weight > 0 {
			total += m.Weight
		}
	}
	if total == 0 {
		// No healthy member has positive weight. Degrade to
		// round-robin across healthy members rather than fail; this
		// matches the "cold-spare with Weight=0" use case described
		// on ProviderMember.Weight.
		return p.nextRoundRobin()
	}
	step := int(atomic.AddUint64(&p.counter, 1)-1) % total // #nosec G115 -- negative-result wrap handled explicitly below.
	if step < 0 {
		step += total
	}
	for _, m := range p.members {
		if !m.IsHealthy() || m.Weight <= 0 {
			continue
		}
		if step < m.Weight {
			return m, nil
		}
		step -= m.Weight
	}
	// Unreachable under the modulo arithmetic above, but return a
	// typed error rather than panicking if future refactors break
	// the invariant.
	return nil, ErrNoProvider
}

// nextFailover returns the first healthy member, in declared order.
// No counter advance — failover is sticky so long as the primary
// stays healthy, which is the whole contract.
func (p *ProviderPool) nextFailover() (*ProviderMember, error) {
	for _, m := range p.members {
		if m.IsHealthy() {
			return m, nil
		}
	}
	return nil, ErrNoProvider
}

// Call picks a provider via Next and invokes Chat, automatically
// retrying on the next healthy member when the current one returns an
// error. On retry, the prior member is marked unhealthy so a sticky
// failure doesn't get re-tried every call in the same process.
//
// Call stops after len(members) attempts and returns a wrapped error
// listing every provider it tried plus the last underlying error.
// Callers that need finer-grained failure classification (rate limit
// vs auth vs transport) should invoke Next() themselves.
func (p *ProviderPool) Call(req provider.ChatRequest) (*provider.ChatResponse, error) {
	p.mu.RLock()
	attempts := len(p.members)
	p.mu.RUnlock()

	var lastErr error
	tried := make([]string, 0, attempts)
	for i := 0; i < attempts; i++ {
		m, err := p.Next()
		if err != nil {
			if lastErr == nil {
				return nil, err
			}
			break
		}
		tried = append(tried, m.Name())
		resp, callErr := m.Provider.Chat(req)
		if callErr == nil {
			return resp, nil
		}
		lastErr = callErr
		// Mark this member unhealthy so the next Next() picks a
		// different one. Callers who wanted idempotent retries on
		// the same provider already have retry logic inside the
		// provider (see internal/provider/anthropic.go Chat retry).
		m.MarkUnhealthy()
	}
	if lastErr == nil {
		return nil, ErrNoProvider
	}
	return nil, fmt.Errorf("provider pool: all %d providers failed (tried=%v): %w", len(tried), tried, lastErr)
}

// totalWeight sums the Weight field across every member regardless of
// health. Construction-time helper only; the runtime selection path
// uses the inline accumulator in nextWeighted for cache locality.
func totalWeight(members []*ProviderMember) int {
	total := 0
	for _, m := range members {
		if m != nil && m.Weight > 0 {
			total += m.Weight
		}
	}
	return total
}
