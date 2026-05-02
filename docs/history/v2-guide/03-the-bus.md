# 03 — The Bus

The bus is Stoke's runtime event and hooks engine. It is a peer to the ledger, not nested inside it. The ledger is the persistent substrate; the bus is the runtime substrate. The ledger holds what Stoke remembers; the bus carries what Stoke is doing right now. Together they are the two foundations every other component is built on.

Workers emit events on the bus as they work. The ledger emits change-stream events on the bus as nodes and edges get committed. Hooks subscribe to event patterns and fire when matched. The supervisor (component 4) is the primary user of the bus — the supervisor's rules are implemented as hooks that fire on event patterns and inject forced invocations back into the worker pool. Other consumers (the dashboard, the bench, debugging tools, replay sessions) also subscribe to the bus for their own purposes.

**Architectural principle: nothing in Stoke polls.** Stoke is event-driven from top to bottom. There are no periodic re-evaluation loops, no tickers, no "every N seconds the supervisor walks the state to see if anything needs doing." Every supervisor rule fires on a specific event or specific event combination. Time-based behavior (timeouts, deadlines, slow-drift detection, scheduled check-ins) is implemented through the bus's `PublishDelayed` primitive, which schedules an event for delivery after a duration and supports cancellation if the watched condition resolves before the timer fires. The supervisor's rules then fire on the delivered delayed events the same way they fire on any other event. This is non-negotiable: a polling loop anywhere in Stoke is an architectural violation, and the validation gates check for it.

This file specifies what the bus is and what its API contracts are. It does not specify implementation details that may change as the codebase matures. The contracts are what other components depend on, and the contracts are what get locked in here.

---

## What the bus carries

Three categories of events flow through the bus:

**Worker events.** Emitted by stances (workers) as they execute. These describe what a worker is doing, what state transitions the worker is going through, what claims the worker is making, and what the worker is asking for. Examples: `worker.action.started`, `worker.action.completed`, `worker.declaration.done`, `worker.dissent.raised`, `worker.escalation.requested`, `worker.consensus.invitation.sent`, `worker.research.requested`. Worker events are always tagged with the worker's stance ID, the loop the worker is operating within, the task DAG node the worker is scoped to, and the supervisor instance responsible for the worker.

**Ledger change-stream events.** Emitted by the ledger every time a node or edge is committed. These let the supervisor and other consumers react to ledger state without polling. Examples: `ledger.node.added` (with node type and ID), `ledger.edge.added` (with edge type and source/target IDs), `ledger.batch.committed`. The ledger change-stream events are produced by the ledger component itself, after the git commit has succeeded, and are guaranteed to fire in the order the commits happened.

**Supervisor events.** Emitted by supervisor instances when they take action — when a rule fires, when a hook is injected, when a branch supervisor's completion proposal is evaluated, when an escalation is forwarded upward, when a Judge invocation is forced. Examples: `supervisor.rule.fired`, `supervisor.hook.injected`, `supervisor.branch.completion.evaluated`, `supervisor.escalation.forwarded`, `supervisor.judge.forced`. Supervisor events make the supervisor's behavior auditable in the same way worker events make the worker's behavior auditable.

All three categories share the same bus and the same routing. A subscriber that wants only ledger events filters by event-type prefix; a hook that needs to react to a worker event combined with a ledger condition can subscribe to both and correlate them. There is no separation of streams — one bus, multiple event categories, filtered subscriptions.

---

## Events as data

Every event is a structured record with required fields:

- **Type.** A dotted-namespace identifier that uniquely identifies the event kind. Used by subscribers to filter and by hooks to match.
- **Timestamp.** When the event was emitted. Monotonic within an emitter; causally ordered across emitters where causality is known.
- **Emitter.** Who produced the event — a worker stance ID, the ledger component, a supervisor instance ID.
- **Scope.** Tags identifying which mission, which branch, which loop, which task DAG node, which ledger node the event relates to. Scope is the primary input for hook matching — most hooks are interested in events from a specific scope rather than from everywhere.
- **Payload.** The event-type-specific data. A `worker.declaration.done` event's payload includes what the worker is declaring done; a `ledger.node.added` event's payload includes the node ID and type.
- **Causality.** Optional reference to the parent event that triggered this one, when causality is known. A worker action that was injected by a supervisor hook carries a causality reference back to the hook firing event. This is what makes the audit trail walkable in both directions.
- **Sequence number.** Globally ordered per mission. Used by replay and durability — the supervisor advances a cursor over the sequence number to track what it has processed.

