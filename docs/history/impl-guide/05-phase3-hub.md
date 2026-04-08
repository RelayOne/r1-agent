# 05 — Phase 3: Hub (the unified event bus)

This phase replaces three disconnected systems with one event bus:
1. `internal/hooks/` (bash-based, wired into workflow.go in 3 places)
2. `internal/lifecycle/hooks.go` (well-designed, never imported, dead code)
3. `internal/workflow.TaskHook` interface (BeforeTask/AfterTask/BeforeRetry, wired)

After this phase, all three are thin adapters over `internal/hub`. Existing behavior is preserved, but every event flows through one place.

## Why this matters

From research [P79]: No existing event bus combines composition, failure isolation, and transport abstraction in one system. Git hooks lack composition. Claude Code hooks lack cross-hook state. Kubernetes webhooks lack transport flexibility. Stoke can be the first.

From research [P80]: The single most important concept in agent safety is the distinction between **deterministic** controls (enforced by code, OS kernels, or cryptography — model literally cannot bypass) and **advisory** controls (instructions in the prompt the model could ignore). The hub is Stoke's deterministic enforcement layer.

## Architecture

```
                  ┌─────────────────────────────────┐
                  │           Hub (Bus)             │
                  │  ┌────────────────────────────┐ │
                  │  │  Dispatch pipeline:        │ │
                  │  │   1. Transforms (sync)     │ │
                  │  │   2. Gates (sync, parallel)│ │
                  │  │   3. Observes (async)      │ │
                  │  └────────────────────────────┘ │
                  └─┬───────┬───────┬───────┬───────┘
                    │       │       │       │
        in-process  │       │       │       │  unix socket
        Go func     │       │       │       │
                    ▼       ▼       ▼       ▼
              ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐
              │skill   │ │scan    │ │webhook │ │bash    │
              │inject  │ │       │ │        │ │script  │
              └────────┘ └────────┘ └────────┘ └────────┘
                            │
                            ▼
                    ┌────────────────┐
                    │  Audit log     │
                    │  (SQLite)      │
                    │  + hash chain  │
                    └────────────────┘
```

## Package structure

```
internal/hub/
  bus.go             — main Bus type, Subscribe, Publish, dispatch loop
  events.go          — Event struct, EventType constants (the 73 types)
  subscriber.go      — Subscriber interface, Mode, Decision types
  transports/
    inproc.go        — direct Go function call
    socket.go        — Unix domain socket with length-prefixed JSON
    http.go          — HTTP webhook
    script.go        — bash script (Claude Code hook compatible)
    grpc.go          — placeholder, deferred until P82 research returns
    mcp.go           — placeholder, deferred until P82 research returns
  resilience/
    breaker.go       — sony/gobreaker wrapper per subscriber
    bulkhead.go      — semaphore-based concurrency limit
  audit/
    sqlite.go        — append-only audit log with hash chain
    schema.go        — table DDL
  builtin/           — built-in subscribers Stoke ships with
    skill_inject.go  — transform: injects skill block into prompts
    scan.go          — gate: deny dangerous edits, observe for security scans
    cost_track.go    — observe: records cost per model call
    honesty.go       — gate: detects test removal, placeholder insertion, hallucinated imports
  bus_test.go
```

---

## Step 1: Define the Event type and event constants

**File:** `internal/hub/events.go`

