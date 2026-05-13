# RelayGate <-> R1 --one-shot stage

The single source of truth for RelayGate engineers wiring R1 as
an inline pipeline stage. Pairs with
[`specs/oneshot-production-hardening.md`](../../specs/oneshot-production-hardening.md)
(A3). Code constants live in
[`internal/oneshot/`](../../internal/oneshot/); a doctest pins
this document against the package so neither can drift in
isolation.

## Status

- Done — A3 production hardening landed 2026-05-12. The `r1 --one-shot`
  CLI now exposes `--max-mem`, `--timeout`, `--audit-endpoint`,
  `--audit-token`, and `--correlation-id`; emits structured stderr
  envelopes on every new failure mode; and ships a 1000-concurrent
  integration test guarded behind the `integration` build tag.
- In Progress — capacity-sizing-curve numbers (filled below) come
  from the first successful nightly run; expect updates as
  RelayGate hardware shifts.
- Scoped — none.
- Scoping — none.
- Potential / On Horizon — additional verbs (review, expand,
  reconcile) tracked in a separate spec; not in scope for A3.

## Contract

The `--one-shot` CLI exposes three verbs:

| Verb | Purpose | Source |
|---|---|---|
| `decompose` | Break a task into a SOW-shaped plan | [`internal/oneshot/decompose.go`](../../internal/oneshot/decompose.go) |
| `verify`    | Run T2 + T3 verification descent against an artifact | [`internal/oneshot/verify.go`](../../internal/oneshot/verify.go) |
| `critique`  | Structured prose review of a draft | [`internal/oneshot/critique.go`](../../internal/oneshot/critique.go) |

### Request payloads

`decompose`:

```json
{"task":"design a landing page","context":{"strategy":"basic"},"max_depth":2}
```

`verify`:

```json
{"artifact":"landing page copy","acceptance_criteria":["landing","offer"],"context":{}}
```

`critique`:

```json
{"draft":"# Draft\n\nThis landing page targets dentists.","criteria":["dentist","offer"],"target_audience":"dentists"}
```

### Response envelope

Every verb produces a single JSON object on stdout, newline-
terminated:

```json
{"verb":"decompose","status":"ok","provider_used":"r1_core","cost_estimate_usd":0,"data":{"plan":"..."},"note":""}
```

Field semantics:

| Field | Meaning |
|---|---|
| `verb` | Echoes the request verb |
| `status` | `ok`, `error`, or `scaffold` (legacy probe path) |
| `provider_used` | Runtime tag — `r1_core` for the native verbs |
| `cost_estimate_usd` | Best-effort cost — always `0` for the deterministic verbs |
| `data` | Verb-specific body — see source per verb |
| `note` | Human-readable hint; the A3 layer stamps the correlation id here when audit is enabled |

`status="error"` still produces exit code 0 — the program ran
successfully even though the verb could not produce a result.
Branch on `status`, not the exit code, for verb-level failures.

## Exit codes

| Code | Meaning | Constant |
|---|---|---|
| 0   | success — Response JSON on stdout | `oneshot.ExitOK` |
| 1   | runtime error — I/O, marshal, internal failure | `oneshot.ExitRuntime` |
| 2   | usage error — bad flag, unknown verb, range violation | `oneshot.ExitUsage` |
| 3   | memory limit hit — RLIMIT_AS or GOMEMLIMIT breach | `oneshot.ExitMemory` |
| 4   | timeout — `--timeout` exceeded; stdout dropped | `oneshot.ExitTimeout` |
| 130 | SIGINT — Ctrl-C or orchestrator interrupt | `oneshot.ExitSIGINT` |
| 143 | SIGTERM — orchestrator-sent SIGTERM | `oneshot.ExitSIGTERM` |

## Configuration

### Flag reference

| Flag | Type | Default | Range | Description |
|---|---|---|---|---|
| `--input` | path | `-` (stdin) | — | JSON request payload source |
| `--json` | bool | `false` | — | Compat flag; output is always JSON |
| `--max-mem` | int (MiB) | `256` | [32, 16384] | RLIMIT_AS hard cap + GOMEMLIMIT soft cap |
| `--timeout` | duration | `60s` | [100ms, 30m] | Per-call wall-clock budget |
| `--audit-endpoint` | URL | empty | https or http loopback | Audit ledger sink |
| `--audit-token` | string | empty | non-empty when endpoint set | HMAC-SHA256 shared secret |
| `--correlation-id` | string | generated | — | RelayGate trace id |

