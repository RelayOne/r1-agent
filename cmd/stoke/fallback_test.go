package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRole is a ModelRole double used in FallbackPair tests. It
// records how many times Call was invoked and lets the test dictate
// the next (output, err) pair returned. All methods are goroutine-
// safe so concurrent-Call tests can share a single fakeRole.
type fakeRole struct {
	name string

	mu        sync.Mutex
	calls     int32 // atomic
	replies   []fakeReply
	nextIdx   int
	defaultOK fakeReply
}

type fakeReply struct {
	out string
	err error
}

func newFakeRole(name string, defaultOK string) *fakeRole {
	return &fakeRole{
		name:      name,
		defaultOK: fakeReply{out: defaultOK},
	}
}

func (r *fakeRole) Name() string { return r.name }

// queue adds one scripted reply. Subsequent Calls drain the queue
// in order; once exhausted, Call returns defaultOK.
func (r *fakeRole) queue(out string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replies = append(r.replies, fakeReply{out: out, err: err})
}

// Calls returns the total number of Call invocations so tests can
// assert traffic routing.
func (r *fakeRole) Calls() int32 { return atomic.LoadInt32(&r.calls) }

func (r *fakeRole) Call(_ context.Context, _ string) (string, error) {
	atomic.AddInt32(&r.calls, 1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.nextIdx < len(r.replies) {
		rep := r.replies[r.nextIdx]
		r.nextIdx++
		return rep.out, rep.err
	}
	return r.defaultOK.out, r.defaultOK.err
}

// mockClock exposes a settable "now" for injecting into FallbackPair
// under test.
type mockClock struct {
	mu  sync.Mutex
	now time.Time
}

func newMockClock(start time.Time) *mockClock { return &mockClock{now: start} }

func (c *mockClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mockClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newTestPair builds a FallbackPair with mocked clock + mocked
// health-ping so tests don't invoke the real claudeCall/codexCall.
// The healthPing hook just calls role.Call with a fixed prompt,
// which is plenty because fakeRole ignores the prompt.
func newTestPair(role string, primary, secondary ModelRole, clk *mockClock) *FallbackPair {
	fp := NewFallbackPair(role, primary, secondary)
	fp.now = clk.Now
	fp.lastHealthCheck.Store(clk.Now())
	fp.healthPing = func(r ModelRole) (string, error) {
		return r.Call(context.Background(), "ping")
	}
	return fp
}

// TestFallbackPair_HealthyPrimaryNoSwap: a single Call to a healthy
// primary must not swap and must route zero traffic to the secondary.
func TestFallbackPair_HealthyPrimaryNoSwap(t *testing.T) {
	p := newFakeRole("claude", "hello from claude")
	s := newFakeRole("codex", "hello from codex")
	clk := newMockClock(time.Now())
	fp := newTestPair("writer", p, s, clk)

	out, err := fp.Call(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out != "hello from claude" {
		t.Fatalf("unexpected output: %q", out)
	}
	if p.Calls() != 1 {
		t.Fatalf("primary must be called once, got %d", p.Calls())
	}
	if s.Calls() != 0 {
		t.Fatalf("secondary must not be called, got %d", s.Calls())
	}
	if fp.OnSecondary() {
		t.Fatalf("pair must not be on secondary after healthy call")
	}
}

// TestFallbackPair_PrimaryRateLimitsSwapsToSecondary: when the
// primary returns a rate-limit signature, the pair must swap and
// the secondary's output must be returned.
func TestFallbackPair_PrimaryRateLimitsSwapsToSecondary(t *testing.T) {
	p := newFakeRole("claude", "never reached")
	p.queue("claude error: exit status 1", errors.New("exit status 1"))
	s := newFakeRole("codex", "hello from codex")
	clk := newMockClock(time.Now())
	fp := newTestPair("writer", p, s, clk)

	out, err := fp.Call(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out != "hello from codex" {
		t.Fatalf("expected secondary output, got %q", out)
	}
	if p.Calls() != 1 || s.Calls() != 1 {
		t.Fatalf("calls: primary=%d secondary=%d (want 1/1)", p.Calls(), s.Calls())
	}
	if !fp.OnSecondary() {
		t.Fatalf("pair should be on secondary after swap")
	}
}

// TestFallbackPair_BothRateLimit_ReturnsError: when both primary
// and secondary rate-limit, Call must return a non-nil error so
// the caller can decide what to do. Pair stays on secondary (the
// primary is still toxic, so we don't swap back until the health
// check clears it).
func TestFallbackPair_BothRateLimit_ReturnsError(t *testing.T) {
	p := newFakeRole("claude", "")
	p.queue("claude error: exit status 1", errors.New("exit status 1"))
	s := newFakeRole("codex", "")
	s.queue("", errors.New("no last agent message"))
	clk := newMockClock(time.Now())
	fp := newTestPair("writer", p, s, clk)

	_, err := fp.Call(context.Background(), "hi")
	if err == nil {
		t.Fatalf("expected error when both rate-limit, got nil")
	}
	if p.Calls() != 1 || s.Calls() != 1 {
		t.Fatalf("each role must be tried once, got p=%d s=%d", p.Calls(), s.Calls())
	}
	if !fp.OnSecondary() {
		t.Fatalf("pair should be pinned to secondary when primary still toxic")
	}
}

// TestFallbackPair_HealthCheckRestoresPrimary: after a swap to
// secondary, advancing the clock past healthCheckEvery should
// trigger a health ping of the (inactive) primary; if the primary
// answers cleanly the pair must swap back.
func TestFallbackPair_HealthCheckRestoresPrimary(t *testing.T) {
	p := newFakeRole("claude", "primary recovered")
	// First call fails, after that healthy default is returned.
	p.queue("claude error: exit status 1", errors.New("exit status 1"))
	s := newFakeRole("codex", "codex response")
	clk := newMockClock(time.Now())
	fp := newTestPair("writer", p, s, clk)

	// Trigger the swap.
	if _, err := fp.Call(context.Background(), "hi"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !fp.OnSecondary() {
		t.Fatalf("expected swap after primary rate-limit")
	}

	// Advance past healthCheckEvery and issue another Call; the
	// pair should ping the inactive primary, find it healthy, swap
	// back, then serve via the now-active primary.
	clk.Advance(6 * time.Minute)
	out, err := fp.Call(context.Background(), "second")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	// The restored primary is active AFTER the health check. The
	// Call invoked on the active role — which is now the primary —
	// returns the primary's healthy default.
	if out != "primary recovered" {
		t.Fatalf("expected primary output after restore, got %q", out)
	}
	if fp.OnSecondary() {
		t.Fatalf("expected pair restored to primary")
	}
}

// TestFallbackPair_HealthCheckNoSwapWhenPrimaryActive: when we're
// already on the primary, a health check that confirms the secondary
// is healthy must NOT swap away from the primary.
func TestFallbackPair_HealthCheckNoSwapWhenPrimaryActive(t *testing.T) {
	p := newFakeRole("claude", "primary ok")
	s := newFakeRole("codex", "secondary ok")
	clk := newMockClock(time.Now())
	fp := newTestPair("writer", p, s, clk)

	// One normal call — everybody healthy.
	if _, err := fp.Call(context.Background(), "hi"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if fp.OnSecondary() {
		t.Fatalf("must not be on secondary after healthy primary call")
	}

	// Advance clock → second call triggers a health check of the
	// inactive secondary. The secondary responds cleanly; the pair
	// must stay on primary.
	clk.Advance(6 * time.Minute)
	_, err := fp.Call(context.Background(), "again")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if fp.OnSecondary() {
		t.Fatalf("primary was active and healthy; health check must not swap")
	}
}

// TestFallbackPair_ConcurrentCalls: five goroutines hammering Call
// concurrently on a healthy primary must all complete without races
// and the primary must receive every call (no swaps).
func TestFallbackPair_ConcurrentCalls(t *testing.T) {
	p := newFakeRole("claude", "ok")
	s := newFakeRole("codex", "unused")
	clk := newMockClock(time.Now())
	fp := newTestPair("writer", p, s, clk)

	const n = 5
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if _, err := fp.Call(context.Background(), "hi"); err != nil {
					t.Errorf("concurrent call: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if p.Calls() != n*20 {
		t.Fatalf("primary calls=%d want %d", p.Calls(), n*20)
	}
	if s.Calls() != 0 {
		t.Fatalf("secondary must not be called when primary healthy, got %d", s.Calls())
	}
}

// TestFallbackPair_MockableClockDoesNotSleep: verify that tests
// using the mocked clock complete in well under the
// healthCheckEvery interval. If this test takes more than a
// fraction of a second, the mock clock is leaking into real
// time.Sleep paths.
func TestFallbackPair_MockableClockDoesNotSleep(t *testing.T) {
	p := newFakeRole("claude", "ok")
	s := newFakeRole("codex", "ok")
	clk := newMockClock(time.Now())
	fp := newTestPair("writer", p, s, clk)

	start := time.Now()
	// Advance the mock clock by an hour and issue 10 calls. Should
	// still finish instantly.
	for i := 0; i < 10; i++ {
		clk.Advance(time.Hour)
		if _, err := fp.Call(context.Background(), "hi"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("mocked clock leaked: wall-clock %v > 500ms", elapsed)
	}
}

// TestFallbackPair_CodexRateLimitSignature: the codex-path specific
// rate-limit signatures (no-last-agent-message, empty content) must
// trigger a swap. Reviewer pair semantics.
func TestFallbackPair_CodexRateLimitSignature(t *testing.T) {
	// reviewer pair: codex primary, claude secondary.
	p := newFakeRole("codex", "")
	p.queue("", errors.New("no last agent message after 3 retries"))
	s := newFakeRole("claude", "claude review text")
	clk := newMockClock(time.Now())
	fp := newTestPair("reviewer", p, s, clk)

	out, err := fp.Call(context.Background(), "review this")
	if err != nil {
		t.Fatalf("expected clean recovery on codex signature, got err=%v", err)
	}
	if out != "claude review text" {
		t.Fatalf("expected claude fallback output, got %q", out)
	}
	if !fp.OnSecondary() {
		t.Fatalf("expected pair on secondary after codex rate-limit")
	}
}

// TestFallbackPair_ActiveNameChanges_ReflectsSwap guards the
// observable bit of state operators see in logs.
func TestFallbackPair_ActiveNameChanges_ReflectsSwap(t *testing.T) {
	p := newFakeRole("claude", "")
	p.queue("claude error: exit status 1", errors.New("exit status 1"))
	s := newFakeRole("codex", "codex ok")
	clk := newMockClock(time.Now())
	fp := newTestPair("writer", p, s, clk)

	if fp.ActiveName() != "claude" {
		t.Fatalf("initial ActiveName should be primary, got %s", fp.ActiveName())
	}
	_, _ = fp.Call(context.Background(), "go")
	if fp.ActiveName() != "codex" {
		t.Fatalf("post-swap ActiveName should be secondary, got %s", fp.ActiveName())
	}
}

// TestFallbackPair_IsRateLimit_Unit covers the classifier matrix
// for various (output, err, role) combinations so future tweaks to
// the signature don't silently break swap decisions.
func TestFallbackPair_IsRateLimit_Unit(t *testing.T) {
	fp := NewFallbackPair("writer", ccRole{}, codexRole{})
	cc := ccRole{}
	cx := codexRole{}
	cases := []struct {
		name   string
		role   ModelRole
		out    string
		err    error
		expect bool
	}{
		{"cc clean success", cc, "big response body from cc", nil, false},
		{"cc empty no error", cc, "", nil, true}, // empty + no err → swap signal
		{"cc exit1 short", cc, "claude error: exit status 1", errors.New("exit 1"), true},
		{"cc short error", cc, "oops", errors.New("something broke"), true},
		{"codex no-last-agent", cx, "", errors.New("no last agent message"), true},
		{"codex wrote empty", cx, "", errors.New("wrote empty content"), true},
		{"codex clean success", cx, "review done", nil, false},
	}
	for _, tc := range cases {
		if got := fp.isRateLimit(tc.role, tc.out, tc.err); got != tc.expect {
			t.Errorf("%s: isRateLimit=%v want %v", tc.name, got, tc.expect)
		}
	}
}
