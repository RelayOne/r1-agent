<!-- STATUS: done -->
<!-- CREATED: 2026-05-11 -->
<!-- BUILD_COMPLETED: 2026-05-12 -->
<!-- DEPENDS_ON: -->
<!-- BUILD_ORDER: 35 -->

# `--one-shot` Production Hardening (RelayGate Phase K-3)

## Context

R1 already ships a working `--one-shot` mode. The CLI entry lives in
`cmd/r1/oneshot_cmd.go` (75 LOC), the dispatch switch is in
`cmd/r1/main.go` at line 665 (`case "--one-shot", "one-shot":`), and the
verb logic lives in `internal/oneshot/{oneshot.go,decompose.go,
verify.go,critique.go}` with three verbs: `decompose`, `verify`,
`critique`.

Today the mode is functional but not safe to use as an inline pipeline
stage inside RelayGate's `ContextWorker`. RelayGate wants to spawn R1
as a sub-millisecond inline step. To do that under production load
(target: 1000 concurrent invocations from a single replica) we need
six deltas — bounded memory, bounded time, deterministic shutdown,
remote audit egress, a concurrency proof, and an integration document.

This spec is a delta only. It does NOT introduce new verbs. It does
NOT add persistence. It does NOT couple `--one-shot` to the daemon. It
extends the existing exit-code surface (0/1/2) with two new codes (3,
4) plus the standard signal codes (130, 143).

## Research notes

Research run 2026-05-11 informs the design choices below.

**Go memory bounds in production.** Go 1.19+ exposes
`runtime/debug.SetMemoryLimit` (and the equivalent `GOMEMLIMIT` env
var) as a soft target for the runtime's GC pacer. It does not enforce
a hard cap — the runtime will exceed it when live heap grows past the
target, but it will trade CPU for GC pressure to stay near it. Best
practice is to set `GOMEMLIMIT` to roughly 85–90% of the hard limit
(the 10–15% headroom rule) and combine it with an OS-level hard cap.
On Linux the hard cap is `RLIMIT_AS` (address space ceiling) via
`prlimit(2)` / `setrlimit(2)`, accessible from Go through
`golang.org/x/sys/unix`. Go runtime proposal #75164 tracks making
GOMEMLIMIT cgroup-aware by default; until that lands we must set both
GOMEMLIMIT and RLIMIT_AS explicitly. Reference:
KimMachineGun/automemlimit, Weaviate GOMEMLIMIT post, golang/go#75164.

**Concurrent fork/exec on Linux.** A naive forking server starts to
suffer past ~1500 concurrent processes on commodity 16-core hardware;
the dominant cost is page-table copy proportional to RSS of the
parent. Each forked Go binary inherits its parent's file descriptor
table, so the parent's `ulimit -n` must be high enough to cover all
children's open files. The Linux per-user process cap (`ulimit -u`,
`RLIMIT_NPROC`) and the global `kernel.pid_max` also apply. For our
target of 1000 concurrent `r1 --one-shot` children spawned by one
RelayGate replica, the host needs `nproc >= 4096`, `nofile >= 65535`,
and adequate `kernel.pid_max` (default 4 million is fine). Forking a
process with 1 GiB RSS takes ~22 ms on average, so keeping the R1
binary's startup RSS below 64 MiB is a precondition for hitting the
500 ms p50 cold-start target. Reference: mondalaci/fork-benchmark,
unixism.net forking-servers post.

## Boundaries

- DO NOT add new verbs. `decompose`, `verify`, `critique` remain the
  canonical set. New verbs go in a separate spec.
- DO NOT introduce persistence. `--one-shot` stays stateless. The
  audit POST is the only egress and is fire-and-forget.
- DO NOT depend on `internal/daemon/` or `internal/r1d/`.
  `--one-shot` must boot without a daemon attached.
- DO NOT break existing exit codes: 0 = success, 1 = runtime, 2 =
  usage. We add 3 (memory) and 4 (timeout). Signal exits use the
  conventional 128+signo (130 for SIGINT, 143 for SIGTERM).