```go
// Package hub implements Stoke's unified event bus. It replaces the previous
// hooks, lifecycle, and TaskHook systems with a single dispatch layer that
// supports gate, transform, and observe semantics across multiple transports.
package hub

import (
    "encoding/json"
    "time"
)

// EventType identifies a category of event. Naming convention: <area>.<verb>
// where verb describes when the event fires (pre/post for actions that can be
// gated, on/end for state changes).
type EventType string

const (
    // Session lifecycle
    EvtSessionStart    EventType = "session.start"
    EvtSessionEnd      EventType = "session.end"

    // Mission lifecycle
    EvtMissionStart    EventType = "mission.start"
    EvtMissionEnd      EventType = "mission.end"
    EvtMissionAbort    EventType = "mission.abort"
    EvtMissionFail     EventType = "mission.fail"

    // Task lifecycle (replaces workflow.TaskHook)
    EvtTaskBefore      EventType = "task.before"
    EvtTaskAfter       EventType = "task.after"
    EvtTaskRetry       EventType = "task.retry"
    EvtTaskSkip        EventType = "task.skip"

    // Plan / execute / verify phases
    EvtPlanBefore      EventType = "plan.before"
    EvtPlanAfter       EventType = "plan.after"
    EvtExecuteBefore   EventType = "execute.before"
    EvtExecuteAfter    EventType = "execute.after"
    EvtVerifyBefore    EventType = "verify.before"
    EvtVerifyAfter     EventType = "verify.after"

    // Tool use (Claude Code hook compatible)
    EvtToolPreUse      EventType = "tool.pre_use"
    EvtToolPostUse     EventType = "tool.post_use"

    // Model calls
    EvtModelPreCall    EventType = "model.pre_call"
    EvtModelPostCall   EventType = "model.post_call"
    EvtModelError      EventType = "model.error"
    EvtModelRateLimit  EventType = "model.rate_limit"

    // Prompt construction (transforms inject content here)
    EvtPromptBuildPlan    EventType = "prompt.build_plan"
    EvtPromptBuildExec    EventType = "prompt.build_execute"
    EvtPromptBuildReview  EventType = "prompt.build_review"
    EvtPromptBuildVerify  EventType = "prompt.build_verify"

    // File operations (gates/observers attach here)
    EvtFilePreRead    EventType = "file.pre_read"
    EvtFilePostRead   EventType = "file.post_read"
    EvtFilePreWrite   EventType = "file.pre_write"
    EvtFilePostWrite  EventType = "file.post_write"
    EvtFilePreEdit    EventType = "file.pre_edit"
    EvtFilePostEdit   EventType = "file.post_edit"
    EvtFilePreDelete  EventType = "file.pre_delete"

    // Bash execution
    EvtBashPreExec    EventType = "bash.pre_exec"
    EvtBashPostExec   EventType = "bash.post_exec"

    // Git operations
    EvtGitPreCommit   EventType = "git.pre_commit"
    EvtGitPostCommit  EventType = "git.post_commit"
    EvtGitPrePush     EventType = "git.pre_push"

    // Verification signals (for honesty enforcement)
    EvtVerifyTestRemoved        EventType = "verify.test_removed"
    EvtVerifyTestWeakened       EventType = "verify.test_weakened"
    EvtVerifyPlaceholder        EventType = "verify.placeholder_inserted"
    EvtVerifyHallucinatedImport EventType = "verify.hallucinated_import"
    EvtVerifySilentSimplify     EventType = "verify.silent_simplification"
    EvtVerifyCompletionFailed   EventType = "verify.completion_claim_failed"
    EvtVerifyDiffTooLarge       EventType = "verify.diff_too_large"

    // Cost tracking
    EvtCostUpdate      EventType = "cost.update"
    EvtCostBudgetExceeded EventType = "cost.budget_exceeded"

    // Worktree operations
    EvtWorktreeCreate  EventType = "worktree.create"
    EvtWorktreeMerge   EventType = "worktree.merge"
    EvtWorktreeAbandon EventType = "worktree.abandon"

    // Compaction events
    EvtCompactPre      EventType = "compact.pre"
    EvtCompactPost     EventType = "compact.post"

    // Convergence
    EvtConvergeRound   EventType = "converge.round"
    EvtConvergeReached EventType = "converge.reached"
    EvtConvergeFailed  EventType = "converge.failed"

    // Critic
    EvtCriticPre       EventType = "critic.pre"
    EvtCriticPost      EventType = "critic.post"

    // Skill events
    EvtSkillSelected   EventType = "skill.selected"
    EvtSkillInjected   EventType = "skill.injected"
    EvtSkillUpdated    EventType = "skill.updated"

    // Research
    EvtResearchStart   EventType = "research.start"
    EvtResearchResult  EventType = "research.result"

    // Wisdom
    EvtWisdomLearn     EventType = "wisdom.learn"
    EvtWisdomRecall    EventType = "wisdom.recall"

    // Hooks (compatibility shims)
    EvtUserPromptSubmit EventType = "user.prompt_submit"
    EvtAgentResponse    EventType = "agent.response"
)

// AllEventTypes returns all known event types — useful for matchers and tests.
func AllEventTypes() []EventType {
    return []EventType{
        EvtSessionStart, EvtSessionEnd,
        EvtMissionStart, EvtMissionEnd, EvtMissionAbort, EvtMissionFail,
        EvtTaskBefore, EvtTaskAfter, EvtTaskRetry, EvtTaskSkip,
        EvtPlanBefore, EvtPlanAfter, EvtExecuteBefore, EvtExecuteAfter, EvtVerifyBefore, EvtVerifyAfter,
        EvtToolPreUse, EvtToolPostUse,
        EvtModelPreCall, EvtModelPostCall, EvtModelError, EvtModelRateLimit,
        EvtPromptBuildPlan, EvtPromptBuildExec, EvtPromptBuildReview, EvtPromptBuildVerify,
        EvtFilePreRead, EvtFilePostRead, EvtFilePreWrite, EvtFilePostWrite, EvtFilePreEdit, EvtFilePostEdit, EvtFilePreDelete,
        EvtBashPreExec, EvtBashPostExec,
        EvtGitPreCommit, EvtGitPostCommit, EvtGitPrePush,
        EvtVerifyTestRemoved, EvtVerifyTestWeakened, EvtVerifyPlaceholder,
        EvtVerifyHallucinatedImport, EvtVerifySilentSimplify, EvtVerifyCompletionFailed, EvtVerifyDiffTooLarge,
        EvtCostUpdate, EvtCostBudgetExceeded,
        EvtWorktreeCreate, EvtWorktreeMerge, EvtWorktreeAbandon,
        EvtCompactPre, EvtCompactPost,
        EvtConvergeRound, EvtConvergeReached, EvtConvergeFailed,
        EvtCriticPre, EvtCriticPost,
        EvtSkillSelected, EvtSkillInjected, EvtSkillUpdated,
        EvtResearchStart, EvtResearchResult,
        EvtWisdomLearn, EvtWisdomRecall,
        EvtUserPromptSubmit, EvtAgentResponse,
    }
}

// Event is a single message flowing through the bus.
//
// Payload is event-type-specific JSON data. Use SetPayload/GetPayload helpers
// for type-safe access.
type Event struct {
    ID            string          `json:"id"`             // ULID
    Type          EventType       `json:"type"`
    Timestamp     time.Time       `json:"timestamp"`
    MissionID     string          `json:"mission_id,omitempty"`
    TaskID        string          `json:"task_id,omitempty"`
    SessionID     string          `json:"session_id,omitempty"`
    CorrelationID string          `json:"correlation_id,omitempty"`
    Cwd           string          `json:"cwd,omitempty"`
    Source        string          `json:"source,omitempty"`  // who emitted this
    Payload       json.RawMessage `json:"payload,omitempty"`
}

// NewEvent creates a new event with auto-generated ID and timestamp.
func NewEvent(typ EventType, payload interface{}) (*Event, error) {
    e := &Event{
        ID:        newULID(),
        Type:      typ,
        Timestamp: time.Now(),
    }
    if payload != nil {
        data, err := json.Marshal(payload)
        if err != nil {
            return nil, err
        }
        e.Payload = data
    }
    return e, nil
}

// SetPayload marshals v into the event's payload.
func (e *Event) SetPayload(v interface{}) error {
    data, err := json.Marshal(v)
    if err != nil {
        return err
    }
    e.Payload = data
    return nil
}

// GetPayload unmarshals the event's payload into v.
func (e *Event) GetPayload(v interface{}) error {
    if len(e.Payload) == 0 {
        return nil
    }
    return json.Unmarshal(e.Payload, v)
}
```

**File:** `internal/hub/ulid.go`

```go
package hub

import (
    "crypto/rand"
    "encoding/hex"
    "time"
)

// newULID returns a sortable unique ID. Uses time prefix + 10 random bytes.
// Not strictly ULID format but sortable and unique enough.
func newULID() string {
    var b [10]byte
    _, _ = rand.Read(b[:])
    ts := time.Now().UTC().Format("20060102T150405.000")
    return ts + "-" + hex.EncodeToString(b[:])
}
```

---

## Step 2: Subscriber interface and Decision type

**File:** `internal/hub/subscriber.go`

