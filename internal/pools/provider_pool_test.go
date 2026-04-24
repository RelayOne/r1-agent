package pools

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// fakeProvider is a minimal provider.Provider for exercising the pool
// without making any real network calls. The Chat hook lets each test
// customise success / failure without re-declaring the struct.
type fakeProvider struct {
	name  string
	calls int
	chat  func(provider.ChatRequest) (*provider.ChatResponse, error)
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	f.calls++
	if f.chat != nil {
		return f.chat(req)
	}
	return &provider.ChatResponse{ID: f.name, Model: req.Model}, nil
}

func (f *fakeProvider) ChatStream(req provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	return f.Chat(req)
}

func member(name string, weight int) *ProviderMember {
	return &ProviderMember{Provider: &fakeProvider{name: name}, Weight: weight}
}

func TestStrategy_String(t *testing.T) {
	cases := map[Strategy]string{
		StrategyRoundRobin: "round-robin",
		StrategyWeighted:   "weighted",
		StrategyFailover:   "failover",
		Strategy(99):       "strategy(99)",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("Strategy(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func TestNewProviderPool_RejectsEmpty(t *testing.T) {
	if _, err := NewProviderPool(StrategyRoundRobin, nil); !errors.Is(err, ErrEmptyPool) {
		t.Fatalf("empty pool: err = %v, want ErrEmptyPool", err)
	}
}

func TestNewProviderPool_RejectsNilMember(t *testing.T) {
	_, err := NewProviderPool(StrategyRoundRobin, []*ProviderMember{nil})
	if err == nil || !strings.Contains(err.Error(), "is nil") {
		t.Fatalf("nil member: err = %v, want message containing 'is nil'", err)
	}
}

func TestNewProviderPool_RejectsNilProvider(t *testing.T) {
	_, err := NewProviderPool(StrategyRoundRobin, []*ProviderMember{{Weight: 1}})
	if err == nil || !strings.Contains(err.Error(), "nil Provider") {
		t.Fatalf("nil Provider: err = %v, want 'nil Provider'", err)
	}
}

func TestNewProviderPool_RejectsNegativeWeight(t *testing.T) {
	m := &ProviderMember{Provider: &fakeProvider{name: "a"}, Weight: -1}
	_, err := NewProviderPool(StrategyRoundRobin, []*ProviderMember{m})
	if err == nil || !strings.Contains(err.Error(), "negative weight") {
		t.Fatalf("negative weight: err = %v, want 'negative weight'", err)
	}
}

func TestNewProviderPool_WeightedRequiresPositiveTotal(t *testing.T) {
	m := &ProviderMember{Provider: &fakeProvider{name: "a"}, Weight: 0}
	_, err := NewProviderPool(StrategyWeighted, []*ProviderMember{m})
	if err == nil || !strings.Contains(err.Error(), "Weight > 0") {
		t.Fatalf("zero total weight: err = %v, want 'Weight > 0'", err)
	}
}

func TestNewProviderPool_MarksAllHealthy(t *testing.T) {
	m1 := member("a", 1)
	m2 := member("b", 1)
	// Manually flip one unhealthy before construction to verify that
	// NewProviderPool normalises state.
	m1.MarkUnhealthy()
	pool, err := NewProviderPool(StrategyRoundRobin, []*ProviderMember{m1, m2})
	if err != nil {
		t.Fatalf("NewProviderPool: %v", err)
	}
	if !m1.IsHealthy() || !m2.IsHealthy() {
		t.Fatalf("all members should be healthy post-construction: m1=%v m2=%v", m1.IsHealthy(), m2.IsHealthy())
	}
	if pool.Strategy() != StrategyRoundRobin {
		t.Fatalf("Strategy() = %v, want RoundRobin", pool.Strategy())
	}
}

func TestProviderPool_Len_Members(t *testing.T) {
	m1 := member("a", 1)
	m2 := member("b", 2)
	pool, err := NewProviderPool(StrategyRoundRobin, []*ProviderMember{m1, m2})
	if err != nil {
		t.Fatalf("NewProviderPool: %v", err)
	}
	if pool.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", pool.Len())
	}
	snap := pool.Members()
	if len(snap) != 2 || snap[0] != m1 || snap[1] != m2 {
		t.Fatalf("Members() snapshot mismatch: %v", snap)
	}
	// Mutating the returned slice must not affect the pool.
	snap[0] = nil
	if pool.Members()[0] != m1 {
		t.Fatalf("Members() returned a live reference, not a snapshot")
	}
}

func TestProviderPool_RoundRobin_Cycles(t *testing.T) {
	m1 := member("a", 0)
	m2 := member("b", 0)
	m3 := member("c", 0)
	pool, err := NewProviderPool(StrategyRoundRobin, []*ProviderMember{m1, m2, m3})
	if err != nil {
		t.Fatalf("NewProviderPool: %v", err)
	}
	want := []string{"a", "b", "c", "a", "b", "c"}
	for i, w := range want {
		got, err := pool.Next()
		if err != nil {
			t.Fatalf("iter %d: Next() error: %v", i, err)
		}
		if got.Name() != w {
			t.Fatalf("iter %d: got %s, want %s", i, got.Name(), w)
		}
	}
}

func TestProviderPool_RoundRobin_SkipsUnhealthy(t *testing.T) {
	m1 := member("a", 0)
	m2 := member("b", 0)
	m3 := member("c", 0)
	pool, err := NewProviderPool(StrategyRoundRobin, []*ProviderMember{m1, m2, m3})
	if err != nil {
		t.Fatalf("NewProviderPool: %v", err)
	}
	m2.MarkUnhealthy()
	want := []string{"a", "c", "a", "c"}
	for i, w := range want {
		got, err := pool.Next()
		if err != nil {
			t.Fatalf("iter %d: Next() error: %v", i, err)
		}
		if got.Name() != w {
			t.Fatalf("iter %d: got %s, want %s", i, got.Name(), w)
		}
	}
}

func TestProviderPool_RoundRobin_AllUnhealthy(t *testing.T) {
	m1 := member("a", 0)
	m2 := member("b", 0)
	pool, err := NewProviderPool(StrategyRoundRobin, []*ProviderMember{m1, m2})
	if err != nil {
		t.Fatalf("NewProviderPool: %v", err)
	}
	m1.MarkUnhealthy()
	m2.MarkUnhealthy()
	if _, err := pool.Next(); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("all unhealthy: err = %v, want ErrNoProvider", err)
	}
}