- DO NOT mutate the existing JSON response contract on stdout. New
  envelopes (`oneshot.memory.limit_hit`, `oneshot.timeout`,
  `oneshot.shutdown`, `oneshot.audit.dropped`) are written to STDERR
  only. Stdout invariant: a single complete JSON object followed by a
  newline, or nothing at all.

## Acceptance criteria

These are measurable, gating, and verified by T5:

- 1000 concurrent `r1 --one-shot decompose` invocations from one
  replica finish within 60 seconds wall-clock on a 16-core /
  32-GiB-RAM host with zero orphan PIDs (parent's `wait4` returns 0
  unreaped children).
- p50 cold-start (process start to first stdout byte) under 500 ms.
- p99 cold-start under 2 s.
- RSS per child process stays at or below 256 MiB within ±10% (so
  ~282 MiB ceiling).
- Audit POST success rate >= 99% on a stable LAN (the mock RelayGate
  audit endpoint records >= 990 of 1000 envelopes).
- Operator runbook example in `docs/integrations/relaygate-r1-stage.md`
  builds and runs against a local mock RelayGate audit server.

---

## T1 — Memory bound enforcement

Goal: cap the R1 child process at a configured RSS / address-space
ceiling, emit a structured envelope and exit code 3 when the cap is
hit, and document the GOMEMLIMIT headroom rule.

### T1.1 — Add `--max-mem` flag

- File: `cmd/r1/oneshot_cmd.go`. Inside `runOneShotCmd`'s flagset
  add `fs.IntVar(&maxMemMiB, "max-mem", 256, "max memory (MiB) — Linux RLIMIT_AS + Go GOMEMLIMIT")`.
- Allowed range: 32 .. 16384. Out-of-range returns exit 2 with
  stderr `oneshot: --max-mem must be in [32, 16384] MiB`.
- Acceptance: `r1 --one-shot decompose --max-mem 0` returns 2 with
  the range message; `--max-mem 256` runs to completion.

### T1.2 — `applyMemoryLimit` helper across platforms

- New `internal/oneshot/memlimit.go` (cross-platform):
  `func applyMemoryLimit(maxMemMiB int) error` with body —
  `hardBytes := uint64(maxMemMiB) << 20`,
  `softBytes := int64(float64(hardBytes) * 0.87)` (13% headroom),
  `debug.SetMemoryLimit(softBytes)`, then
  `return applyOSMemoryLimit(hardBytes)`.
- Doc comment: "GOMEMLIMIT is a soft target — the runtime can
  exceed it on allocation spikes. RLIMIT_AS is the hard cap.
  Setting GOMEMLIMIT to ~87% of the hard cap gives the GC pacer
  room to react before the kernel kills the process."
- New `internal/oneshot/memlimit_linux.go` (build tag
  `//go:build linux`): `func applyOSMemoryLimit(bytes uint64) error`
  using `golang.org/x/sys/unix`:
  ```
  rl := unix.Rlimit{Cur: bytes, Max: bytes}
  return unix.Prlimit(0, unix.RLIMIT_AS, &rl, nil)
  ```
  Error wrapped as `fmt.Errorf("oneshot: prlimit RLIMIT_AS: %w", err)`.
- New `internal/oneshot/memlimit_other.go` (build tag
  `//go:build !linux`): returns nil so darwin / windows compile.
- Acceptance: unit test `TestApplyMemoryLimit_Headroom` calls
  `applyMemoryLimit(256)` and asserts `debug.SetMemoryLimit(-1)`
  returns a value between 85% and 90% of 256 MiB. Linux-only test
  `TestApplyOSMemoryLimit_Linux` (build tag `linux`) sets a 256 MiB
  cap and asserts `make([]byte, 1<<29)` (512 MiB) panics with
  ENOMEM (recovered).

### T1.3 — Wire into `runOneShotCmd` and emit envelope on breach

- File: `cmd/r1/oneshot_cmd.go`. After flag parse and before
  `oneshot.RunFromFile`, call `applyMemoryLimit(maxMemMiB)`. On
  error write
  `{"event":"oneshot.memory.limit_hit","reason":"prlimit_failed","detail":"<err>","max_mem_mib":<n>}\n`
  to stderr and return exit 3.
