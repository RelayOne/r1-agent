package topology

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func makeTasks(n int) []Task {
	out := make([]Task, n)
	for i := range out {
		out[i] = Task{ID: string(rune('a' + i)), Payload: i}
	}
	return out
}

// countingRunner returns a Runner that records how many tasks
// ran and in what order.
type runOrder struct {
	mu    sync.Mutex
	order []string
}

func (r *runOrder) add(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.order = append(r.order, id)
}

func TestSequential_PreservesOrder(t *testing.T) {
	var ord runOrder
	run := func(_ context.Context, task Task) TaskResult {
		ord.add(task.ID)
		return TaskResult{TaskID: task.ID, Duration: time.Millisecond}
	}
	results := Sequential{}.Run(context.Background(), makeTasks(4), run)
	if len(results) != 4 {
		t.Errorf("results len=%d want 4", len(results))
	}
	// Order is deterministic in sequential.
	for i, id := range ord.order {
		if id != string(rune('a'+i)) {
			t.Errorf("order[%d]=%q want %q", i, id, string(rune('a'+i)))
		}
	}
}

func TestSequential_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var ran int32
	run := func(ctx context.Context, task Task) TaskResult {
		atomic.AddInt32(&ran, 1)
		if task.ID == "b" {
			cancel()
		}
		return TaskResult{TaskID: task.ID}
	}
	_ = Sequential{}.Run(ctx, makeTasks(5), run)
	got := atomic.LoadInt32(&ran)
	if got > 2 {
		t.Errorf("ran %d tasks after cancel, want at most 2", got)
	}
}

func TestConcurrentFanOut_AllRun(t *testing.T) {
	var counter int32
	run := func(_ context.Context, task Task) TaskResult {
		atomic.AddInt32(&counter, 1)
		return TaskResult{TaskID: task.ID}
	}
	results := ConcurrentFanOut{}.Run(context.Background(), makeTasks(10), run)
	if len(results) != 10 {
		t.Errorf("results=%d want 10", len(results))
	}
	if atomic.LoadInt32(&counter) != 10 {
		t.Errorf("counter=%d want 10", counter)
	}
}

func TestConcurrentFanOut_ActuallyConcurrent(t *testing.T) {
	// If tasks ran sequentially, 5 × 50ms each would take 250ms.
	// Concurrent execution should finish well under that.
	run := func(_ context.Context, task Task) TaskResult {
		time.Sleep(50 * time.Millisecond)
		return TaskResult{TaskID: task.ID, Duration: 50 * time.Millisecond}
	}
	start := time.Now()
	_ = ConcurrentFanOut{}.Run(context.Background(), makeTasks(5), run)
	elapsed := time.Since(start)
	if elapsed > 200*time.Millisecond {
		t.Errorf("concurrent run took %v — should be well under 250ms (sequential cost)", elapsed)
	}
}

func TestSupervisorWorker_SupervisorFirst(t *testing.T) {
	var ord runOrder
	run := func(_ context.Context, task Task) TaskResult {
		ord.add(task.ID)
		return TaskResult{TaskID: task.ID}
	}
	results := SupervisorWorker{}.Run(context.Background(), makeTasks(4), run)
	if len(results) != 4 {
		t.Errorf("results=%d want 4", len(results))
	}
	ord.mu.Lock()
	defer ord.mu.Unlock()
	if len(ord.order) < 1 || ord.order[0] != "a" {
		t.Errorf("supervisor should run first; order=%v", ord.order)
	}
}

func TestSupervisorWorker_SupervisorFailureStops(t *testing.T) {
	supErr := errors.New("supervisor failed")
	run := func(_ context.Context, task Task) TaskResult {
		if task.ID == "a" {
			return TaskResult{TaskID: task.ID, Err: supErr}
		}
		return TaskResult{TaskID: task.ID}
	}
	results := SupervisorWorker{}.Run(context.Background(), makeTasks(4), run)
	if len(results) != 1 {
		t.Errorf("supervisor failure should halt worker dispatch; got %d results", len(results))
	}
}