func TestProviderPool_Weighted_Proportions(t *testing.T) {
	m1 := member("a", 1)
	m2 := member("b", 3) // 3x as often as a
	pool, err := NewProviderPool(StrategyWeighted, []*ProviderMember{m1, m2})
	if err != nil {
		t.Fatalf("NewProviderPool: %v", err)
	}
	counts := map[string]int{}
	// Run 4000 iterations; expect 1000 a / 3000 b within a small margin.
	for i := 0; i < 4000; i++ {
		got, err := pool.Next()
		if err != nil {
			t.Fatalf("iter %d: Next() error: %v", i, err)
		}
		counts[got.Name()]++
	}
	// Exact deterministic split: step counter is modulo total_weight=4
	// so we get exactly 1000/3000 with no jitter.
	if counts["a"] != 1000 || counts["b"] != 3000 {
		t.Fatalf("weighted distribution off: %v", counts)
	}
}

func TestProviderPool_Weighted_SkipsUnhealthy(t *testing.T) {
	m1 := member("a", 1)
	m2 := member("b", 3)
	pool, err := NewProviderPool(StrategyWeighted, []*ProviderMember{m1, m2})
	if err != nil {
		t.Fatalf("NewProviderPool: %v", err)
	}
	m2.MarkUnhealthy()
	// Only "a" is eligible; every Next() must return it.
	for i := 0; i < 20; i++ {
		got, err := pool.Next()
		if err != nil {
			t.Fatalf("iter %d: Next() error: %v", i, err)
		}
		if got.Name() != "a" {
			t.Fatalf("iter %d: got %s, want a", i, got.Name())
		}
	}
}

