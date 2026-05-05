<!-- STATUS: done -->
<!-- BUILD_STARTED: 2026-05-02 -->
<!-- BUILD_COMPLETED: 2026-05-02 -->
<!-- CREATED: 2026-05-02 -->
<!-- DEPENDS_ON: (foundation — none in this scope) -->
<!-- BUILD_ORDER: 1 -->

# Cortex Core — Implementation Spec

## Overview

The Cortex is r1's parallel-cognition substrate: a Global Workspace Theory-style shared mutable view (`Workspace`) where N concurrent specialist threads (`Lobes`) publish typed findings (`Notes`) that the main `agentloop.Loop` drains at well-defined checkpoints. This spec ships the **foundation only** — the empty stage, the actor abstraction, the broadcast hub, the superstep barrier, the spotlight selector, the agent-decides Router (Haiku 4.5 LLM that routes mid-turn user input to one of {interrupt, steer, queue_mission, just_chat}), the drop-partial interrupt protocol, the cache pre-warm pump, the Lobe budget controller, and write-through persistence to the existing `internal/bus/` WAL. **No specific Lobes are implemented here** — that is spec 2 (`cortex-concerns`). **No lane wire format is specified here** — that is spec 3 (`lanes-protocol`). What this spec produces is a new `internal/cortex/` package that compiles, ships passing unit + race-detector tests, and integrates with `agentloop.Loop` via the existing `MidturnCheckFn` and `PreEndTurnCheckFn` hooks plus one new hook `OnUserInputMidTurn`. Operators (the `cmd/r1` chat REPL) wire a `*cortex.Cortex` into the loop's `Config`; if they don't, behavior is unchanged from today.

## Stack & Versions

- **Go 1.25.5** (see `go.mod` line 3: `go 1.25.5`). If `go.work` is added in the future, keep parity with `go.mod`.
- **Pure standard library** for all primitives: `context`, `sync`, `sync/atomic`, `time`, `encoding/json`, `errors`. No new direct dependencies for the cortex package.
- **Anthropic SDK** for the Router LLM call: reuse the existing `internal/provider/` interface (`provider.Provider.ChatStream`) — no SDK changes; the Router builds its own `provider.ChatRequest` with model `claude-haiku-4-5`.
- **Existing internal deps reused**:
  - `internal/hub` (typed event hub — for emitting `cortex.*` events that spec 3 will subscribe to)
  - `internal/bus` (durable WAL — for Note write-through persistence)
  - `internal/provider` (model client interface — for Router + warming pump)
  - `internal/agentloop` (the loop being augmented — adds `Cortex` field on `Config`)
- **No new third-party deps.** If a future implementer feels the need for one, the answer is no — the GWT pattern is `RWMutex + WaitGroup + chan`, all of which Go's stdlib already nails.

## Existing Patterns to Follow

The cortex code MUST mirror these proven primitives in r1, both for review legibility and to keep race-test surface area familiar:

- **RWMutex over a slice with copy-on-read** — model after `internal/conversation/runtime.go:67-99` (`Runtime.AddMessage` / `Runtime.Messages`). The `Workspace` is the same shape: writers append under `Lock`; readers copy under `RLock`.
- **WaitGroup + buffered results channel for fan-out** — model after `internal/agentloop/loop.go:601-613` (parallel `executeTools`) and `internal/specexec/specexec.go:113-220` (semaphore + `wg.Wait()`). The `Round` superstep barrier is the same shape.
- **Hub subscriber pattern** — model after `internal/hub/bus.go:78-116` (`Bus.Register`). Cortex emits via `hub.Bus.Emit` / `hub.Bus.EmitAsync`; downstream consumers (TUI lanes, web UI) subscribe via `hub.Bus.Register` in spec 3+.
- **Per-subscriber goroutine + buffered chan** — model after `internal/bus/bus.go:213-258` (`Subscription.run()`). The `Lobe` runner has the same shape: one goroutine per Lobe, buffered input chan, panic-recover, observe ctx.Done.
- **Semaphore via buffered chan** — model after `internal/specexec/specexec.go:130-145` (`sem := make(chan struct{}, MaxParallel)`). The `LobeSemaphore` is identical.
- **Drop-partial interrupt protocol** — verbatim from `specs/research/raw/RT-CANCEL-INTERRUPT.md` §6 "Recommended pattern for r1-agent". Reproduce the per-turn `context.WithCancel`, the `partial *Message` accumulator outside committed history, the `<-streamDone` drain after `cancel()`, and the synthetic-user-message append.
- **Cache-aligned prompt construction** — model after `internal/agentloop/loop.go:450-495` (`buildRequest` + `BuildCachedSystemPrompt` + `SortToolsDeterministic`). The warming-pump request reuses the same builder so cache breakpoints align.
- **Tier-4 concurrency budget** — verbatim from `specs/research/raw/RT-CONCURRENT-CLAUDE-API.md` §1 + §7 (5 LLM Lobes default, hard cap 8).

## Public API of `internal/cortex/`

All file paths below are relative to repo root. The package layout is:

```
internal/cortex/
├── cortex.go        — Cortex struct + constructors + integration shim
├── workspace.go     — Workspace + Note + Spotlight
├── lobe.go          — Lobe interface + LobeRunner (goroutine wrapper)
├── round.go         — Round (superstep barrier)
├── router.go        — Router (Haiku 4.5 mid-turn user-input handler)
├── interrupt.go     — drop-partial interrupt protocol helpers
├── prewarm.go       — cache pre-warm pump
├── budget.go        — LobeSemaphore + token-budget controller
├── persist.go       — bus/WAL write-through + replay
└── *_test.go        — table-driven unit tests + race-detector integration test
```

### `Note` (workspace.go)

```go
// Severity tags drive both supervisor injection priority and the
// PreEndTurnCheckFn gate. Critical Notes refuse end_turn until resolved.
type Severity string

const (
    SevInfo     Severity = "info"
    SevAdvice   Severity = "advice"
    SevWarning  Severity = "warning"
    SevCritical Severity = "critical"
)

// Note is the unit of Lobe output. Append-only; a Note is never mutated.
// Resolution is modeled as a follow-on Note with Resolves=parentID.
type Note struct {
    ID         string         // ULID-like; monotonic per Workspace
    LobeID     string         // who emitted (e.g. "memory-recall")
    Severity   Severity       // info|advice|warning|critical
    Title      string         // ≤80 chars, single-line
    Body       string         // free-form markdown, no length cap
    Tags       []string       // free-form, sorted; e.g. ["plan-divergence","secret-shape"]
    Resolves   string         // optional: ID of a prior Note this resolves
    EmittedAt  time.Time
    Round      uint64         // the Round in which this Note was published
    Meta       map[string]any // free-form structured payload
}
```

### `Workspace` (workspace.go)

```go
// Workspace is the shared mutable view. RWMutex-protected slice of Notes
// + per-Lobe last-published index + pub-sub via hub.Bus events.
//
// Invariants:
//   - Notes is append-only. Resolution is a NEW Note with Resolves=parentID.
//   - All writes emit hub.Event of type "cortex.note.published".
//   - All persistence writes go through bus.Bus.Append (durable before return).
type Workspace struct { /* unexported fields */ }

// NewWorkspace constructs a Workspace bound to a hub.Bus and an optional
// bus.Bus (durable WAL). If durable is nil, persistence is skipped (in-memory).
func NewWorkspace(events *hub.Bus, durable *bus.Bus) *Workspace

// Publish appends a Note. Returns the Note with ID + Round populated.
// Thread-safe. Emits hub.Event{Type:"cortex.note.published"} synchronously
// and writes to bus.Bus durably before returning.
func (w *Workspace) Publish(ctx context.Context, n Note) (Note, error)

// Snapshot returns a copy of all unresolved Notes (Resolves chain followed),
// sorted by Severity desc then EmittedAt asc. Safe to retain.
func (w *Workspace) Snapshot() []Note

// Unresolved returns the subset of Snapshot() with Severity==SevCritical
// AND no resolving Note. Drives PreEndTurnCheckFn.
func (w *Workspace) UnresolvedCritical() []Note

// Drain returns all Notes with Round >= sinceRound and increments the
// internal "drained-up-to" cursor. Used by MidturnCheckFn to format the
// supervisor injection block.
func (w *Workspace) Drain(sinceRound uint64) ([]Note, uint64)

// Subscribe registers a callback fired synchronously after each Publish.
// Returns an unregister fn. Callback MUST be fast (<1ms) — long work
// belongs in a Lobe, not a subscriber.
func (w *Workspace) Subscribe(fn func(Note)) (cancel func())

// Replay rebuilds the in-memory state from durable WAL on startup.
// Idempotent. Errors abort daemon start.
func (w *Workspace) Replay(ctx context.Context) error
```

