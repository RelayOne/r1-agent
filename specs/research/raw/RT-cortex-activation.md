# RT-cortex-activation — Integration map for ACTIVATING the cortex (audit finding B2)

Scope: READ-ONLY investigation. Goal: give a spec author every exact signature,
file:line, dependency, and wiring point needed to write self-contained checklist
items that make `*cortex.Cortex` a live `agentloop.Config.Cortex` hook, DEFAULT-ON
with a kill-switch, with no live-LLM dependency for the deterministic lobe set.

## Confirmed finding (B2)

- `internal/cortex/` is fully built (~9k LOC). `*cortex.Cortex` already satisfies
  `agentloop.CortexHook` (static assertion at `internal/cortex/cortex.go:488`).
- `agentloop.Config.Cortex` (the hook field) is **NEVER assigned in any live path**.
  Repo-wide `grep '\.Cortex *=' --include=*.go` (excluding tests) returns exactly ONE
  hit — `internal/config/policy.go:446` — which assigns the *policy* `CortexConfig`
  struct, NOT the agentloop hook. So the loop never sees a cortex.
- `cortex.Start()` is **NEVER called in any non-test path** (grep confirms zero hits
  outside `_test.go`).
- `cortex.New(` is called in exactly ONE non-test file: `cmd/r1/mcp_serve_runtime.go`
  (the MCP backend, lines 84/98/130) — and even there it is constructed with a
  `stubProvider{}`, never Started, and only used to expose `Workspace()` /
  `LobeStatus()` to MCP tools. The MCP backend never wires the cortex into an
  agentloop and never calls `Start`/`MidturnNote`/`PreEndTurnGate`.
- The chat REPL already exposes a `--cortex` flag and a `CortexEnabled` config field
  (`cmd/r1/chat_interactive_cmd.go:92,55,119`), but `CortexEnabled` is plumbed into
  the config struct and **consumed nowhere** (grep confirms the only 3 hits are the
  flag/field/assignment in that one file). It is dead plumbing awaiting this work.

This is exactly what `specs/cortex-core.md` Overview predicted: "Operators (the
cmd/r1 chat REPL) wire a `*cortex.Cortex` into the loop's Config; if they don't,
behavior is unchanged."

---

## 1. THE HOOK INTERFACE

File: `internal/agentloop/loop.go`.

The interface (declared in agentloop, not cortex, to break the import cycle since
cortex imports agentloop for `Message`):

```go
// internal/agentloop/loop.go:42-45
type CortexHook interface {
	MidturnNote(messages []Message, turn int) string
	PreEndTurnGate(messages []Message) string
}
```

The Config field:

```go
// internal/agentloop/loop.go:151
Cortex CortexHook
```

Composition / invocation wiring lives in `Config.defaults()` (loop.go:242-301).
When `c.Cortex != nil` (loop.go:262), defaults() composes the cortex INTO the two
operator hook fields. This is the critical design fact: **the cortex is not invoked
directly inside the run loop; it is composed into `MidturnCheckFn` and
`PreEndTurnCheckFn` by `defaults()`**, which runs inside `agentloop.New`
(loop.go:431).

- `MidturnNote` composition (loop.go:262-281): cortex fires FIRST, operator hook
  SECOND, outputs joined with `"\n\n"`. The wrapper closes over the prior
  `c.MidturnCheckFn`.
- `PreEndTurnGate` composition (loop.go:282-291): cortex fires FIRST and
  **short-circuits** on a non-empty (critical) return; only when empty does the
  operator `PreEndTurnCheckFn` run.

Where the *composed* hooks are invoked inside the loop (these are the real
hot-loop call sites — the cortex rides on them):

- `MidturnCheckFn` is called at **loop.go:678-679** — AFTER tool results are
  appended, BEFORE the next API call. A non-empty return is attached as a
  `[SUPERVISOR NOTE]` text block on the existing user message (loop.go:684-689).
- `PreEndTurnCheckFn` is called at **loop.go:592-593** — when the model attempts
  `end_turn`. A non-empty return injects a `[BUILD VERIFICATION FAILED ...]` user
  message and `continue`s the loop (loop.go:597-604), forcing another turn instead
  of ending. This is exactly the "refuse end_turn" semantics the cortex's
  `PreEndTurnGate` relies on.