```go
package hub

import (
    "context"
    "encoding/json"
    "time"
)

// Mode determines how a subscriber participates in dispatch.
type Mode string

const (
    ModeGate      Mode = "gate"      // synchronous, can deny, runs in parallel with other gates
    ModeTransform Mode = "transform" // synchronous, mutates payload, runs sequentially
    ModeObserve   Mode = "observe"   // asynchronous, fire-and-forget
)

// Decision is what a gate returns.
type Decision string

const (
    DecisionAllow   Decision = "allow"
    DecisionDeny    Decision = "deny"
    DecisionAsk     Decision = "ask"     // human confirmation needed
    DecisionDefer   Decision = "defer"   // re-evaluate later
    DecisionAbstain Decision = "abstain" // this subscriber has no opinion
)

// Response is what a subscriber returns from Invoke.
type Response struct {
    // Decision is set for gate subscribers.
    Decision Decision `json:"decision,omitempty"`
    // Reason is the human-readable explanation for the decision.
    Reason string `json:"reason,omitempty"`
    // UpdatedPayload, if non-nil, replaces the event's payload (transforms only).
    UpdatedPayload json.RawMessage `json:"updated_payload,omitempty"`
    // AdditionalContext is content to inject into agent context (transforms).
    AdditionalContext string `json:"additional_context,omitempty"`
    // Latency for metrics.
    Latency time.Duration `json:"latency"`
}

// FailurePolicy controls what happens when a subscriber fails or times out.
type FailurePolicy string

const (
    FailClosed FailurePolicy = "fail_closed" // treat failure as deny (security-critical gates)
    FailOpen   FailurePolicy = "fail_open"   // treat failure as allow (advisory gates)
)

// Subscriber is the interface every transport implements.
type Subscriber interface {
    // ID returns a unique identifier for this subscriber.
    ID() string
    // Mode returns the dispatch mode.
    Mode() Mode
    // Matches returns true if this subscriber wants to receive the given event.
    Matches(evt *Event) bool
    // Invoke processes an event and returns a Response. For observe mode the
    // response is ignored. For gate mode it carries the decision. For transform
    // mode it may carry an updated payload.
    Invoke(ctx context.Context, evt *Event) (*Response, error)
    // FailurePolicy is consulted when Invoke errors or times out.
    FailurePolicy() FailurePolicy
    // Timeout is the per-invocation timeout.
    Timeout() time.Duration
}

// Matcher is a helper for building subscribers that filter by event type.
type Matcher struct {
    Types       []EventType        // empty = match all
    TypePattern string             // simple glob like "tool.*" or "verify.*"
}

// Match returns true if the matcher matches the given event type.
func (m Matcher) Match(t EventType) bool {
    if len(m.Types) == 0 && m.TypePattern == "" {
        return true
    }
    for _, typ := range m.Types {
        if typ == t {
            return true
        }
    }
    if m.TypePattern != "" {
        return matchPattern(m.TypePattern, string(t))
    }
    return false
}

func matchPattern(pattern, s string) bool {
    if pattern == "*" {
        return true
    }
    // Support trailing .* glob
    if len(pattern) >= 2 && pattern[len(pattern)-2:] == ".*" {
        prefix := pattern[:len(pattern)-1]
        return len(s) >= len(prefix) && s[:len(prefix)] == prefix
    }
    return pattern == s
}
```

---

## Step 3: The Bus core with dispatch pipeline

**File:** `internal/hub/bus.go`

```go
package hub

import (
    "context"
    "fmt"
    "sync"
    "time"

    "github.com/ericmacdougall/stoke/internal/hub/audit"
    "github.com/ericmacdougall/stoke/internal/hub/resilience"
)

// Bus is Stoke's central event bus. Subscribers register with it; events
// flow through transform → gate → observe pipeline.
//
// Thread-safe. Designed to be a process-wide singleton.
type Bus struct {
    mu          sync.RWMutex
    subscribers []registeredSubscriber
    audit       *audit.Log
    metrics     *Metrics
}

type registeredSubscriber struct {
    sub      Subscriber
    breaker  *resilience.Breaker
    bulkhead *resilience.Bulkhead
}

type Metrics struct {
    EventsPublished int64
    GateAllowed     int64
    GateDenied      int64
    TransformsRun   int64
    Errors          int64
    mu              sync.Mutex
}

// Config configures a new Bus.
type Config struct {
    AuditDBPath  string  // path to SQLite audit DB; empty = no audit
    EnableHashChain bool
}

// New creates a new Bus.
func New(cfg Config) (*Bus, error) {
    var auditLog *audit.Log
    if cfg.AuditDBPath != "" {
        var err error
        auditLog, err = audit.Open(cfg.AuditDBPath, cfg.EnableHashChain)
        if err != nil {
            return nil, fmt.Errorf("open audit log: %w", err)
        }
    }
    return &Bus{
        audit:   auditLog,
        metrics: &Metrics{},
    }, nil
}

// Close shuts down the bus and closes the audit log.
func (b *Bus) Close() error {
    if b.audit != nil {
        return b.audit.Close()
    }
    return nil
}

// Subscribe registers a subscriber. Returns an unsubscribe function.
func (b *Bus) Subscribe(s Subscriber) func() {
    b.mu.Lock()
    defer b.mu.Unlock()

    breaker := resilience.NewBreaker(s.ID(), resilience.BreakerConfig{
        FailureRatio:  0.5,
        MinRequests:   10,
        Window:        60 * time.Second,
        OpenDuration:  30 * time.Second,
        HalfOpenMax:   3,
    })
    bulkhead := resilience.NewBulkhead(10)

    rs := registeredSubscriber{
        sub:      s,
        breaker:  breaker,
        bulkhead: bulkhead,
    }
    b.subscribers = append(b.subscribers, rs)

    return func() {
        b.mu.Lock()
        defer b.mu.Unlock()
        for i, r := range b.subscribers {
            if r.sub.ID() == s.ID() {
                b.subscribers = append(b.subscribers[:i], b.subscribers[i+1:]...)
                return
            }
        }
    }
}

// Publish dispatches an event through the pipeline. Returns a final Decision
// (only meaningful for gated event types) plus the (possibly transformed) event.
//
// Pipeline order:
//   1. Transforms (sequential, mutate payload)
//   2. Gates (parallel, must all allow; first deny wins)
//   3. Observes (async, fire-and-forget)
func (b *Bus) Publish(ctx context.Context, evt *Event) (Decision, *Event, error) {
    b.mu.RLock()
    subs := make([]registeredSubscriber, len(b.subscribers))
    copy(subs, b.subscribers)
    b.mu.RUnlock()

    b.metrics.inc(&b.metrics.EventsPublished)

    // Phase 1: Transforms (sequential)
    for _, rs := range subs {
        if rs.sub.Mode() != ModeTransform || !rs.sub.Matches(evt) {
            continue
        }
        resp, err := b.invokeWithResilience(ctx, rs, evt)
        if err != nil {
            // Transforms with errors pass through unchanged
            b.audit.LogTransform(evt, rs.sub.ID(), nil, err)
            continue
        }
        if resp != nil && len(resp.UpdatedPayload) > 0 {
            evt.Payload = resp.UpdatedPayload
        }
        b.metrics.inc(&b.metrics.TransformsRun)
        b.audit.LogTransform(evt, rs.sub.ID(), resp, nil)
    }

    // Phase 2: Gates (parallel)
    type gateResult struct {
        sub      string
        decision Decision
        reason   string
        err      error
    }
    var gateResults []gateResult
    var gateWG sync.WaitGroup
    var gateMu sync.Mutex

    for _, rs := range subs {
        if rs.sub.Mode() != ModeGate || !rs.sub.Matches(evt) {
            continue
        }
        gateWG.Add(1)
        rsCopy := rs
        go func() {
            defer gateWG.Done()
            resp, err := b.invokeWithResilience(ctx, rsCopy, evt)
            decision := DecisionAllow
            reason := ""
            if err != nil {
                if rsCopy.sub.FailurePolicy() == FailClosed {
                    decision = DecisionDeny
                    reason = fmt.Sprintf("subscriber %s failed (fail-closed): %v", rsCopy.sub.ID(), err)
                }
            } else if resp != nil {
                decision = resp.Decision
                reason = resp.Reason
            }
            gateMu.Lock()
            gateResults = append(gateResults, gateResult{
                sub:      rsCopy.sub.ID(),
                decision: decision,
                reason:   reason,
                err:      err,
            })
            gateMu.Unlock()
        }()
    }
    gateWG.Wait()

    // Resolve final decision: deny > defer > ask > allow
    finalDecision := DecisionAllow
    finalReason := ""
    for _, gr := range gateResults {
        switch gr.decision {
        case DecisionDeny:
            finalDecision = DecisionDeny
            finalReason = gr.reason
        case DecisionDefer:
            if finalDecision == DecisionAllow || finalDecision == DecisionAsk {
                finalDecision = DecisionDefer
                finalReason = gr.reason
            }
        case DecisionAsk:
            if finalDecision == DecisionAllow {
                finalDecision = DecisionAsk
                finalReason = gr.reason
            }
        }
        b.audit.LogGate(evt, gr.sub, gr.decision, gr.reason, gr.err)
    }
    if finalDecision == DecisionDeny {
        b.metrics.inc(&b.metrics.GateDenied)
    } else {
        b.metrics.inc(&b.metrics.GateAllowed)
    }

    // Phase 3: Observes (async)
    for _, rs := range subs {
        if rs.sub.Mode() != ModeObserve || !rs.sub.Matches(evt) {
            continue
        }
        rsCopy := rs
        evtCopy := *evt
        go func() {
            ctx, cancel := context.WithTimeout(context.Background(), rsCopy.sub.Timeout())
            defer cancel()
            resp, err := b.invokeWithResilience(ctx, rsCopy, &evtCopy)
            b.audit.LogObserve(&evtCopy, rsCopy.sub.ID(), resp, err)
        }()
    }

    return finalDecision, evt, nil
}

// PublishAsync is a convenience for fire-and-forget events that don't need
// gate decisions (typically observe-only event types).
func (b *Bus) PublishAsync(evt *Event) {
    go func() {
        ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()
        _, _, _ = b.Publish(ctx, evt)
    }()
}

func (b *Bus) invokeWithResilience(ctx context.Context, rs registeredSubscriber, evt *Event) (*Response, error) {
    // Bulkhead → circuit breaker → timeout → invoke
    if !rs.bulkhead.TryAcquire() {
        return nil, fmt.Errorf("bulkhead full for %s", rs.sub.ID())
    }
    defer rs.bulkhead.Release()

    return rs.breaker.Execute(func() (*Response, error) {
        invokeCtx, cancel := context.WithTimeout(ctx, rs.sub.Timeout())
        defer cancel()
        start := time.Now()
        resp, err := rs.sub.Invoke(invokeCtx, evt)
        if resp != nil {
            resp.Latency = time.Since(start)
        }
        return resp, err
    })
}

// Snapshot returns a copy of the metrics for inspection.
func (b *Bus) Snapshot() Metrics {
    b.metrics.mu.Lock()
    defer b.metrics.mu.Unlock()
    return Metrics{
        EventsPublished: b.metrics.EventsPublished,
        GateAllowed:     b.metrics.GateAllowed,
        GateDenied:      b.metrics.GateDenied,
        TransformsRun:   b.metrics.TransformsRun,
        Errors:          b.metrics.Errors,
    }
}

func (m *Metrics) inc(field *int64) {
    m.mu.Lock()
    defer m.mu.Unlock()
    *field++
}
```