### `Spotlight` (workspace.go)

```go
// Spotlight is the GWT "currently broadcast" Note — the single highest-
// priority unresolved Note that the main thread should attend to. One per
// Workspace; updated atomically when Publish escalates priority.
type Spotlight struct { /* unexported */ }

// Current returns the Spotlight Note (or zero Note if empty).
func (s *Spotlight) Current() Note

// OnChange registers a callback for Spotlight-Note changes. Used by spec 4
// (TUI) to highlight the "loudest concern". Returns unregister fn.
func (s *Spotlight) OnChange(fn func(Note)) (cancel func())
```

### `Lobe` (lobe.go)

```go
// Lobe is the parallel-cognition specialist. Implementations run in a
// dedicated goroutine; they read message history (read-only) and write
// Notes via Workspace.Publish.
//
// Lobe contract:
//   - Run MUST observe ctx.Done(); return nil on graceful shutdown.
//   - Run MAY be called multiple times across daemon restarts; state is
//     externalized to Workspace + bus.WAL.
//   - Run MUST be panic-safe; a Lobe panic is logged + recovered + emits
//     hub.Event{Type:"cortex.lobe.panic"} but does NOT bring down the loop.
//
// Lobes do NOT implement persistence themselves — the runner handles it.
type Lobe interface {
    ID() string                       // stable; used as LobeID on Notes
    Description() string              // human-readable, for /status
    Kind() LobeKind                   // Deterministic | LLM
    Run(ctx context.Context, in LobeInput) error
}

// LobeKind drives semaphore acquisition: LLM Lobes bind against
// LobeSemaphore; Deterministic Lobes run free.
type LobeKind int

const (
    KindDeterministic LobeKind = iota
    KindLLM
)

// LobeInput is the read-only context handed to each Lobe per Round.
type LobeInput struct {
    Round       uint64
    History     []agentloop.Message  // current conversation; deep-copied
    Workspace   WorkspaceReader      // read-only Workspace handle
    Provider    provider.Provider    // model client (Lobes use as needed)
    Bus         *hub.Bus             // for emitting status events
}

// WorkspaceReader is the read-only subset Lobes get. Forces the contract
// "Lobes WRITE only via Publish; everything else is read-only".
type WorkspaceReader interface {
    Snapshot() []Note
    UnresolvedCritical() []Note
}

// LobeRunner wraps a Lobe in a goroutine + panic-recover + ctx-cancel
// shutdown. One runner per registered Lobe. Owned by Cortex.
type LobeRunner struct { /* unexported */ }

func NewLobeRunner(l Lobe, w *Workspace, sem *LobeSemaphore, bus *hub.Bus) *LobeRunner

// Start launches the goroutine. Idempotent.
func (r *LobeRunner) Start(ctx context.Context) error

// Stop signals shutdown via ctx and blocks until the Lobe goroutine exits
// (or until 5s timeout, in which case it logs and returns nil — we never
// hang the daemon on a misbehaving Lobe).
func (r *LobeRunner) Stop(ctx context.Context) error
```

### `Round` (round.go)

```go
// Round is the superstep barrier. Pattern:
//   1. Open(N) — declare N participating Lobes.
//   2. Each Lobe calls Done() exactly once per round.
//   3. Wait(ctx) blocks until all N have Done() OR deadline OR ctx cancelled.
//   4. Close() advances the Round counter; subsequent Publish carries new ID.
//
// Round is owned by Cortex and ticked by the agentloop's between-turn hook.
type Round struct { /* unexported */ }

func NewRound() *Round

// Open declares a new round with N expected participants. Returns the new
// round ID.
func (r *Round) Open(n int) uint64

// Done is called by each Lobe (or its runner) when its work for this round
// is complete. Idempotent per (round, lobe) — duplicate calls are no-ops
// and logged at DEBUG.
func (r *Round) Done(roundID uint64, lobeID string)

// Wait blocks until all N participants have Done() or ctx is cancelled or
// the deadline elapses. Returns nil on success, ctx.Err() on cancellation,
// ErrRoundDeadlineExceeded on timeout. Late Lobes are NOT cancelled — they
// keep running for the next round (Notes carry the Round they finished in).
func (r *Round) Wait(ctx context.Context, deadline time.Duration) error

// Close advances the round counter and clears participant tracking.
// Called by Cortex after Wait returns.
func (r *Round) Close(roundID uint64) uint64

// Current returns the active round ID without mutation.
func (r *Round) Current() uint64
```

### `Router` (router.go)

```go
// Router decides what to do with mid-turn user input. Calls Haiku 4.5
// once with 4 tools; the model picks one. The chosen tool's effect is
// applied to the in-flight turn.
type Router struct { /* unexported */ }

type RouterConfig struct {
    Provider     provider.Provider
    Model        string             // default "claude-haiku-4-5"
    MaxTokens    int                // default 1024
    SystemPrompt string             // default = DefaultRouterSystemPrompt
}

func NewRouter(cfg RouterConfig) *Router

// RouterDecision is one of {DecisionInterrupt, DecisionSteer,
// DecisionQueueMission, DecisionJustChat}. The matching field is populated.
type RouterDecision struct {
    Kind        DecisionKind
    Interrupt   *InterruptPayload   // populated when Kind==DecisionInterrupt
    Steer       *SteerPayload       // populated when Kind==DecisionSteer
    Queue       *QueuePayload       // populated when Kind==DecisionQueueMission
    JustChat    *ChatPayload        // populated when Kind==DecisionJustChat
    RawToolName string              // for debugging
}

// Route is called by the chat REPL when stdin/WS receives a user message
// during an in-flight turn. It builds the Router prompt (system prompt +
// 4 tool defs + last-N history snapshot + the new user input) and returns
// the decision. Synchronous — the REPL awaits it. Typical p99 ≤ 2s.
func (r *Router) Route(ctx context.Context, in RouterInput) (RouterDecision, error)

type RouterInput struct {
    UserInput    string                  // the raw new user message
    History      []agentloop.Message     // last N (default 10) messages
    Workspace    []Note                  // current Workspace.Snapshot()
}
```

### `Cortex` — the bundle (cortex.go)

```go
// Cortex bundles Workspace + Lobes + Round + Router + Pre-warm + Budget.
// One per agentloop.Loop. Lifecycle is tied to the Loop's parent ctx.
type Cortex struct { /* unexported */ }

type Config struct {
    EventBus    *hub.Bus            // required
    Durable     *bus.Bus            // optional; nil = in-memory only
    Provider    provider.Provider   // required for Router + pre-warm
    Lobes       []Lobe              // registered at New(); cannot mutate after Start
    MaxLLMLobes int                 // default 5; hard cap 8
    PreWarmModel    string          // default "claude-haiku-4-5"
    PreWarmInterval time.Duration   // default 4 * time.Minute
    RoundDeadline   time.Duration   // default 2 * time.Second; soft barrier
    RouterCfg       RouterConfig
}

func New(cfg Config) (*Cortex, error)

// Start launches all LobeRunners + the pre-warm pump. Idempotent.
func (c *Cortex) Start(ctx context.Context) error

// Stop signals shutdown to all Lobes + pre-warm pump. Blocks until exit
// or 10s timeout.
func (c *Cortex) Stop(ctx context.Context) error

// --- agentloop integration shims ---

// MidturnNote is wired into agentloop.Config.MidturnCheckFn. It opens a
// Round, lets Lobes run, waits up to RoundDeadline, then drains Notes
// and formats them as a single supervisor-note string (or "" if none).
func (c *Cortex) MidturnNote(messages []agentloop.Message, turn int) string

// PreEndTurnGate is wired into agentloop.Config.PreEndTurnCheckFn. Returns
// "" if no unresolved critical Note; otherwise returns a formatted error
// string that the loop will inject + force another turn.
func (c *Cortex) PreEndTurnGate(messages []agentloop.Message) string

// OnUserInputMidTurn is invoked by the chat REPL on stdin/WS user input
// while a turn is in-flight. It calls Router.Route, then enacts the
// chosen decision (e.g. cancels the per-turn context for Interrupt, or
// queues a Note for Steer, or no-ops for JustChat). The cmd/r1 chat
// REPL must wire this; the agentloop itself does not call it.
func (c *Cortex) OnUserInputMidTurn(ctx context.Context, userInput string, turnCancel context.CancelFunc) (RouterDecision, error)

// Workspace exposes the underlying Workspace for direct read access
// (TUI lanes, web UI).
func (c *Cortex) Workspace() *Workspace

// Spotlight exposes the underlying Spotlight.
func (c *Cortex) Spotlight() *Spotlight
```

