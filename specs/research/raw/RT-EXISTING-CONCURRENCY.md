# RT-EXISTING-CONCURRENCY.md — Parallel/Concurrent Surfaces in r1-agent

**Scope:** Comprehensive inventory of concurrency, parallelism, multi-stream, and context-sharing primitives in r1-agent. Code-grounded analysis of what exists, what's goroutine-backed, and architectural gaps for "parallel thoughts with shared context."

**Stats:** 1260 .go files, 129 `go func` launches, 357 sync primitives (Mutex/RWMutex/WaitGroup), 251 channel definitions.

---

## CORE LOOP

### agentloop/loop.go (loop + parallel tool execution)
**What it does:**  
Anthropic Messages API-driven agentic loop with streaming, token tracking, and **parallel tool execution via WaitGroup** (loop.go:?). Sequential turns but concurrent tool invocations within a turn.

**Concurrency:**  
- Line ~350-400: `var wg sync.WaitGroup` + `go func(idx int, call ContentBlock)` for each tool_use block  
- `executeTools(ctx)` spans N goroutines to run multiple tools in parallel, blocks via `wg.Wait()` before appending results  
- No inter-goroutine state mutation; tool results collected into a slice protected by implicit turn-level synchronization  
- Context cancellation supported: `select { case <-ctx.Done() }`

**Primitives to reuse:**  
- `WaitGroup` pattern for fan-out/fan-in of independent work  
- Streaming callback hooks (`OnTextFunc`, `OnToolUseFunc`) for side-channel observation  
- `CompactFn` hook allows mid-turn intervention (supervisor injection point)  
- `MidturnCheckFn` hook fires between turns — good template for "pause and check rules" before API call  
- Pre-end-turn gate (`PreEndTurnCheckFn`) intercepts model stops for verification

**Gaps:**  
- No goroutine per-tool; no cancellation of slow tools once one wins  
- No inter-tool communication or shared state (each tool is pure function)  
- No early-exit / speculative pruning within a turn (all tools must complete)  
- Turn serialization: next API call waits for all tools + compaction + checks; cannot overlap with next turn's setup

---

### specexec/specexec.go (speculative parallel execution)
**What it does:**  
Run 4+ strategies in parallel, isolated git worktrees, pick winner by score (test pass rate, diff size, duration). Fan-out/fan-in pattern inspired by Devin's multi-approach exploration.

**Concurrency:**  
- Line ~130-195: `var wg sync.WaitGroup`, `sem := make(chan struct{}, spec.MaxParallel)` (semaphore for concurrency limit)  
- Spawns N goroutines (one per strategy), each acquires semaphore, runs `Executor(ctx, strategy)`, updates `outcomes[idx]` under `mu.Lock()`  
- Early stop: `earlyStop := make(chan struct{})` closed when first strategy exceeds `StopThreshold` (all others check and exit)  
- Per-strategy timeout: `context.WithTimeout(execCtx, spec.Timeout)`  
- Panic recovery per goroutine + centralized outcome collection

**Primitives to reuse:**  
- Semaphore pattern (buffered chan) for concurrency-limited fan-out  
- Early-stop signaling via closed channel  
- Per-goroutine context timeout  
- Scoring/selection logic (pick best of N concurrent outcomes)  
- `Outcome` struct bundles all results (success, score, duration, artifacts, insights)

**Gaps:**  
- No shared memory between strategies (each is isolated); cannot exchange intermediate results  
- No inter-strategy signaling (e.g., "strategy A found a pattern, skip related work in B")  
- Outcomes collected post-hoc; no real-time merging of insights  
- Not integrated with agentloop; separate execution path for spec-exec mode only

---

### scheduler/scheduler.go (GRPW + parallel task dispatch)
**What it does:**  
Task-level parallelism with dependency tracking and file-lock conflict resolution. Dispatches ready tasks to N worker goroutines, respects GRPW priority (greatest rank positional weight) and file-scope serialization.