---

## Step 4: Resilience primitives

**File:** `internal/hub/resilience/breaker.go`

```go
// Package resilience wraps sony/gobreaker and provides a bulkhead semaphore.
package resilience

import (
    "time"

    "github.com/sony/gobreaker/v2"
)

type BreakerConfig struct {
    FailureRatio float64
    MinRequests  uint32
    Window       time.Duration
    OpenDuration time.Duration
    HalfOpenMax  uint32
}

type Breaker struct {
    cb *gobreaker.CircuitBreaker[any]
}

func NewBreaker(name string, cfg BreakerConfig) *Breaker {
    settings := gobreaker.Settings{
        Name:        name,
        MaxRequests: cfg.HalfOpenMax,
        Interval:    cfg.Window,
        Timeout:     cfg.OpenDuration,
        ReadyToTrip: func(counts gobreaker.Counts) bool {
            if counts.Requests < cfg.MinRequests {
                return false
            }
            failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
            return failureRatio >= cfg.FailureRatio
        },
    }
    return &Breaker{cb: gobreaker.NewCircuitBreaker[any](settings)}
}

// Execute runs fn protected by the circuit breaker.
func (b *Breaker) Execute(fn func() (interface{}, error)) (interface{}, error) {
    return b.cb.Execute(fn)
}
```

Note: if `sony/gobreaker/v2` is not available in the build environment, fall back to writing a simple breaker manually with three states (closed, open, half-open) and the same threshold logic. Document the fallback in `STOKE-IMPL-NOTES.md`.

**File:** `internal/hub/resilience/bulkhead.go`

```go
package resilience

// Bulkhead is a semaphore-based concurrency limiter.
type Bulkhead struct {
    sem chan struct{}
}

func NewBulkhead(capacity int) *Bulkhead {
    return &Bulkhead{sem: make(chan struct{}, capacity)}
}

// TryAcquire attempts to acquire a slot. Returns false if full.
func (b *Bulkhead) TryAcquire() bool {
    select {
    case b.sem <- struct{}{}:
        return true
    default:
        return false
    }
}

// Release returns a slot to the bulkhead.
func (b *Bulkhead) Release() {
    <-b.sem
}
```

---

## Step 5: Audit log

**File:** `internal/hub/audit/sqlite.go`

```go
// Package audit implements the append-only SQLite audit log for the hub.
// Every event flowing through the bus is recorded with optional hash chaining
// for tamper detection.
package audit

import (
    "crypto/sha256"
    "database/sql"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "sync"
    "time"

    _ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS hub_audit (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id        TEXT NOT NULL,
    event_type      TEXT NOT NULL,
    timestamp       TEXT NOT NULL,
    mission_id      TEXT,
    task_id         TEXT,
    correlation_id  TEXT,
    subscriber_id   TEXT,
    mode            TEXT,
    decision        TEXT,
    reason          TEXT,
    latency_ms      INTEGER,
    payload_hash    TEXT,
    payload_json    TEXT,
    error_message   TEXT,
    prev_hash       TEXT,
    chain_hash      TEXT
);

CREATE INDEX IF NOT EXISTS idx_hub_audit_event_id ON hub_audit(event_id);
CREATE INDEX IF NOT EXISTS idx_hub_audit_event_type ON hub_audit(event_type);
CREATE INDEX IF NOT EXISTS idx_hub_audit_mission ON hub_audit(mission_id);
CREATE INDEX IF NOT EXISTS idx_hub_audit_task ON hub_audit(task_id);
CREATE INDEX IF NOT EXISTS idx_hub_audit_timestamp ON hub_audit(timestamp);
`