- At top of `runOneShotCmd`: `debug.SetPanicOnFault(true)` and a
  `defer recover()` that pattern-matches `runtime: out of memory`
  or `cannot allocate memory`. On match write
  `{"event":"oneshot.memory.limit_hit","reason":"out_of_memory","max_mem_mib":<n>}\n`
  to stderr and `os.Exit(3)`. Stdout MUST stay empty in this path.
- Acceptance: integration test
  `internal/oneshot/memlimit_breach_test.go` (build tag
  `integration`) invokes the built `r1` binary with `--max-mem 64`
  and a payload that triggers a 1 GiB allocation via a test-only
  verb stub; asserts exit 3 and stderr contains
  `oneshot.memory.limit_hit`.

---

## T2 — Per-call timeout

Goal: cap wall-clock execution at a configured duration, drop any
partial output on timeout, emit a structured envelope, exit code 4.

### T2.1 — Add `--timeout` flag and root context

- File: `cmd/r1/oneshot_cmd.go`.
  `fs.DurationVar(&timeout, "timeout", 60*time.Second, "per-call wall-clock timeout")`.
- Allowed range: 100ms .. 30m. Out-of-range returns exit 2 with
  stderr `oneshot: --timeout must be in [100ms, 30m]`.
- After flag parse: `ctx, cancel := context.WithTimeout(parentCtx, timeout); defer cancel()`
  (parentCtx supplied by the signal-aware wrapper from T3.1).
- Acceptance: `r1 --one-shot decompose --timeout 50ms` returns 2.

### T2.2 — Thread `context.Context` through Run / Dispatch

- File: `internal/oneshot/oneshot.go`. New signatures (breaking
  inside the package):
  - `func Run(ctx context.Context, verb string, r io.Reader, w io.Writer) error`
  - `func RunFromFile(ctx context.Context, verb, inputPath string, w io.Writer) error`
  - `func Dispatch(ctx context.Context, verb string, payload json.RawMessage) (Response, error)`
- Pass `ctx` to `handleDecompose`, `handleVerify`, `handleCritique`.
- Each handler checks `ctx.Err()` before any expensive step; on
  cancel returns `fmt.Errorf("oneshot: %s: %w", verb, ctx.Err())`.
- Update every call site in `cmd/r1/oneshot_cmd.go` and the
  existing tests in `oneshot_test.go` to pass `context.Background()`.
- Package doc comment in `oneshot.go` gains a paragraph: "Mid-stream
  cancel of an Anthropic SSE response leaks a goroutine and an
  orphan `tool_use` block (anthropic-sdk-go issue #3003). Verb
  handlers that consume SSE MUST select on `<-ctx.Done()` AND drain
  the response body before returning. The three current verbs do
  not stream; this guard is for future verbs."
- Acceptance: `go test ./internal/oneshot/...` and `go vet ./...`
  pass; a stub verb that blocks on `<-ctx.Done()` returns within
  `timeout + 250ms`.

### T2.3 — Drop-partial pattern on timeout

- File: `cmd/r1/oneshot_cmd.go`. Replace the direct `stdout`
  argument to `oneshot.RunFromFile` with a `var buf bytes.Buffer`
  staging buffer. After return:
  - `if errors.Is(ctx.Err(), context.DeadlineExceeded)`: do NOT
    copy `buf` to stdout. Write
    `{"event":"oneshot.timeout","timeout":"<d>","verb":"<v>"}\n`
    to stderr and return 4.
  - Otherwise `io.Copy(stdout, &buf)` to flush.
- Stdout invariant: complete envelope or nothing — RelayGate's
  parser never sees a half-emitted JSON object.
- Acceptance: integration test
  `internal/oneshot/timeout_drop_test.go` (build tag
  `integration`) runs `r1 --one-shot decompose --timeout 100ms`
  against a test-only verb stub that sleeps 5s; asserts exit 4 and
  `len(stdoutBytes) == 0`.

---

## T3 — Deterministic shutdown

Goal: SIGTERM / SIGINT result in a clean exit with no partial stdout,
no orphaned background work, and a structured shutdown envelope on
stderr.

### T3.1 — `signal.NotifyContext` + signal capture

- File: `cmd/r1/oneshot_cmd.go`. At top of `runOneShotCmd`:
  ```
  sigCtx, sigStop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
  defer sigStop()
  ```
  Then derive `ctx, cancel := context.WithTimeout(sigCtx, timeout)`.
- Capture which signal fired with a parallel `sigCh := make(chan os.Signal, 1); signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)`
  goroutine that stores the received signal in
  `var firedSig atomic.Value` (typed `syscall.Signal`). `sigStop`
  unregisters the `NotifyContext` handler before exit; the manual
  `sigCh` is closed via `defer signal.Stop(sigCh)`.
- Acceptance: unit test `TestRunOneShotCmd_SignalCtxRegistered` (new
  `cmd/r1/oneshot_cmd_test.go`) starts `runOneShotCmd` in a goroutine,
  sends `syscall.Kill(os.Getpid(), syscall.SIGTERM)`, asserts the
  function returns within 250 ms.

### T3.2 — Buffered stdout + deferred flush

- File: `cmd/r1/oneshot_cmd.go`. Wrap `stdout` with
  `bufW := bufio.NewWriter(stdout)` and `defer bufW.Flush()` as a
  safety net. Pairs with T2.3's in-memory staging buffer (source of
  truth). RunFromFile writes into the staging buffer; after success
  checks, `io.Copy(bufW, &buf)` flushes through bufW.