**Concurrency:**  
- Line ~123: `var wg sync.WaitGroup`, `results := make(chan TaskResult, len(tasks))`  
- Dispatch loop: collects ready tasks under `s.stateMu.Lock()`, then launches goroutines outside lock (line ~216-230)  
- Per-task: `go func(task plan.Task) { ... results <- execFn(ctx, task) }`  
- Drain results non-blocking: `select { case r := <-results }` (line ~172-184)  
- File conflict resolution: `s.fileLocks[filename] = taskID` prevents concurrent writes to same file  
- Dependency validation: `s.depsOK(t)` checks all predecessors completed  
- Failure propagation: if dep failed, cancel dependent tasks (line ~243-248)

**Primitives to reuse:**  
- WaitGroup + buffered results chan for bounded fan-out  
- Non-blocking drainResults loop (essential for responsive dispatch)  
- File-scope mutex model (per-file exclusive write)  
- Dependency DAG validation  
- `PriorityFunc` registry (pluggable sort algorithms: GRPW, PLAS, KV-cache affinity)  
- State maps: completed/failed/running prevent re-dispatch

**Gaps:**  
- No inter-task communication (only sequential file I/O)  
- No real-time priority adjustment (priority computed once upfront)  
- No task cancellation if dependent task fails (fires at end of current task)  
- MessageBus + DispatchQueue are write-only (status broadcast, not feedback loop)  
- No work-stealing or dynamic load balancing across workers

---

### branch/explorer.go (conversation branching)
**What it does:**  
Fork conversation history at decision points; each branch gets own message list; branches scored and best selected. Minimal concurrency: not parallel execution, but parallel exploration paths stored in memory.

**Concurrency:**  
- Line ~46-51: `type Explorer struct { mu sync.Mutex, branches map[string]*Branch }`  
- `Fork()` and `ForkFrom()` acquire `e.mu.Lock()` to add branch; copy trunk messages (thread-safe copy-on-fork)  
- All branch operations are single-threaded mutations under mutex; no goroutines spawned

**Primitives to reuse:**  
- Copy-on-fork pattern for branching (each branch owns independent message slice)  
- Branch ID generation (monotonic counter + mutex)  
- Score + Status tracking per branch

**Gaps:**  
- No concurrent execution of branches; only sequential evaluation  
- No asynchronous branch evaluation; scoring is single-threaded  
- No inter-branch communication (each is fully isolated)  
- Used for exploration UI only, not integrated into agentloop execution

---

## EVENT / MESSAGE PLUMBING

### hub/hub.go + bus/bus.go (typed event hub & durable WAL-backed bus)
**What it does:**  
Event-driven runtime substrate. Hub is in-memory typed subscribers. Bus is durable (WAL-backed) with privileged hooks + passive subscriptions. Events published atomically; subscriber delivery async via per-subscriber goroutine.

**Hub (in-memory):**  
- `type Bus struct { ... subscribers map[Pattern][]func(Event) }`  
- `OnEvent(pattern, handler)` registers handler; fires synchronously on `Publish(event)`  
- Used for local lifecycle events (tool use, cost, honesty gates)

**Bus (durable):**  
- Line ~269-286 in bus/bus.go: `type Bus struct { mu sync.Mutex, wal *WAL, seq, hooks [], subscribers []*Subscription, delayed map[string]*delayedEntry, subIndex, hookIndex }`  
- **Hooks:** privileged handlers (supervisor only) registered via `RegisterHook()`, fire before subscribers, synchronously within `Publish()`  
- **Subscriptions:** each owns goroutine + buffered chan (size 1024, line ~197); Publish enqueues event async  
- Line ~213-237: per-subscriber `run()` goroutine drains chan, invokes handler, panic-recovers  
- **Delayed events:** `make(chan struct{})` + `time.Timer` for scheduled event replay  
- WAL durability: all events persisted before Publish returns

**Concurrency:**  
- Hooks fire sync in Publish's critical section (under `mu`)  
- Subscribers fire async: each subscription owns independent goroutine + buffered chan  
- No cross-subscriber coordination; panics isolated per subscriber  
- Event indices (subIndex, hookIndex) for O(1) pattern matching (prefix-based)  
- Overflow detection: if subscriber chan fills, `EvtBusSubscriberOverflow` event emitted

