# RT-04: NDJSON Event Streaming Patterns for Stoke <-> CloudSwarm

Date: 2026-04-20
Scope: Stoke (Go) emits NDJSON events on stdout. CloudSwarm (Python, Temporal) reads line-by-line, persists to Postgres, republishes to NATS. Stoke reads stdin for HITL decisions.

## 1. Comparable protocols in the wild

### 1.1 Claude Code `--output-format stream-json`
Claude Code emits NDJSON (one JSON object per line) when `-p --output-format stream-json --verbose --include-partial-messages` is set. Every event carries `type`, `uuid`, and `session_id`. Notable events:

- `system/init` (first event, session metadata, tools, plugins)
- `system/plugin_install` (status enum: `started|installed|failed|completed`)
- `system/api_retry` (with `attempt`, `max_retries`, `retry_delay_ms`, `error_status`, `error` category)
- `stream_event` (wraps SSE deltas: `message_start`, `text_delta`, `content_block_stop`, `message_stop`)

Design takeaways for Stoke: (a) always a `uuid` per event for replay dedup; (b) `session_id` on every event; (c) use a hierarchical `type` / `subtype` instead of a flat enum so new subtypes don't break old parsers; (d) retry is visible as an event, not swallowed. The first event is a handshake/init containing capabilities.

### 1.2 LSP (JSON-RPC with `Content-Length` framing)
LSP uses HTTP-style framing: `Content-Length: N\r\n\r\n<json>`. This solves the "partial line" and "embedded newline" problems NDJSON has, at the cost of parser complexity. LSP also defines `$/progress` notifications for streaming partial results with a client-issued token -- the server may interleave partial results into any long-running request.