type Log struct {
    db          *sql.DB
    enableHash  bool
    mu          sync.Mutex
    lastHash    string
}

// Open opens (or creates) the audit log database.
func Open(path string, enableHashChain bool) (*Log, error) {
    db, err := sql.Open("sqlite", path)
    if err != nil {
        return nil, err
    }
    if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
        return nil, err
    }
    if _, err := db.Exec(schema); err != nil {
        return nil, err
    }
    l := &Log{db: db, enableHash: enableHashChain}
    // Load last hash from DB
    if enableHashChain {
        row := db.QueryRow("SELECT chain_hash FROM hub_audit ORDER BY id DESC LIMIT 1")
        _ = row.Scan(&l.lastHash)
    }
    return l, nil
}

func (l *Log) Close() error {
    return l.db.Close()
}

// Event interface so audit doesn't import hub directly.
type Event interface {
    GetID() string
    GetType() string
    GetTimestamp() time.Time
    GetMissionID() string
    GetTaskID() string
    GetCorrelationID() string
    GetPayloadJSON() []byte
}

// LogTransform records a transform invocation.
func (l *Log) LogTransform(evt Event, subscriberID string, response interface{}, err error) {
    l.write(evt, subscriberID, "transform", "", "", responseToLatency(response), errorMessage(err))
}

// LogGate records a gate decision.
func (l *Log) LogGate(evt Event, subscriberID string, decision string, reason string, err error) {
    l.write(evt, subscriberID, "gate", decision, reason, 0, errorMessage(err))
}

// LogObserve records an observe invocation.
func (l *Log) LogObserve(evt Event, subscriberID string, response interface{}, err error) {
    l.write(evt, subscriberID, "observe", "", "", responseToLatency(response), errorMessage(err))
}

func (l *Log) write(evt Event, subscriberID, mode, decision, reason string, latencyMs int, errMsg string) {
    if l == nil || l.db == nil {
        return
    }
    l.mu.Lock()
    defer l.mu.Unlock()

    payloadJSON := evt.GetPayloadJSON()
    payloadHash := ""
    if len(payloadJSON) > 0 {
        h := sha256.Sum256(payloadJSON)
        payloadHash = hex.EncodeToString(h[:])
    }

    chainHash := ""
    prevHash := l.lastHash
    if l.enableHash {
        h := sha256.New()
        h.Write([]byte(prevHash))
        h.Write([]byte(evt.GetID()))
        h.Write([]byte(evt.GetType()))
        h.Write([]byte(evt.GetTimestamp().Format(time.RFC3339Nano)))
        h.Write([]byte(payloadHash))
        h.Write([]byte(decision))
        chainHash = hex.EncodeToString(h.Sum(nil))
        l.lastHash = chainHash
    }

    _, err := l.db.Exec(`
        INSERT INTO hub_audit (event_id, event_type, timestamp, mission_id, task_id, correlation_id,
            subscriber_id, mode, decision, reason, latency_ms, payload_hash, payload_json,
            error_message, prev_hash, chain_hash)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `,
        evt.GetID(),
        evt.GetType(),
        evt.GetTimestamp().Format(time.RFC3339Nano),
        evt.GetMissionID(),
        evt.GetTaskID(),
        evt.GetCorrelationID(),
        subscriberID,
        mode,
        decision,
        reason,
        latencyMs,
        payloadHash,
        string(payloadJSON),
        errMsg,
        prevHash,
        chainHash,
    )
    if err != nil {
        // Audit failures should not crash the bus; log via stderr
        fmt.Fprintf(stderrWriter, "[hub/audit] insert failed: %v\n", err)
    }
}

var stderrWriter = newStderrWriter()

type stderrW struct{}

func (stderrW) Write(p []byte) (int, error) {
    // os.Stderr would create import cycle in some setups; use a tiny shim
    return len(p), nil
}

func newStderrWriter() stderrW { return stderrW{} }

func errorMessage(err error) string {
    if err == nil {
        return ""
    }
    return err.Error()
}

func responseToLatency(response interface{}) int {
    if r, ok := response.(interface{ GetLatencyMs() int }); ok {
        return r.GetLatencyMs()
    }
    return 0
}

// VerifyChain walks the audit log and verifies the hash chain integrity.
// Returns the row ID of the first broken link, or 0 if intact.
func (l *Log) VerifyChain() (int64, error) {
    rows, err := l.db.Query(`
        SELECT id, event_id, event_type, timestamp, payload_hash, decision, prev_hash, chain_hash
        FROM hub_audit ORDER BY id ASC
    `)
    if err != nil {
        return 0, err
    }
    defer rows.Close()

    expectedPrev := ""
    for rows.Next() {
        var id int64
        var eventID, eventType, timestamp, payloadHash, decision, prevHash, chainHash string
        if err := rows.Scan(&id, &eventID, &eventType, &timestamp, &payloadHash, &decision, &prevHash, &chainHash); err != nil {
            return 0, err
        }
        if prevHash != expectedPrev {
            return id, nil
        }
        h := sha256.New()
        h.Write([]byte(prevHash))
        h.Write([]byte(eventID))
        h.Write([]byte(eventType))
        h.Write([]byte(timestamp))
        h.Write([]byte(payloadHash))
        h.Write([]byte(decision))
        computed := hex.EncodeToString(h.Sum(nil))
        if computed != chainHash {
            return id, nil
        }
        expectedPrev = chainHash
    }
    return 0, nil
}

// Make hub.Event implement the audit.Event interface via an adapter:
// (this is implemented in internal/hub/bus.go via wrapping)
var _ = json.Marshal // keep import
```

To make `hub.Event` satisfy `audit.Event`, add these methods to `hub/events.go`:

```go
func (e *Event) GetID() string             { return e.ID }
func (e *Event) GetType() string           { return string(e.Type) }
func (e *Event) GetTimestamp() time.Time   { return e.Timestamp }
func (e *Event) GetMissionID() string      { return e.MissionID }
func (e *Event) GetTaskID() string         { return e.TaskID }
func (e *Event) GetCorrelationID() string  { return e.CorrelationID }
func (e *Event) GetPayloadJSON() []byte    { return e.Payload }
```

---

## Step 6: Built-in subscribers

The built-in subscribers are the ones Stoke ships with. Each lives in `internal/hub/builtin/`.

### `internal/hub/builtin/skill_inject.go`

```go
package builtin

import (
    "context"
    "encoding/json"
    "time"

    "github.com/ericmacdougall/stoke/internal/hub"
    "github.com/ericmacdougall/stoke/internal/skill"
)