- `OnUserInputMidTurn`: **does not exist** as a named hook in loop.go. The nearest
  analog is `EndTurnContinuation func() string` (loop.go:187), invoked at
  loop.go:630-631 to convert pending inbound user input into another turn. The
  cortex Router (mid-turn input routing) is NOT auto-wired through agentloop today;
  `Cortex.Router()` (cortex.go:253) is exposed for the chat REPL to call directly.
  Spec author: if router-based mid-turn input handling is in scope, it must be
  wired at the REPL layer, not via the CortexHook interface (which has only the two
  methods above).

Static assertion that `*cortex.Cortex` satisfies the interface:

```go
// internal/cortex/cortex.go:488
var _ agentloop.CortexHook = (*Cortex)(nil)
```

`MidturnNote` impl: cortex.go:529-600. `PreEndTurnGate` impl: cortex.go:619-632
(returns non-empty block listing unresolved `SevCritical` notes from
`workspace.UnresolvedCritical()`).

---

## 2. CORTEX CONSTRUCTION

File: `internal/cortex/cortex.go`.

Constructor signature:

```go
// internal/cortex/cortex.go:167
func New(cfg Config) (*Cortex, error)
```

`Config` struct (cortex.go:79-92). Required vs defaulted:

```go
type Config struct {
	SessionID           string            // optional; telemetry/audit correlation only
	EventBus            *hub.Bus          // REQUIRED (New errors if nil — cortex.go:168)
	Durable             *bus.Bus          // optional; nil = in-memory mode (no WAL)
	Provider            provider.Provider // REQUIRED (New errors if nil — cortex.go:171)
	Lobes               []Lobe            // may be empty; cannot mutate after Start
	MaxLLMLobes         int               // 0→5; <0 error; >8 panics (cortex.go:174-182)
	PreWarmModel        string            // ""→"claude-haiku-4-5"
	PreWarmInterval     time.Duration     // 0→4*time.Minute
	PreWarmSystemPrompt string            // byte-for-byte parity w/ downstream loop
	PreWarmTools        []provider.ToolDef
	RoundDeadline       time.Duration     // 0→2*time.Second  (KEY SAFETY KNOB, §7)
	RouterCfg           RouterConfig      // Provider/Bus inherit cfg values if blank
}
```

Lifecycle:

```go
// internal/cortex/cortex.go:351
func (c *Cortex) Start(parentCtx context.Context) error   // idempotent (atomic CAS), non-blocking
// internal/cortex/cortex.go:452
func (c *Cortex) Stop(stopCtx context.Context) error      // idempotent (sync.Once), bounded 10s
```

`Start` (cortex.go:351-430): captures a cancellable child of `parentCtx`, registers
a hub subscriber for `EventModelPostCall` (feeds BudgetTracker), fires one
synchronous best-effort pre-warm, launches the pre-warm pump goroutine, and starts
every `LobeRunner`. **Start does not block.** `Stop` (cortex.go:452-482): cancels
the ctx, unregisters the budget subscriber, and waits on all runners bounded by a
single cumulative `cortexStopTimeout = 10*time.Second` (cortex.go:39).

### Building with the 4 DETERMINISTIC lobes only (NO LLM provider needed for lobes)