func TestProviderPool_Weighted_ZeroTotal_FallsBackToRoundRobin(t *testing.T) {
	// Mix of Weight=0 cold spares and one Weight=1 primary.
	m1 := member("a", 0)
	m2 := member("b", 1)
	pool, err := NewProviderPool(StrategyWeighted, []*ProviderMember{m1, m2})
	if err != nil {
		t.Fatalf("NewProviderPool: %v", err)
	}
	// Primary is healthy → weighted picks it every time.
	for i := 0; i < 5; i++ {
		got, _ := pool.Next()
		if got.Name() != "b" {
			t.Fatalf("iter %d: got %s, want b", i, got.Name())
		}
	}
	// Kill the primary → weighted total is 0 → fallback to round-robin
	// across remaining healthy members (just "a").
	m2.MarkUnhealthy()
	for i := 0; i < 5; i++ {
		got, err := pool.Next()
		if err != nil {
			t.Fatalf("iter %d (fallback): Next() error: %v", i, err)
		}
		if got.Name() != "a" {
			t.Fatalf("iter %d (fallback): got %s, want a", i, got.Name())
		}
	}
}

func TestProviderPool_Failover_SticksToPrimary(t *testing.T) {
	m1 := member("primary", 0)
	m2 := member("backup", 0)
	pool, err := NewProviderPool(StrategyFailover, []*ProviderMember{m1, m2})
	if err != nil {
		t.Fatalf("NewProviderPool: %v", err)
	}
	for i := 0; i < 10; i++ {
		got, _ := pool.Next()
		if got.Name() != "primary" {
			t.Fatalf("iter %d: got %s, want primary", i, got.Name())
		}
	}
	// Trip primary → backup takes over.
	m1.MarkUnhealthy()
	for i := 0; i < 10; i++ {
		got, err := pool.Next()
		if err != nil {
			t.Fatalf("iter %d (failover): Next() error: %v", i, err)
		}
		if got.Name() != "backup" {
			t.Fatalf("iter %d (failover): got %s, want backup", i, got.Name())
		}
	}
	// Restore primary → traffic snaps back.
	m1.MarkHealthy()
	got, _ := pool.Next()
	if got.Name() != "primary" {
		t.Fatalf("after MarkHealthy: got %s, want primary", got.Name())
	}
}