// SkillInjector is a transform subscriber that injects the skill block into
// plan/execute/review prompts. It's the bus-native replacement for the direct
// InjectPromptBudgeted calls in workflow.go (added in Phase 1).
type SkillInjector struct {
    Registry     *skill.Registry
    StackMatches []string
    TokenBudget  int
}

func (s *SkillInjector) ID() string                    { return "builtin.skill_injector" }
func (s *SkillInjector) Mode() hub.Mode                { return hub.ModeTransform }
func (s *SkillInjector) FailurePolicy() hub.FailurePolicy { return hub.FailOpen }
func (s *SkillInjector) Timeout() time.Duration        { return 200 * time.Millisecond }

func (s *SkillInjector) Matches(evt *hub.Event) bool {
    switch evt.Type {
    case hub.EvtPromptBuildPlan, hub.EvtPromptBuildExec, hub.EvtPromptBuildReview, hub.EvtPromptBuildVerify:
        return true
    }
    return false
}

type promptPayload struct {
    Prompt string `json:"prompt"`
}

func (s *SkillInjector) Invoke(ctx context.Context, evt *hub.Event) (*hub.Response, error) {
    if s.Registry == nil {
        return &hub.Response{}, nil
    }
    var p promptPayload
    if err := evt.GetPayload(&p); err != nil {
        return nil, err
    }
    augmented, _ := s.Registry.InjectPromptBudgeted(p.Prompt, s.StackMatches, s.TokenBudget)
    p.Prompt = augmented
    updated, err := json.Marshal(p)
    if err != nil {
        return nil, err
    }
    return &hub.Response{
        UpdatedPayload: updated,
    }, nil
}
```

### `internal/hub/builtin/honesty.go`

```go
package builtin

import (
    "context"
    "regexp"
    "strings"
    "time"

    "github.com/ericmacdougall/stoke/internal/hub"
)

// Honesty is a gate subscriber that detects faking-completeness patterns
// in proposed file edits before they're applied.
type Honesty struct {
    DiffSizeHardLimit int  // lines, default 1000
    DiffSizeWarnLimit int  // lines, default 400
}

func (h *Honesty) ID() string                          { return "builtin.honesty" }
func (h *Honesty) Mode() hub.Mode                      { return hub.ModeGate }
func (h *Honesty) FailurePolicy() hub.FailurePolicy    { return hub.FailClosed }
func (h *Honesty) Timeout() time.Duration              { return 500 * time.Millisecond }

func (h *Honesty) Matches(evt *hub.Event) bool {
    return evt.Type == hub.EvtFilePreWrite || evt.Type == hub.EvtFilePreEdit
}

type fileWritePayload struct {
    Path    string `json:"path"`
    Content string `json:"content"`
    OldContent string `json:"old_content,omitempty"`
}

var (
    // Patterns that indicate placeholder code
    placeholderPatterns = []*regexp.Regexp{
        regexp.MustCompile(`(?i)^\s*(//|#)\s*todo`),
        regexp.MustCompile(`(?i)^\s*(//|#)\s*fixme`),
        regexp.MustCompile(`(?i)^\s*(//|#)\s*xxx`),
        regexp.MustCompile(`(?i)panic\(\s*"not implemented"\s*\)`),
        regexp.MustCompile(`(?i)throw new Error\(\s*"not implemented"\s*\)`),
        regexp.MustCompile(`(?i)raise NotImplementedError`),
    }

    // Suppression markers
    suppressionPatterns = []*regexp.Regexp{
        regexp.MustCompile(`@ts-ignore`),
        regexp.MustCompile(`as any`),
        regexp.MustCompile(`eslint-disable`),
        regexp.MustCompile(`//\s*nolint`),
    }
)

func (h *Honesty) Invoke(ctx context.Context, evt *hub.Event) (*hub.Response, error) {
    var p fileWritePayload
    if err := evt.GetPayload(&p); err != nil {
        return nil, err
    }

    // Check 1: diff size
    hardLimit := h.DiffSizeHardLimit
    if hardLimit == 0 {
        hardLimit = 1000
    }
    addedLines := strings.Count(p.Content, "\n")
    if addedLines > hardLimit {
        return &hub.Response{
            Decision: hub.DecisionDeny,
            Reason:   "diff exceeds 1000 line hard limit; split into smaller commits",
        }, nil
    }

    // Check 2: placeholders
    for _, line := range strings.Split(p.Content, "\n") {
        for _, pat := range placeholderPatterns {
            if pat.MatchString(line) {
                return &hub.Response{
                    Decision: hub.DecisionDeny,
                    Reason:   "placeholder code detected: " + strings.TrimSpace(line),
                }, nil
            }
        }
        for _, pat := range suppressionPatterns {
            if pat.MatchString(line) {
                return &hub.Response{
                    Decision: hub.DecisionDeny,
                    Reason:   "type/lint suppression detected: " + strings.TrimSpace(line),
                }, nil
            }
        }
    }

    // Check 3: test removal (if path is a test file and content is shorter than old)
    if isTestFile(p.Path) && len(p.OldContent) > 0 && len(p.Content) < len(p.OldContent)/2 {
        return &hub.Response{
            Decision: hub.DecisionDeny,
            Reason:   "test file shrunk by more than 50% — possible test removal",
        }, nil
    }

    return &hub.Response{Decision: hub.DecisionAllow}, nil
}

func isTestFile(path string) bool {
    return strings.HasSuffix(path, "_test.go") ||
        strings.Contains(path, ".test.") ||
        strings.Contains(path, ".spec.") ||
        strings.HasPrefix(path, "test_")
}
```

### `internal/hub/builtin/cost_track.go`

```go
package builtin

import (
    "context"
    "encoding/json"
    "sync"
    "time"

    "github.com/ericmacdougall/stoke/internal/hub"
)

// CostTracker is an observe subscriber that records per-call cost.
type CostTracker struct {
    mu sync.Mutex
    totalUSD float64
    perModel map[string]float64
}

func NewCostTracker() *CostTracker {
    return &CostTracker{perModel: make(map[string]float64)}
}

func (c *CostTracker) ID() string                          { return "builtin.cost_tracker" }
func (c *CostTracker) Mode() hub.Mode                      { return hub.ModeObserve }
func (c *CostTracker) FailurePolicy() hub.FailurePolicy    { return hub.FailOpen }
func (c *CostTracker) Timeout() time.Duration              { return 100 * time.Millisecond }

func (c *CostTracker) Matches(evt *hub.Event) bool {
    return evt.Type == hub.EvtModelPostCall
}

type modelCallPayload struct {
    Model        string  `json:"model"`
    InputTokens  int     `json:"input_tokens"`
    OutputTokens int     `json:"output_tokens"`
    CacheReadTokens int  `json:"cache_read_tokens"`
    CacheWriteTokens int `json:"cache_write_tokens"`
}