**Primitives to reuse:**  
- Per-subscriber goroutine + buffered chan for async, isolated delivery  
- Panic recovery pattern (recover in handler, emit event)  
- Pattern-based subscription (prefix matching on dotted event type)  
- WAL durability guarantees (events persisted before Publish returns)  
- Hook injection as action-trigger (supervisor rules fire hooks to spawn workers, pause/resume stances)  
- Delayed event scheduling (fire event at specific time)  
- Event `Scope` for scoping events to mission/branch/stance/loop

**Gaps:**  
- No inter-subscriber messaging (subscribers are isolated observers)  
- No backpressure; subscriber overflow silently emits event, not coordinated  
- No cancellation of delayed events from event handler (timer already set)  
- Hooks are synchronous, blocking Publish; long-running checks force turn delay  
- No consensus on event (e.g., "majority of subscribers agree" gate)  
- Only bus itself is durable; subscriber state is not replayed on restart

---

### dispatch/queue.go (three-tier message dispatch with idempotency)
**What it does:**  
Reliable message delivery with retry backoff, idempotency keys, and three-tier priority (Critical, High, Normal, Low). Persists message state; retries with exponential backoff on failure.

**Concurrency:**  
- Line ~84-101: `type Queue struct { mu sync.Mutex, config, messages, seen }`  
- `Enqueue()`, `Dequeue()`, `Deliver()` all acquire `mu.Lock()`  
- Single-threaded dequeue; caller responsible for spawning delivery goroutines  
- Idempotency: `seen[idempotencyKey]` prevents duplicate side effects

**Primitives to reuse:**  
- Idempotency key dedup pattern (essential for at-least-once delivery without duplicate effects)  
- Exponential backoff with max cap  
- Priority-based draining (Critical drains before High before Normal)  
- Message lifecycle states (Pending → Sent → Delivered | Failed → Expired)

**Gaps:**  
- No async delivery; queue expects caller to spawn goroutines for `DeliverFunc`  
- No concurrent delivery coordination (if multiple messages for same recipient, no batching)  
- No cross-message dependencies (cannot enforce order of delivery)  
- Not integrated with bus; separate delivery mechanism (neither pushes events nor pulls from bus)

---

### agentmsg/protocol.go (inter-agent communication)
**What it does:**  
Protocol for agents to send structured messages (not documented in detail; assumed to be schema/marshaling layer over bus or dispatch queue).

**Concurrency:**  
- Minimal intrinsic concurrency; piggybacked on bus/queue for delivery

---

### notify/notify.go (webhooks + notifications)
**What it does:**  
Event-to-webhook adapters. `WebhookNotifier` POSTs to URL; includes retry logic (2 attempts, 5s timeout).

**Concurrency:**  
- Single request per event; no goroutine spawning; blocking on `http.Client.Do()`  
- Caller can spawn goroutine to avoid blocking (not integrated with bus)

---

## STANCE / MULTI-AGENT

### harness/harness.go (stance lifecycle: spawn/pause/resume/terminate)
**What it does:**  
Runtime layer that spawns worker stances (model-driven agents for specific roles: Dev, CTO, Reviewer, PO). Manages model selection, system prompt construction, session state, tool authorization, and pause/resume lifecycle.

**Concurrency:**  
- Line ~32-39: `type Harness struct { stances map[string]*StanceSession, mu sync.RWMutex, seq uint64 }`  
- `SpawnStance()` acquires `mu` to add stance to map  
- No explicit goroutines launched; stances run via external agentloop invocation  
- Session initialization idempotent: can resume paused stance without state reset

**Primitives to reuse:**  
- Role-based spawning template (extend to parallel role assignment)  
- Concern field projection (per-stance view of shared context)  
- Tool authorization per stance (role-based permissions)  
- System prompt templating (role-specific system message)

**Gaps:**  
- No concurrent stance execution; each stance runs sequentially via agentloop  
- No inter-stance communication (each gets independent context)  
- No shared mutable state between stances (only concern field, which is read-only projection)  
- Pause/resume not truly concurrent; pause is explicit hook, resume is restart of same agentloop

---

### concern/builder.go (per-stance context projection)
**What it does:**  
Builds "concern field" — curated view of ledger + plan + mission state projected for specific role/face (proposing vs reviewing). Renders as string to inject into system prompt.