- Acceptance: integration test
  `internal/oneshot/signal_partial_test.go` (build tag `integration`)
  spawns a child stubbed to pause mid-encode using a `chan struct{}`,
  sends SIGTERM, asserts the child's stdout is either a complete
  JSON envelope or zero bytes — never truncated.

### T3.3 — Emit `oneshot.shutdown` envelope and map exit codes

- File: `cmd/r1/oneshot_cmd.go`. After `RunFromFile` returns and
  AFTER the timeout check from T2.3:
  ```
  if sigCtx.Err() != nil {
      sig := "UNKNOWN"
      code := 1
      if v, ok := firedSig.Load().(syscall.Signal); ok {
          switch v {
          case syscall.SIGINT:  sig, code = "SIGINT", 130
          case syscall.SIGTERM: sig, code = "SIGTERM", 143
          }
      }
      fmt.Fprintf(stderr, `{"event":"oneshot.shutdown","signal":%q,"verb":%q}`+"\n", sig, verb)
      return code
  }
  ```
- Stdout invariant: the buf is NOT copied to stdout in this path —
  shutdown is treated like a timeout.
- Acceptance: integration test
  `internal/oneshot/shutdown_codes_test.go` (build tag
  `integration`) runs `r1 --one-shot decompose --timeout 30s`
  against a stubbed long-running verb, sends SIGTERM, asserts exit
  143 and stderr contains `"signal":"SIGTERM"`. Repeats for SIGINT
  -> 130.
- Companion library-level test in `oneshot_test.go`:
  `TestRun_StdoutIsAtomic` uses a custom `io.Writer` that blocks
  until a channel fires, cancels the ctx, asserts the writer
  received either zero bytes or a complete JSON object — never a
  partial prefix. Pins the invariant against future refactors.

---

## T4 — Remote audit ledger configuration