Takeaway: NDJSON is fine only if emitters **guarantee no embedded unescaped newlines** (Go's `encoding/json` does this by default) and the reader uses a scanner with sufficient buffer. For events >~1 MB consider Content-Length framing as a fallback.

### 1.3 Debug Adapter Protocol (DAP)
Same HTTP-style framing as LSP. Every message has `seq` (monotonic int), `type` ∈ {`request`, `response`, `event`}, and `command`/`event` name. Responses reference originating `request_seq`. This bidirectional seq pattern is ideal for HITL: Stoke emits a `request` to CloudSwarm, CloudSwarm replies with a matching `response` on stdin.

### 1.4 AWS Bedrock Agents event stream
`InvokeAgent` returns a chunked stream; each chunk has either `chunk.bytes` (response body) or a `trace` part (reasoning/tool traces). Final answer lives in the last chunk's `orchestrationTrace.finalResponse`. Takeaway: separate "content/result" events from "observability/trace" events so consumers can subscribe to either.

## 2. Event schema recommendations

Required top-level fields on every event:

| Field | Type | Purpose |
|---|---|---|
| `schema_version` | string (`"1"`) | monotonic integer-as-string; bump on breaking changes |
| `type` | string | hierarchical (`task.started`, `hitl.request`, `ledger.node_added`) |
| `id` | string (UUIDv7) | per-event id, time-ordered, used for dedup/replay |
| `seq` | uint64 | monotonic per process; detects drops on consumer side |
| `ts` | string (RFC3339Nano UTC) | human-readable + sortable; Go `time.Format(time.RFC3339Nano)` |
| `session_id` | string | stoke session/mission id |
| `payload` | object | event-specific; consumers parse based on `type` |

Evolution: **additive-only**. New optional fields are fine; never remove/rename. Breaking changes -> bump `schema_version` and emit both during a grace window. Treat unknown `type` values as "observability only, don't fail."

Size: NATS default max payload is 1 MB. Cap Stoke events at ~512 KB serialized and emit a synthetic `event.truncated` with a pointer (ledger node hash) when exceeding. Large content (diffs, file bodies) should be ledger-addressed, not inlined.

Timestamps: RFC3339Nano is canonical. Unix millis as a secondary `ts_ms` is fine for ordering in Postgres but primary should be string-typed to avoid int precision pitfalls across JSON parsers.

## 3. Flush semantics (Go specifics)

Key facts:

- `os.Stdout` is a `*os.File` and is **unbuffered** at the Go layer (golang/go#36619). Each `Write` is a `write(2)` syscall.
- `fmt.Println(os.Stdout, ...)` -> one syscall per call. OK for low rates but inefficient at event rates >1kHz.
- `json.NewEncoder(w).Encode(v)` writes payload plus trailing `\n`, and **does not internally buffer** beyond what `w` buffers.
- If you wrap with `bufio.NewWriter`, **you must `Flush()` after every event** or CloudSwarm sees nothing until the buffer (4 KB default) fills.
- `os.Stdout.Sync()` calls `fsync(2)` which is a no-op on pipes -- it's only meaningful for regular files. **Do not call it in the hot path** to "force a flush"; it doesn't do that for pipes.
- When stdout is a pipe (not a TTY), glibc stdio defaults to fully-buffered, but Go bypasses stdio entirely, so this doesn't bite us.
- Concurrent `Write`s from multiple goroutines can interleave bytes mid-line. Serialize through a single goroutine or a `sync.Mutex`.

Recommendation: use `json.Encoder` directly on an unbuffered `os.Stdout` wrapped by a `sync.Mutex`. Encoder appends `\n` so each line is atomic w.r.t. the mutex. Skip `bufio.Writer` unless you measure a syscall bottleneck.

## 4. Backpressure

If CloudSwarm stops reading, the OS pipe buffer (Linux: 64 KB default, `fcntl F_SETPIPE_SZ` up to `/proc/sys/fs/pipe-max-size`) fills, and the next `write(2)` blocks. Options:

1. **Block** (simplest, what Claude Code does). Stoke stalls -- acceptable because CloudSwarm has no reason to stop reading mid-task, and Temporal will timeout the activity.
2. **Bounded channel + drop-oldest observability events**, keep critical events (decisions, results) always-blocking. Two-lane design.
3. **Spool to tempfile** if downstream is slow. Complexity explodes; avoid unless the pipe consumer is known-slow.

Recommendation: two-lane. Channel capacity 1024. Critical events (`hitl.*`, `task.completed`, `mission.completed`, errors) always-block. Observability/trace events are dropped-oldest with a counter emitted as `stream.dropped` every N drops so CloudSwarm knows.

Also: set pipe buffer larger where possible (`F_SETPIPE_SZ` to 1 MB) in CloudSwarm when it creates the subprocess.

## 5. SIGSTOP / SIGCONT behavior

When a process is SIGSTOPed:
- Data already `write(2)`-ed into the pipe is already in the kernel buffer -- the reader can still drain it. SIGSTOP does not freeze the pipe.
- Anything buffered in user space (bufio.Writer, channels) stays put until SIGCONT.
- SIGCONT cancels pending stop signals and resumes normal execution; no automatic flush happens because none is needed.

Implication: to make pause/resume safe, **emit events with monotonic `seq` and idempotent payloads**. On resume, Stoke should emit a `supervisor.resumed` event with the `seq` range covered while paused (there is none) so consumers can verify continuity. Any in-flight HITL request should be re-announced on resume since CloudSwarm may have timed out the prompt.

## 6. Graceful shutdown

- Trap SIGINT/SIGTERM in a single signal goroutine.
- On signal: cancel root `context.Context`, then wait up to `ShutdownGrace` (e.g. 5s) for the emitter to drain its channel, then close stdout.
- Emit `mission.aborted` with `reason=signal` as the last event before closing.
- Exit codes: `0` success, `1` generic failure, `2` config/usage error, `3` budget exceeded, `4` verification failed, `130` SIGINT, `143` SIGTERM. Temporal distinguishes retryable (>=1, !=2) from fatal (2) when wrapped.
- Do NOT exit from inside the signal handler -- signal the main goroutine via context so the emitter drains.

## 7. HITL stdin protocol

Constraints: Go's `os.Stdin.SetDeadline` fails on Linux for pipes (golang/go#24842). So timeouts must be implemented via goroutine + channel + `select`.

Recommended framing: **NDJSON both directions**. Each stdin line is a JSON object with `{type:"hitl.response", request_id, decision, ...}`. This mirrors DAP's request/response pattern.

Reader pattern:

```go
func stdinReader(ctx context.Context, lines chan<- []byte) {
    r := bufio.NewReaderSize(os.Stdin, 1<<20)
    for {
        line, err := r.ReadBytes('\n')
        if len(line) > 0 {
            select {
            case lines <- line:
            case <-ctx.Done():
                return
            }
        }
        if err != nil { // io.EOF or closed pipe
            return
        }
    }
}
```

HITL timeout (10 min is fine; orchestrator decides):

```go
func (e *Emitter) RequestDecision(ctx context.Context, req HITLRequest, timeout time.Duration) (HITLResponse, error) {
    req.ID = newULID()
    if err := e.Emit(Event{Type: "hitl.request", Payload: req}); err != nil {
        return HITLResponse{}, err
    }
    ch := e.registerWaiter(req.ID)
    defer e.unregisterWaiter(req.ID)
    t := time.NewTimer(timeout)
    defer t.Stop()
    select {
    case resp := <-ch:
        return resp, nil
    case <-t.C:
        _ = e.Emit(Event{Type: "hitl.timeout", Payload: map[string]string{"request_id": req.ID}})
        return HITLResponse{}, ErrHITLTimeout
    case <-ctx.Done():
        return HITLResponse{}, ctx.Err()
    }
}
```

## 8. Concrete Go sketch: emitter

```go
type Event struct {
    SchemaVersion string          `json:"schema_version"`
    Type          string          `json:"type"`
    ID            string          `json:"id"`           // UUIDv7
    Seq           uint64          `json:"seq"`
    Ts            string          `json:"ts"`           // RFC3339Nano
    SessionID     string          `json:"session_id"`
    Payload       json.RawMessage `json:"payload,omitempty"`
}

type Emitter struct {
    mu        sync.Mutex
    enc       *json.Encoder     // wraps os.Stdout, no bufio
    seq       atomic.Uint64
    sessionID string
    critical  chan Event        // unbounded-ish or large
    observ    chan Event        // bounded, drop-oldest
    dropped   atomic.Uint64
    closed    atomic.Bool
}

func NewEmitter(sessionID string) *Emitter {
    e := &Emitter{
        enc:       json.NewEncoder(os.Stdout),
        sessionID: sessionID,
        critical:  make(chan Event, 256),
        observ:    make(chan Event, 1024),
    }
    go e.run()
    return e
}

func (e *Emitter) run() {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case ev, ok := <-e.critical:
            if !ok { return }
            e.write(ev)
        case ev, ok := <-e.observ:
            if !ok { return }
            e.write(ev)
        case <-ticker.C:
            if n := e.dropped.Swap(0); n > 0 {
                e.write(e.build("stream.dropped", map[string]uint64{"count": n}))
            }
        }
    }
}

func (e *Emitter) write(ev Event) {
    e.mu.Lock()
    defer e.mu.Unlock()
    _ = e.enc.Encode(&ev) // Encode appends '\n'; os.Stdout is unbuffered
}

func (e *Emitter) build(t string, payload any) Event {
    b, _ := json.Marshal(payload)
    return Event{
        SchemaVersion: "1", Type: t,
        ID: ulid.Make().String(),
        Seq: e.seq.Add(1),
        Ts: time.Now().UTC().Format(time.RFC3339Nano),
        SessionID: e.sessionID,
        Payload: b,
    }
}

func (e *Emitter) Emit(t string, payload any) {
    if e.closed.Load() { return }
    ev := e.build(t, payload)
    if strings.HasPrefix(t, "hitl.") || strings.HasPrefix(t, "task.completed") ||
       strings.HasPrefix(t, "mission.") || strings.HasPrefix(t, "error.") {
        e.critical <- ev // block on purpose
        return
    }
    select {
    case e.observ <- ev:
    default:
        // drop-oldest: pop one, push new
        select { case <-e.observ: default: }
        select { case e.observ <- ev: default: e.dropped.Add(1) }
    }
}

func (e *Emitter) Close(ctx context.Context) {
    e.closed.Store(true)
    close(e.critical); close(e.observ)
    // caller waits on run() goroutine via a done chan (omitted for brevity)
}
```

### Correctness notes

1. `json.Encoder.Encode` appends exactly one `\n` and does one `Write`. Combined with the mutex, lines are atomic.
2. `os.Stdout` is unbuffered -- no `Flush` needed; no `Sync` either (no-op on pipes).
3. `seq` monotonic so consumer can detect gaps. If consumer sees gap, it can fetch the ledger by `id` to backfill.
4. Critical events block the caller -- intentional. If CloudSwarm stalls, Stoke stalls; Temporal activity timeout handles total-failure case.
5. Observability events drop-oldest under pressure. `stream.dropped` emitted every 5 s with count so consumer knows.
6. Max line size: `bufio.Scanner` default is 64 KB. CloudSwarm must set scanner buffer to at least 1 MB. Stoke events should stay under 512 KB; larger content goes to the ledger with a pointer.
7. HITL: request/response correlation by `request_id`; stdin reader runs in its own goroutine and dispatches to registered waiters by id.
8. Shutdown: signal goroutine -> cancel ctx -> emitter drains (bounded wait) -> close stdout -> exit with mapped code.
9. SIGSTOP/SIGCONT: on resume, re-emit any pending HITL request so CloudSwarm can reattach. All events are idempotent by `id`.
10. Schema: additive evolution only. Unknown `type` values are observability-silent on the consumer.

## Sources

- [Run Claude Code programmatically (Anthropic docs)](https://code.claude.com/docs/en/headless)
- [Streaming output in `--verbose --print` (anthropics/claude-code#733)](https://github.com/anthropics/claude-code/issues/733)
- [LSP 3.17 specification](https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/)
- [Debug Adapter Protocol specification](https://microsoft.github.io/debug-adapter-protocol/specification.html)
- [Bedrock Agents trace-events](https://docs.aws.amazon.com/bedrock/latest/userguide/trace-events.html)
- [Bedrock AgentCore response streaming](https://docs.aws.amazon.com/bedrock-agentcore/latest/devguide/response-streaming.html)
- [JSON streaming (Wikipedia)](https://en.wikipedia.org/wiki/JSON_streaming)
- [NDJSON.com -- JSON Lines format](https://ndjson.com/)
- [Stdout Buffering (eklitzke.org)](https://eklitzke.org/stdout-buffering)
- [os,fmt: os.Stdout is unbuffered (golang/go#36619)](https://github.com/golang/go/issues/36619)
- [os.Stdin.SetDeadline fails on Linux (golang/go#24842)](https://github.com/golang/go/issues/24842)
- [os: non-blocking I/O for pollable files (golang/go#18507)](https://github.com/golang/go/issues/18507)
- [bufio package docs (pkg.go.dev)](https://pkg.go.dev/bufio)
- [Job Control Signals (GNU libc manual)](https://www.gnu.org/software/libc/manual/html_node/Job-Control-Signals.html)
- [Two great signals: SIGSTOP and SIGCONT (major.io)](https://major.io/p/two-great-signals-sigstop-and-sigcont/)
- [Node.js Streaming in Production: Backpressure (dev.to)](https://dev.to/axiom_agent/nodejs-streaming-in-production-backpressure-pipelines-and-memory-efficient-processing-2ilh)
- [StreamJsonRpc protocol extensibility (Microsoft)](https://microsoft.github.io/vs-streamjsonrpc/docs/extensibility.html)