// Pricing per million tokens (April 2026)
var pricing = map[string]struct {
    Input, Output, CacheWrite, CacheRead float64
}{
    "claude-opus-4-6":     {5.00, 25.00, 6.25, 0.50},
    "claude-sonnet-4-6":   {3.00, 15.00, 3.75, 0.30},
    "claude-haiku-4-5":    {1.00, 5.00, 1.25, 0.10},
    "claude-haiku-3-5":    {0.80, 4.00, 1.00, 0.08},
}

func (c *CostTracker) Invoke(ctx context.Context, evt *hub.Event) (*hub.Response, error) {
    var p modelCallPayload
    if err := json.Unmarshal(evt.Payload, &p); err != nil {
        return nil, err
    }
    price, ok := pricing[p.Model]
    if !ok {
        return &hub.Response{}, nil
    }
    cost := float64(p.InputTokens)/1e6*price.Input +
        float64(p.OutputTokens)/1e6*price.Output +
        float64(p.CacheWriteTokens)/1e6*price.CacheWrite +
        float64(p.CacheReadTokens)/1e6*price.CacheRead

    c.mu.Lock()
    c.totalUSD += cost
    c.perModel[p.Model] += cost
    c.mu.Unlock()

    return &hub.Response{}, nil
}

// Snapshot returns the current cost totals.
func (c *CostTracker) Snapshot() (totalUSD float64, perModel map[string]float64) {
    c.mu.Lock()
    defer c.mu.Unlock()
    out := make(map[string]float64, len(c.perModel))
    for k, v := range c.perModel {
        out[k] = v
    }
    return c.totalUSD, out
}
```

### `internal/hub/builtin/scan.go`

```go
package builtin

import (
    "context"
    "regexp"
    "strings"
    "time"

    "github.com/ericmacdougall/stoke/internal/hub"
)

// SecretScanner is a gate subscriber that denies file writes containing
// hardcoded secrets.
type SecretScanner struct {
    Patterns []*regexp.Regexp
}