## Integration points with `agentloop.Loop`

Three integration points; all backwards-compatible (cortex absent → behavior unchanged).

### 1. New `Cortex` field on `agentloop.Config`

`internal/agentloop/loop.go` gets one new field on `Config`:

```go
// Cortex, when non-nil, plumbs the parallel-cognition substrate into
// MidturnCheckFn + PreEndTurnCheckFn automatically. Operators may still
// set those hooks directly; if both are set, Cortex hooks compose with
// operator hooks (Cortex first, then operator; non-empty results
// concatenate with "\n\n" separator).
//
// Cortex's OnUserInputMidTurn is NOT plumbed by the loop — the chat REPL
// owns that wiring (it owns the per-turn cancel func).
Cortex *cortex.Cortex
```

### 2. `MidturnCheckFn` composition

Wire-up sketch (in `agentloop.New` or a new `agentloop.NewWithCortex`):

```go
if cfg.Cortex != nil {
    operatorHook := cfg.MidturnCheckFn
    cfg.MidturnCheckFn = func(messages []Message, turn int) string {
        cortexNote := cfg.Cortex.MidturnNote(messages, turn)
        if operatorHook == nil {
            return cortexNote
        }
        opNote := operatorHook(messages, turn)
        switch {
        case cortexNote == "" && opNote == "": return ""
        case cortexNote == "":                 return opNote
        case opNote == "":                     return cortexNote
        default:                                return cortexNote + "\n\n" + opNote
        }
    }
}
```

### 3. `PreEndTurnCheckFn` composition

```go
if cfg.Cortex != nil {
    operatorGate := cfg.PreEndTurnCheckFn
    cfg.PreEndTurnCheckFn = func(messages []Message) string {
        if msg := cfg.Cortex.PreEndTurnGate(messages); msg != "" {
            return msg // critical Note refuses end_turn — short-circuit
        }
        if operatorGate != nil {
            return operatorGate(messages)
        }
        return ""
    }
}
```

### 4. NEW: `OnUserInputMidTurn` (REPL-owned)

The chat REPL (currently `cmd/r1/chat_interactive_cmd.go`) must:

1. Hold the per-turn `cancelTurn` returned by `context.WithCancel(parentCtx)`.
2. Run a stdin reader goroutine concurrently with `loop.Run`.
3. On stdin input, call `cortex.OnUserInputMidTurn(ctx, line, cancelTurn)`.
4. The Router's chosen tool dictates next action:
   - `interrupt` → Router calls `cancelTurn()`; loop drops partial; REPL appends synthetic user message and starts a fresh turn.
   - `steer` → Router publishes a `SevAdvice` Note via `Workspace.Publish`; loop picks it up at next `MidturnCheckFn` boundary.
   - `queue_mission` → Router enqueues a mission via existing `mission.Runner` (out of cortex-core scope; the spec only describes the routing decision and its payload).
   - `just_chat` → Router publishes a `SevInfo` Note; UI shows it; loop unaffected.

**Spec 1 ships the routing + Note publication; spec 2 wires the chat REPL to actually call OnUserInputMidTurn.** Cortex-core only ships the function and a unit-tested fake-stdin demo.

## The Router — Haiku 4.5 with 4 tools

### Model

- **Default model:** `claude-haiku-4-5` (per `docs/decisions/index.md` D-2026-05-02-04 and D-C7).
- **MaxTokens:** 1024 (the Router only emits a tool call; no long prose).
- **Temperature:** 0 — deterministic routing.
- **Cache control:** system prompt + tool defs are cache-stable; reuse breakpoint shared with main thread (`agentloop.BuildCachedSystemPrompt` style).

### System prompt (verbatim — `cortex.DefaultRouterSystemPrompt`)

```
You are r1's mid-turn input router. The user has typed a message while the
main agent is in the middle of a turn. Your only job is to decide how that
message should be handled by calling EXACTLY ONE of the four tools below.

Decision rubric:

- Use `interrupt` ONLY when the new input contradicts, retracts, or makes
  the in-flight work unsafe (e.g. "stop", "wait that's wrong", "actually
  use Postgres not MySQL"). Interrupt cancels the live API stream and
  drops partial work.
- Use `steer` when the new input adds context, clarifies, or nudges
  direction without invalidating in-flight work (e.g. "also add a tests
  folder", "make sure it's typed strictly"). Steer attaches a soft note
  the main agent reads at the next safe boundary.
- Use `queue_mission` when the new input is a fully separate task that
  should run after the current one (e.g. "after this, also fix the bug
  in auth.go"). Queue does not affect the in-flight turn at all.
- Use `just_chat` when the new input is conversational — a question to
  YOU (the router) about what's happening, an acknowledgement, a thank-
  you, etc. Just_chat surfaces a short reply in the UI without touching
  the main agent.

Hard rules:
1. You MUST call exactly one tool. Do not emit text before or after.
2. Bias toward `steer` when in doubt — interrupt is destructive.
3. If the user says any of {"stop", "cancel", "abort", "halt"} alone or
   as the first word, you MUST use `interrupt`.
4. Never invoke a tool you were not given.
```

### Tool schemas (verbatim — JSON Schema as accepted by the Anthropic Messages API)

```json
[
  {
    "name": "interrupt",
    "description": "Cancel the in-flight turn and inject a synthetic user message. Use only for retractions, hard stops, or contradictions.",
    "input_schema": {
      "type": "object",
      "properties": {
        "reason":   {"type": "string", "description": "≤200 chars, why interrupting"},
        "new_direction": {"type": "string", "description": "the synthetic-user-message body the main agent will see on resume"}
      },
      "required": ["reason", "new_direction"]
    }
  },
  {
    "name": "steer",
    "description": "Attach a soft note the main agent will read at the next safe boundary. Use for clarifications, additions, nudges.",
    "input_schema": {
      "type": "object",
      "properties": {
        "severity": {"type": "string", "enum": ["info", "advice", "warning"], "description": "soft-note priority; 'critical' is reserved for system Lobes"},
        "title":    {"type": "string", "description": "≤80 chars"},
        "body":     {"type": "string", "description": "free-form markdown"}
      },
      "required": ["severity", "title", "body"]
    }
  },
  {
    "name": "queue_mission",
    "description": "Enqueue a separate task to run after the current one. Does not affect the in-flight turn.",
    "input_schema": {
      "type": "object",
      "properties": {
        "brief":    {"type": "string", "description": "the task brief; ≤2000 chars"},
        "priority": {"type": "string", "enum": ["low", "normal", "high"], "default": "normal"}
      },
      "required": ["brief"]
    }
  },
  {
    "name": "just_chat",
    "description": "Conversational reply. Does not touch the main agent. Used for status questions, acknowledgements, off-topic.",
    "input_schema": {
      "type": "object",
      "properties": {
        "reply": {"type": "string", "description": "a short conversational reply, ≤400 chars"}
      },
      "required": ["reply"]
    }
  }
]
```

## Drop-partial interrupt protocol

Verbatim Go pseudocode the implementing agent must follow. Extracted from `RT-CANCEL-INTERRUPT.md` §6 and adapted to r1's idioms.

