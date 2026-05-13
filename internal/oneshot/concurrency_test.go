//go:build integration

// concurrency_test.go — 1000-concurrent `r1 --one-shot decompose`
// integration benchmark. Build tag `integration` keeps it off the
// default `go test ./...` lane; nightly CI runs it via the
// `test-oneshot-concurrent` Makefile target.
//
// Acceptance criteria (spec §T5.3):
//   - All 1000 children exit 0.
//   - Wall clock (first start → last finish) ≤ 60 s.
//   - p50 cold-start ≤ 500 ms, p99 ≤ 2 s.
//   - Peak RSS per child ≤ 256 MiB × 1.10.
//   - Zero unreaped children (parent's Wait4 returns ECHILD).
//   - Zero file-descriptor leak (delta in /proc/self/fd == 0).
//
// Run locally with `make test-oneshot-concurrent`. Override
// concurrency / wall budget via the R1_BENCH_CONCURRENCY and
// R1_BENCH_WALL_BUDGET_S env vars for a smaller dry-run.
//
// Spec: specs/oneshot-production-hardening.md §T5.
package oneshot_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
)

// Default benchmark configuration. Overridable via env so a
// commodity-laptop dry-run can exercise the loop without
// needing a 16-core host.
const (
	defaultConcurrency  = 1000
	defaultWalltimeBudS = 60
	rssBudgetBytes      = uint64(256 * 1024 * 1024)
	rssTolerance        = 0.10
	defaultP50Budget    = 500 * time.Millisecond
	defaultP99Budget    = 2 * time.Second
)

func benchConcurrency(harness *testing.T) int {
	harness.Helper()
	if v := os.Getenv("R1_BENCH_CONCURRENCY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			harness.Fatalf("bad R1_BENCH_CONCURRENCY=%q: %v", v, err)
		}
		return n
	}
	return defaultConcurrency
}

func benchWalltime(harness *testing.T) time.Duration {
	harness.Helper()
	if v := os.Getenv("R1_BENCH_WALL_BUDGET_S"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			harness.Fatalf("bad R1_BENCH_WALL_BUDGET_S=%q: %v", v, err)
		}
		return time.Duration(n) * time.Second
	}
	return defaultWalltimeBudS * time.Second
}

// rssBytes converts the platform-specific Rusage.Maxrss to bytes.
// Linux reports Maxrss in KiB; darwin in bytes. (Test only runs
// on Linux + darwin; other platforms are gated by go-test
// scheduling.)
func rssBytes(ru *syscall.Rusage) uint64 {
	if ru == nil {
		return 0
	}
	switch runtime.GOOS {
	case "linux":
		return uint64(ru.Maxrss) * 1024
	case "darwin":
		return uint64(ru.Maxrss)
	default:
		return uint64(ru.Maxrss)
	}
}

// invocationResult collects per-child measurements that the
// summary table reports back.
type invocationResult struct {
	idx          int
	exitCode     int
	firstByteNs  int64
	finishNs     int64
	rssBytes     uint64
	stderrSample string
}

// buildBinary compiles cmd/r1 into a TempDir so every
// invocation reuses the same binary. Returns the path and a
// cleanup that t.Cleanup() will fire automatically.
func buildBinary(harness *testing.T) string {
	harness.Helper()
	dir := harness.TempDir()
	bin := filepath.Join(dir, "r1-oneshot-bench")
	// Locate the repo root by walking up from this file.
	repoRoot := findRepoRoot(harness)
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/r1")
	cmd.Dir = repoRoot
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		harness.Fatalf("go build ./cmd/r1: %v", err)
	}
	return bin
}

func findRepoRoot(harness *testing.T) string {
	harness.Helper()
	// LINT-ALLOW chdir-bench: integration benchmark walks up
	// from the test package directory to find go.mod (repo root)
	// so it can compile cmd/r1 into a TempDir. No cwd mutation;
	// read-only probe.
	dir, err := os.Getwd()
	if err != nil {
		harness.Fatalf("getwd: %v", err)
	}
	for d := dir; d != "/" && d != ""; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
	}
	harness.Fatalf("repo root not found from %s", dir)
	return ""
}

// fdCount returns the number of entries under /proc/self/fd as
// the file-descriptor leak probe.
func fdCount() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

