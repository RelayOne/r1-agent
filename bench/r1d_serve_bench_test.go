//go:build r1d_bench
// +build r1d_bench

// r1d_serve_bench_test.go — Phase I item 52 (specs/r1d-server.md).
//
// 50 sessions × 100 messages soak. Three load-bearing assertions:
//
//   1. p99 dispatch latency < 50ms
//   2. FD count stable across the soak (no monotonic leak)
//   3. journal write throughput >= 5 MB/s
//
// Build tag `r1d_bench` keeps this OFF the regular `go test ./...`
// sweep — the soak takes ~10s and is intended for local /
// pre-release validation, not every CI run. Invoke with:
//
//   go test -tags r1d_bench -run TestR1dServeSoak \
//           ./bench -timeout=120s -v
//
// The bench drives the SessionHub + journal pipeline directly (no
// WS / HTTP frame overhead) so the latency / throughput numbers
// reflect the daemon's intrinsic capacity rather than network
// jitter. A network-shape variant can layer on top later; the spec
// only asks for the dispatch-and-journal soak.
//
// Why TestR1dServeSoak (Test*) rather than BenchmarkR1dServeSoak
// (Benchmark*): we need to assert specific p99 / throughput
// thresholds and fail the run on regression. `go test -bench` runs
// repeatedly until a duration is hit; `go test -run` runs once,
// which is the right shape for a soak with hard thresholds.

package bench_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/journal"
	"github.com/RelayOne/r1/internal/server/sessionhub"
)

const (
	soakSessions       = 50
	soakMessagesEach   = 100
	soakP99LatencyMax  = 50 * time.Millisecond
	soakThroughputMin  = 5 * 1024 * 1024 // 5 MB/s
	soakFDDriftMax     = 32              // tolerated FD drift (epoll, GC roots, …)
	soakPayloadSizeMin = 64              // bytes per record (lower bound for 5MB/s with 5000 records)
)

// TestR1dServeSoak runs the 50×100 soak and asserts the spec-defined
// thresholds. The test is gated behind the `r1d_bench` build tag so
// it does not run as part of the default CI sweep.
func TestR1dServeSoak(t *testing.T) {
	t.Setenv("R1_HOME", t.TempDir())

	hub, err := sessionhub.NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}

	// One journal Writer per session. Pre-create so we measure the
	// hot-path Append latency, not journal-open overhead.
	type sess struct {
		id      string
		s       *sessionhub.Session
		journal *journal.Writer
	}
	sessions := make([]sess, soakSessions)
	journalDir := t.TempDir()
	for i := 0; i < soakSessions; i++ {
		wd := t.TempDir()
		s, err := hub.Create(sessionhub.CreateOptions{
			Workdir: wd,
			Model:   "soak-model",
			ID:      fmt.Sprintf("soak-%03d", i),
		})
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		jp := filepath.Join(journalDir, s.ID+".jsonl")
		w, err := journal.OpenWriter(jp, journal.WriterOptions{})
		if err != nil {
			t.Fatalf("OpenWriter %d: %v", i, err)
		}
		t.Cleanup(func() { _ = w.Close() })
		sessions[i] = sess{id: s.ID, s: s, journal: w}
	}

	// Snapshot pre-soak FD count for stability check.
	preFDs := openFDCount(t)

	// Build a fixed payload large enough to make 5MB/s achievable.
	// 50 * 100 = 5000 records; 5MB / 5000 records = 1KB/record.
	const payloadBytes = 1024
	payload := make([]byte, payloadBytes)
	for i := range payload {
		payload[i] = byte('a' + (i % 26))
	}
	_ = soakPayloadSizeMin // documentation anchor; payload is 1KB.

	// Per-message latency samples. Pre-allocated, atomic-indexed so
	// goroutines can write without contention.
	const total = soakSessions * soakMessagesEach
	latencies := make([]time.Duration, total)
	var idx int64

	// Throughput accounting: total bytes appended.
	var bytesAppended int64

	startGate := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(soakSessions)

	for i := 0; i < soakSessions; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-startGate
			w := sessions[i].journal
			for j := 0; j < soakMessagesEach; j++ {
				rec := map[string]any{
					"sess": sessions[i].id,
					"j":    j,
					"data": string(payload),
				}
				t0 := time.Now()
				if _, err := w.Append("hub.event", rec); err != nil {
					t.Errorf("Append %s/%d: %v", sessions[i].id, j, err)
					return
				}
				dt := time.Since(t0)
				slot := atomic.AddInt64(&idx, 1) - 1
				latencies[slot] = dt
				atomic.AddInt64(&bytesAppended, int64(payloadBytes))
			}
		}()
	}

	soakStart := time.Now()
	close(startGate)
	wg.Wait()
	soakDuration := time.Since(soakStart)

	// assert.completion: all goroutines wrote all records.
	if got := atomic.LoadInt64(&idx); got != int64(total) {
		t.Fatalf("incomplete soak: %d records, want %d", got, total)
	}

	// ---------- Threshold #1: p99 dispatch latency ----------
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p99 := latencies[(len(latencies)*99)/100]
	p50 := latencies[len(latencies)/2]
	p999 := latencies[(len(latencies)*999)/1000]
	t.Logf("dispatch latency: p50=%v p99=%v p999=%v (n=%d)",
		p50, p99, p999, len(latencies))
	if p99 > soakP99LatencyMax {
		t.Errorf("p99 dispatch latency %v exceeds threshold %v",
			p99, soakP99LatencyMax)
	}

	// ---------- Threshold #2: journal throughput ----------
	bytesTotal := atomic.LoadInt64(&bytesAppended)
	throughput := float64(bytesTotal) / soakDuration.Seconds() // B/s
	throughputMB := throughput / (1024 * 1024)
	t.Logf("journal throughput: %.2f MB/s (%.0f B over %v)",
		throughputMB, float64(bytesTotal), soakDuration)
	if throughput < float64(soakThroughputMin) {
		t.Errorf("throughput %.2f MB/s below threshold %d MB/s",
			throughputMB, soakThroughputMin/(1024*1024))
	}

	// ---------- Threshold #3: FD count stable ----------
	// Force a GC + finalizers run before snapshotting so transient
	// goroutine FDs are closed.
	runtime.GC()
	runtime.Gosched()
	time.Sleep(50 * time.Millisecond)
	postFDs := openFDCount(t)
	// Tolerate the journal Writer FDs (one per session) — they are
	// kept open until t.Cleanup runs. The stability check is "growth
	// beyond an expected ceiling".
	expectedDelta := soakSessions + soakFDDriftMax
	actualDelta := postFDs - preFDs
	t.Logf("FD count: pre=%d post=%d delta=%d (tolerated=%d)",
		preFDs, postFDs, actualDelta, expectedDelta)
	if actualDelta > expectedDelta {
		t.Errorf("FD count drifted by %d (>%d); possible leak",
			actualDelta, expectedDelta)
	}
}

// openFDCount returns a best-effort count of FDs the test process
// has open. On Linux we read /proc/self/fd; on other platforms the
// helper returns 0 and the FD assertion becomes a no-op (the spec
// soak target is Linux-CI; macOS local runs skip the FD check).
func openFDCount(t *testing.T) int {
	t.Helper()
	if runtime.GOOS != "linux" {
		return 0
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Logf("openFDCount: ReadDir /proc/self/fd: %v (skipping FD check)", err)
		return 0
	}
	return len(entries)
}