**Concurrency:**  
- Line ~29-40: `type Builder struct { ledger *Ledger, bu sync.RWMutex }`  
- `BuildConcernField()` reads ledger under RWMutex; no writes  
- Rendering is read-only; can be parallelized across stances

**Primitives to reuse:**  
- Read-only ledger queries (no inter-stance coordination needed)  
- Context projection (select relevant subset for role)

**Gaps:**  
- No dynamic concern updates during turn (static projection computed once at spawn)  
- No inter-role coordination (each role sees same facts, just different emphasis)

---

### supervisor/supervisor.go (deterministic rules engine)
**What it does:**  
Fire 30 rules across 10 categories (consensus, cross-team, drift, hierarchy, research, SDM, skill, snapshot, trust, etc.). Rules emit actions: spawn worker, pause/resume, checkpoint, rule injection into turn. Driven by events (via bus hooks).

**Concurrency:**  
- Line ~? (not read in detail): assumes synchronous rule evaluation in hook context  
- Hooks fire serially in bus's critical section; cannot parallelize rule eval without redesign

**Primitives to reuse:**  
- Rule registry (extensible set of rules + conditions)  
- Rule action types (spawn, pause, checkpoint, inject into turn)  
- Scope-based filtering (apply rule only if mission/branch/loop matches)

**Gaps:**  
- No parallel rule evaluation (would require deterministic ordering)  
- No inter-rule communication (rule outcomes not fed to other rules)  
- Rules are reactive (fire on event); no proactive periodic checks  
- Hook execution synchronous; long-running rule blocks Publish()

---

### handoff/chain.go (agent-to-agent context transfer)
**What it does:**  
Transfer context from one agent invocation to next (e.g., Dev → Reviewer → CTO). Chains agents with context bridging.

**Concurrency:**  
- Minimal concurrency; handoff is sequential transition  

**Primitives to reuse:**  
- Context snapshot/restore pattern  
- Chain composition

---

## CONTEXT-SHARING

### memory/memory.go (persistent cross-session knowledge)
**What it does:**  
Store of learned facts (gotchas, patterns, fixes, codebase facts). Persists across sessions. Supports decay over time (older entries lose confidence).

**Concurrency:**  
- Line ~51-56: `type Store struct { mu sync.RWMutex, entries, path, nextID, maxAge }`  
- `Remember()`, `Recall()` acquire `mu.RWMutex`  
- Single-threaded writes; multiple readers allowed  
- File-backed: `s.load()` / `s.save()` marshals/unmarshals JSON (not atomic across process boundaries)

**Primitives to reuse:**  
- RWMutex for readers >> writers workload  
- Category-based organization (gotcha, pattern, fact, anti-pattern, fix, preference)  
- Confidence decay model (time-based staleness)  
- Tag-based lookup

**Gaps:**  
- No inter-session consensus on memory (no voting or conflict resolution)  
- File persistence not atomic; race between processes is possible  
- No versioning; updates replace entries  
- Decay is passive (computed on `Recall()`); no background cleanup

---

### wisdom/wisdom.go (cross-task learnings — assumed similar to memory)
**What it does:**  
Similar to memory but focuses on cross-task patterns and anti-patterns learned during workflow.

**Concurrency:**  
- Likely similar RWMutex pattern as memory  

---

### context/context.go (three-tier context budget & progressive compaction)
**What it does:**  
Assembles context blocks (L0 identity, L1 critical, L2 topical, L3 deep) and progressively compacts when utilization exceeds thresholds. Manages token budget for entire conversation.

**Concurrency:**  
- Line ~76-82: `type Manager struct { budget, blocks, compactCount, lastCompaction, peakUtil }`  
- Single-threaded usage (no mutex); assumes caller serializes `Add()` and `Compact()` calls  
- Per-phase compaction hook in agentloop (compactFn called before each API call)

**Primitives to reuse:**  
- Tier-based priority (L0 always loaded, L1 always loaded, L2/L3 on-demand)  
- Progressive compaction levels (gentle, moderate, aggressive)  
- Block-based composition (modular context chunks)

**Gaps:**  
- No concurrent context assembly (blocks added sequentially)  
- No inter-phase context reuse (each phase rebuilds from scratch)  
- No caching of expensive computations (repomap, semantic search results)  
- Compaction is single-threaded; cannot prepare next phase's context in parallel