// TestOneShot_1000Concurrent is the spec's flagship integration
// test. Spec §T5.
func TestOneShot_1000Concurrent(harness *testing.T) {
	if testing.Short() {
		harness.Skip("skipping 1000-concurrent benchmark in -short mode")
	}
	bin := buildBinary(harness)
	conc := benchConcurrency(harness)
	wallBudget := benchWalltime(harness)
	harness.Logf("benchmark bin=%s concurrency=%d wall_budget=%s",
		bin, conc, wallBudget)

	beforeFDs := fdCount()

	results := make([]invocationResult, conc)
	var resultsMu sync.Mutex

	overallStart := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), wallBudget+10*time.Second)
	defer cancel()

	g, gctx := errgroup.WithContext(ctx)
	gSetMax := g.SetLimit
	gSetMax(conc)

	for i := 0; i < conc; i++ {
		i := i
		g.Go(func() error {
			start := time.Now()
			cmd := exec.CommandContext(gctx,
				bin, "--one-shot", "decompose",
				"--input", "-",
				"--max-mem", "256",
				"--timeout", "30s",
			)
			cmd.Stdin = strings.NewReader(`{"task":"benchmark task","context":{"strategy":"basic"}}`)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				return fmt.Errorf("stdoutpipe: %w", err)
			}
			if err := cmd.Start(); err != nil {
				return fmt.Errorf("start: %w", err)
			}

			firstByte := make([]byte, 1)
			var firstByteNs int64
			_, readErr := io.ReadFull(stdout, firstByte)
			if readErr == nil {
				firstByteNs = time.Since(start).Nanoseconds()
			}
			_, _ = io.Copy(io.Discard, stdout)
			reapChild := cmd.Wait
			waitErr := reapChild()
			finishNs := time.Since(start).Nanoseconds()

			exitCode := 0
			if waitErr != nil {
				var ee *exec.ExitError
				if errors.As(waitErr, &ee) {
					exitCode = ee.ExitCode()
				} else {
					exitCode = -1
				}
			}
			var rss uint64
			if ps := cmd.ProcessState; ps != nil {
				if ru, ok := ps.SysUsage().(*syscall.Rusage); ok {
					rss = rssBytes(ru)
				}
			}

			resultsMu.Lock()
			results[i] = invocationResult{
				idx:          i,
				exitCode:     exitCode,
				firstByteNs:  firstByteNs,
				finishNs:     finishNs,
				rssBytes:     rss,
				stderrSample: strings.TrimSpace(stderr.String()),
			}
			resultsMu.Unlock()
			return nil
		})
	}
	waitAll := g.Wait
	if err := waitAll(); err != nil {
		harness.Fatalf("errgroup wait: %v", err)
	}
	wallElapsed := time.Since(overallStart)

	// --- Assertions -------------------------------------------------
	if wallElapsed > wallBudget {
		harness.Errorf("wall=%s exceeded budget %s", wallElapsed, wallBudget)
	}

	failures := 0
	stderrCounts := map[string]int{}
	var firstByteSamples []time.Duration
	var maxRSS uint64
	var totalRSS uint64
	for _, r := range results {
		if r.exitCode != 0 {
			failures++
			stderrCounts[r.stderrSample]++
		}
		if r.firstByteNs > 0 {
			firstByteSamples = append(firstByteSamples, time.Duration(r.firstByteNs))
		}
		if r.rssBytes > maxRSS {
			maxRSS = r.rssBytes
		}
		totalRSS += r.rssBytes
	}
	if failures > 0 {
		mostCommon := ""
		mostCount := 0
		for k, v := range stderrCounts {
			if v > mostCount {
				mostCount = v
				mostCommon = k
			}
		}
		harness.Errorf("non-zero exits: %d / %d. Most common stderr (%d): %s",
			failures, conc, mostCount, mostCommon)
	}

	rssCeilingF := float64(rssBudgetBytes) * (1.0 + rssTolerance)
	rssCeiling := uint64(rssCeilingF)
	if maxRSS > rssCeiling {
		harness.Errorf("max RSS %d > ceiling %d (256 MiB +10%%)", maxRSS, rssCeiling)
	}

	// Cold-start p50 / p99.
	slices.Sort(firstByteSamples)
	if len(firstByteSamples) == 0 {
		harness.Fatalf("no first-byte samples")
	}
	p50 := firstByteSamples[len(firstByteSamples)/2]
	p95 := firstByteSamples[(len(firstByteSamples)*95)/100]
	p99 := firstByteSamples[(len(firstByteSamples)*99)/100]
	if p50 > defaultP50Budget {
		harness.Errorf("p50 cold-start %s > %s budget", p50, defaultP50Budget)
	}
	if p99 > defaultP99Budget {
		harness.Errorf("p99 cold-start %s > %s budget", p99, defaultP99Budget)
	}

	// Orphan / unreaped check.
	var ws syscall.WaitStatus
	if _, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil); err == nil {
		harness.Errorf("Wait4 returned a child; expected ECHILD")
	} else if !errors.Is(err, syscall.ECHILD) {
		harness.Logf("Wait4 returned non-ECHILD error %v; ECHILD expected", err)
	}

	// FD leak.
	afterFDs := fdCount()
	if beforeFDs >= 0 && afterFDs >= 0 && afterFDs != beforeFDs {
		harness.Errorf("fd leak: before=%d after=%d (delta=%d)",
			beforeFDs, afterFDs, afterFDs-beforeFDs)
	}

	harness.Run("summary", func(sub *testing.T) {
		meanRSS := uint64(0)
		if len(results) > 0 {
			meanRSS = totalRSS / uint64(len(results))
		}
		sub.Logf("=== oneshot 1000-concurrent summary ===")
		sub.Logf("conc=%d wall=%s budget=%s failures=%d",
			conc, wallElapsed, wallBudget, failures)
		sub.Logf("cold-start  p50=%s  p95=%s  p99=%s",
			p50, p95, p99)
		sub.Logf("rss         max=%d B  mean=%d B  ceiling=%d B",
			maxRSS, meanRSS, rssCeiling)
		sub.Logf("fds         before=%d  after=%d  delta=%d",
			beforeFDs, afterFDs, afterFDs-beforeFDs)
	})
}