Lobe taxonomy (verified via each lobe's `Kind()` method). NOTE: the CLAUDE.md
package map lists 7 lobe names but the actual on-disk lobe packages are:
`memoryrecall, walkeeper, rulecheck, planupdate, clarifyq, memorycurator, antitrunc`
(plus a shared `llm/` helper package). Split:

| Lobe | Package | Kind() | Provider required? | Constructor (file:line) |
|---|---|---|---|---|
| MemoryRecall | `lobes/memoryrecall` | `KindDeterministic` (lobe.go:142) | NO | `NewMemoryRecallLobe(ws *cortex.Workspace, mem *memory.Store, wis *wisdom.Store, bus *hub.Bus) *MemoryRecallLobe` (memoryrecall/lobe.go:109) |
| WALKeeper | `lobes/walkeeper` | `KindDeterministic` (lobe.go:163) | NO | `NewWALKeeperLobe(h *hub.Bus, w *bus.Bus, ws *cortex.Workspace, framing WALFraming) *WALKeeperLobe` (walkeeper/lobe.go:129) |
| RuleCheck | `lobes/rulecheck` | `KindDeterministic` (lobe.go:104) | NO | `NewRuleCheckLobe(durable *bus.Bus, ws *cortex.Workspace) *RuleCheckLobe` (rulecheck/lobe.go:88) |
| AntiTrunc | `lobes/antitrunc` | `KindDeterministic` (lobe_wrapper.go:77) | NO | `NewAntiTruncLobe(ws *cortex.Workspace, planPath, specGlob string) *AntiTruncLobe` (antitrunc/lobe_wrapper.go:52); optional `.WithGitLog(fn)` (lobe_wrapper.go:62) |
| PlanUpdate | `lobes/planupdate` | `KindLLM` (lobe.go:166) | **YES** (`client provider.Provider`, planupdate/lobe.go:128-131) | LLM-backed — exclude from deterministic set |
| ClarifyingQ | `lobes/clarifyq` | `KindLLM` (lobe.go:146) | **YES** (`client provider.Provider`, clarifyq/lobe.go:117-118) | LLM-backed — exclude |
| MemoryCurator | `lobes/memorycurator` | `KindLLM` (lobe.go:225) | **YES** (`client provider.Provider`, memorycurator/lobe.go:187-188) | LLM-backed — exclude |

**Deterministic-only lobe set = {memoryrecall, walkeeper, rulecheck, antitrunc}.**
None of these constructors take a `provider.Provider`. They all take the cortex's
`*cortex.Workspace` (obtained from a shell cortex via `shell.Workspace()`), plus the
`*hub.Bus` and the durable `*bus.Bus`. memoryrecall additionally needs a
`*memory.Store` and `*wisdom.Store` (both creatable credential-free in-memory:
`memory.NewStore(memory.Config{})`, `wisdom.NewStore()`).

**This exact construction already exists, fully working, at
`cmd/r1/mcp_serve_runtime.go:118-123`** (the `buildCortexBackend` "shell + live"
two-cortex pattern). The spec author can lift that block verbatim. It currently
passes `stubProvider{}` as the cortex Provider (mcp_serve_runtime.go:35-48), which
is acceptable ONLY because the Router (see §5) is never exercised when Start is not
called and no LLM lobe runs — BUT for an activated cortex that DOES call
`MidturnNote`/`Start`, see §5 for why a real provider should be passed instead of
the stub.

---

## 3. NATIVE LOOP WIRING POINT

File: `internal/engine/native_runner.go`.

`agentloop.Config` is constructed at **native_runner.go:348-354**:

```go
cfg := agentloop.Config{
	Model:              n.model,
	MaxTurns:           spec.Phase.MaxTurns,
	MaxConsecutiveErrs: 3,
	MaxTokens:          16000,
	SystemPrompt:       systemPrompt,
}
```

The loop is created at **native_runner.go:476**: `loop := agentloop.New(p, cfg, toolDefs, handler)`.
`agentloop.New` calls `cfg.defaults()` (loop.go:431) which performs the cortex
composition described in §1 — so **assigning `cfg.Cortex` before line 476 is all
that's mechanically required** for the loop to consult the cortex.

### Where `cfg.Cortex` should be assigned

Insert between native_runner.go:354 (end of the Config literal) and 476 (the
`agentloop.New` call). Recommended placement: alongside the existing optional-hook
blocks (PreEndTurnCheckFn at 375-414, MidturnCheckFn at 446-473). Build the cortex,
call `Start`, assign `cfg.Cortex = c`, and arrange `Stop` via defer.

### Dependencies already in scope at that point

- **Provider**: `p provider.Provider` is in scope (native_runner.go:94-110) — the
  real Anthropic/OpenAI-compat provider already constructed for the loop. This is
  the provider to pass to `cortex.Config.Provider` (§5). No stub needed.
- **Hub bus**: `n.EventBus *hub.Bus` (native_runner.go:44) — optional, may be nil.
  `cortex.New` REQUIRES a non-nil `EventBus`. So the wiring must either (a) require
  `n.EventBus != nil` to enable cortex, or (b) construct a fresh `hub.New()` local
  to the run when `n.EventBus == nil`. Note: the loop only emits the
  `EventModelPostCall` that the cortex BudgetTracker consumes when
  `l.eventBus != nil` (loop.go:540) — so for budget accounting to work, the SAME
  bus must be set on the loop (via `loop.SetEventBus`, native_runner.go:479-481)
  AND passed to `cortex.New`. The wiring should share ONE bus between loop and cortex.
- **Durable bus**: NOT in scope in native_runner.go. `cortex.Durable` is optional
  (nil → in-memory). For walkeeper/rulecheck to do useful work a durable bus is
  needed (`bus.New(busDir)`), but nil is safe (those lobes degrade to no-ops per
  their constructor docs). MVP can pass `Durable: nil`.
- **SessionID**: `spec.SessionID` (types.go:316) is available on RunSpec — pass to
  `cortex.Config.SessionID` for correlation.
- **WorktreeDir / plan / specs for AntiTrunc**: `spec.WorktreeDir` (types.go:195)
  is in scope; `NewAntiTruncLobe(ws, planPath, specGlob)` wants a plan path + spec
  glob. RunSpec does not carry these directly today (the build path uses
  `AntiTruncPlanPath`/`AntiTruncSpecPaths` on agentloop.Config, loop.go:207-211 —
  those are a SEPARATE antitrunc gate, not the cortex lobe). MVP: pass `"", ""` (the
  lobe still publishes critical notes from conversation-history regex scanning;
  plan/spec scanning is additive — see antitrunc/lobe_wrapper.go:84-89).
- **memory/wisdom stores for memoryrecall**: not in scope; create credential-free
  in-memory instances as in mcp_serve_runtime.go:112-116.

### Start/Stop lifecycle bracketing

`native_runner.Run` already has `ctx` (the run context) and `start := time.Now()`
at the top (native_runner.go:82,87). Bracket the run:

```go
// after building cortex c and assigning cfg.Cortex = c, BEFORE agentloop.New:
if err := c.Start(ctx); err != nil { /* log, proceed without cortex */ }
defer c.Stop(context.Background()) // bounded 10s; use a fresh ctx so cancel doesn't truncate shutdown
```

`Start(ctx)` ties every lobe goroutine + pre-warm pump to the run's ctx; the run
returns at native_runner.go (Run end), and the deferred `Stop` winds it down. Because
`Start` is non-blocking and idempotent and `MidturnNote` falls back to
`context.Background()` if `c.ctx == nil` (cortex.go:549-556), even a missed Start
does not hang the loop — but Start IS required for the lobe runners to actually run
(without Start, runners are inert and every round drains zero notes → MidturnNote
returns "" → behavior unchanged). So Start is mandatory for the cortex to DO anything.

---

## 4. CHAT REPL WIRING POINT

File: `cmd/r1/chat_interactive_cmd.go`.

The cortex-core spec named the chat REPL specifically. Current state:

- `--cortex` flag: `cortexEnabled := fs.Bool("cortex", false, "Enable parallel-cognition Lobes (cortex-core spec 1; off by default)")` (chat_interactive_cmd.go:92).
- Config field: `CortexEnabled bool` (chat_interactive_cmd.go:55), assigned at
  chat_interactive_cmd.go:119.
- **It is consumed nowhere.** The REPL does not itself build an agentloop — it calls
  `session.runWorkflow(...)` (chat_interactive_cmd.go:129-134) which delegates to
  `internal/workflow`, which ultimately reaches the engine runners. The REPL has no
  direct `agentloop.New` call site of its own.

Construction site for the spec author: the REPL builds its session at
chat_interactive_cmd.go:108-128 (`chatInteractiveConfig` → `chatInteractiveSession`),
and dispatches via `planFn`/`execFn` (lines 129-134) → `runWorkflow` →
`internal/workflow` → engine. The cleanest activation is therefore NOT in the REPL
file itself but in the engine (`native_runner.go`, §3) — the REPL's `CortexEnabled`
should be threaded down through the workflow/engine call chain into a RunSpec field
(NEW field, e.g. `RunSpec.CortexEnabled bool`) that native_runner reads at the §3
wiring point. The REPL flag becomes the per-session kill switch; default-on is set
at the flag default (§6).

(Action for spec: thread `CortexEnabled` from chatInteractiveConfig →
workflow.Result plumbing → engine.RunSpec. Today the field dead-ends at line 119.)

---

## 5. PROVIDER for the Router

`internal/provider.Provider` interface (anthropic.go:29-33):

```go
type Provider interface {
	Name() string
	Chat(req ChatRequest) (*ChatResponse, error)
	ChatStream(req ChatRequest, onEvent func(stream.Event)) (*ChatResponse, error)
}
```

`cortex.New` REQUIRES a non-nil Provider (cortex.go:171). It is used for two things:
the **Router** (Haiku-class mid-turn input router, `NewRouter(rcfg)` at cortex.go:211,
inheriting `cfg.Provider` when `RouterCfg.Provider == nil`, cortex.go:204-207) and
the **pre-warm pump** (cortex.go:396-422, model defaults to `claude-haiku-4-5`).

For the deterministic-lobes-only set:
- No lobe calls the provider (all 4 are `KindDeterministic`).
- The Router is only exercised on explicit `Cortex.Router().Route(...)` calls, which
  the CortexHook (MidturnNote/PreEndTurnGate) does NOT trigger. So the Router is dormant.
- The pre-warm pump WILL call the provider on `Start` (synchronous initial fire at
  cortex.go:396 + periodic pump at 413). With `stubProvider{}` this fails on every
  fire, but failures are best-effort and only logged at WARN (cortex.go:404-407) —
  they never abort Start. So a stub would technically "work" but spams WARN logs and
  warms no cache.

**Recommendation: pass the real `p` already in scope at native_runner.go:94-110 as
`cortex.Config.Provider`** (NOT the stub). It costs nothing extra (pre-warm is the
only auto-fired use and it actually warms the cache that the very next agentloop turn
benefits from — that is the pre-warm pump's entire purpose, cortex.go gotcha #8). To
make pre-warm cache-aligned, also set `PreWarmSystemPrompt: systemPrompt` (the same
`systemPrompt` var at native_runner.go:340-346) and `PreWarmTools: toolDefs`
(native_runner.go:123) so the pre-warmed slot matches the real turn byte-for-byte
(cortex.go:59-72). To disable pre-warm cost entirely set `PreWarmInterval` very large;
the deterministic-lobes MVP may set `PreWarmInterval: time.Hour` to suppress the pump
(the cortex tests use exactly this — `PreWarmInterval: time.Hour`, cortex_test.go:352)
and pass the real provider only to satisfy the non-nil requirement without recurring cost.

---

## 6. CONFIG + DEFAULT-ON + kill-switch

### Existing policy.Cortex block (NOT the agentloop hook — separate concern)

- `Policy.Cortex CortexConfig` (config/policy.go:212), assigned at policy.go:446 via
  `parseCortexBlock` (config/schema.go:81-87).
- `CortexConfig` struct (config/schema.go:24-26) → `LobeFlags` (schema.go:32-39):
  per-lobe `Enabled` flags (`memory_recall`, `wal_keeper`, `rule_check`,
  `plan_update`, `clarifying_q`, `memory_curator`) plus a richer `MemoryCuratorFlag`
  (schema.go:59-63). This is the per-lobe enable surface a deployment YAML can use.
- It is parsed but **not consumed to gate lobe construction anywhere live** — same
  dead-plumbing situation as the REPL flag. The spec author should connect these
  flags to the lobe-selection logic at the §3 wiring point.

### Adding `--cortex` / `--no-cortex` (mirror the `--specexec` pattern)

The `--specexec` precedent in `cmd/r1/main.go`:
- Flag: `specExec := fs.Bool("specexec", false, "Enable speculative parallel execution ...")` (main.go:1185).
- RunConfig field: `SpecExec bool` (main.go:116).
- Assigned into config: `SpecExec: *specExec` (main.go:1465).
- Consumed: `if cfg.SpecExec { ... }` (main.go:535) and `if *specExec { ... }`
  (main.go:1424) to wrap the exec fn via `scheduler.WithSpecExec(...)`.

To make cortex DEFAULT-ON with a kill switch, invert the boolean polarity vs
`--specexec` (which is default-off). Two standard Go-flag idioms:

1. Single bool defaulting true: `fs.Bool("cortex", true, "parallel-cognition lobes (default on; --cortex=false to disable)")`. Kill switch = `--cortex=false`.
2. A dedicated negative flag mirroring the existing MCP precedent: `cmd/r1/mcp.go:108`
   already does `noCortex := fs.Bool("no-cortex", false, "skip cortex backend wiring ...")`.
   Mirror that: keep `--cortex` (default true) AND add `--no-cortex` (default false)
   where `--no-cortex` overrides to off. The MCP subcommand is the existing precedent
   for the `--no-cortex` spelling, so reuse it for consistency.

RunConfig wiring (main.go pattern): add `CortexEnabled bool` to the RunConfig struct
(near main.go:116 `SpecExec`), default it true, assign from the flag, and thread into
`engine.RunSpec.CortexEnabled` (NEW field per §4) consumed at native_runner.go §3.

The chat REPL flag (chat_interactive_cmd.go:92) must flip its default from `false`
to `true` to be default-on, and keep `--cortex=false` as the per-session kill switch.

### Test asserting cortex-off (must not break, or must be updated)

- `internal/config/schema_test.go:39-56` asserts a specific default-flag profile:
  MemoryRecall/WALKeeper/RuleCheck **Enabled=true**, PlanUpdate/ClarifyingQ/
  MemoryCurator **Enabled=false** (i.e. deterministic-on, LLM-off). This is a
  POLICY-block test, not an agentloop test — it already encodes "deterministic lobes
  default on", which is consistent with making the deterministic set default-on. Do
  NOT regress these assertions.
- `internal/engine/*_test.go`: grep for `Cortex` returns ZERO hits — there is no
  engine test asserting cortex-off, so wiring cortex into native_runner will not trip
  an existing engine test. (A new test should be added — see §8.)
- No other live test asserts `cfg.Cortex == nil`.

---

## 7. RISK / DEADLINE — the default-on safety property

The single most important property for default-on: **`MidturnNote` cannot hang the
hot loop**, because the Round barrier wait is bounded.

`MidturnNote` waits on the round via `c.round.Wait(waitCtx, roundID, c.cfg.RoundDeadline)`
(cortex.go:557). `RoundDeadline` defaults to **2 seconds** (cortex.go:183-185, default
applied in `New`). The wait implementation:

```go
// internal/cortex/round.go:111-138
func (r *Round) Wait(ctx context.Context, roundID uint64, deadline time.Duration) error {
	...
	select {
	case <-doneCh:                 return nil
	case <-time.After(deadline):   return ErrRoundDeadlineExceeded   // round.go:133
	case <-ctx.Done():             return ctx.Err()
	}
}
```

Three guarantees:
1. The `time.After(deadline)` arm (round.go:133, also 123 for unknown rounds) means
   the wait ALWAYS returns within `RoundDeadline` (default 2s) even if every lobe is
   wedged.
2. On deadline/ctx error, `MidturnNote` does NOT propagate the error — it logs at
   WARN (cortex.go:561-565) and proceeds to drain whatever notes landed (cortex.go:568).
   So a slow round degrades to a partial/empty note block, never a stall.
3. Slow lobes are NOT cancelled (spec gotcha #6) — their notes simply land on a future
   round; this keeps the deadline a pure upper bound on the synchronous critical path.

`PreEndTurnGate` (cortex.go:619-632) does NO waiting at all — it is a pure read of
`workspace.UnresolvedCritical()`, so it cannot block.

Therefore worst-case added latency per agent turn = `RoundDeadline` (2s default),
bounded and tunable via `cortex.Config.RoundDeadline`. The spec author should expose
this as a config knob and may lower it (e.g. 1s) for the hot interactive loop.

---

## 8. TEST PATTERN for a synthetic integration test (NO live LLM)

Existing cortex test patterns to lift (`internal/cortex/cortex_test.go`):

- **Fake deterministic lobe** that publishes a predictable Note when ticked:
  `midturnLobe` (cortex_test.go:285-325) — `Kind() KindDeterministic` (line 314),
  `Run` publishes a `Note{LobeID, Severity, Title}` (lines 320-324). Use this shape
  to build a lobe that publishes a `SevCritical` note for the PreEndTurnGate test.
- **Fake provider** with no network: `startStopProvider` (cortex_test.go:20-35) —
  satisfies `provider.Provider` with canned returns; pass it as `cortex.Config.Provider`
  so `New`/`Start` need no API key. (This is the "NOT a stub that errors" pattern for
  tests — it returns a valid empty response so pre-warm succeeds quietly.)
- **MidturnNote produces formatted notes**: `TestMidturnNoteFormat`
  (cortex_test.go:339-396). Pattern: `New(Config{EventBus: hub.New(),
  Provider: &startStopProvider{}, Lobes: [...], PreWarmInterval: time.Hour,
  RoundDeadline: 60*time.Second})` → wire lobe workspaces to `c.workspace`
  (lines 361-362) → `c.Start(context.Background())` (364) →
  `out := c.MidturnNote([]agentloop.Message{}, 0)` (369) → assert
  `strings.HasPrefix(out, "[CORTEX NOTES — round 1]\n")` (373) and that both LobeIDs
  + titles appear (377-384). `RoundDeadline: 60s` in tests removes flakiness; use a
  blocking gate lobe if you need deterministic ordering.
- **MidturnNote empty on no lobes**: `TestMidturnNoteEmptyOnNoLobes`
  (cortex_test.go:402-428) — proves the "behavior unchanged" contract: zero lobes →
  `MidturnNote` returns `""` and does not even increment the round counter (428).
- **MidturnNote severity sort**: `TestMidturnNoteSortsBySeverity` (cortex_test.go:437+).
- **PreEndTurnGate blocks on a critical note**: `TestPreEndTurnGateBlocks`
  (cortex_test.go:525-568+). Pattern: `New(...)` → `ws := c.Workspace()` →
  `ws.Publish(Note{LobeID, Title, Severity: SevCritical})` (538-542) → recover the
  assigned ID via `ws.Snapshot()` (547-551) → `got := c.PreEndTurnGate(...)` (556) →
  assert non-empty + contains `"CRITICAL"` + LobeID + Title (557-568) → publish a
  resolving Note (`Resolves: critID`) and assert the gate flips back to `""` (570+).
- **PreEndTurnGate green-light**: `TestPreEndTurnGateEmpty` (cortex_test.go:495-509)
  — no critical notes → `""`.

### Recommended synthetic integration test of the WIRED native loop (no live LLM)

There is no existing engine-level cortex test (§6). Add one in
`internal/engine/native_runner_*_test.go` shape:

1. Build a `cortex.New(Config{EventBus: hub.New(), Provider: &fakeProvider{},
   Lobes: [criticalNoteLobe], PreWarmInterval: time.Hour, RoundDeadline: 60*time.Second})`
   where `criticalNoteLobe` publishes a `SevCritical` note on first tick.
2. Build an `agentloop.Config{Cortex: c, MaxTurns: 2}` and a fake `provider.Provider`
   that returns an immediate `end_turn` response.
3. `c.Start(ctx)`; run `agentloop.New(fakeProvider, cfg, nil, handler).Run(ctx, "go")`.
4. Assert the loop did NOT terminate on the first `end_turn` — because the composed
   `PreEndTurnCheckFn` (loop.go:283-291) sees the cortex's non-empty critical block and
   injects the `[BUILD VERIFICATION FAILED ...]` continuation (loop.go:597-604). Then
   publish a resolving note (or have the lobe stop emitting) and assert the next
   `end_turn` is honored. This exercises the FULL wired path (defaults() composition +
   loop.go:592 invocation + cortex.PreEndTurnGate) with zero network.
5. A parallel test: assert `MidturnNote` output reaches the model — give the loop one
   `tool_use` turn then `end_turn`; assert the next user message contains a
   `[SUPERVISOR NOTE] [CORTEX NOTES — round 1]` block (loop.go:684-689 + cortex.go:595).

---

## Spec recommendations (concrete checklist items)

1. **Add `engine.RunSpec.CortexEnabled bool`** (internal/engine/types.go near line
   173) and document it mirrors the `--specexec`/`SpecExec` precedent.
2. **Wire cortex construction into `native_runner.Run`** between the Config literal
   (native_runner.go:354) and `agentloop.New` (native_runner.go:476): when
   `spec.CortexEnabled` (default true), build a shell cortex
   (`cortex.New(cortex.Config{EventBus: <shared hub.Bus>, Provider: p, SessionID:
   spec.SessionID, PreWarmSystemPrompt: systemPrompt, PreWarmTools: toolDefs,
   RoundDeadline: <tunable, default 2s>})`), take `ws := shell.Workspace()`, construct
   the 4 deterministic lobes against `ws` (lift mcp_serve_runtime.go:112-123 verbatim,
   substituting the real `p`), build the live cortex with `Lobes: [...]`, assign
   `cfg.Cortex = live`.
3. **Share ONE `*hub.Bus`** between the loop and the cortex: pass it to `cortex.New`
   AND to `loop.SetEventBus` (native_runner.go:479-481) so `EventModelPostCall`
   (loop.go:540) reaches the BudgetTracker. When `n.EventBus == nil`, allocate a
   local `hub.New()` for the run.
4. **Pass the real provider `p`** (native_runner.go:94-110) as `cortex.Config.Provider`
   — NOT `stubProvider{}`. Suppress pre-warm cost in MVP via
   `PreWarmInterval: time.Hour` if cache-warming is out of scope; otherwise set
   `PreWarmSystemPrompt`/`PreWarmTools` for byte-for-byte cache alignment.
5. **Bracket lifecycle**: `c.Start(ctx)` right after assignment;
   `defer c.Stop(context.Background())`. Tolerate Start errors (log + proceed without
   cortex) so a cortex failure never aborts a coding run.
6. **Flags**: in `cmd/r1/main.go` add `--cortex` defaulting **true** (and/or
   `--no-cortex` mirroring `cmd/r1/mcp.go:108`); add `CortexEnabled bool` to RunConfig
   (near main.go:116); assign and thread into `RunSpec.CortexEnabled`. In
   `cmd/r1/chat_interactive_cmd.go:92` flip the `--cortex` default from `false` to
   `true` and thread `CortexEnabled` (chat_interactive_cmd.go:119) down through
   workflow → engine RunSpec (it currently dead-ends).
7. **Kill switch contract**: `--cortex=false` (or `--no-cortex`) sets
   `RunSpec.CortexEnabled=false`; native_runner skips cortex construction entirely so
   `cfg.Cortex == nil` and `defaults()` composition is a no-op (loop.go:262) →
   behavior identical to today. Verify this path in a test.
8. **Connect `policy.Cortex.Lobes.*.Enabled`** (config/schema.go:32-39, parsed at
   policy.go:446 but unconsumed) to lobe selection at the §3 wiring point so a
   deployment YAML can disable individual lobes. Preserve schema_test.go:39-56
   default profile (deterministic-on, LLM-off).
9. **Expose `RoundDeadline` as a tunable** (config or RunSpec), defaulting to the 2s
   safety bound (cortex.go:183-185); document the §7 latency property in the spec as
   the load-bearing default-on safety justification.
10. **Add the engine-level integration test** from §8 (no live LLM): one asserting the
    composed `PreEndTurnCheckFn` blocks `end_turn` on a critical cortex note, one
    asserting `MidturnNote` output reaches the next user message, one asserting
    `CortexEnabled=false` leaves the loop byte-identical to today.
11. **Decide on Router/mid-turn-input scope**: the CortexHook interface has only
    MidturnNote + PreEndTurnGate; `Cortex.Router()` is NOT auto-wired. If router-based
    mid-turn input routing is in scope, wire it at the REPL layer via
    `EndTurnContinuation` (loop.go:187, invoked loop.go:630) — call out explicitly,
    otherwise it stays dormant (safe).