```go
// In internal/cortex/interrupt.go.

// RunTurnWithInterrupt wraps a single agentloop turn so it can be
// interrupted mid-stream. Used by the chat REPL when Router decides
// DecisionInterrupt. The agentloop.Loop itself does NOT use this — it
// only knows about ctx cancellation. This helper is the glue that
// turns a Router interrupt into a clean cancel + drain + replay.
//
// PATTERN A from RT-CANCEL-INTERRUPT (drop partial). We never persist
// a partial assistant message; the committed history ends with the
// most recent user message (which is valid for the API).
func RunTurnWithInterrupt(
    parentCtx context.Context,
    msgs []agentloop.Message,
    callAPI func(ctx context.Context, msgs []agentloop.Message) (resp <-chan StreamEvent, done <-chan error),
    interrupt <-chan InterruptPayload,
) (next []agentloop.Message, reason StopReason, err error) {

    turnCtx, cancelTurn := context.WithCancel(parentCtx)
    defer cancelTurn() // ALWAYS — defensive

    respCh, doneCh := callAPI(turnCtx, msgs)

    var partial agentloop.Message // accumulator OUTSIDE committed history

    // 30s ping-based idle watchdog (RT-CONCURRENT-CLAUDE-API)
    lastEvent := time.Now()
    var lastEventMu sync.Mutex
    watchdogDone := make(chan struct{})
    go func() {
        defer close(watchdogDone)
        t := time.NewTicker(5 * time.Second)
        defer t.Stop()
        for {
            select {
            case <-turnCtx.Done(): return
            case <-t.C:
                lastEventMu.Lock()
                stale := time.Since(lastEvent) > 30*time.Second
                lastEventMu.Unlock()
                if stale {
                    cancelTurn()
                    return
                }
            }
        }
    }()

    for {
        select {
        case ev, ok := <-respCh:
            if !ok { continue } // chan closed; wait for doneCh
            lastEventMu.Lock(); lastEvent = time.Now(); lastEventMu.Unlock()
            partial = accumulate(partial, ev) // see RT-CANCEL-INTERRUPT §1

        case finalErr := <-doneCh:
            <-watchdogDone
            if finalErr != nil { return msgs, StopErr, finalErr }
            // Normal completion — commit partial as the assistant message.
            return append(msgs, partial), StopNormal, nil

        case ip := <-interrupt:
            cancelTurn()         // (1) tear down stream
            // (2) Drain BOTH the response channel AND the done channel.
            //     We MUST consume until both close, otherwise the SSE
            //     reader goroutine leaks (RT-CANCEL-INTERRUPT §4).
            for range respCh {}  // drain remaining buffered events
            <-doneCh             // wait for SSE reader to exit cleanly
            <-watchdogDone

            // (3) Pattern A — discard `partial` entirely. Never persist
            //     half-emitted tool_use blocks; would 400 the next API call.

            // (4) Append synthetic user message describing the interrupt.
            return append(msgs, agentloop.Message{
                Role: "user",
                Content: []agentloop.ContentBlock{{
                    Type: "text",
                    Text: fmt.Sprintf(
                        "<system-interrupt source=%q severity=%q>\n%s\n</system-interrupt>\n\nNew direction: %s",
                        ip.Source, ip.Severity, ip.Reason, ip.NewDirection,
                    ),
                }},
            }), StopInterrupted, nil

        case <-parentCtx.Done():
            cancelTurn()
            for range respCh {}
            <-doneCh
            <-watchdogDone
            return msgs, StopCancelled, parentCtx.Err()
        }
    }
}
```

Key invariants (the implementing agent MUST verify in tests):

- `partial` is **never** appended to `msgs` on the interrupt path.
- `cancelTurn()` is called before draining channels; channels are drained before returning.
- The watchdog goroutine always exits — its `defer close(watchdogDone)` guarantees this.
- The synthetic user message is the FINAL message in the returned slice, so the next API call is `user`-terminated and valid.

## Cache pre-warm pump

Per `RT-CONCURRENT-CLAUDE-API.md` §2 ("Pre-warming for Concurrent Scenarios"):

### When it fires

- **On Cortex.Start():** one warming request is sent before any Lobe goroutine is launched.
- **Every 4 minutes thereafter** (5-minute TTL minus a 1-minute margin per D-C5). Implemented as a `time.Ticker` goroutine inside `prewarm.go`.
- **On Lobe registration changes:** if (future) a Lobe is hot-loaded, fire one warming request immediately; spec 1 doesn't support hot-loading, so this is a TODO marker.

### What the warming request looks like

```go
// Identical builder to the main thread's buildRequest, but with
// MaxTokens=0 (Anthropic returns immediately after cache write) and
// a constant trivial user message.
chatReq := provider.ChatRequest{
    Model:        cfg.PreWarmModel,                      // claude-haiku-4-5
    SystemRaw:    agentloop.BuildCachedSystemPrompt(...), // SAME breakpoint as main
    Messages:     []provider.ChatMessage{{Role:"user", Content: warmupContent("warm")}},
    MaxTokens:    1, // Anthropic API rejects 0 — use 1 as the closest legal value
    Tools:        agentloop.SortToolsDeterministic(toolDefs), // SAME order as main
    CacheEnabled: true,
}
```

### Cache breakpoint sharing

The pre-warm and main calls **must** present byte-identical:

1. System prompt block (same `cache_control: ephemeral` breakpoint).
2. Tool list (same alphabetical sort order — `agentloop.SortToolsDeterministic` is the canonical sort).
3. (Lobes inherit the same breakpoint; per-Lobe role suffixes are separate cache breakpoints with 1-hour TTL — deferred to spec 2.)

### Failure handling

- Network/API error during pre-warm: log at WARN, emit `hub.Event{Type:"cortex.prewarm.failed"}`, continue. **Do NOT fail Cortex.Start().** Pre-warm is best-effort cost optimization, not correctness.

## Budget controller

### `LobeSemaphore` (budget.go)

```go
// LobeSemaphore is a buffered chan used by KindLLM Lobes to throttle
// concurrent Anthropic API calls. KindDeterministic Lobes never acquire.
//
// Capacity = Cortex.Config.MaxLLMLobes (default 5, hard cap 8).
type LobeSemaphore struct { /* unexported */ }

func NewLobeSemaphore(capacity int) *LobeSemaphore // panics if capacity > 8

// Acquire blocks until a slot is free or ctx cancels.
func (s *LobeSemaphore) Acquire(ctx context.Context) error

// Release frees a slot. MUST be called for every successful Acquire.
func (s *LobeSemaphore) Release()
```

### Per-turn token budget

- **Rule:** Lobes collectively cap at **30% of the main thread's output tokens** for the most recent turn (D-2026-05-02-06).
- **Enforcement:** A `BudgetTracker` in `budget.go` accumulates per-Lobe token usage from `provider.ChatStream` responses and is reset at each `Round.Close()`. When the cap is hit, subsequent `Acquire()` calls block until next round even if the semaphore has slots free.
- **Escalation Haiku → Sonnet:** Allowed in two cases (D-C7):
  1. A Lobe emits a `SevCritical` Note within the same Round.
  2. Operator config tags a Lobe `escalate_on_failure: true` AND its previous run errored.
- **Escalation enforcement:** At Round Open, the Cortex consults `EscalationPolicy.ResolveModel(lobeID, lastRoundOutcome)` and passes the result via `LobeInput.Provider`. The Lobe itself doesn't decide — the policy does.

```go
type BudgetTracker struct { /* unexported */ }
func (b *BudgetTracker) Charge(lobeID string, usage stream.TokenUsage)
func (b *BudgetTracker) RoundOutputBudget() int // 30% of main's last turn
func (b *BudgetTracker) Exceeded() bool
func (b *BudgetTracker) ResetRound()
```

## Workspace persistence (write-through to bus/WAL)

Every `Workspace.Publish` performs:

1. Append to in-memory slice under `Lock()`.
2. Append to `bus.Bus` (durable WAL — call `bus.Bus.Publish(evt)` per `internal/bus/bus.go:321`; the public method is `Publish`, not `Append`) BEFORE releasing `Lock()`. Failure → return error, no in-memory append.
3. Emit `hub.Event{Type:"cortex.note.published"}` after lock release. Failure → log only; the Note is already durable.