Events are immutable once published. The bus does not have an `update` or `redact` operation. An event that turns out to be wrong is followed by a corrective event; the original remains in the stream forever (as long as the stream's retention policy keeps it). Same property as the ledger, applied to the runtime side: the historical record cannot be silently rewritten.

---

## The API surface

The bus exposes a small Go package at `internal/bus`. The public surface is:

```go
package bus

// Publish emits an event onto the bus. Returns the assigned sequence
// number. The event is durable before this call returns — a subsequent
// crash will not lose the event. Subscribers and hooks see the event
// after publication, not before.
Publish(ctx context.Context, evt Event) (SeqNum, error)

// Subscribe registers a passive subscriber for events matching the
// given pattern. The subscriber receives events as they are published,
// in sequence order, after Publish has returned. Subscribers cannot
// affect the events they receive — they observe.
//
// The pattern can match by event type, by scope, or both, with wildcards.
// A subscriber can hold its position in the stream via a cursor, so a
// reconnecting subscriber resumes from where it left off rather than
// missing events that arrived during disconnection.
Subscribe(ctx context.Context, pattern Pattern, handler func(Event)) (Subscription, error)

// RegisterHook registers an active hook for events matching the given
// pattern. Unlike subscribers, hooks have action authority — when a
// hook fires, it can inject new events, create worker stances, force
// state transitions on ledger nodes, and pause in-flight workers.
//
// Hooks fire after the matching event has been published and durability-
// committed, but before the event is delivered to passive subscribers.
// This ensures that hooks see the event before any subscriber can
// observe its consequences.
//
// Hook registration is privileged — only the supervisor and components
// the supervisor explicitly authorizes can register hooks. The bus
// validates the caller's authority at registration time.
RegisterHook(ctx context.Context, pattern Pattern, hook Hook) (HookID, error)

// Replay walks the event stream from a given sequence number forward,
// delivering historical events to a handler in order. Replay is
// read-only — it does not re-fire hooks, does not re-create workers,
// does not re-commit ledger nodes (the ledger commits already happened).
// Used for debugging, post-mortem analysis, and warm-starting new
// subscribers that missed events.
Replay(ctx context.Context, fromSeq SeqNum, pattern Pattern, handler func(Event)) error

// Cursor returns the current sequence number, useful for subscribers
// that want to record their position before a planned shutdown.
Cursor(ctx context.Context) (SeqNum, error)

// PublishDelayed schedules an event for delivery after the specified
// duration. Returns a cancellation handle that can be used to cancel
// the delivery before the timer fires. This is the bus's mechanism for
// time-based triggers in an event-driven architecture: schedule a future
// event at the moment a watched condition begins, cancel it when the
// condition resolves before the timer fires, and the supervisor's rules
// react to the delivered event the same way they react to any other event.
//
// Delayed events are durable. A crash before the timer fires preserves
// the schedule; the bus restarts the timer on recovery. The cancellation
// handle is also durable — a cancellation issued before the timer fires
// is honored across crashes.
//
// Used by the supervisor for: consensus partner timeouts, slow-drift
// detection, budget threshold checks that need to fire after a duration
// rather than after an event count, and any other rule that needs to
// fire after a wall-clock interval rather than in response to an event.
//
// There is no polling anywhere in Stoke. Time-based behavior is
// implemented through this primitive, not through periodic checks.
PublishDelayed(ctx context.Context, evt Event, after time.Duration) (CancellationHandle, error)

// CancelDelayed cancels a previously scheduled delayed event. If the
// event has already been delivered, the call is a no-op. If the event
// is still pending, it is removed from the schedule and never delivered.
CancelDelayed(ctx context.Context, handle CancellationHandle) error
```

The Event, Pattern, Subscription, HookID, and Hook types are structured records and interfaces with their own fields. The event schema is shared across all event categories; the pattern syntax supports type-prefix matching, scope-tag matching, and combinations of both.

There is no `Unsubscribe` from a passive subscription that's been delivered events — the subscription's lifetime is bound to its context, and cancelling the context terminates delivery. There is `RemoveHook(HookID)` for hook deregistration, because hook registration is privileged and an authorized component may need to clean up hooks it installed.

The API has no operation to modify a published event, redact it, delete it, or skip it in delivery to consumers that haven't yet seen it. Events flow forward; nothing reaches backward into the stream.

---

## Hooks have action authority

This is the property that distinguishes hooks from passive subscriptions, and it is the load-bearing piece of the supervisor's enforcement model.

A passive subscriber receives events and does whatever it wants with them in its own scope — it might log them, render them in a dashboard, count them, route them to another system. It cannot change anything in Stoke's worker or ledger state from inside the handler. A subscriber observes.

A hook, by contrast, has authority to act on Stoke's runtime state when it fires. Specifically, a hook can:

- **Inject a synthetic event** onto the bus, which downstream subscribers and hooks see as if it had been published normally. This is how a supervisor hook can cause a forced consensus loop to start: the hook fires on `worker.declaration.done`, evaluates the rule, and injects a `consensus.review.required` event that triggers the creation of a Reviewer stance.
- **Create a new worker stance.** The hook can call into the harness to spawn a worker of a specific type (Reviewer, Judge, CTO consultation, etc.), pass it the relevant context (the artifact under review, the loop history, the original user intent), and put it to work. The new worker emits its own events as it runs, and those events flow back through the bus normally.
- **Force a state transition on a ledger node.** The hook can write a ledger node that explicitly marks a loop or artifact as transitioning to a new state. This is how the supervisor's "loop has converged" determination becomes effective — the supervisor evaluates the convergence criterion against the ledger, and if it holds, the supervisor fires a hook that commits the convergence transition.
- **Pause an in-flight worker.** The hook can signal a worker to halt at the next safe checkpoint, holding its state, until the supervisor releases it. This is how forced second-opinion checks work — when a worker emits a "done" declaration, the supervisor pauses the worker and spawns a fresh-context Reviewer; the worker is unpaused only after the Reviewer has agreed (or after the dissent has been resolved through another iteration).

These four powers — inject events, create workers, transition ledger nodes, pause workers — are the entire action surface of hooks. A hook cannot delete events, cannot modify ledger nodes (only add new ones), cannot kill workers (only pause them), cannot register new hooks dynamically (hook registration is the supervisor's job at startup, not runtime). The action surface is intentionally narrow because the bus is the medium through which Stoke's enforcement flows — a wider action surface would create more places for the rules to leak.

**Hook authority is privileged.** Only the supervisor (and components the supervisor explicitly authorizes at startup) can register hooks. A worker stance trying to register a hook gets an error. This is the structural defense against workers bypassing the rules: workers can publish events, but they cannot install handlers that would let them respond to their own events with action authority. The action authority chain runs from the supervisor downward; workers never get it.

---

## Ordering and causality

Events on the bus are ordered globally per mission. Two events from the same mission can always be compared by sequence number, and the comparison reflects causal order where causality is known.

**Within a single emitter, events are strictly ordered.** A worker that publishes event A and then event B is guaranteed that A appears in the stream before B and that all subscribers see A before B. This is the trivial case.

**Across emitters, events are causally ordered.** If event B is caused by event A (the causality field on B references A), then B's sequence number is greater than A's. The bus enforces this at publish time — an event whose causality reference points to a sequence number greater than or equal to the current cursor is rejected. This prevents temporal anomalies where a "consequence" event appears in the stream before its "cause" event.

**Across emitters with no known causality, events are sequence-ordered but not causally meaningful.** Two workers publishing events with no causality relationship between them get sequence numbers in arbitrary order, and a subscriber should not infer causation from sequence-number ordering alone.

The supervisor uses causality references heavily. When the supervisor evaluates a rule, the events the rule fires on are referenced by causality from the resulting hook fire event, which is referenced by causality from the worker spawn event, which is referenced by causality from the events the new worker emits. The full chain of "this happened because that happened because the original event was X" is walkable forward and backward. This is what makes the runtime auditable — every action in Stoke can be traced to the events that produced it.

---

## Durability and replay

Events are durable before `Publish` returns. A crash immediately after `Publish` returns does not lose the event. The supervisor and other subscribers can recover from crashes by reading their last cursor position from durable storage and replaying from that point forward.

The durable backing store for the bus is **append-only**, like the ledger but operationally rather than via git. The bus event stream is implemented as a write-ahead log per mission, stored under `.stoke/bus/{mission-id}/events.log`. The log is sequentially numbered and cannot be modified after a write completes. Old events are retained according to a per-mission retention policy (default: keep events for the lifetime of the mission, plus some grace period after mission completion for post-mortem analysis).

Replay reads from the log without re-firing hooks or re-creating workers. A replay is a *read* of historical events into a *new* handler that is told "you are reading replayed events, do not act as if these are happening live." Most replay consumers are debugging tools or analysis scripts; the supervisor itself uses replay only at startup to rebuild its in-memory state (which workers are active, which loops are in flight, which hooks are armed) from the durable event log after a crash.

The replay-without-side-effects rule is enforced by the bus API: `Replay` delivers events through a separate handler signature that does not have access to the action surface. A handler reading replayed events cannot inject events, create workers, transition ledger nodes, or pause workers. The bus structurally prevents replay from causing side effects, which means a replay can never accidentally re-execute something that already happened.

**Durability is a contract, not best-effort.** The bus does not return success from `Publish` until the event is on durable storage. The cost is a small write latency per event; the benefit is that the supervisor can rely on "if I saw this event, it survived." For high-volume events that don't need durability (e.g., debug-level worker traces), the bus has a separate `PublishEphemeral` path that delivers to subscribers but does not write to the durable log. Ephemeral events are not visible to replay and are not used for any rule that affects worker or ledger state. The default is durable; ephemeral is opt-in for performance-sensitive use cases that don't need the audit trail.

---

## Hook conflict resolution

Multiple hooks can match the same event. The bus resolves the order in which they fire, and the rules for how their actions interact, as follows:

**Hooks have explicit priorities** assigned at registration time. Higher-priority hooks fire first. When two hooks have the same priority and match the same event, they fire in registration order (earlier-registered hooks fire first). The supervisor's hooks are conventionally given the highest priority because the supervisor is the rule enforcer.

**Hook actions are sequenced.** When hook A fires and injects a new event, hook B (which would also have matched the original event) does not see the injected event until after it has fired on the original event and returned. This prevents two hooks from racing on the same matching event. Each hook gets a complete pass on the original event before any of them gets to see consequences of the others.

**Conflicting actions are resolved by hook priority.** If hook A pauses a worker and hook B (lower priority) wants to send the same worker an action, hook B sees the worker as paused and either no-ops or queues its action for after the pause is released. Pause is the strongest action — once a worker is paused, no other action against it can take effect until the pause is released. This is the property that makes second-opinion enforcement reliable: once the supervisor pauses a worker pending a fresh-context Reviewer, no other rule can cause the worker to keep going.

**The supervisor resolves cross-supervisor conflicts.** If a branch supervisor and the mission supervisor both have hooks that match the same event, the mission supervisor's hook wins by priority. This is how the hierarchy enforcement (component 4) gets implemented: hierarchical authority maps to hook priority.

---

## Scope and propagation across the supervisor hierarchy

The bus is per-mission, but the supervisor hierarchy means events from a branch may need to be visible to the mission supervisor as well as to the branch supervisor.

The default propagation rule is **filtered upward propagation**. When a worker in branch A emits an event, the branch A supervisor sees it (full fidelity). The mission supervisor sees a filtered subset — structural events (worker spawned, worker completed, completion proposed, escalation raised, judge forced, branch state transitions) but not high-volume operational events (every action, every minor consensus partner reply, every worker heartbeat).

The filter is configurable per mission. The wizard surfaces the filter as a tradeoff: tighter filtering means lower load on the mission supervisor but a less complete view at the mission level; looser filtering means more complete visibility but more load. The default leans toward tighter filtering on the assumption that most missions don't need the mission supervisor to see every worker heartbeat in every branch.

**Filtering happens at publication time, not at delivery time.** When a worker publishes an event, the bus determines which scopes (branch, mission) need to see it based on the propagation rules and tags the event accordingly. Subscribers and hooks at each scope only see events tagged for their scope. This means the mission supervisor's hooks never have to filter out branch noise themselves — the noise simply never reaches them.

**Workers always emit events to their own branch supervisor.** A worker doesn't address events to the mission supervisor directly. The branch supervisor (and the propagation rules) decides what gets forwarded upward. This preserves the principle that the supervisor hierarchy is the authority chain: subordinate workers talk to their direct supervisor, which decides what to escalate.

---

## What the bus does not do

A few things the bus explicitly does not handle, with brief notes on where they live instead:

- **Persistent reasoning.** Reasoning, decisions, plans, skills, snapshot annotations — these are ledger nodes, not bus events. The bus carries notifications about ledger changes (the change stream) but the reasoning itself lives in the ledger's append-only graph. A bus event might say "a new decision node was committed"; the decision's content lives in the ledger, accessed by ID.

- **Source code.** Source code is in the repo's normal git tracking. Bus events may reference code (a worker emitting `worker.code.committed` with the commit SHA), but the code itself is not in the bus. Same boundary as the ledger.

- **Stance session state.** A worker stance's in-flight model context, system prompt, and reasoning trace are held by the stance itself in its session, not in the bus. The bus carries the events the worker chooses to emit; it does not have visibility into the worker's internal state. When a worker is paused, the stance holds its own state; when it is resumed, it picks up from where it left off in its own session.

- **Configuration.** Stoke's configuration (the wizard's output, the supervisor's rule definitions, the bus's filter rules) lives in `.stoke/config.yaml` and related files, not in the bus. Configuration changes are normal git commits; they may emit events when applied (`config.changed`) but the configuration data is not bus state.

- **Cross-mission coordination.** The bus is per-mission. Two missions running concurrently against the same repo have two separate buses. They share the ledger (which is per-repo) but they do not share runtime event streams. If two missions need to coordinate, they do so through the ledger, not through the bus. This keeps each mission's runtime state independent and bounded.

The bus carries the *runtime stream of what is happening right now*. Everything else — what was decided, what the code looks like, how the system is configured — lives in its appropriate layer.

---

## Validation gate

Before any other component depends on the bus, the bus has to pass its own validation gate. The gate is:

1. ✅ `go vet ./...` clean, `go test ./internal/bus/...` passes with >70% coverage
2. ✅ `go build ./cmd/r1` succeeds
3. ✅ Publish persists events to durable storage before returning (verified by crash-during-publish test)
4. ✅ Two subscribers to the same pattern receive events in the same order
5. ✅ Hooks fire before passive subscribers see the event
6. ✅ Hook registration from a non-supervisor caller is rejected (verified with a test that registers from a worker context)
7. ✅ Hooks fire in priority order; same-priority hooks fire in registration order
8. ✅ A paused worker cannot have its pause overridden by a lower-priority hook
9. ✅ Replay does not have access to the action surface (verified by API shape — the Replay handler signature does not include the action methods)
10. ✅ Replay does not re-fire hooks or re-create workers (verified by integration test that replays a recorded event log and confirms no side effects)
11. ✅ Causality references that point to future sequence numbers are rejected at Publish time
12. ✅ Filtered upward propagation works: a worker event in a branch scope reaches the branch supervisor's subscribers but not the mission supervisor's subscribers (when the filter excludes that event type)
13. ✅ Cross-mission isolation: two missions running concurrently against the same repo have independent event streams that do not cross-contaminate
14. ✅ Ephemeral events are not written to the durable log (verified by checking the log file size after a series of ephemeral publishes)
15. ✅ The bus restarts cleanly from a crash mid-stream, with the supervisor recovering its cursor and resuming hook firing
16. ✅ A delayed event scheduled with `PublishDelayed` is delivered after the specified duration
17. ✅ A delayed event cancelled before its timer fires is never delivered
18. ✅ Delayed events survive a crash — a delayed event scheduled before a crash is delivered after recovery, with its remaining duration honored
19. ✅ Cancellation of a delayed event survives a crash — a cancellation issued before a crash is honored after recovery, even if the cancellation race window means the timer might otherwise have fired during the crash
20. ✅ The supervisor codebase contains no polling loops, ticker-based periodic checks, or sleep-and-loop patterns (verified by grep against the supervisor and component packages — every time-based behavior must use `PublishDelayed`)
21. ✅ The validation gate is committed to `STOKE-IMPL-NOTES.md`

---

## Forward references

This file is component 3 of the new guide. It refers to several things specified in later components:

- **The supervisor** is component 4. This file describes the bus's hook authority model, which the supervisor uses, but does not specify the supervisor's specific rules. The supervisor's rules and hierarchy mechanics are in the next file.
- **The consensus loop** is component 5. The loop's state transitions are driven by hooks the supervisor fires on bus events. This file specifies that hooks have transition authority; the loop spec specifies which transitions correspond to which conditions.
- **The harness** is later. The harness is what creates worker stances when hooks call into it. This file specifies that hooks can create workers; the harness spec specifies how the creation actually happens (model selection, system prompt construction, context loading, session initialization).
- **Worker event types** are not enumerated here. The full taxonomy of `worker.*` events is part of the worker stance contracts, which are specified alongside the team roster's session-shape rules. This file specifies the event schema and routing; the worker event types are spec'd where the workers themselves are spec'd.

The next file to write is `04-the-supervisor.md`.