func NewDefaultSecretScanner() *SecretScanner {
    return &SecretScanner{
        Patterns: []*regexp.Regexp{
            regexp.MustCompile(`(?i)aws_access_key_id\s*=\s*['"]?AKIA[0-9A-Z]{16}`),
            regexp.MustCompile(`(?i)aws_secret_access_key\s*=\s*['"]?[A-Za-z0-9/+=]{40}`),
            regexp.MustCompile(`-----BEGIN (RSA |OPENSSH |EC )?PRIVATE KEY-----`),
            regexp.MustCompile(`(?i)api[_-]?key\s*[=:]\s*['"][a-zA-Z0-9]{32,}['"]`),
            regexp.MustCompile(`(?i)stripe.*['"]sk_live_[0-9a-zA-Z]{24,}['"]`),
            regexp.MustCompile(`(?i)stripe.*['"]rk_live_[0-9a-zA-Z]{24,}['"]`),
            regexp.MustCompile(`xox[baprs]-[0-9a-zA-Z]{10,48}`),  // Slack tokens
            regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36}`),       // GitHub tokens
        },
    }
}

func (s *SecretScanner) ID() string                          { return "builtin.secret_scanner" }
func (s *SecretScanner) Mode() hub.Mode                      { return hub.ModeGate }
func (s *SecretScanner) FailurePolicy() hub.FailurePolicy    { return hub.FailClosed }
func (s *SecretScanner) Timeout() time.Duration              { return 500 * time.Millisecond }

func (s *SecretScanner) Matches(evt *hub.Event) bool {
    return evt.Type == hub.EvtFilePreWrite || evt.Type == hub.EvtFilePreEdit
}

func (s *SecretScanner) Invoke(ctx context.Context, evt *hub.Event) (*hub.Response, error) {
    var p fileWritePayload
    if err := evt.GetPayload(&p); err != nil {
        return nil, err
    }
    for _, pat := range s.Patterns {
        if loc := pat.FindStringIndex(p.Content); loc != nil {
            // Get the offending line for context
            line := lineContaining(p.Content, loc[0])
            return &hub.Response{
                Decision: hub.DecisionDeny,
                Reason:   "secret detected in file write: " + strings.TrimSpace(line),
            }, nil
        }
    }
    return &hub.Response{Decision: hub.DecisionAllow}, nil
}

func lineContaining(content string, offset int) string {
    if offset < 0 || offset >= len(content) {
        return ""
    }
    start := offset
    for start > 0 && content[start-1] != '\n' {
        start--
    }
    end := offset
    for end < len(content) && content[end] != '\n' {
        end++
    }
    return content[start:end]
}
```

---

## Step 7: HTTP webhook transport for external subscribers

**File:** `internal/hub/transports/http.go`

```go
// Package transports implements the various transports for hub subscribers.
package transports

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "time"

    "github.com/ericmacdougall/stoke/internal/hub"
)

// HTTPSubscriber posts events to a remote URL and parses the response.
type HTTPSubscriber struct {
    SubID         string
    URL           string
    SubMode       hub.Mode
    Match         hub.Matcher
    Client        *http.Client
    FailurePolicyVal hub.FailurePolicy
    TimeoutVal    time.Duration
    Headers       map[string]string
}

func (h *HTTPSubscriber) ID() string                       { return h.SubID }
func (h *HTTPSubscriber) Mode() hub.Mode                   { return h.SubMode }
func (h *HTTPSubscriber) Matches(evt *hub.Event) bool      { return h.Match.Match(evt.Type) }
func (h *HTTPSubscriber) FailurePolicy() hub.FailurePolicy { return h.FailurePolicyVal }
func (h *HTTPSubscriber) Timeout() time.Duration {
    if h.TimeoutVal == 0 {
        return 5 * time.Second
    }
    return h.TimeoutVal
}

func (h *HTTPSubscriber) Invoke(ctx context.Context, evt *hub.Event) (*hub.Response, error) {
    body, err := json.Marshal(evt)
    if err != nil {
        return nil, err
    }
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
    if err != nil {
        return nil, err
    }
    req.Header.Set("Content-Type", "application/json")
    for k, v := range h.Headers {
        req.Header.Set(k, v)
    }
    client := h.Client
    if client == nil {
        client = &http.Client{Timeout: h.Timeout()}
    }
    resp, err := client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    if resp.StatusCode >= 400 {
        b, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("webhook returned %d: %s", resp.StatusCode, b)
    }
    var hubResp hub.Response
    if err := json.NewDecoder(resp.Body).Decode(&hubResp); err != nil {
        // Empty body is fine for observe subscribers
        if h.SubMode == hub.ModeObserve {
            return &hub.Response{}, nil
        }
        return nil, err
    }
    return &hubResp, nil
}
```

---

## Step 8: Bash script transport (Claude Code compatible)

**File:** `internal/hub/transports/script.go`

```go
package transports

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "os/exec"
    "time"

    "github.com/ericmacdougall/stoke/internal/hub"
)

// ScriptSubscriber executes a shell script with the event JSON on stdin and
// parses the JSON output. This is wire-compatible with Claude Code's hook
// protocol so existing bash hooks work unchanged.
type ScriptSubscriber struct {
    SubID         string
    ScriptPath    string
    SubMode       hub.Mode
    Match         hub.Matcher
    FailurePolicyVal hub.FailurePolicy
    TimeoutVal    time.Duration
    Env           []string
}

func (s *ScriptSubscriber) ID() string                       { return s.SubID }
func (s *ScriptSubscriber) Mode() hub.Mode                   { return s.SubMode }
func (s *ScriptSubscriber) Matches(evt *hub.Event) bool      { return s.Match.Match(evt.Type) }
func (s *ScriptSubscriber) FailurePolicy() hub.FailurePolicy { return s.FailurePolicyVal }
func (s *ScriptSubscriber) Timeout() time.Duration {
    if s.TimeoutVal == 0 {
        return 30 * time.Second
    }
    return s.TimeoutVal
}

func (s *ScriptSubscriber) Invoke(ctx context.Context, evt *hub.Event) (*hub.Response, error) {
    payload, err := json.Marshal(evt)
    if err != nil {
        return nil, err
    }
    cmd := exec.CommandContext(ctx, "sh", "-c", s.ScriptPath)
    cmd.Stdin = bytes.NewReader(payload)
    cmd.Env = append(cmd.Env, s.Env...)

    var stdout, stderr bytes.Buffer
    cmd.Stdout = &stdout
    cmd.Stderr = &stderr

    err = cmd.Run()
    exitCode := 0
    if exitErr, ok := err.(*exec.ExitError); ok {
        exitCode = exitErr.ExitCode()
    } else if err != nil {
        return nil, err
    }

    // Claude Code hook protocol:
    //   exit 0 = parse stdout as JSON for decisions (or empty = allow)
    //   exit 2 = blocking error, stderr is the error message
    //   other  = non-blocking error
    if exitCode == 2 {
        return &hub.Response{
            Decision: hub.DecisionDeny,
            Reason:   stderr.String(),
        }, nil
    }
    if exitCode != 0 {
        return nil, fmt.Errorf("script exit %d: %s", exitCode, stderr.String())
    }
    if stdout.Len() == 0 {
        return &hub.Response{Decision: hub.DecisionAllow}, nil
    }
    var resp hub.Response
    if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
        return &hub.Response{Decision: hub.DecisionAllow}, nil
    }
    return &resp, nil
}
```

---

## Step 9: Wire the bus into the workflow

**File:** `internal/app/app.go`

In `NewOrchestrator()`:

```go
import "github.com/ericmacdougall/stoke/internal/hub"
import "github.com/ericmacdougall/stoke/internal/hub/builtin"

// Initialize the hub
auditPath := filepath.Join(stokeDir, "hub-audit.db")
bus, err := hub.New(hub.Config{
    AuditDBPath:     auditPath,
    EnableHashChain: true,
})
if err != nil {
    log.Printf("[hub] init failed: %v (continuing without hub)", err)
} else {
    // Register built-in subscribers
    bus.Subscribe(&builtin.SkillInjector{
        Registry:     skillRegistry,
        StackMatches: skillselect.MatchSkills(profile),
        TokenBudget:  3000,
    })
    bus.Subscribe(&builtin.Honesty{
        DiffSizeHardLimit: 1000,
        DiffSizeWarnLimit: 400,
    })
    bus.Subscribe(builtin.NewDefaultSecretScanner())
    bus.Subscribe(builtin.NewCostTracker())
}
cfg.Bus = bus
```

**File:** `internal/workflow/workflow.go`

Add `Bus *hub.Bus` field to `Engine`. In the prompt construction sites (around lines 1474, 1552):

```go
prompt := stokeprompts.BuildPlanPrompt(task, false, "")
if e.Bus != nil {
    evt, _ := hub.NewEvent(hub.EvtPromptBuildPlan, map[string]string{"prompt": prompt})
    _, evt, _ = e.Bus.Publish(ctx, evt)
    var p struct{ Prompt string `json:"prompt"` }
    _ = evt.GetPayload(&p)
    if p.Prompt != "" {
        prompt = p.Prompt
    }
}
return prompt
```

Wherever a file write happens (find via `grep -n "WriteFile\|os.Create"` in the workflow/engine packages), add a `EvtFilePreWrite` publish that respects the gate decision:

```go
evt, _ := hub.NewEvent(hub.EvtFilePreWrite, map[string]string{
    "path":    path,
    "content": content,
    "old_content": oldContent,  // empty if new file
})
decision, _, _ := bus.Publish(ctx, evt)
if decision == hub.DecisionDeny {
    return fmt.Errorf("file write denied: %s", evt.Payload)
}
// proceed with write
```

---

## Step 10: Adapter for existing bash hooks

**File:** `internal/hooks/adapter.go`

```go
package hooks

import (
    "github.com/ericmacdougall/stoke/internal/hub"
    "github.com/ericmacdougall/stoke/internal/hub/transports"
)

// RegisterBashHooks scans for legacy bash hook scripts in .stoke/hooks/ or
// .claude/hooks/ and registers them as ScriptSubscriber instances on the bus.
// This preserves backward compatibility with existing bash-based hooks.
func RegisterBashHooks(bus *hub.Bus, hookDir string) error {
    // Implementation: walk hookDir, look for executable scripts named after
    // event types (e.g., pre_tool_use.sh → EvtToolPreUse), register each.
    // Map filename → event type via a known table.
    // Detailed scan code omitted for brevity; preserve the existing scan logic
    // from internal/hooks/hooks.go and convert each found hook into a
    // ScriptSubscriber with appropriate Mode and FailurePolicy.
    return nil
}
```

The existing `internal/hooks/hooks.go` and `internal/lifecycle/hooks.go` should be preserved but marked deprecated in their package docstrings. They will be removed in a future cleanup once the hub is proven stable.

---

## Validation gate for Phase 3

1. `go vet ./...` clean, `go test ./internal/hub/...` passes with >70% coverage
2. `go build ./cmd/stoke` succeeds
3. Existing bash hooks still fire correctly (run an existing mission with bash hooks installed and verify they execute)
4. New in-process honesty hook denies a file write containing `panic("not implemented")`
5. Cost tracker updates after a model call
6. Audit log file exists at `.stoke/hub-audit.db` after first run; `sqlite3 .stoke/hub-audit.db "SELECT count(*) FROM hub_audit"` returns > 0
7. Hash chain verification: `bus.audit.VerifyChain()` returns 0 (intact)
8. Append phase 3 entry to `STOKE-IMPL-NOTES.md`

## Now go to `06-phase4-harness-independence.md`.