### Note replay on session resume

`Workspace.Replay(ctx)` reads the durable WAL and rebuilds the in-memory slice. Implementation:

1. Call `durable.Replay(pattern, 0, handler)` (see `internal/bus/bus.go:702`) with a `bus.Pattern` matching event type `"cortex.note.published"`. Per-session scoping is the responsibility of the caller (one `bus.Bus` instance per session via `bus.New(dir)` rooted at the session WAL dir; see `internal/bus/bus.go:289`); the cortex package itself does not filter on session ID.
2. For each entry, JSON-decode the `Note` from `evt.Payload` (which is `json.RawMessage`, see `internal/bus/bus.go:114`).
3. Append in WAL order (which equals publish order — bus is monotonic).
4. After replay, advance `drainedUpTo` cursor to current `len(notes)` so the **next** turn's Drain returns nothing (resumed sessions don't re-inject stale notes; only NEW notes from post-resume work get drained).
5. Replay is idempotent — calling twice is a no-op (cursor remains at end).

### Bus event types emitted

These events are emitted on the in-process `hub.Bus` (`internal/hub/bus.go`); the `Custom` column is `hub.Event.Custom map[string]any` (see `internal/hub/events.go:171`). Events written to the durable `bus.Bus` (`internal/bus/bus.go`) use `bus.Event.Payload json.RawMessage` (see `internal/bus/bus.go:114`) — the `cortex.note.published` durable event payload is the JSON-marshaled `Note` (see item 22).

| Event type | When | Payload (hub.Event.Custom) |
|------------|------|---------|
| `cortex.note.published` | Workspace.Publish completes | `Custom["note"]` = serialized `Note` (also written to durable bus.Bus with `bus.Event.Payload` = same JSON) |
| `cortex.spotlight.changed` | Spotlight Note changes | `Custom["from"]`, `Custom["to"]` = Note IDs |
| `cortex.lobe.started` | LobeRunner.Start launches goroutine | `Custom["lobe_id"]` |
| `cortex.lobe.panic` | Lobe panic recovered | `Custom["lobe_id"]`, `Custom["err"]` |
| `cortex.round.opened` | Round.Open | `Custom["round"]`, `Custom["n"]` |
| `cortex.round.closed` | Round.Close | `Custom["round"]`, `Custom["timed_out"]` |
| `cortex.router.decided` | Router.Route returns | `Custom["kind"]`, `Custom["latency_ms"]` |
| `cortex.prewarm.fired` | Pre-warm pump completes | `Custom["cache_status"]` |
| `cortex.prewarm.failed` | Pre-warm error | `Custom["err"]` |

Spec 3 (lanes-protocol) will define the wire format consumers see; spec 1 only emits.

## Test plan

### Unit tests (table-driven, race-detector enabled)

All new packages MUST pass `go test -race ./internal/cortex/...`.

| Test file | Coverage |
|-----------|----------|
| `workspace_test.go` | Publish appends; Snapshot returns deep copies; Drain advances cursor; UnresolvedCritical filters correctly; concurrent Publish from 100 goroutines under -race |
| `lobe_test.go` | LobeRunner.Start launches goroutine exactly once; Stop blocks until exit; panic in Lobe.Run is recovered + emits `cortex.lobe.panic` |
| `round_test.go` | Open/Done/Wait/Close happy path; Wait deadline exceeded; ctx cancellation; duplicate Done is no-op; Close advances counter |
| `router_test.go` | Mocked Provider returns each of the 4 tool calls in turn; Route correctly populates each payload variant; "stop" alone forces interrupt; missing tool call returns error |
| `interrupt_test.go` | Drop-partial: feed a fake stream cut mid-`tool_use`; assert returned messages slice is `user`-terminated and never contains the partial assistant message; assert `streamDone` is drained; assert watchdog goroutine exits |
| `prewarm_test.go` | Pre-warm fires on Start; ticker fires every PreWarmInterval (use a fake clock); failure is logged but Start succeeds |
| `budget_test.go` | LobeSemaphore: acquire/release; capacity panics for >8; Acquire blocks under contention; ctx cancel returns error. BudgetTracker: 30% cap blocks acquisition past threshold; ResetRound clears |
| `persist_test.go` | Publish writes to fake bus.Bus; Replay rebuilds state from WAL; cursor advances correctly so post-replay Drain returns nothing |

### Integration test: `cortex_integration_test.go`

End-to-end: 3 fake Lobes (1 deterministic, 2 LLM with mocked providers) all publish Notes during a Round. Cortex.MidturnNote drains them and returns a single formatted supervisor-note string; assert string contains all 3 Lobe IDs and is sorted by Severity desc. **MUST run under `-race` and have ≥3 goroutines per Lobe to exercise the WaitGroup barrier.**

### Acceptance criteria

- WHEN `Cortex.Start` is called THE SYSTEM SHALL launch one goroutine per registered Lobe and emit `cortex.lobe.started` for each.
- WHEN a Lobe panics THE SYSTEM SHALL recover, emit `cortex.lobe.panic`, and keep all other Lobes running.
- WHEN `Workspace.Publish` is called concurrently from N goroutines THE SYSTEM SHALL produce N distinct Note IDs in monotonic Round-then-time order (verified under -race).
- WHEN any unresolved `SevCritical` Note exists THE SYSTEM SHALL cause `Cortex.PreEndTurnGate` to return a non-empty message, refusing `end_turn`.
- WHEN `OnUserInputMidTurn` receives "stop" THE SYSTEM SHALL return a `DecisionInterrupt` from the Router and call `turnCancel()` exactly once.
- WHEN the pre-warm pump fails THE SYSTEM SHALL log + emit `cortex.prewarm.failed` AND continue Cortex.Start successfully.
- WHEN `agentloop.Config.Cortex == nil` THE SYSTEM SHALL behave identically to today (no regressions in `internal/agentloop/loop_test.go`, `pre_end_turn_test.go`, `compact_test.go`).

## Risks & Gotchas

The implementing agent MUST be aware of these subtleties up front:

1. **Import cycle agentloop ↔ cortex.** The cortex package imports `agentloop` for `Message`/`ContentBlock`; agentloop cannot import cortex directly. Item 16 resolves this by defining a small `agentloop.CortexHook` interface inside the agentloop package; `*cortex.Cortex` satisfies it structurally without agentloop importing cortex. Do NOT shortcut this with an `interface{}` field — the type-assertion at hook-call time would defeat compile-time safety.
2. **CortexHook composition order.** Cortex hook runs FIRST, operator hook SECOND, joined with `"\n\n"`. The order is load-bearing: PreEndTurnCheck short-circuits on the first non-empty return, so cortex's critical-Note gate must fire before any operator-defined gate. If you reverse the order, an operator-permissive gate could let the loop end_turn while a SevCritical Note is unresolved.
3. **Drop-partial drain ordering.** On interrupt, `cancelTurn()` MUST be called BEFORE draining `respCh`/`doneCh`; otherwise the SSE reader keeps producing events and `respCh` may never close. After cancel, both channels MUST be drained to completion or the SSE goroutine leaks (RT-CANCEL-INTERRUPT §4).
4. **Partial-tool_use never persisted.** `agentloop.Message` has no `Meta` field — partial-block tracking must live in a local map inside `RunTurnWithInterrupt`, not on the Message. Persisting an incomplete `tool_use` to history will 400 the next API call (Anthropic rejects malformed `input_json`).
5. **Workspace.Publish persistence-before-emit ordering.** Durable WAL write happens UNDER the workspace mutex; hub event emit happens AFTER lock release. Inverting this risks emitting events for Notes that fail to persist (consumers see ghosts) OR holding the mutex during async fan-out (deadlock if a subscriber calls Workspace.Snapshot).
6. **Round.Wait late-Lobe handling.** Lobes that miss the Round deadline are NOT cancelled — they keep running and their next Note carries a later `Round` value. This is intentional: cancelling a partway-LLM-call wastes API spend. Tests must assert that a slow Lobe does NOT lose its Note (it just lands on the next round's Drain).
7. **LobeSemaphore vs BudgetTracker interaction.** Item 21 specifies that `LobeRunner` checks `tracker.Exceeded()` AFTER `Acquire` returns. If you check before, you risk a race where the budget appears unexceeded at check-time but exceeds during Acquire's blocking wait. Always Acquire first, then check, then Release-and-skip if exceeded.
8. **Pre-warm cache breakpoint parity.** The pre-warm request and main thread's `buildRequest` must produce byte-identical system blocks AND tool ordering. Use `agentloop.SortToolsDeterministic` and `agentloop.BuildCachedSystemPrompt`. A 1-byte drift = 0% cache hit + zero cost savings + silent degradation. Add a test that diffs the two byte slices.
9. **bus.Bus.Publish vs hub.Bus.Emit confusion.** Two different "buses" with overlapping names. `bus.Bus.Publish` (in `internal/bus/`) writes durably to a WAL. `hub.Bus.Emit` (in `internal/hub/`) is in-process pub-sub. Cortex uses BOTH: durable for Notes (replayable), in-process for ephemeral lifecycle events. Mixing them up will silently drop persistence.
10. **Router determinism caveat.** Temperature=0 does NOT guarantee identical outputs across model versions — Anthropic may rev the underlying weights without changing the model ID. Tests MUST use a mocked provider that returns canned tool calls, never a live API call.

## Out of scope (explicit)

- **Specific Lobes** (MemoryRecallLobe, WALKeeperLobe, RuleCheckLobe, PlanUpdateLobe, ClarifyingQLobe, MemoryCuratorLobe). All deferred to **spec 2: cortex-concerns**. Spec 1 ships only the `Lobe` interface and a fake `EchoLobe` test stub.
- **Lane wire format** (the JSON-RPC envelope on the WebSocket / SSE pipe). Deferred to **spec 3: lanes-protocol**. Spec 1 only emits `hub.Bus` events; spec 3 adopts the same event types and defines the on-wire shape.
- **TUI rendering** of Lobe lanes (Bubble Tea, lipgloss, AdaptiveColor). Deferred to **spec 4: tui-lanes**.
- **Web/desktop UI** for Lobes. Deferred to specs 5–7.
- **Agentic test harness** for cortex (Playwright-MCP / teatest integration). Deferred to **spec 8: agentic-test-harness**.
- **Mission queue integration** for `RouterDecision.Queue`. Spec 1 returns the decision payload but does NOT enqueue against `mission.Runner`; that wiring lives in spec 2 (where the chat REPL gains `OnUserInputMidTurn` plumbing).
- **Per-Lobe model overrides** beyond the Haiku→Sonnet escalation policy. Operator-config-driven model picking is spec 2.
- **Hot Lobe registration / removal at runtime.** Spec 1 supports register-at-`New`, run-until-`Stop`. Hot-load is a future spec.
- **Cross-session Workspace sharing.** Each session has its own Workspace; the WAL is scoped by session ID.

## Implementation Checklist

Each item is self-contained. The implementing agent has only the item plus links to the research files. Each item names exact files to create/modify, exact functions to add, and the tests it must produce.

### Workspace primitives

1. [ ] **Create the `internal/cortex/` package skeleton.** Add `internal/cortex/cortex.go` containing only the `package cortex` declaration plus a top-of-file doc comment summarizing the GWT/cortex pattern (1-paragraph; cite `specs/research/synthesized/cortex.md`). Add a placeholder `New(cfg Config) (*Cortex, error)` that returns `nil, errors.New("not implemented")` so dependents can compile-link. No tests yet.

2. [ ] **Define `Note` + `Severity` in `internal/cortex/workspace.go`.** Implement the `Note` struct and `Severity` constants verbatim from §"Note" above. Add a `func (n Note) Validate() error` that rejects empty `LobeID`, empty `Title` >80 runes, or unknown `Severity`. Write `workspace_test.go::TestNoteValidate` covering all 3 failure modes + happy path.

3. [ ] **Implement `Workspace` struct in `internal/cortex/workspace.go`.** Internal fields: `mu sync.RWMutex`, `notes []Note`, `seq uint64`, `drainedUpTo uint64`, `events *hub.Bus`, `durable *bus.Bus`, `subs []func(Note)`. Constructor `NewWorkspace(events *hub.Bus, durable *bus.Bus) *Workspace`. Follow the RWMutex/copy-on-read pattern from `internal/conversation/runtime.go:67-99`.

4. [ ] **Implement `Workspace.Publish` in `internal/cortex/workspace.go`.** Acquire `Lock`, validate the Note (item 2), assign `ID = fmt.Sprintf("note-%d", w.seq)` and `w.seq++`, set `EmittedAt = time.Now().UTC()` if zero, set `Round = w.currentRound` (set externally by Cortex; default 0), append to `notes`, persist via `persist.go::writeNote(w.durable, n)` (item 22), release `Lock`, then emit `hub.Event{Type:"cortex.note.published"}` with `Custom["note"]=n` and call all subscribers. Test in `workspace_test.go::TestPublishConcurrent`: 100 goroutines, each publishes once; verify all 100 IDs are unique and the `notes` slice has exactly 100 entries. Run under `-race`.

5. [ ] **Implement `Workspace.Snapshot`, `Workspace.UnresolvedCritical`, `Workspace.Drain` in `internal/cortex/workspace.go`.** All three acquire `RLock` and return deep copies. `UnresolvedCritical` filters Severity==SevCritical AND no later Note has `Resolves==n.ID`. `Drain(sinceRound)` returns Notes with `Round >= sinceRound`, advances `drainedUpTo`, returns `(notes, newCursor)`. Test in `workspace_test.go::TestSnapshotDeepCopy` (mutate returned slice; assert internal state untouched), `TestUnresolvedCriticalFilter` (publish critical, then resolving Note, assert empty), `TestDrainAdvancesCursor`.

6. [ ] **Implement `Workspace.Subscribe` in `internal/cortex/workspace.go`.** Returns `cancel func()` that removes the callback. Subscribers fire synchronously inside `Publish` AFTER lock release. Document the <1ms contract in the Subscribe doc comment. Test `TestSubscribeUnsubscribe` covering multi-subscriber fan-out and unregister.

7. [ ] **Implement `Spotlight` in `internal/cortex/workspace.go`.** Internal fields: `mu sync.Mutex`, `current Note`, `subs []func(Note)`. The `Workspace.Publish` call site (item 4) must invoke `spotlight.maybeUpdate(n)` after persistence, where `maybeUpdate` upgrades `current` if `n.Severity` ranks higher (Critical > Warning > Advice > Info; ties broken by `EmittedAt` desc) and is unresolved. Emit `hub.Event{Type:"cortex.spotlight.changed"}` with `Custom["from"]/Custom["to"]` IDs on change. Test `TestSpotlightUpgrade`: publish 3 Notes of increasing severity; assert `Spotlight.Current` matches the highest at each step.

### Lobe primitives

8. [ ] **Define `Lobe`, `LobeKind`, `LobeInput`, `WorkspaceReader` in `internal/cortex/lobe.go`.** Verbatim from §"Lobe" above. Add a private adapter `workspaceReader struct { w *Workspace }` that satisfies `WorkspaceReader` by delegating to item 5's methods. Add a stub `EchoLobe` (in `lobe_test.go`, NOT in production code) that publishes `Note{Severity:SevInfo, Title:"echo"}` once per Run and exits — used by item 9's tests.

9. [ ] **Implement `LobeRunner` in `internal/cortex/lobe.go`.** Internal fields: `lobe Lobe`, `ws *Workspace`, `sem *LobeSemaphore`, `bus *hub.Bus`, `started atomic.Bool`, `stopOnce sync.Once`, `stopped chan struct{}`. `Start(ctx)` launches one goroutine that: emits `cortex.lobe.started`, acquires the semaphore IFF `lobe.Kind()==KindLLM`, defers Release, runs `lobe.Run(ctx, in)` inside a `defer recover()` block, on panic emits `cortex.lobe.panic`. `Stop(ctx)` cancels via context (Cortex owns the ctx passed to Start) and waits on `stopped` chan with 5s timeout. Test `lobe_test.go::TestLobeRunnerLifecycle` (Start once; Stop blocks until done), `TestLobeRunnerPanic` (panicking Lobe emits event AND stops cleanly), `TestLobeRunnerSemaphore` (LLM Lobes acquire; Deterministic skip).

### Round superstep barrier

10. [ ] **Implement `Round` in `internal/cortex/round.go`.** Internal fields: `mu sync.Mutex`, `current uint64`, `participants map[uint64]map[string]bool`, `done map[uint64]chan struct{}`, `expected map[uint64]int`. Methods per §"Round" above. `Done` decrements `expected[roundID]`; when it hits 0, close `done[roundID]`. `Wait` selects on `done[roundID]` vs `time.After(deadline)` vs `ctx.Done()`. Define `var ErrRoundDeadlineExceeded = errors.New("cortex: round deadline exceeded")`. Test `round_test.go::TestRoundHappy` (3 participants Done, Wait returns nil), `TestRoundDeadline`, `TestRoundCtxCancel`, `TestRoundDuplicateDone` (no panic, no double-decrement). Run under `-race`.

11. [ ] **Wire `Round` to `Workspace`.** Add `Workspace.SetRound(roundID uint64)` (called by Cortex per round) so subsequent `Publish` calls stamp the current round on each Note. Update item 4's test to verify `Note.Round` reflects the active round. Cross-ref: item 4's Publish reads the current round; item 10's `Round.Open` is the only writer.

### Cortex bundle + Workspace integration

12. [ ] **Implement `Cortex.New` in `internal/cortex/cortex.go`.** Validate config (require `EventBus`, `Provider`; default `MaxLLMLobes=5`, `RoundDeadline=2s`, `PreWarmInterval=4*time.Minute`, `PreWarmModel="claude-haiku-4-5"`); panic if `MaxLLMLobes>8`; build `Workspace` (item 3), `Round` (item 10), `LobeSemaphore` (item 19), `Router` (item 17), `BudgetTracker` (item 20), `LobeRunner`s for each Lobe (item 9). Replace the placeholder from item 1. Test `cortex_test.go::TestNewValidates` (missing EventBus → error; MaxLLMLobes=9 → panic).

13. [ ] **Implement `Cortex.Start` and `Cortex.Stop` in `internal/cortex/cortex.go`.** Start: emit `cortex.prewarm.fired` after the synchronous initial pre-warm (item 18), launch the pre-warm ticker goroutine, launch each `LobeRunner.Start(ctx)`. Stop: cancel the ctx (held internally), wait on every LobeRunner.Stop with 10s timeout. Idempotent. Test `cortex_test.go::TestStartStopIdempotent`.

14. [ ] **Implement `Cortex.MidturnNote(messages, turn)` in `internal/cortex/cortex.go`.** Calls `Round.Open(len(c.lobes))`, sets `Workspace.SetRound(roundID)`, signals each LobeRunner to begin a new tick (via a per-runner `tick chan struct{}` added in item 9 — extend that struct now and back-patch the test), waits via `Round.Wait(ctx, c.cfg.RoundDeadline)`, drains via `Workspace.Drain(roundID)`, formats Notes as a single string of the form `"[CORTEX NOTES — round %d]\n- [%s] %s: %s\n..."` sorted by Severity desc then EmittedAt asc, calls `Round.Close(roundID)`, returns the string ("" if empty). Test `cortex_test.go::TestMidturnNoteFormat` with 2 fake Lobes publishing predictable Notes.

15. [ ] **Implement `Cortex.PreEndTurnGate(messages)` in `internal/cortex/cortex.go`.** Calls `Workspace.UnresolvedCritical()`. If empty, return "". Else format as `"[CRITICAL CORTEX NOTES — resolve before ending turn]\n- %s: %s\n..."` and return. Test `cortex_test.go::TestPreEndTurnGateBlocks`: publish a SevCritical Note, assert non-empty return; publish a resolving Note, assert empty.

16. [ ] **Wire `Cortex` into `agentloop.Config`.** Modify `internal/agentloop/loop.go` to add the `Cortex *cortex.Cortex` field exactly as specified in §"Integration points" (item: NEW `Cortex` field). To avoid an import cycle (cortex imports agentloop for `Message`), introduce a small `agentloop.CortexHook` interface with two methods (`MidturnNote([]Message,int) string`, `PreEndTurnGate([]Message) string`) and store a `CortexHook` on `Config`, not `*cortex.Cortex` directly. The cortex.Cortex satisfies CortexHook automatically. Modify `agentloop.Config.defaults()` (or extend `agentloop.RunWithHistory`) to compose CortexHook with operator hooks per §"Integration points" §2 and §3 (cortex first, operator second, "\n\n" join). Test `internal/agentloop/loop_cortex_test.go` with a fake CortexHook returning `"X"` and operator hook returning `"Y"`; assert MidturnCheck output is `"X\n\nY"` and PreEndTurn short-circuits when CortexHook returns non-empty.

### Router

17. [ ] **Implement `Router` in `internal/cortex/router.go`.** Define `DefaultRouterSystemPrompt` as a const verbatim from §"The Router" above. Define `routerTools []provider.ToolDef` with the 4 tool schemas verbatim. `NewRouter(cfg RouterConfig)` validates Provider non-nil; defaults `Model="claude-haiku-4-5"`, `MaxTokens=1024`. `Route(ctx, in)` builds a `provider.ChatRequest` with system prompt + 4 tools + last-10-message snapshot from `in.History` + a final user message containing the new input + a one-line summary of `in.Workspace`, calls `provider.ChatStream`, parses the response for exactly one `tool_use` block, populates the matching `RouterDecision` payload, emits `hub.Event{Type:"cortex.router.decided"}` with `Custom["kind"]` and latency. Errors: 0 tool calls → `errors.New("cortex/router: model emitted no tool call")`; >1 → use the first and log WARN. Test `router_test.go::TestRouteAllFour` (mocked provider, one test per tool), `TestRouteStopForcesInterrupt` (input "stop" → assert DecisionInterrupt regardless of mock — actually a no-op for cortex-core because the model decides; the system-prompt rule is the enforcement; just assert the prompt contains the rule and a "stop"-input mock returns Interrupt).

### Drop-partial interrupt protocol

18. [ ] **Implement `RunTurnWithInterrupt` in `internal/cortex/interrupt.go`.** Verbatim per §"Drop-partial interrupt protocol" above. Define `InterruptPayload` (Source, Severity, Reason, NewDirection strings), `StopReason` (constants StopNormal, StopInterrupted, StopErr, StopCancelled), and a small `StreamEvent` struct mirroring the subset the loop consumes. Provide a private helper `accumulate(partial agentloop.Message, ev StreamEvent) agentloop.Message` that appends text deltas and tracks per-block completion. Note: `agentloop.Message` (see `internal/agentloop/loop.go:158-162`) has no `Meta` field — the partial-tracking state must live in a local map inside `RunTurnWithInterrupt` keyed by block index (`map[int]bool` for "this block saw `content_block_stop`"), NOT inside `Message`. On the interrupt path, drop any block from `partial.Content` whose index is missing from the "completed" set, matching the Cline pattern in RT-CANCEL-INTERRUPT §3. Test `interrupt_test.go::TestDropPartialOnInterrupt` (feed a fake stream cut mid-`input_json_delta`; assert returned msgs has no assistant message, ends with synthetic user message), `TestStreamDoneDrained` (assert respCh drain happens before return), `TestWatchdogIdle` (no events for 31s with a fake clock → cancel fires).

### Cache pre-warm pump

19. [ ] **Implement the pre-warm pump in `internal/cortex/prewarm.go`.** Function `runPreWarmOnce(ctx, p provider.Provider, model, systemPrompt string, tools []provider.ToolDef) error` builds the request per §"Cache pre-warm pump" (MaxTokens=1, system+tools identical to main thread, message=`"warm"`). On success, emit `hub.Event{Type:"cortex.prewarm.fired"}` with `Custom["cache_status"]=resp.Usage.CacheRead>0`. On error, emit `cortex.prewarm.failed` and return the error (caller decides whether to fail). Function `runPreWarmPump(ctx, interval time.Duration, fire func(context.Context) error)` runs a `time.Ticker` and calls `fire` every interval, exiting on ctx.Done. Test `prewarm_test.go::TestPreWarmFires` (mocked provider, count invocations after fake-clock advance), `TestPreWarmFailureContinues` (error from fire is logged but pump keeps running).

### Budget controller

20. [ ] **Implement `LobeSemaphore` in `internal/cortex/budget.go`.** Internal: `slots chan struct{}`. `NewLobeSemaphore(capacity int)` panics if `capacity > 8 || capacity < 1`. `Acquire(ctx)` selects on `slots` send vs `ctx.Done()`. `Release` non-blocking send back. Test `budget_test.go::TestSemaphoreCapacity` (8 acquires succeed, 9th blocks until release), `TestSemaphoreCtxCancel`, `TestSemaphorePanicsOversize`.

21. [ ] **Implement `BudgetTracker` in `internal/cortex/budget.go`.** Internal: `mu sync.Mutex`, `mainOutputLastTurn int`, `lobeOutputThisRound int`. `Charge(lobeID string, usage stream.TokenUsage)` adds to `lobeOutputThisRound`. `RoundOutputBudget()` returns `mainOutputLastTurn * 30 / 100`. `Exceeded()` returns `lobeOutputThisRound >= RoundOutputBudget()`. `ResetRound()` zeroes `lobeOutputThisRound`. `RecordMainTurn(usage stream.TokenUsage)` sets `mainOutputLastTurn = usage.Output` — Cortex calls this from a hub.Bus subscription on `EventModelPostCall` (item 23 wires this). Modify item 9's `LobeRunner` LLM-acquire path to call `tracker.Exceeded()` AFTER `Acquire` returns; if true, call `Release` immediately and skip this round's invocation, emitting `cortex.lobe.budget_skipped`. Test `budget_test.go::TestBudgetTracker` (Charge accumulates; ResetRound clears), `TestBudgetExceededBlocks`.

### Bus persistence

22. [ ] **Implement `persist.go` write-through.** Function `writeNote(durable *bus.Bus, n Note) error`: marshal `Note` to JSON via `json.Marshal`, then call `durable.Publish(bus.Event{Type: "cortex.note.published", Payload: jsonBytes})`. The public API on `internal/bus.Bus` is `Publish(evt Event) error` (see `internal/bus/bus.go:321`); `Append` is a private method on the underlying `wal`. `bus.Event.Payload` has type `json.RawMessage` (see `internal/bus/bus.go:114`). If `durable == nil`, return nil (in-memory mode). Errors propagate to caller (item 4 returns the error to the Lobe). Test `persist_test.go::TestWriteNoteDurable` with a fake `bus.Bus` that records every Publish call, asserting payload round-trips through json.Marshal/Unmarshal. Cross-ref: item 4 calls `writeNote` BEFORE releasing the workspace mutex.

23. [ ] **Implement `Workspace.Replay` in `internal/cortex/persist.go`.** Read all `bus.Event` entries with `Type=="cortex.note.published"` from the durable WAL via `durable.Replay(pattern bus.Pattern, from uint64, handler func(bus.Event)) error` (see `internal/bus/bus.go:702`; pass a pattern matching `cortex.note.published` and `from=0`). Inside the handler, JSON-decode `Note` from `evt.Payload` (which is `json.RawMessage`) and append to `w.notes` directly under `Lock` (bypassing `Publish` to avoid double-emit and double-persist). After replay, set `w.drainedUpTo = uint64(len(w.notes))`. Idempotent: if Replay is called when `len(w.notes) > 0`, log + return nil. Test `persist_test.go::TestReplayRebuilds` (write 3 fake events to a fake Bus, call Replay on fresh Workspace, assert Snapshot returns 3 Notes in order; call Replay again, assert no duplicates).

24. [ ] **Wire main-turn token accounting through hub.Bus.** In `Cortex.Start` (item 13), register a hub.Bus subscriber for `hub.EventModelPostCall` events (see `internal/hub/events.go:50`) with `ev.MissionID==c.sessionID && ev.Model != nil && ev.Model.Role=="main"` that calls `c.tracker.RecordMainTurn(ev.Model.OutputTokens)`. The hub event carries a `*ModelEvent` (see `internal/hub/events.go:194-206`) with `OutputTokens int`, `InputTokens int`, `Role string`, etc. — there is no `TokensUsage` aggregate; pass the int directly. Update item 21's `BudgetTracker.RecordMainTurn` signature to `RecordMainTurn(outputTokens int)`. Document that the agentloop must emit this event with `Model.Role:"main"` in its post-call hook (a one-line addition in `internal/agentloop/loop.go` after each successful API response — call it out as part of this checklist item, not item 16, because it's budget-specific). Test `cortex_test.go::TestRecordMainTurnViaBus` with a synthetic event.