Goal: every one-shot invocation POSTs a signed envelope to a
configured remote endpoint (RelayGate's audit pipeline). Fire-and-
forget, never blocks the process exit on a stuck audit endpoint.

### T4.1 — Audit flags, env, and validation

- File: `cmd/r1/oneshot_cmd.go`. New flags:
  - `fs.StringVar(&auditEndpoint, "audit-endpoint", "", "audit URL (empty disables)")`.
  - `fs.StringVar(&auditTokenFlag, "audit-token", "", "HMAC-SHA256 signing key")`.
  - `fs.StringVar(&correlationIDFlag, "correlation-id", "", "RelayGate trace id")`.
- Token resolution: flag > env `R1_AUDIT_TOKEN` > empty. Non-empty
  endpoint with empty token returns exit 2 stderr
  `oneshot: --audit-endpoint requires --audit-token or R1_AUDIT_TOKEN`.
- Endpoint must start with `https://` or `http://127.0.0.1`. Other
  values return exit 2 stderr
  `oneshot: --audit-endpoint must be https or http loopback`.
- Correlation ID resolution: env `R1_CORRELATION_ID` > flag >
  UUIDv4 generated via `crypto/rand` (RFC 4122 hex). Propagated
  into the audit envelope, stderr error envelopes, and
  `Response.Note` so the caller can correlate even if audit
  dropped.
- Acceptance: unit tests `TestRunOneShotCmd_AuditFlagValidation`
  (covers scheme + missing-token branches) and
  `TestRunOneShotCmd_CorrelationIDPrecedence` (covers all three
  branches).

### T4.2 — `internal/oneshot/audit.go` — types and client

- New file. Defines:
  ```
  type AuditEnvelope struct {
      Verb            string  `json:"verb"`
      Status          string  `json:"status"`
      DurationMs      int64   `json:"duration_ms"`
      CostEstimateUSD float64 `json:"cost_estimate_usd"`
      PayloadSHA256   string  `json:"payload_sha256"`
      ResponseSHA256  string  `json:"response_sha256"`
      CorrelationID   string  `json:"correlation_id"`
      SchemaVersion   string  `json:"schema_version"` // "r1.audit.v1"
      EmittedAt       string  `json:"emitted_at"`     // RFC3339 nano
  }

  type AuditClient struct {
      Endpoint string
      Token    []byte
      HTTP     *http.Client      // Timeout: 5 * time.Second
      queue    chan AuditEnvelope // 64-slot buffer
      dropped  atomic.Int64
      wg       sync.WaitGroup
  }

  func NewAuditClient(endpoint, token string) *AuditClient   // starts worker
  func (a *AuditClient) Submit(env AuditEnvelope)            // non-blocking
  func (a *AuditClient) DrainOrDrop(ctx context.Context) (sent, dropped int)
  ```
- `Submit` is non-blocking: a full queue drops the envelope and
  bumps `dropped`.
- Acceptance: unit test `TestAuditClient_NonBlocking` queues 1000
  envelopes against a 64-slot client whose worker blocks forever,
  asserts every `Submit` returns within 1 ms.

### T4.3 — HMAC-SHA256 signing + retry worker

- File: `internal/oneshot/audit.go`. Worker loop per envelope:
  1. `body, _ := json.Marshal(env)`.
  2. `mac := hmac.New(sha256.New, a.Token); mac.Write(body); sig := hex.EncodeToString(mac.Sum(nil))`.
  3. POST with headers: `Content-Type: application/json`,
     `X-R1-Audit-Sig: <hex>`,
     `X-R1-Audit-CorrelationID: <env.CorrelationID>`,
     `X-R1-Audit-SchemaVersion: r1.audit.v1`.
  4. Retry on non-2xx: 3 attempts at 200ms / 800ms / 3.2s. Each
     attempt inherits `HTTP.Timeout = 5s`.
  5. Final failure logs stderr:
     `{"event":"oneshot.audit.failed","correlation_id":"%s","verb":"%s","attempts":4,"last_status":%d}`.
- Acceptance: unit test `TestAuditClient_RetriesThenLogs` runs an
  `httptest.Server` returning 500; asserts exactly 4 POSTs land and
  stderr contains `oneshot.audit.failed`.

### T4.4 — Wire into `runOneShotCmd`; drain on exit; ctx-cancel drop

- File: `cmd/r1/oneshot_cmd.go`. After flag parse, if
  `auditEndpoint != ""`:
  ```
  audit := oneshot.NewAuditClient(auditEndpoint, token)
  defer func() {
      drainCtx, drainCancel := context.WithTimeout(context.Background(), 2*time.Second)
      defer drainCancel()
      sent, dropped := audit.DrainOrDrop(drainCtx)
      if dropped > 0 {
          fmt.Fprintf(stderr, `{"event":"oneshot.audit.dropped","dropped":%d,"sent":%d}`+"\n", dropped, sent)
      }
  }()
  ```
- After `RunFromFile` returns (regardless of error), compute
  `sha256(payload)` and `sha256(response)`, build the
  `AuditEnvelope`, call `audit.Submit(env)` — never blocking the
  exit path.
- `DrainOrDrop` respects its ctx: on `ctx.Done()` returns immediately
  with `dropped = len(queue) + 1 (in-flight)` and stops the worker.
- Acceptance: integration test
  `internal/oneshot/audit_wired_test.go` spawns a mock server,
  invokes `r1 --one-shot decompose --audit-endpoint <url>
  --audit-token secret`, asserts exactly one POST with valid HMAC
  and matching SHA256 fields. Unit test
  `TestAuditClient_DrainContextCancel` enqueues 5 envelopes against
  a 10s-slow server, calls `DrainOrDrop` with 100ms timeout,
  asserts return within 150ms and `dropped >= 4`.

---

## T5 — Integration test: 1000 concurrent invocations

Goal: a reproducible benchmark proving the acceptance criteria
(60 s wall clock, p50 500 ms, p99 2 s, RSS 256 MiB ±10%, zero
orphans, zero fd leak).

### T5.1 — Test file scaffolding

- New file: `internal/oneshot/concurrency_test.go` with build tag
  `//go:build integration`. Package `oneshot_test` (external — uses
  `os/exec`, avoids import cycle). Top-of-file constants
  (overridable via env `R1_BENCH_CONCURRENCY`,
  `R1_BENCH_WALL_BUDGET_S`):
  ```
  const (
      concurrency    = 1000
      walltimeBudget = 60 * time.Second
      rssBudgetBytes = uint64(256 * 1024 * 1024)
      rssTolerance   = 0.10  // ±10%
      p50ColdStart   = 500 * time.Millisecond
      p99ColdStart   = 2 * time.Second
  )
  ```
- `TestMain` builds the binary via
  `go build -o $TMPDIR/r1-oneshot-bench ./cmd/r1` and reuses it
  for all invocations. Fails fast on build error.
- Acceptance: in verbose mode the test logs the binary path and
  the resolved concurrency / wall budget.

### T5.2 — Spawn loop and measurements

- Each of `concurrency` goroutines (gated by
  `golang.org/x/sync/errgroup.Group` with `SetLimit(concurrency)`):
  1. Record `startNs := time.Now()`.
  2. `cmd := exec.Command(binPath, "--one-shot", "decompose", "--input", "-", "--max-mem", "256", "--timeout", "30s")`.
  3. `cmd.Stdin = strings.NewReader(\`{"task":"benchmark task","context":{"strategy":"basic"}}\`)`.
  4. Use `cmd.StdoutPipe()`; read the first byte to record
     `firstByteNs`, then drain the rest into a `bytes.Buffer`.
  5. `cmd.Stderr = &stderrBuf` then `cmd.Wait()`.
  6. Exit code via `cmd.ProcessState.ExitCode()`.
  7. Peak RSS via `cmd.ProcessState.SysUsage().(*syscall.Rusage).Maxrss * 1024`
     on Linux (Maxrss is in KiB on Linux, bytes on darwin — gate
     with build tag).
- Acceptance:
  `go test -tags integration -run TestOneShot_1000Concurrent ./internal/oneshot/`
  completes for all invocations.

### T5.3 — Assertions and summary table

- All exits == 0. Non-zero counts and their most-common stderr
  message fail the test.
- Wall clock (first start to last finish) <= `walltimeBudget`.
- Cold-start (start -> first stdout byte): `slices.Sort` the
  `firstByteNs` slice; p50 <= 500ms, p99 <= 2s.
- Peak RSS per child <= 256 MiB * 1.10 = 282 MiB. Any breach
  fails with its rss + exit code.
- After all invocations, parent calls `unix.Wait4(-1, nil, unix.WNOHANG, nil)`;
  expects ECHILD (no unreaped). Other return fails.
- Parent records `len(os.ReadDir("/proc/self/fd"))` before and
  after; delta must equal 0 (no fd leak).
- `t.Run("summary", ...)` block writes a table to `testing.T.Log`
  with p50, p95, p99 cold-start, mean RSS, max RSS, failures.

### T5.4 — `make test-oneshot-concurrent` + nightly CI

- File: `/home/eric/repos/r1-agent/Makefile` (edit). Add:
  ```
  .PHONY: test-oneshot-concurrent
  test-oneshot-concurrent:
  	@ulimit -n 65535 && ulimit -u 4096 && \
  	echo "nofile=$$(ulimit -n) nproc=$$(ulimit -u) nproc(host)=$$(nproc)" && \
  	go test -tags integration -timeout 5m -run TestOneShot_1000Concurrent -v ./internal/oneshot/
  ```
  Recipe prints the host's ulimits so capacity problems are
  obvious. Acceptance: succeeds on a 16-core / 32-GiB host.
- File: `/home/eric/repos/r1-agent/.github/workflows/nightly.yml`
  (edit, or create). Add job `oneshot-concurrent` running on a
  self-hosted runner labeled `large-linux`, steps:
  `actions/checkout@v4`, `actions/setup-go@v5` (Go 1.23+), then
  `make test-oneshot-concurrent`. Uploads the summary table as an
  artifact. Guarded by `if: github.event.schedule != ''` so it only
  runs on the nightly cron — PR CI continues to run `go test ./...`
  which skips integration-tagged files.
- Acceptance: workflow file lints clean under `actionlint`;
  `R1_BENCH_CONCURRENCY=100 R1_BENCH_WALL_BUDGET_S=10 make test-oneshot-concurrent`
  runs a smaller benchmark and passes on commodity hardware.

---

## T6 — Documentation

Goal: a single integration document RelayGate engineers can use as
ground truth. Must include a working Go snippet that compiles.

### T6.1 — Create the doc file with status + contract

- New file:
  `/home/eric/repos/r1-agent/docs/integrations/relaygate-r1-stage.md`.
- Top: `# RelayGate <-> R1 --one-shot stage` plus the status block
  required by `/home/eric/repos/CLAUDE.md` (Done / In Progress /
  Scoped / Scoping / Potential-On Horizon). Initial: this spec
  lives under "Scoped" until T1-T5 land.
- `## Contract` section covers:
  - Verb set: `decompose`, `verify`, `critique` (link to package
    doc in `internal/oneshot/oneshot.go`).
  - Request schema: one JSON example per verb, pulled verbatim from
    the struct tags in `decompose.go` / `verify.go` /
    `critique.go`.
  - Response envelope:
    `{verb,status,provider_used,cost_estimate_usd,data,note}` with
    per-field semantics; note that `status="error"` still produces
    exit 0.
- Acceptance: doc-test
  `internal/oneshot/doctest_test.go` reads the markdown file and
  asserts every fenced ` ```json ` block parses as valid JSON.

### T6.2 — Exit codes table generated from code

- `## Exit codes` table:
  | Code | Meaning | Source |
  | --- | --- | --- |
  | 0 | success | normal return |
  | 1 | runtime error | I/O, marshal, internal failure |
  | 2 | usage error | bad flag / verb / range |
  | 3 | memory limit hit | RLIMIT_AS or GOMEMLIMIT breach |
  | 4 | timeout | `--timeout` exceeded; stdout dropped |
  | 130 | SIGINT | Ctrl-C or SIGINT received |
  | 143 | SIGTERM | orchestrator-sent SIGTERM |
- New file `internal/oneshot/exit_codes.go` exports the codes as
  constants (`ExitOK`, `ExitRuntime`, `ExitUsage`, `ExitMemory`,
  `ExitTimeout`, `ExitSIGINT`, `ExitSIGTERM`). The doc-test loads
  both and asserts string equality so the table can't drift.

### T6.3 — Configuration reference and event constants

- `## Configuration` section: flag table covering name, type,
  default, allowed range, description for `--input`, `--json`,
  `--max-mem`, `--timeout`, `--audit-endpoint`, `--audit-token`,
  `--correlation-id`. Env vars: `R1_AUDIT_TOKEN`,
  `R1_CORRELATION_ID`, `R1_BENCH_CONCURRENCY`,
  `R1_BENCH_WALL_BUDGET_S`.
- New file `internal/oneshot/events.go` exports the stderr-envelope
  names as constants: `EventMemoryLimitHit = "oneshot.memory.limit_hit"`,
  `EventTimeout = "oneshot.timeout"`,
  `EventShutdown = "oneshot.shutdown"`,
  `EventAuditDropped = "oneshot.audit.dropped"`,
  `EventAuditFailed = "oneshot.audit.failed"`. Every stderr emission
  in T1-T4 references the constant rather than a string literal.
- Acceptance: doc-test asserts (a) every flag in the table appears
  in the `cmd/r1/oneshot_cmd.go` flag-set, (b) every event constant
  in `events.go` is documented in the markdown.

### T6.4 — Capacity sizing curve from T5

- `## Capacity sizing` section: table with rows for 10 / 100 / 500 /
  1000 concurrent, columns p50 cold-start, p99 cold-start, mean RSS,
  max RSS. Filled from the first successful nightly run; explicitly
  states host class (16-core / 32-GiB) and ulimits
  (`nofile=65535`, `nproc=4096`).
- Acceptance: table has at least one row with measured numbers
  (not placeholders) after the first nightly run lands.

### T6.5 — Failure modes + observability

- `## Failure modes` bullets: memory breach (exit 3,
  `oneshot.memory.limit_hit` on stderr, raise `--max-mem` or shrink
  payload); timeout (exit 4, `oneshot.timeout`, raise `--timeout`
  capped at 30m); shutdown (exit 130/143, `oneshot.shutdown`,
  normal during pod rollover); audit drop (exit 0, stdout normal,
  `oneshot.audit.dropped`, check audit endpoint health). Each
  bullet links to the corresponding test in `internal/oneshot/` so
  an engineer can reproduce.
- `## Observability` section: documents the
  `X-R1-Audit-Sig`, `X-R1-Audit-CorrelationID`,
  `X-R1-Audit-SchemaVersion` headers; the correlation ID round-trip
  (env -> flag -> `Response.Note` -> audit envelope); the fact that
  stderr is line-delimited JSON for structured log ingestion.

### T6.6 — RelayGate adapter snippet (compiled)

- `## RelayGate-side adapter` section: a self-contained Go snippet
  (~40 lines) that (1) builds an `exec.CommandContext` with the
  RelayGate-supplied flags, (2) pipes a JSON payload into stdin,
  (3) reads stdout, parses as `oneshot.Response`, (4) maps exit
  codes to RelayGate's internal error taxonomy via a switch on
  `cmd.ProcessState.ExitCode()`, (5) propagates
  `X-R1-Audit-CorrelationID` through to RelayGate's tracing layer.
- Lives in a fenced ` ```go ` block. A doc-test
  `internal/oneshot/relaygate_doctest_test.go` (build tag
  `integration`) extracts the block, writes it to a temp file,
  runs `go build -o /tmp/snippet`. Deliberate breakage (remove a
  `return`) fails the doc-test.

### T6.7 — Operator runbook (mock RelayGate)

- `## Operator runbook — local mock RelayGate` section:
  step-by-step block that (1) launches the mock audit binary
  `go run ./internal/oneshot/cmd/mockaudit -addr 127.0.0.1:9111 -token devtoken`
  (new ~40 LOC binary at
  `internal/oneshot/cmd/mockaudit/main.go` that logs every POST
  with HMAC verification), (2) invokes
  `echo '{"task":"refactor module X"}' | r1 --one-shot decompose --audit-endpoint http://127.0.0.1:9111 --audit-token devtoken --max-mem 256 --timeout 60s`,
  (3) shows the expected stdout JSON envelope and the expected mock
  server log line with the matching correlation ID.
- Acceptance: integration test
  `TestRunbook_LocalMockRelayGate` (build tag `integration`) runs
  the documented commands in sequence and asserts the mock server
  received one POST with a valid HMAC and matching SHA256 fields.

---

## Cross-cutting verification

After T1-T6 all land, run the following gate manually as a final
acceptance step:

1. `go build ./cmd/r1` — clean.
2. `go vet ./...` — clean.
3. `go test ./...` — clean (excludes integration tag).
4. `make test-oneshot-concurrent` on a 16-core host — clean.
5. `r1 --one-shot decompose --help` shows every new flag.
6. Manually trigger each new exit code path and confirm the
   corresponding stderr envelope appears.

If any of these fail, the spec is not complete. Open a fresh repair
spec under `specs/` rather than amending this one.

<!-- BUILD_COMPLETED: 2026-05-12 -->