---

### microcompact/microcompact.go (cache-aligned context compaction)
**What it does:**  
Compact context to cache-line boundaries (align context blocks to 1024-token chunks for prompt cache efficiency).

**Concurrency:**  
- Not detailed; likely single-threaded transformation  

---

### promptcache/promptcache.go (cache-aligned prompt construction)
**What it does:**  
Construct prompts to maximize cache hits (system prompt + instructions are static and cached; only user input changes per turn).

**Concurrency:**  
- Cache hits are per-turn benefit; no inter-turn concurrency

---

## WORKFLOW INTEGRATION

### workflow/workflow.go (phase machine: plan → execute+verify loop)
**What it does:**  
Drives task lifecycle: Plan → Execute → Verify (retry loop), with hooks for before/after task, before retry. Retries on failure up to limit; compiles final merge at end.

**Concurrency:**  
- Line ~100: `type Engine struct { ... }`; task execution is sequential  
- Hooks are synchronous callbacks (BeforeTask, AfterTask, BeforeRetry)  
- No goroutine spawning within Engine; relies on scheduler for task-level parallelism

**Primitives to reuse:**  
- Task hook interface (pluggable callbacks for wisdom, costtrack, critic)  
- Retry logic with failure analysis  
- Phase sequencing (plan → execute → verify)