### Tests + integration

25. [ ] **Write `cortex_integration_test.go`.** Three fake Lobes: `EchoLobe` (deterministic, publishes 1 SevInfo Note), `WarnLobe` (LLM, mocked provider returns 1 SevWarning Note), `CritLobe` (LLM, mocked provider returns 1 SevCritical Note). Construct a Cortex with all three, call `Start(ctx)`, then drive 1 round by invoking `c.MidturnNote(messages, 0)`, assert returned string contains all 3 LobeIDs in Critical→Warning→Info order. Call `c.PreEndTurnGate(messages)` — assert non-empty (CritLobe blocks). Publish a resolving Note manually via `c.Workspace().Publish` with `Resolves=critNote.ID`. Re-call PreEndTurnGate — assert empty. Stop the Cortex. Run under `-race` with `t.Parallel()` enabled. Cross-ref: items 9, 12, 13, 14, 15.

26. [ ] **Add benchmarks `cortex_bench_test.go`.** `BenchmarkWorkspacePublish` (single goroutine), `BenchmarkWorkspacePublishContended` (16 goroutines), `BenchmarkRoundOpenWaitClose` (3 participants). Goal: Publish ≤500ns single, ≤5µs contended, full Round cycle ≤50µs. Don't add as CI gate; document targets so future regressions are visible.

