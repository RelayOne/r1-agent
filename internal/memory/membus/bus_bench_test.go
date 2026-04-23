// Package membus — bus_bench_test.go
//
// Throughput benchmark for the writer-goroutine + batched-transaction
// pattern. The research-verified target is ≥20k Remember-completions/sec;
// the naive "eight goroutines each calling ExecContext" shape collapses to
// <1k/sec because every writer serializes on the SQLite journal lock.
//
// The benchmark drives N producer goroutines, each calling Remember in a
// tight loop with a unique dedup key (so the UPSERT stays on the fast
// INSERT path and doesn't degenerate to read-modify-write of the same
// row). It measures wall-clock throughput from the first producer start
// to the last producer finish and surfaces that as a custom metric
// `inserts/sec`.
//
// DSN notes:
//
//   - `_txlock=immediate` makes every BeginTx issue BEGIN IMMEDIATE. This
//     is the knob called out in specs/work-stoke.md §TASK 5 — it
//     guarantees the batch transaction takes a reserved lock on open
//     instead of upgrading mid-batch (which would deadlock with a
//     concurrent reader holding the shared lock).
//   - `_journal_mode=WAL` + `_synchronous=NORMAL` are the standard
//     durability-vs-throughput tradeoff SQLite recommends for
//     high-concurrency loads. applyBusPragmas re-asserts these; the DSN
//     param keeps every pooled connection aligned from the first use.
//
// Target machine: commodity SSD. On a modern NVMe the writer goroutine
// plus 256-row batches routinely clears 40k/sec. The bench guards only
// the 20k/sec floor so flakiness on CI spinning rust is bounded.
package membus

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"database/sql"
)

// benchDSN produces the DSN used by the throughput benchmark. Exposed as
// a helper so any additional benches in this file share the same shape.
func benchDSN(dir string) string {
	return "file:" + filepath.Join(dir, "membus.db") +
		"?_journal_mode=WAL" +
		"&_synchronous=NORMAL" +
		"&_busy_timeout=5000" +
		"&_txlock=immediate"
}

// BenchmarkBus_WriterGoroutine_20kPerSec measures sustained Remember
// throughput with the writer goroutine + 256-row batch pattern. It asserts
// the research-verified ≥20k inserts/sec floor and records the observed
// rate via b.ReportMetric so `go test -bench` output carries the number
// into the commit body.
func BenchmarkBus_WriterGoroutine_20kPerSec(b *testing.B) {
	// Fix the workload size. b.N would shrink to 1 on the first probe
	// call and the measured rate would be pure startup cost; we want a
	// steady-state throughput number. 128 producers × 2_000 iterations
	// each gives the writer enough concurrent callers to saturate its
	// non-blocking drain window: every producer blocks on a done
	// channel waiting for its commit, so the only way the writer sees
	// more than ~producer-count rows per batch is to have many more
	// concurrent callers than producer-count / batch-commit-latency.
	// A small producer count (e.g. 8) collapses the batch to
	// producer-count rows and flatlines throughput at
	// producer-count / commit-latency — well below the 20k target
	// even though the writer-goroutine pattern is working as designed.
	const (
		producers = 128
		perProd   = 2_000 // 128 * 2_000 = 256_000 total
	)
	total := producers * perProd

	dir := b.TempDir()
	db, err := sql.Open("sqlite3", benchDSN(dir))
	if err != nil {
		b.Fatalf("open sqlite: %v", err)
	}
	// Size the pool so the writer goroutine and any opportunistic readers
	// don't stall on a single shared connection. The writer uses exactly
	// one connection at a time via BeginTx; leaving headroom prevents
	// the driver from serializing us with pool-shape quirks.
	db.SetMaxOpenConns(runtime.NumCPU() + 2)
	defer db.Close()

	bus, err := NewBus(db, Options{})
	if err != nil {
		b.Fatalf("NewBus: %v", err)
	}
	defer bus.Close()

	// Pre-warm: one synchronous write to force the SQLite WAL file into
	// existence, the driver's prepared-statement cache to warm up, and
	// the writer goroutine into steady-state scheduling. Otherwise the
	// first real batch would pay these one-time costs and depress the
	// measured rate.
	if err := bus.Remember(context.Background(), RememberRequest{
		Scope:   ScopeSession,
		Key:     "warmup",
		Content: "warmup-content",
		Author:  "system",
	}); err != nil {
		b.Fatalf("warmup: %v", err)
	}

	ctx := context.Background()
	var (
		started atomic.Int32
		errs    atomic.Int64
	)
	readyCh := make(chan struct{})
	// doneCh is a channel-based join in place of sync.WaitGroup.
	doneCh := make(chan struct{}, producers)

	for p := 0; p < producers; p++ {
		go func(prodID int) {
			defer func() { doneCh <- struct{}{} }()
			// Park until all producers are scheduled so the timer
			// reflects steady-state contention, not ramp-up fan-out.
			started.Add(1)
			<-readyCh
			for i := 0; i < perProd; i++ {
				// Unique dedup key per (producer, iteration) so every
				// write is an INSERT rather than an UPSERT update —
				// the target of the research claim is INSERT rate.
				req := RememberRequest{
					Scope:   ScopeSession,
					Key:     fmt.Sprintf("p%d-i%d", prodID, i),
					Content: "bench-content",
					Author:  "system",
				}
				if err := bus.Remember(ctx, req); err != nil {
					errs.Add(1)
					return
				}
			}
		}(p)
	}

	// Spin until every producer is parked on readyCh so the timer
	// reflects active work only. Bounded loop with sleep yields to the
	// scheduler instead of busy-looping at 100% CPU.
	for started.Load() < int32(producers) {
		time.Sleep(50 * time.Microsecond)
	}

	b.ResetTimer()
	start := time.Now()
	close(readyCh)
	for i := 0; i < producers; i++ {
		<-doneCh
	}
	elapsed := time.Since(start)
	b.StopTimer()

	if e := errs.Load(); e > 0 {
		b.Fatalf("producer errors: %d", e)
	}

	// Verify every row made it to the database. If the writer dropped
	// anything (e.g. via the close-race path) the test must fail — a
	// silently-lost insert is worse than a slow one.
	var rowCount int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM stoke_memory_bus`).Scan(&rowCount); err != nil {
		b.Fatalf("count rows: %v", err)
	}
	// +1 for the warmup row inserted before the timed section.
	if rowCount != int64(total)+1 {
		b.Fatalf("row count = %d, want %d (total inserts + 1 warmup)", rowCount, total+1)
	}

	insertsPerSec := float64(total) / elapsed.Seconds()
	b.ReportMetric(insertsPerSec, "inserts/sec")
	b.ReportMetric(float64(elapsed.Nanoseconds())/float64(total), "ns/insert")
	b.Logf("producers=%d per=%d total=%d elapsed=%s rate=%.0f inserts/sec",
		producers, perProd, total, elapsed, insertsPerSec)

	// 20k/sec floor. The research note in the Bus doc comment calls this
	// out as the break-even point where the writer-goroutine pattern
	// decisively beats naive concurrent Execs. Fall below and the
	// benchmark has lost its purpose.
	const minRate = 20_000.0
	if insertsPerSec < minRate {
		b.Fatalf("rate = %.0f inserts/sec < target %.0f inserts/sec", insertsPerSec, minRate)
	}
}
