package fanout

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTask is the standard mock used across every test. Fields are tuned
// per-test to shape success/error/panic/cost/duration independently.
type fakeTask struct {
	id      string
	sleep   time.Duration
	err     error
	panicV  any
	cost    float64
	onStart func()
}

func (f *fakeTask) ID() string            { return f.id }
func (f *fakeTask) EstimateCost() float64 { return f.cost }
func (f *fakeTask) Execute(ctx context.Context) (any, error) {
	if f.onStart != nil {
		f.onStart()
	}
	if f.panicV != nil {
		panic(f.panicV)
	}
	if f.sleep > 0 {
		select {
		case <-time.After(f.sleep):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.id + ":ok", nil
}

// TestBasicFanOut covers the spec "Happy" acceptance case: 10 tasks, all
// succeed, every Result.Error is nil, Values are in declaration order.
func TestBasicFanOut(t *testing.T) {
	ctx := context.Background()
	tasks := make([]*fakeTask, 10)
	for i := range tasks {
		tasks[i] = &fakeTask{id: fmt.Sprintf("t%d", i)}
	}
	results := FanOut[*fakeTask](ctx, tasks, FanOutConfig{MaxParallel: 4})
	if len(results) != 10 {
		t.Fatalf("want 10 results, got %d", len(results))
	}
	for i, r := range results {
		if r.Error != nil {
			t.Errorf("task %d: unexpected error: %v", i, r.Error)
		}
		if r.Cancelled {
			t.Errorf("task %d: unexpected cancellation", i)
		}
		if got, want := r.Value, fmt.Sprintf("t%d:ok", i); got != want {
			t.Errorf("task %d: Value=%v want %v", i, got, want)
		}
		if r.Task.ID() != fmt.Sprintf("t%d", i) {
			t.Errorf("task %d: declaration order broken, got %s", i, r.Task.ID())
		}
	}
}

// TestFanOutEmpty: empty task slice returns nil with no events.
func TestFanOutEmpty(t *testing.T) {
	results := FanOut[*fakeTask](context.Background(), nil, FanOutConfig{})
	if results != nil {
		t.Fatalf("want nil results, got %v", results)
	}
}

// TestFanOutConcurrencyCap: with MaxParallel=3, at most 3 tasks are in-flight
// simultaneously (verified via atomic peak counter inside OnChildStart).
func TestFanOutConcurrencyCap(t *testing.T) {
	const N = 10
	const P = 3
	var inflight, peak int64
	var released [N]chan struct{}
	for i := range released {
		released[i] = make(chan struct{})
	}
	tasks := make([]*fakeTask, N)
	for i := range tasks {
		i := i
		tasks[i] = &fakeTask{
			id: fmt.Sprintf("t%d", i),
			onStart: func() {
				cur := atomic.AddInt64(&inflight, 1)
				for {
					p := atomic.LoadInt64(&peak)
					if cur <= p || atomic.CompareAndSwapInt64(&peak, p, cur) {
						break
					}
				}
				<-released[i]
				atomic.AddInt64(&inflight, -1)
			},
		}
	}
	done := make(chan []Result, 1)
	go func() {
		done <- FanOut[*fakeTask](context.Background(), tasks, FanOutConfig{MaxParallel: P})
	}()
	// Release tasks one by one; peak concurrency should never exceed P.
	for i := 0; i < N; i++ {
		// Let the dispatcher reach steady state before releasing.
		time.Sleep(5 * time.Millisecond)
		close(released[i])
	}
	<-done
	if atomic.LoadInt64(&peak) > int64(P) {
		t.Errorf("peak concurrency %d exceeded MaxParallel=%d", peak, P)
	}
	if atomic.LoadInt64(&peak) == 0 {
		t.Errorf("no task ever ran (peak=0)")
	}
}

// TestFailFastCancelsSiblings: 5 tasks, task #1 errors fast, FailFast=true.
// Later tasks observe ctx cancellation.
func TestFailFastCancelsSiblings(t *testing.T) {
	tasks := []*fakeTask{
		{id: "t0", sleep: 50 * time.Millisecond},
		{id: "t1", err: errors.New("boom")},
		{id: "t2", sleep: 1 * time.Second}, // would sleep long; should be cancelled
		{id: "t3", sleep: 1 * time.Second},
		{id: "t4", sleep: 1 * time.Second},
	}
	start := time.Now()
	results := FanOut[*fakeTask](context.Background(), tasks, FanOutConfig{
		MaxParallel: 5,
		FailFast:    true,
	})
	elapsed := time.Since(start)
	if elapsed > 900*time.Millisecond {
		t.Errorf("fail-fast did not short-circuit: elapsed=%v", elapsed)
	}
	if results[1].Error == nil {
		t.Errorf("task 1 should have errored")
	}
	// At least one of t2-t4 should record cancellation.
	cancelled := 0
	for i := 2; i < 5; i++ {
		if results[i].Cancelled || (results[i].Error != nil && errors.Is(results[i].Error, context.Canceled)) {
			cancelled++
		}
	}
	if cancelled == 0 {
		t.Errorf("expected at least one sibling cancelled, got none; results=%+v", results)
	}
}

// TestFailFastOff: same scenario, FailFast=false → siblings complete normally.
func TestFailFastOff(t *testing.T) {
	tasks := []*fakeTask{
		{id: "t0", sleep: 10 * time.Millisecond},
		{id: "t1", err: errors.New("boom")},
		{id: "t2", sleep: 20 * time.Millisecond},
		{id: "t3", sleep: 20 * time.Millisecond},
		{id: "t4", sleep: 20 * time.Millisecond},
	}
	results := FanOut[*fakeTask](context.Background(), tasks, FanOutConfig{
		MaxParallel: 5,
		FailFast:    false,
	})
	if results[1].Error == nil {
		t.Fatalf("task 1 should have errored")
	}
	for _, i := range []int{0, 2, 3, 4} {
		if results[i].Error != nil {
			t.Errorf("task %d: unexpected error when FailFast=false: %v", i, results[i].Error)
		}
		if results[i].Cancelled {
			t.Errorf("task %d: unexpected cancellation when FailFast=false", i)
		}
	}
}

// TestBudgetPreflight: sum of estimates exceeds limit → every Result carries
// ErrBudgetPreflight and no tasks execute.
func TestBudgetPreflight(t *testing.T) {
	var ran int64
	mk := func(id string, cost float64) *fakeTask {
		return &fakeTask{
			id:   id,
			cost: cost,
			onStart: func() {
				atomic.AddInt64(&ran, 1)
			},
		}
	}
	tasks := []*fakeTask{
		mk("t0", 2.0), mk("t1", 2.0), mk("t2", 2.0),
	}
	results := FanOut[*fakeTask](context.Background(), tasks, FanOutConfig{
		MaxParallel: 3,
		BudgetLimit: 1.0, // sum=6 > 1
	})
	if atomic.LoadInt64(&ran) != 0 {
		t.Errorf("preflight should have prevented execution, ran=%d", ran)
	}
	for i, r := range results {
		if !errors.Is(r.Error, ErrBudgetPreflight) {
			t.Errorf("task %d: want ErrBudgetPreflight, got %v", i, r.Error)
		}
	}
}

// TestBudgetPreflightSkippedWhenZeroEstimate: any zero estimate disables
// preflight; runtime path takes over.
func TestBudgetPreflightSkippedWhenZeroEstimate(t *testing.T) {
	tasks := []*fakeTask{
		{id: "t0", cost: 0},  // zero -> skip preflight
		{id: "t1", cost: 10}, // huge, but not counted
	}
	results := FanOut[*fakeTask](context.Background(), tasks, FanOutConfig{
		MaxParallel: 2,
		BudgetLimit: 0.01,
	})
	// At least one result should NOT be ErrBudgetPreflight (proves preflight skipped).
	sawNonPreflight := false
	for _, r := range results {
		if !errors.Is(r.Error, ErrBudgetPreflight) {
			sawNonPreflight = true
		}
	}
	if !sawNonPreflight {
		t.Errorf("expected preflight skipped when a task has zero estimate")
	}
}

// TestTrustClamping: parent ctx trust=4; cfg.TrustCeiling=2 → each child sees 2.
func TestTrustClamping(t *testing.T) {
	var seen []int
	var mu sync.Mutex
	tasks := []*captureTrustTask{
		{fakeTask: fakeTask{id: "a"}, seen: &seen, mu: &mu},
		{fakeTask: fakeTask{id: "b"}, seen: &seen, mu: &mu},
		{fakeTask: fakeTask{id: "c"}, seen: &seen, mu: &mu},
	}
	ctx := WithTrustCeiling(context.Background(), 4)
	_ = FanOut[*captureTrustTask](ctx, tasks, FanOutConfig{
		MaxParallel:  3,
		TrustCeiling: 2,
	})
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 {
		t.Fatalf("want 3 observed trust values, got %d", len(seen))
	}
	for _, v := range seen {
		if v != 2 {
			t.Errorf("child observed trust=%d, want 2 (clamped)", v)
		}
	}
}

// TestTrustInheritance: parent ctx trust=1; cfg.TrustCeiling=3 → child sees 1
// (min wins; the higher cfg ceiling cannot raise the parent-provided limit).
func TestTrustInheritance(t *testing.T) {
	var seen []int
	var mu sync.Mutex
	tasks := []*captureTrustTask{
		{fakeTask: fakeTask{id: "a"}, seen: &seen, mu: &mu},
	}
	ctx := WithTrustCeiling(context.Background(), 1)
	_ = FanOut[*captureTrustTask](ctx, tasks, FanOutConfig{
		MaxParallel:  1,
		TrustCeiling: 3,
	})
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != 1 {
		t.Errorf("want seen=[1], got %v", seen)
	}
}

// captureTrustTask records ctx trust in a slice for assertion.
type captureTrustTask struct {
	fakeTask
	seen *[]int
	mu   *sync.Mutex
}

func (c *captureTrustTask) Execute(ctx context.Context) (any, error) {
	c.mu.Lock()
	*c.seen = append(*c.seen, TrustCeiling(ctx))
	c.mu.Unlock()
	return "ok", nil
}

// TestContextCancellation: cancel parent mid-run; remaining tasks report
// cancellation.
func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var started atomic.Int64
	tasks := make([]*fakeTask, 6)
	for i := range tasks {
		tasks[i] = &fakeTask{
			id:    fmt.Sprintf("t%d", i),
			sleep: 200 * time.Millisecond,
			onStart: func() {
				if started.Add(1) == 2 {
					// Cancel after 2 tasks started.
					cancel()
				}
			},
		}
	}
	results := FanOut[*fakeTask](ctx, tasks, FanOutConfig{MaxParallel: 2})
	// At least one task should be flagged cancelled or have a cancel error.
	cancelled := 0
	for _, r := range results {
		if r.Cancelled || (r.Error != nil && errors.Is(r.Error, context.Canceled)) {
			cancelled++
		}
	}
	if cancelled == 0 {
		t.Errorf("expected at least one cancelled result")
	}
}

// TestPerChildTimeout: one task sleeps longer than TimeoutPerChild → that
// task errors with DeadlineExceeded; siblings unaffected.
func TestPerChildTimeout(t *testing.T) {
	tasks := []*fakeTask{
		{id: "quick", sleep: 10 * time.Millisecond},
		{id: "slow", sleep: 500 * time.Millisecond},
	}
	results := FanOut[*fakeTask](context.Background(), tasks, FanOutConfig{
		MaxParallel:     2,
		TimeoutPerChild: 50 * time.Millisecond,
	})
	if results[0].Error != nil {
		t.Errorf("quick task errored unexpectedly: %v", results[0].Error)
	}
	if results[1].Error == nil || !errors.Is(results[1].Error, context.DeadlineExceeded) {
		t.Errorf("slow task should have timed out with DeadlineExceeded, got %v", results[1].Error)
	}
}

// TestPanicRecovery: a panicking task produces ErrTaskPanic on its Result;
// siblings still complete normally.
func TestPanicRecovery(t *testing.T) {
	tasks := []*fakeTask{
		{id: "t0"},
		{id: "t1", panicV: "kaboom"},
		{id: "t2"},
	}
	results := FanOut[*fakeTask](context.Background(), tasks, FanOutConfig{MaxParallel: 3})
	if results[0].Error != nil || results[2].Error != nil {
		t.Errorf("siblings should be unaffected, got: %v / %v", results[0].Error, results[2].Error)
	}
	if !errors.Is(results[1].Error, ErrTaskPanic) {
		t.Errorf("task 1 should wrap ErrTaskPanic, got %v", results[1].Error)
	}
}

// TestMaxParallelNegativeClamped: MaxParallel=-1 must not wedge; should
// execute serially.
func TestMaxParallelNegativeClamped(t *testing.T) {
	tasks := []*fakeTask{
		{id: "t0"},
		{id: "t1"},
	}
	results := FanOut[*fakeTask](context.Background(), tasks, FanOutConfig{MaxParallel: -1})
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	for i, r := range results {
		if r.Error != nil {
			t.Errorf("task %d: unexpected error: %v", i, r.Error)
		}
	}
}

// TestBudgetTrackerCharge verifies the atomic CAS-loop directly.
func TestBudgetTrackerCharge(t *testing.T) {
	var cancelled atomic.Bool
	bt := newBudgetTracker(1.0, func() { cancelled.Store(true) })
	if !bt.Charge(0.40) {
		t.Fatalf("first charge should succeed")
	}
	if !bt.Charge(0.40) {
		t.Fatalf("second charge should succeed")
	}
	if bt.Charge(0.40) {
		t.Fatalf("third charge should reject (exceeds limit)")
	}
	if !cancelled.Load() {
		t.Errorf("onExceed should have fired")
	}
	if got := bt.Spent(); got < 0.79 || got > 0.81 {
		t.Errorf("Spent()=%v want ~0.80", got)
	}
	if got := bt.Remaining(); got < 0.19 || got > 0.21 {
		t.Errorf("Remaining()=%v want ~0.20", got)
	}
}

// TestBudgetTrackerUnlimited: limit=0 always admits charges.
func TestBudgetTrackerUnlimited(t *testing.T) {
	bt := newBudgetTracker(0, func() { t.Fatalf("onExceed should not fire when unlimited") })
	for i := 0; i < 100; i++ {
		if !bt.Charge(100.0) {
			t.Fatalf("unlimited budget rejected charge %d", i)
		}
	}
}

// TestContextHelpers: RunID and TrustCeiling round-trip cleanly.
func TestContextHelpers(t *testing.T) {
	ctx := context.Background()
	if got := TrustCeiling(ctx); got != defaultTrustCeiling {
		t.Errorf("default TrustCeiling=%d, want %d", got, defaultTrustCeiling)
	}
	if got := RunID(ctx); got != "" {
		t.Errorf("default RunID=%q, want empty", got)
	}
	ctx = WithTrustCeiling(ctx, 2)
	ctx = WithRunID(ctx, "run-xyz")
	if got := TrustCeiling(ctx); got != 2 {
		t.Errorf("TrustCeiling=%d, want 2", got)
	}
	if got := RunID(ctx); got != "run-xyz" {
		t.Errorf("RunID=%q, want run-xyz", got)
	}
}

// TestDeclarationOrderPreserved: random sleep durations don't reorder results.
func TestDeclarationOrderPreserved(t *testing.T) {
	sleeps := []time.Duration{
		30 * time.Millisecond,
		5 * time.Millisecond,
		20 * time.Millisecond,
		1 * time.Millisecond,
		15 * time.Millisecond,
	}
	tasks := make([]*fakeTask, len(sleeps))
	for i, s := range sleeps {
		tasks[i] = &fakeTask{id: fmt.Sprintf("t%d", i), sleep: s}
	}
	results := FanOut[*fakeTask](context.Background(), tasks, FanOutConfig{MaxParallel: 5})
	for i, r := range results {
		if r.Task.ID() != fmt.Sprintf("t%d", i) {
			t.Errorf("declaration order broken at %d: got %s", i, r.Task.ID())
		}
	}
}

// TestOnChildHooks: start and complete hooks fire once per task, in pairs.
func TestOnChildHooks(t *testing.T) {
	var startCt, completeCt atomic.Int64
	tasks := []*fakeTask{
		{id: "t0"}, {id: "t1"}, {id: "t2"},
	}
	_ = FanOut[*fakeTask](context.Background(), tasks, FanOutConfig{
		MaxParallel: 3,
		OnChildStart: func(id string) {
			startCt.Add(1)
		},
		OnChildComplete: func(id string, v any, err error) {
			completeCt.Add(1)
		},
	})
	if startCt.Load() != 3 || completeCt.Load() != 3 {
		t.Errorf("hook counts: start=%d complete=%d (want 3/3)", startCt.Load(), completeCt.Load())
	}
}