func TestRegistry_BuiltInsRegistered(t *testing.T) {
	r := NewRegistry()
	for _, n := range []Name{NameSequential, NameConcurrentFanOut, NameSupervisorWorker} {
		if _, err := r.Get(n); err != nil {
			t.Errorf("builtin %q not registered: %v", n, err)
		}
	}
}

func TestRegistry_UnknownErrors(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Get("made-up"); !errors.Is(err, ErrUnknownTopology) {
		t.Errorf("want ErrUnknownTopology, got %v", err)
	}
}

func TestRegistry_SelectFallsBackToSequential(t *testing.T) {
	r := NewRegistry()
	// No metrics recorded for "fresh-class"; Select should
	// return Sequential as the safe default.
	got := r.Select("fresh-class")
	if got.Name() != NameSequential {
		t.Errorf("Select default = %q want %q", got.Name(), NameSequential)
	}
}

func TestRegistry_RecordAndSelect(t *testing.T) {
	r := NewRegistry()
	// Record 10 successful fast fan-out runs + 10 slow supervisor runs
	// for task class "unit-test".
	for i := 0; i < 10; i++ {
		r.Record(NameConcurrentFanOut, "unit-test", []TaskResult{{Duration: 10 * time.Millisecond}})
		r.Record(NameSupervisorWorker, "unit-test", []TaskResult{{Duration: 100 * time.Millisecond}})
	}
	chosen := r.Select("unit-test")
	if chosen.Name() != NameConcurrentFanOut {
		t.Errorf("Select should prefer fan-out (faster avg latency); got %q", chosen.Name())
	}
}

func TestRegistry_RecordFailureLowersSuccessRate(t *testing.T) {
	r := NewRegistry()
	errResult := []TaskResult{{Err: errors.New("boom")}}
	okResult := []TaskResult{{}}
	// Sequential: 10 successes
	for i := 0; i < 10; i++ {
		r.Record(NameSequential, "c1", okResult)
	}
	// ConcurrentFanOut: 10 failures
	for i := 0; i < 10; i++ {
		r.Record(NameConcurrentFanOut, "c1", errResult)
	}
	chosen := r.Select("c1")
	if chosen.Name() != NameSequential {
		t.Errorf("unreliable topology should be deprioritized; got %q", chosen.Name())
	}
}

func TestRegistry_RunAndRecord(t *testing.T) {
	r := NewRegistry()
	run := func(_ context.Context, task Task) TaskResult {
		return TaskResult{TaskID: task.ID, Tokens: 100, Duration: 5 * time.Millisecond}
	}
	results, name, err := r.RunAndRecord(context.Background(), NameSequential, "c1", makeTasks(3), run)
	if err != nil {
		t.Fatalf("RunAndRecord: %v", err)
	}
	if name != NameSequential {
		t.Errorf("got topology %q want %q", name, NameSequential)
	}
	if len(results) != 3 {
		t.Errorf("results=%d want 3", len(results))
	}
	m := r.MetricFor(NameSequential, "c1")
	if m.TotalRuns != 1 {
		t.Errorf("TotalRuns=%d want 1", m.TotalRuns)
	}
	if m.TotalTokens != 300 {
		t.Errorf("TotalTokens=%d want 300", m.TotalTokens)
	}
}

func TestRegistry_RunAndRecord_ImplicitSelection(t *testing.T) {
	r := NewRegistry()
	run := func(_ context.Context, task Task) TaskResult {
		return TaskResult{TaskID: task.ID}
	}
	// Empty explicit -> heuristic -> default Sequential.
	_, name, err := r.RunAndRecord(context.Background(), "", "fresh", makeTasks(2), run)
	if err != nil {
		t.Fatalf("RunAndRecord: %v", err)
	}
	if name != NameSequential {
		t.Errorf("default selection=%q want %q", name, NameSequential)
	}
}

func TestMetric_HelperZeroSafe(t *testing.T) {
	m := Metric{}
	if m.SuccessRate() != 0 {
		t.Errorf("zero-metric SuccessRate=%v want 0", m.SuccessRate())
	}
	if m.AvgTokens() != 0 {
		t.Errorf("zero-metric AvgTokens=%v want 0", m.AvgTokens())
	}
	if m.AvgLatency() != 0 {
		t.Errorf("zero-metric AvgLatency=%v want 0", m.AvgLatency())
	}
}