**Gaps:**  
- No parallel phase execution (plan waits for execute; execute waits for verify)  
- No background phase preparation (e.g., compile plan for next task while current executes)  
- No inter-phase data exchange (plan doesn't inform execute in real-time; static once generated)

---

### mission/runner.go (mission lifecycle: Created → Researching → Planning → Executing → Validating → Converged → Completed)
**What it does:**  
State machine runner for mission lifecycle. Delegates to pluggable phase handlers. Convergence loop: Validating ↔ Executing until gaps close.

**Concurrency:**  
- Line ~87-90: `type Runner struct { store *Store, config, handlers }`  
- `Run()` is sequential state machine; phase handlers are synchronous callbacks  
- Convergence loop retries Executing if Validating finds gaps (no parallel validation+execution)

**Primitives to reuse:**  
- Phase handler interface (pluggable)  
- Convergence loop counter (limit retries)  
- Gap tracking (what failed, why, what to retry)  
- Consensus voting (2+ models must agree for completion)

**Gaps:**  
- No parallel phase execution (phase N waits for phase N-1 to complete)  
- Convergence loop is synchronous; cannot validate while executing other tasks  
- No predictive lookahead (e.g., prefetch research before execute phase)

---

### orchestrate/orchestrator.go (wires mission store, convergence validator, research, handoff)
**What it does:**  
Integration layer between mission lifecycle and workflow engine. Creates missions, runs convergence, bridges to research storage and handoff chain.

**Concurrency:**  
- Line ~? (not read in detail): likely synchronous delegation to mission.Runner and workflow.Engine

---

### conversation/runtime.go (multi-turn conversation state)
**What it does:**  
Maintains message history with roles, content blocks, timestamps, token counts. Supports add/read operations for conversation continuity.

**Concurrency:**  
- Line ~67-74: `type Runtime struct { mu sync.RWMutex, messages, systemPrompt, maxTokens, totalTokensIn/Out }`  
- `AddMessage()` acquires `mu.Lock()`; `Messages()` acquires `mu.RLock()`  
- Copy-on-read (returns deep copy of messages slice) prevents caller mutation

**Primitives to reuse:**  
- RWMutex for reader-heavy workload  
- Token count accumulation  
- Message history as append-only log

**Gaps:**  
- No inter-turn optimization (cannot prefetch next turn's context)  
- No message pruning (history grows unbounded until manual compaction)  
- No compression of repeated patterns (same tool results redacted)

---

### repl/repl.go (interactive REPL — blocking on user input)
**What it does:**  
Read-eval-print loop for interactive CLI. Blocks on `bufio.Scanner.Scan()` for user input; dispatches to handlers (chat, interview, view).

**Concurrency:**  
- Single-threaded blocking I/O  
- Caller can spawn goroutine to avoid blocking main thread  
- No concurrent command execution (one command at a time)

**Gaps:**  
- No background worker while waiting for input  
- Cannot mix input + concurrent background computation  
- No async result streaming (results printed after command completes)

---

### chat_interactive_cmd.go (interactive chat loop)
**What it does:**  
CLI loop for interactive multi-turn chat. Prompts user for task, approves plan, executes, loops. Serializes: task → plan approval → execution → next task.

**Concurrency:**  
- Line ~139-150+: `for { task, err := s.prompt("task> "); ... }`  
- Blocking on user input (scanner.Scan())  
- Sequential task → plan → execute flow (no concurrency)

**Primitives to reuse:**  
- Prompt/approval gate pattern  
- Conversation history persistence  
- Execution result streaming

**Gaps:**  
- User waits for plan generation before approving (no streaming)  
- Cannot modify plan while execution runs (no concurrent edit+execute)  
- No background monitoring (e.g., "plan is building, do you want to read partial output?")

---

## SUMMARY: EXISTING CONCURRENCY LANDSCAPE

| Package | Goroutines? | Mutexes? | Channels? | Fan-out | Early Exit | Shared State |
|---------|-----------|----------|-----------|---------|-----------|--------------|
| agentloop | Yes (tools) | No | No | WaitGroup | No | Tool results slice |
| specexec | Yes (strategies) | Yes | Yes (sem) | Semaphore | Yes (earlyStop) | Outcomes array |
| scheduler | Yes (tasks) | Yes | Yes (results) | WaitGroup | No | Completed/failed/running maps |
| branch | No | Yes (explorer) | No | — | — | Branch map |
| bus | Yes (subscribers) | Yes | Yes (per-sub) | Buffered chans | Yes (overflow) | WAL + sub/hook indices |
| dispatch | No | Yes | No | — | — | Message queue + idempotency |
| harness | No | Yes | No | — | — | Stance map |
| concern | No | Yes | — | — | — | Ledger read-only |
| supervisor | No | No (sync in hook) | — | — | — | Rules array |
| memory | No | Yes (RWMutex) | No | — | — | Entries slice |
| context | No | No | No | — | — | Blocks slice |
| workflow | No | No | No | — | — | Task state |
| mission | No | No | No | — | — | Mission state (store) |
| conversation | No | Yes (RWMutex) | No | — | — | Messages slice |
| repl | No | No | No | — | — | None |

**Key observations:**
1. Parallelism is **isolated islands**: agentloop tools run in parallel, specexec strategies run in parallel, scheduler tasks run in parallel — but there's **zero inter-island communication**.
2. **No shared mutable state across goroutines** (except in scheduler's completed/failed maps, which are updated sequentially via drain loop).
3. **Event/message plumbing is async-delivery, sync-decision**: Bus hooks fire synchronously (blocking Publish), subscribers fire async but cannot influence other subscribers.
4. **All orchestration is sequential state machines** (mission phases, workflow phases, task phases) — phases do not overlap, next phase waits for prior to complete.
5. **Memory/wisdom are isolated stores** — learned facts do not feed back into concurrent execution (only read at start of task).
6. **Context is static per-turn** — assembled once at phase start, does not adapt mid-turn based on tool results.
7. **Interactive mode is fully blocking** — user waits for plan, plan waits for execution, execution waits for completion; no overlap.

---

## ARCHITECTURAL GAP FOR "PARALLEL THOUGHTS WITH SHARED CONTEXT"

**Central missing primitive:** **Shared-memory concurrent thought threads** with **full context visibility** but **isolated execution paths**.

Today's architecture:
- **Specexec:** N isolated strategies, no inter-strategy communication.
- **Scheduler:** N isolated tasks, only file conflicts halt dispatch.
- **Bus:** N isolated subscribers, hooks cannot coordinate.
- **Agentloop:** N parallel tool calls within one turn, but turn-level serialization.

Needed for parallel thoughts:
1. **Concurrent thought goroutines** within agentloop (not just tool execution, but parallel sub-agents thinking about *different* aspects simultaneously).
2. **Shared read-only context** (all goroutines see same plan, same mission state, same memory, same conversation) — reads must not block, writes must be coordinated.
3. **Thought result merging** (not "pick the winner" like specexec, but "collect insights from all thinking threads and synthesize").
4. **Inter-thought communication** (e.g., "thought A found a pattern, thought B should explore that direction").
5. **Progressive context updates** (as thought threads discover new facts, shared context updates so other threads see them).
6. **Turn-level synchronization** (all thoughts complete before API call; aggregate their suggestions into one API message).

**Existing primitives to build on:**
- `sync.WaitGroup` (proven pattern in agentloop + specexec + scheduler)
- `sync.RWMutex` (proven in memory + conversation + concern builder for read-heavy scenarios)
- `chan` with buffering (proven in bus for async delivery, scheduler for results)
- Hook injection in bus (proven for supervisor interventions)
- Context compaction in agentloop (proven hook for pre-API checks)
- Branch explorer (proof of concept for branching; needs async execution)
- Concern field projection (proof of concept for role-specific context; needs dynamic updates)

**Central redesign needed:**
- Lift `WaitGroup + RWMutex` pattern from tool-level (agentloop) → turn-level → phase-level → mission-level.
- Convert subscribers from isolated async handlers → participating thought threads with shared context.
- Replace "pick best outcome" (specexec) with "synthesize all outcomes" (parallel thoughts merge).
- Change mission/workflow phases from sequential state machine → concurrent phase participants.
- Make context blocks mutable + observable (RWMutex); allow thoughts to append discoveries in real-time.

---

## FILE/LINE REFERENCES (Key Implementation Details)

### Agentloop
- `internal/agentloop/loop.go:233-252` — Loop struct, config, SetEventBus hook
- `internal/agentloop/loop.go:266-275` — Run() entry point, RunWithHistory()
- `internal/agentloop/loop.go:294-303` — CompactFn hook (pre-API call)
- `internal/agentloop/loop.go:?` (not shown) — executeTools() with WaitGroup

### Specexec
- `internal/specexec/specexec.go:113-220` — Run() with WaitGroup, semaphore, early stop

### Scheduler
- `internal/scheduler/scheduler.go:55-100` — Scheduler struct, maxWorkers, file locks, state maps
- `internal/scheduler/scheduler.go:112-250+` — Run() with WaitGroup, drainResults non-blocking loop

### Bus
- `internal/bus/bus.go:196-210` — Subscription struct, per-subscriber goroutine pattern
- `internal/bus/bus.go:213-258` — Subscription.run() with panic recovery, event drain on context done
- `internal/bus/bus.go:269-286` — Bus struct, hooks, subscribers, delayed events, indices
- `internal/bus/bus.go:312+` — Publish() (hooks sync, subscribers async)

### Harness
- `internal/harness/harness.go:32-50` — Harness struct, stances map, RWMutex

### Memory
- `internal/memory/memory.go:51-84` — Store struct, RWMutex, Remember/Recall pattern

### Conversation
- `internal/conversation/runtime.go:67-74` — Runtime struct, RWMutex, AddMessage/Messages copy-on-read

### Mission
- `internal/mission/runner.go:85-100+` — Runner struct, phases, convergence loop config

### Orchestrate
- `internal/orchestrate/orchestrator.go:58-93` — Config, ExecuteFn, ValidateFn, ConsensusModelFn callbacks

---

## CONCLUSION

R1-agent has **mature, battle-tested concurrency primitives** — `WaitGroup`, `RWMutex`, buffered channels, semaphores, panic recovery, context timeouts. These are deployed at three isolated levels: tool-execution (agentloop), strategy-competition (specexec), and task-dispatch (scheduler).

**The gap is not primitive quality but architectural scope.** Parallelism today is **local, disconnected, and winner-take-all** (specexec picks best; scheduler picks tasks; tools run but turn waits for all). For "parallel thoughts with shared context," the gap is:
1. **Thought threads need shared, observable state** (today only shared input, no shared output/discovery).
2. **Phases need concurrent execution** with **progressive merging** (today phases are sequential state machine).
3. **Memory/wisdom need real-time feedback** from concurrent execution (today they're read-only at phase start).
4. **Interactive mode needs streaming** (today fully blocking).

**Next steps** would be: design "Thought Context" struct (RWMutex-protected mutable context blocks), wrap agentloop in "Phase Orchestrator" spawning N thought threads per phase, merge their discoveries pre-API call, and propagate back to mission state.