27. [ ] **Race-detector CI integration.** Add `internal/cortex/...` to the existing `go test -race ./...` invocation in CI (verify `.github/workflows/*.yml` covers it; if cortex is excluded by a path filter, remove the filter). Run the full integration test (item 25) and assert clean. No code changes if CI already runs `-race` repo-wide.

28. [ ] **Add `cmd/r1` flag plumbing for Cortex enablement.** Modify `cmd/r1/main.go` to accept a new `--cortex` boolean flag (default OFF for spec 1; chat REPL doesn't auto-enable). When set, the flag stores `true` in the existing `app.Config` struct (add a `CortexEnabled bool` field if absent). The actual wire-up (constructing Cortex + passing into agentloop) is **NOT done in this checklist**; spec 2 owns it. Test `cmd/r1/main_test.go::TestCortexFlagParse`. Cross-ref: spec 2 will call `cortex.New` and pass it through the existing app.Config plumbing.

29. [ ] **Documentation: extend `internal/cortex/doc.go`.** Add a package-level doc.go covering the GWT pattern, pointer to `specs/cortex-core.md` and `specs/research/synthesized/cortex.md`, and an ASCII diagram of Workspace + Lobes + Round + Spotlight + Router. Keep <150 lines. No tests required.

30. [ ] **Update `CLAUDE.md` package map.** Add the `cortex/` entry to the "V2 GOVERNANCE" section of `/home/eric/repos/r1-agent/CLAUDE.md` exactly between `concern/templates/` and `harness/`, with one-line description: `cortex/                            Parallel-cognition substrate (Workspace, Lobe, Round, Spotlight, Router) — GWT-style shared workspace`. Verify with `go vet ./...` that nothing breaks.

31. [ ] **Self-review pass: confirm cross-references.** Reread items 1–30 in order; for each, confirm the symbol it depends on (e.g. item 4 references item 22's `writeNote`; item 14 references items 10, 5, 6) is created in an earlier-numbered item. If any item references a future symbol, either swap the order or split the item. Output a short note in the PR description: "Cortex-core checklist self-review: all forward references resolved." No code changes if review passes; otherwise fix and re-run.