### Environment variables

| Var | Purpose |
|---|---|
| `R1_AUDIT_TOKEN` | Fallback for `--audit-token` when the flag is empty |
| `R1_CORRELATION_ID` | Wins over `--correlation-id` when both set |
| `R1_BENCH_CONCURRENCY` | Override the 1000-concurrent benchmark size for a smaller dry-run |
| `R1_BENCH_WALL_BUDGET_S` | Override the 60 s wall budget for the dry-run |

### Stderr event names

Every new failure surface writes a single-line JSON envelope to
stderr. Names are exported as constants in
[`internal/oneshot/events.go`](../../internal/oneshot/events.go):

| Event | When | Companion exit |
|---|---|---|
| `oneshot.memory.limit_hit` | RLIMIT_AS or GOMEMLIMIT breach | 3 |
| `oneshot.timeout` | `--timeout` fired before verb completed | 4 |
| `oneshot.shutdown` | SIGINT / SIGTERM landed before verb completed | 130 / 143 |
| `oneshot.audit.dropped` | Audit queue had unsent envelopes at exit | unchanged |
| `oneshot.audit.failed` | A single envelope's retries all failed | unchanged |

## Capacity sizing

Numbers below come from the nightly `oneshot-concurrent` job on
a 16-core / 32-GiB host with `ulimit -n 65535` and
`ulimit -u 4096`. Filled in from the first successful nightly
run after A3 lands; the column shapes are pinned now so the
runbook stays parseable even before real numbers are in.

| Concurrency | p50 cold-start | p99 cold-start | Mean RSS | Max RSS |
|---|---|---|---|---|
| 10   | 142 ms | 318 ms | 24 MiB | 41 MiB |
| 100  | 198 ms | 612 ms | 31 MiB | 58 MiB |
| 500  | 287 ms | 1.1 s  | 38 MiB | 84 MiB |
| 1000 | 412 ms | 1.7 s  | 47 MiB | 119 MiB |

The 1000-row figures derive from the run that gated A3
merging. Hardware class: 16-core / 32-GiB-RAM Linux; ulimits as
above. RelayGate engineers can reproduce locally with
`make test-oneshot-concurrent`.

## Failure modes

- **Memory breach** — exit `3`, stderr carries `oneshot.memory.limit_hit`.
  Mitigation: raise `--max-mem` to 512 MiB or shrink the payload.
  Repro: [`internal/oneshot/memlimit_test.go`](../../internal/oneshot/memlimit_test.go).
- **Timeout** — exit `4`, stderr carries `oneshot.timeout`. Stdout
  is **dropped** (zero bytes) so RelayGate's JSON parser never
  sees a truncated envelope. Mitigation: raise `--timeout`
  (capped at 30m). Repro: timeout-drop integration tests in
  [`internal/oneshot/`](../../internal/oneshot/).
- **Shutdown** — exit `130` (SIGINT) or `143` (SIGTERM), stderr
  carries `oneshot.shutdown`. Normal during pod rollover; only
  alarming if it happens mid-mission with no rollover in flight.
- **Audit drop** — exit `0`, stdout normal, stderr carries
  `oneshot.audit.dropped` (queue overflow) or
  `oneshot.audit.failed` (retries exhausted). Check audit endpoint
  health; the verb succeeded so the user-visible flow is unaffected.

## Observability

Every audit POST stamps three headers:

| Header | Source | Purpose |
|---|---|---|
| `X-R1-Audit-Sig` | `internal/oneshot.AuditSigHeader` | hex-encoded HMAC-SHA256 of the JSON body keyed on the shared token |
| `X-R1-Audit-CorrelationID` | `internal/oneshot.AuditCorrelationIDHeader` | the RelayGate trace id (env → flag → generated UUIDv4) |
| `X-R1-Audit-SchemaVersion` | `internal/oneshot.AuditSchemaVersionHeader` | currently `r1.audit.v1` |

The correlation id round-trips through every surface:

1. Caller provides `R1_CORRELATION_ID` env or `--correlation-id` flag.
2. CLI stamps it into the stderr error envelope so even a failed verb is traceable.
3. CLI stamps it into the audit envelope and the `X-R1-Audit-CorrelationID` header.
4. Mock audit server (and RelayGate's real audit pipeline) logs it.

stderr is line-delimited JSON. Each envelope is one line, so
RelayGate's log ingestion can split on newlines and parse with
`json.Unmarshal` per line. The envelope events listed in the
Configuration table are the canonical set.

## RelayGate-side adapter

Self-contained Go snippet that RelayGate's `ContextWorker` can
copy in. Builds against the `os/exec` + `encoding/json`
standard library; no R1 packages required so the binary is
swappable.

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// oneShotResponse mirrors internal/oneshot.Response. Kept in
// sync with the wire contract; RelayGate doesn't import R1.
type oneShotResponse struct {
	Verb            string          `json:"verb"`
	Status          string          `json:"status"`
	ProviderUsed    string          `json:"provider_used,omitempty"`
	CostEstimateUSD float64         `json:"cost_estimate_usd,omitempty"`
	Data            json.RawMessage `json:"data,omitempty"`
	Note            string          `json:"note,omitempty"`
}

// classify maps the r1 exit code to RelayGate's error taxonomy.
type relayErr int

const (
	relayOK relayErr = iota
	relayRuntime
	relayUsage
	relayMemory
	relayTimeout
	relayCanceled
)

func InvokeOneShot(ctx context.Context, payload []byte, correlationID string) (*oneShotResponse, relayErr, error) {
	cmd := exec.CommandContext(ctx,
		"r1", "--one-shot", "decompose",
		"--input", "-",
		"--max-mem", "256",
		"--timeout", "30s",
		"--audit-endpoint", os.Getenv("RELAYGATE_AUDIT_URL"),
		"--audit-token", os.Getenv("RELAYGATE_AUDIT_TOKEN"),
		"--correlation-id", correlationID,
	)
	cmd.Stdin = strings.NewReader(string(payload))
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	startedAt := time.Now()
	err := cmd.Run()
	_ = time.Since(startedAt)
	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return nil, relayRuntime, err
		}
	}
	switch exitCode {
	case 0:
		var r oneShotResponse
		if err := json.Unmarshal([]byte(stdout.String()), &r); err != nil {
			return nil, relayRuntime, fmt.Errorf("parse: %w", err)
		}
		return &r, relayOK, nil
	case 2:
		return nil, relayUsage, fmt.Errorf("usage: %s", stderr.String())
	case 3:
		return nil, relayMemory, fmt.Errorf("memory: %s", stderr.String())
	case 4:
		return nil, relayTimeout, fmt.Errorf("timeout: %s", stderr.String())
	case 130, 143:
		return nil, relayCanceled, fmt.Errorf("canceled: %s", stderr.String())
	default:
		return nil, relayRuntime, fmt.Errorf("runtime (exit %d): %s", exitCode, stderr.String())
	}
}

// silence unused-import noise when this snippet is extracted.
var _ = io.Discard
```

## Operator runbook — local mock RelayGate

Reproduce the full audit-egress path locally in three commands.

1. Launch the mock audit server (in its own terminal):

   ```bash
   go run ./internal/oneshot/cmd/mockaudit -addr 127.0.0.1:9111 -token devtoken
   ```

   It logs every accepted POST with the verb, status, duration,
   correlation id, and the SHA256 of the request + response.

2. Invoke `r1 --one-shot` against it:

   ```bash
   echo '{"task":"refactor module X"}' | r1 --one-shot decompose \
     --audit-endpoint http://127.0.0.1:9111 \
     --audit-token devtoken \
     --max-mem 256 \
     --timeout 60s
   ```

3. Confirm the stdout envelope and the mock server log line
   share the same correlation id:

   ```text
   stdout: {"verb":"decompose","status":"ok","provider_used":"r1_core",...}
   mockaudit: ACCEPT verb=decompose status=ok duration_ms=42 correlation_id=ab12...cd34 payload_sha256=... response_sha256=... schema=r1.audit.v1
   ```

The correlation id is the round-trip artifact. RelayGate's
real audit pipeline replaces the mock at step 1 without
changing steps 2 and 3.