func TestProviderPool_Call_SuccessFirstProvider(t *testing.T) {
	m1 := member("a", 0)
	m2 := member("b", 0)
	pool, err := NewProviderPool(StrategyRoundRobin, []*ProviderMember{m1, m2})
	if err != nil {
		t.Fatalf("NewProviderPool: %v", err)
	}
	resp, err := pool.Call(provider.ChatRequest{Model: "test"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp == nil || resp.Model != "test" {
		t.Fatalf("Call response: %+v", resp)
	}
	// First call used member "a".
	m1fake, ok := m1.Provider.(*fakeProvider)
	if !ok {
		t.Fatalf("m1.Provider: unexpected type: %T", m1.Provider)
	}
	if m1fake.calls != 1 {
		t.Fatalf("m1.calls = %d, want 1", m1fake.calls)
	}
	m2fake, ok := m2.Provider.(*fakeProvider)
	if !ok {
		t.Fatalf("m2.Provider: unexpected type: %T", m2.Provider)
	}
	if m2fake.calls != 0 {
		t.Fatalf("m2.calls = %d, want 0", m2fake.calls)
	}
}

func TestProviderPool_Call_FailoverOnError(t *testing.T) {
	failA := &fakeProvider{
		name: "a",
		chat: func(_ provider.ChatRequest) (*provider.ChatResponse, error) {
			return nil, fmt.Errorf("rate-limited")
		},
	}
	okB := &fakeProvider{name: "b"}
	m1 := &ProviderMember{Provider: failA}
	m2 := &ProviderMember{Provider: okB}
	pool, err := NewProviderPool(StrategyRoundRobin, []*ProviderMember{m1, m2})
	if err != nil {
		t.Fatalf("NewProviderPool: %v", err)
	}

	resp, err := pool.Call(provider.ChatRequest{Model: "test"})
	if err != nil {
		t.Fatalf("Call should have succeeded via failover: %v", err)
	}
	if resp.ID != "b" {
		t.Fatalf("expected response from b, got %s", resp.ID)
	}
	if m1.IsHealthy() {
		t.Fatalf("failing member a should have been marked unhealthy")
	}
	if !m2.IsHealthy() {
		t.Fatalf("successful member b should still be healthy")
	}
}

func TestProviderPool_Call_AllProvidersFail(t *testing.T) {
	boom := func(tag string) *fakeProvider {
		return &fakeProvider{
			name: tag,
			chat: func(_ provider.ChatRequest) (*provider.ChatResponse, error) {
				return nil, fmt.Errorf("boom-%s", tag)
			},
		}
	}
	m1 := &ProviderMember{Provider: boom("a")}
	m2 := &ProviderMember{Provider: boom("b")}
	pool, err := NewProviderPool(StrategyRoundRobin, []*ProviderMember{m1, m2})
	if err != nil {
		t.Fatalf("NewProviderPool: %v", err)
	}
	_, err = pool.Call(provider.ChatRequest{Model: "test"})
	if err == nil {
		t.Fatalf("expected error when all providers fail")
	}
	msg := err.Error()
	for _, want := range []string{"all 2 providers failed", "a", "b", "boom-b"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
	if m1.IsHealthy() || m2.IsHealthy() {
		t.Fatalf("both members should be unhealthy after all-fail")
	}
}

func TestProviderPool_Call_EmptyHealthyPool(t *testing.T) {
	m1 := member("a", 0)
	pool, err := NewProviderPool(StrategyRoundRobin, []*ProviderMember{m1})
	if err != nil {
		t.Fatalf("NewProviderPool: %v", err)
	}
	m1.MarkUnhealthy()
	_, err = pool.Call(provider.ChatRequest{Model: "test"})
	if !errors.Is(err, ErrNoProvider) {
		t.Fatalf("all unhealthy: err = %v, want ErrNoProvider", err)
	}
}

func TestProviderPool_Name_HandlesNil(t *testing.T) {
	var m *ProviderMember
	if got := m.Name(); got != "<nil>" {
		t.Errorf("nil member: Name() = %q, want <nil>", got)
	}
	m = &ProviderMember{}
	if got := m.Name(); got != "<nil>" {
		t.Errorf("empty member: Name() = %q, want <nil>", got)
	}
}

func TestProviderPool_ConcurrentNext(t *testing.T) {
	// Guards against racy counter increment / healthy-flag access.
	m1 := member("a", 1)
	m2 := member("b", 1)
	m3 := member("c", 1)
	pool, err := NewProviderPool(StrategyRoundRobin, []*ProviderMember{m1, m2, m3})
	if err != nil {
		t.Fatalf("NewProviderPool: %v", err)
	}
	const goroutines = 32
	const perG = 200
	var counts sync.Map // name -> *int64
	done := make(chan struct{}, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			for j := 0; j < perG; j++ {
				got, err := pool.Next()
				if err != nil {
					t.Errorf("Next under load: %v", err)
					done <- struct{}{}
					return
				}
				v, _ := counts.LoadOrStore(got.Name(), new(int64))
				p, ok := v.(*int64)
				if !ok {
					t.Errorf("counts value: unexpected type: %T", v)
					done <- struct{}{}
					return
				}
				atomic.AddInt64(p, 1)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}
	// With 32*200=6400 calls across 3 healthy members under round
	// robin, every member must have served at least one call; if not,
	// either the counter or the healthy-flag access is racy.
	total := int64(0)
	for _, name := range []string{"a", "b", "c"} {
		v, ok := counts.Load(name)
		if !ok {
			t.Fatalf("member %s never served any call", name)
		}
		ptr, ok := v.(*int64)
		if !ok {
			t.Fatalf("counts[%s]: unexpected type: %T", name, v)
		}
		n := atomic.LoadInt64(ptr)
		if n == 0 {
			t.Fatalf("member %s served 0 calls under load", name)
		}
		total += n
	}
	if total != goroutines*perG {
		t.Fatalf("total calls = %d, want %d", total, goroutines*perG)
	}
}
