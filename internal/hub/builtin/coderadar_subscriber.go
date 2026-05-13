// Package builtin — CodeRadar bus subscriber (B3 dogfood).
//
// Mirrors the existing CostTracker shape: a single struct with a
// Register(bus *hub.Bus) method that wires itself to the hub event
// taxonomy. Mode is ModeObserve (async, fire-and-forget) so missions
// and provider hot paths never block on observability.
//
// Failure isolation:
//   - bounded queue; full → drop + emitErrors++
//   - single drain goroutine with recover() per tick
//   - HTTP timeouts hardcoded in coderadar.Client (5s)
//   - circuit breaker self-disables after threshold (T15)
//
// Operator surface mirrors internal/bridge/cost.go (PR #219 pattern):
// EmitErrorCount / LastEmitError / SetOnEmitError.
package builtin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RelayOne/r1/internal/coderadar"
	"github.com/RelayOne/r1/internal/correlation"
	"github.com/RelayOne/r1/internal/hub"
)

// CoderadarSubscriber mirrors hub events to CodeRadar via a bounded
// queue. Constructed once per service; Register wires it to the hub.
type CoderadarSubscriber struct {
	client     *coderadar.Client
	queue      chan coderadar.Event
	queueDepth int
	sampler    *coderadar.Sampler
	redactor   *coderadar.Redactor

	emitErrors    atomic.Uint64
	droppedQueue  atomic.Uint64
	droppedSample atomic.Uint64
	droppedRedact atomic.Uint64
	emittedTotal  atomic.Uint64

	lastErr atomic.Pointer[emitErr]
	onErr   atomic.Pointer[func(stage string, err error)]

	circuit *circuitState

	wg   sync.WaitGroup
	stop chan struct{}
	once sync.Once
}

// emitErr pairs a failure stage with the underlying error (verbatim
// mirror of internal/bridge/cost.go pattern).
type emitErr struct {
	Stage string
	Err   error
	When  time.Time
}

// circuitState tracks the failure-count → self-disable transition.
// See spec T15. ResetTimeout default 5m.
type circuitState struct {
	mu             sync.Mutex
	windowStart    time.Time
	windowFailures uint64
	openUntil      time.Time
	threshold      uint64
	window         time.Duration
	openFor        time.Duration
}

// SubscriberOption customizes a CoderadarSubscriber at construction.
type SubscriberOption func(*CoderadarSubscriber)

// WithQueueDepth overrides the default 4096 queue capacity.
func WithQueueDepth(n int) SubscriberOption {
	return func(s *CoderadarSubscriber) {
		if n > 0 {
			s.queueDepth = n
		}
	}
}

// WithSampler overrides the default env-derived sampler.
func WithSampler(sampler *coderadar.Sampler) SubscriberOption {
	return func(s *CoderadarSubscriber) {
		if sampler != nil {
			s.sampler = sampler
		}
	}
}

// NewCoderadarSubscriber constructs a subscriber and starts its drain
// goroutine. The caller must invoke Close() at shutdown to flush the
// queue and stop the goroutine. In local env the queue is allocated
// with depth=1 and the drain goroutine drops every event (no network).
func NewCoderadarSubscriber(client *coderadar.Client, opts ...SubscriberOption) *CoderadarSubscriber {
	s := &CoderadarSubscriber{
		client:     client,
		queueDepth: 4096,
		redactor:   coderadar.NewRedactor(),
		stop:       make(chan struct{}),
		circuit: &circuitState{
			threshold: 100,
			window:    60 * time.Second,
			openFor:   5 * time.Minute,
		},
	}
	env := ""
	if client != nil {
		env = client.Environment()
	}
	s.sampler = coderadar.SamplerForEnv(env)
	for _, opt := range opts {
		opt(s)
	}
	s.queue = make(chan coderadar.Event, s.queueDepth)
	s.wg.Add(1)
	go s.drainLoop()
	return s
}

// Register wires the subscriber to every hub.EventType in the canonical
// mapping (see mapHubToCanonical). Single subscriber registration with
// a multi-type Events slice — Mode ModeObserve so the publisher returns
// immediately.
func (s *CoderadarSubscriber) Register(bus *hub.Bus) {
	if bus == nil {
		return
	}
	bus.Register(hub.Subscriber{
		ID:       "builtin.coderadar_subscriber",
		Events:   s.subscribedEventTypes(),
		Mode:     hub.ModeObserve,
		Priority: 9100,
		Handler:  s.handle,
	})
}

// Close flushes the queue, stops the drain goroutine, and returns when
// the goroutine has exited. Idempotent — safe to call from multiple
// shutdown paths.
func (s *CoderadarSubscriber) Close() {
	s.once.Do(func() {
		close(s.stop)
	})
	s.wg.Wait()
}

// --- Operator surface (mirrors internal/bridge/cost.go PR #219) ---

// EmitErrorCount returns the cumulative number of emit failures.
func (s *CoderadarSubscriber) EmitErrorCount() uint64 {
	return s.emitErrors.Load()
}

// LastEmitError returns the most recent emit failure with stage tag.
func (s *CoderadarSubscriber) LastEmitError() (stage string, err error, when time.Time, ok bool) {
	last := s.lastErr.Load()
	if last == nil {
		return "", nil, time.Time{}, false
	}
	return last.Stage, last.Err, last.When, true
}

// SetOnEmitError installs a push-callback for emit failures. nil clears.
func (s *CoderadarSubscriber) SetOnEmitError(fn func(stage string, err error)) {
	if fn == nil {
		s.onErr.Store(nil)
		return
	}
	s.onErr.Store(&fn)
}

// Stats returns the live counters surfaced by the telemetry exporter
// and the `r1 ops coderadar-stats` CLI. Safe for concurrent use.
type Stats struct {
	Emitted       uint64
	DroppedQueue  uint64
	DroppedSample uint64
	DroppedRedact uint64
	EmitErrors    uint64
	QueueDepth    int
	QueueCapacity int
}

// Stats returns the live emit / drop counters.
func (s *CoderadarSubscriber) Stats() Stats {
	return Stats{
		Emitted:       s.emittedTotal.Load(),
		DroppedQueue:  s.droppedQueue.Load(),
		DroppedSample: s.droppedSample.Load(),
		DroppedRedact: s.droppedRedact.Load(),
		EmitErrors:    s.emitErrors.Load(),
		QueueDepth:    len(s.queue),
		QueueCapacity: cap(s.queue),
	}
}

// --- Convenience emitters (lifecycle bypass — T12) ---

// EmitLifecycle enqueues a service lifecycle event. Bypasses the hub
// bus (the hub has no canonical service-lifecycle event types) and
// goes straight into the same drain queue.
func (s *CoderadarSubscriber) EmitLifecycle(name string, props map[string]any) {
	if s == nil || s.client == nil || !s.client.Enabled() {
		return
	}
	ev := coderadar.Event{
		Name:          name,
		SchemaVersion: coderadar.SchemaVersions[name],
		Service:       s.client.ServiceName(),
		Env:           s.client.Environment(),
		Timestamp:     time.Now().UTC(),
		EventID:       newEventID(),
		Props:         props,
	}
	s.enqueue(ev)
}

// HealthHeartbeat runs an in-process goroutine that emits a
// service_health_check event every interval until ctx is cancelled.
// subsystemProbe is optional — when non-nil, its result is attached as
// the "subsystems" prop.
func (s *CoderadarSubscriber) HealthHeartbeat(ctx context.Context, interval time.Duration, subsystemProbe func() map[string]string) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-t.C:
			props := map[string]any{"status": "ok"}
			if subsystemProbe != nil {
				if sub := subsystemProbe(); sub != nil {
					props["subsystems"] = sub
				}
			}
			s.EmitLifecycle(coderadar.EventServiceHealthCheck, props)
		}
	}
}

// --- Hub handler + queue plumbing ---

func (s *CoderadarSubscriber) handle(ctx context.Context, ev *hub.Event) *hub.HookResponse {
	if ev == nil || s == nil || s.client == nil || !s.client.Enabled() {
		return &hub.HookResponse{Decision: hub.Allow}
	}
	canon, ok := mapHubToCanonical(ev)
	if !ok {
		return &hub.HookResponse{Decision: hub.Allow}
	}
	// Sample BEFORE redaction so the rate cap applies to the wire stream.
	if !s.sampler.ShouldEmit(canon.Name) {
		s.droppedSample.Add(1)
		return &hub.HookResponse{Decision: hub.Allow}
	}

	// Attach correlation IDs from ctx AND from the hub.Event envelope
	// (Phase-3 ModeObserve dispatch passes context.Background() — see
	// internal/hub/bus.go line ~229 — so ctx-borne ids never reach us
	// in production. The hub.Event MissionID/AgentID/TaskID fields are
	// the canonical fallback.)
	canon = attachCorrelation(ctx, canon)
	canon = attachCorrelationFromEvent(ev, canon)

	// Redact (allowlist + promptguard scrub).
	canon = s.redactor.Redact(canon)
	if canon.Props == nil && canon.Name == "" {
		s.droppedRedact.Add(1)
		return &hub.HookResponse{Decision: hub.Allow}
	}

	canon.EventID = newEventID()
	s.enqueue(canon)
	return &hub.HookResponse{Decision: hub.Allow}
}

func (s *CoderadarSubscriber) enqueue(ev coderadar.Event) {
	// Local env: drop on enqueue with no error.
	if s.client != nil && s.client.Environment() == "local" {
		s.droppedQueue.Add(1)
		return
	}
	select {
	case s.queue <- ev:
		// success
	default:
		s.droppedQueue.Add(1)
		s.recordEmitError("queue.full", errQueueFull)
	}
}

// drainLoop reads from the queue, batches up to 20 events / 500ms,
// and POSTs via the client. Runs forever until stop is closed.
func (s *CoderadarSubscriber) drainLoop() {
	defer s.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			s.recordEmitError("drain.panic", errPanic{val: r})
		}
	}()
	const (
		batchMax     = 20
		batchTimeout = 500 * time.Millisecond
	)
	var batch []coderadar.Event
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if s.circuitOpen() {
			// drop the batch while the circuit is open
			s.droppedQueue.Add(uint64(len(batch)))
			batch = batch[:0]
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := func() (out error) {
			defer func() {
				if r := recover(); r != nil {
					out = errPanic{val: r}
				}
			}()
			return s.client.EmitBatch(ctx, batch)
		}()
		if err != nil {
			s.recordEmitError("http.post", err)
			s.circuitRecordFailure()
		} else {
			s.emittedTotal.Add(uint64(len(batch)))
		}
		batch = batch[:0]
	}
	timer := time.NewTimer(batchTimeout)
	defer timer.Stop()
	for {
		select {
		case <-s.stop:
			// Drain remaining buffered events on shutdown.
			for {
				select {
				case ev := <-s.queue:
					batch = append(batch, ev)
					if len(batch) >= batchMax {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case ev := <-s.queue:
			batch = append(batch, ev)
			if len(batch) >= batchMax {
				flush()
				resetTimer(timer, batchTimeout)
			}
		case <-timer.C:
			flush()
			resetTimer(timer, batchTimeout)
		}
	}
}

func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

func (s *CoderadarSubscriber) recordEmitError(stage string, err error) {
	s.emitErrors.Add(1)
	s.lastErr.Store(&emitErr{Stage: stage, Err: err, When: time.Now()})
	if cb := s.onErr.Load(); cb != nil {
		(*cb)(stage, err)
	}
}

// --- Circuit breaker (T15) ---

func (s *CoderadarSubscriber) circuitOpen() bool {
	cs := s.circuit
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if !cs.openUntil.IsZero() && time.Now().Before(cs.openUntil) {
		return true
	}
	if !cs.openUntil.IsZero() && !time.Now().Before(cs.openUntil) {
		// Reset on cooldown end. Emit a health_check transition so
		// operators see the circuit recover.
		cs.openUntil = time.Time{}
		cs.windowFailures = 0
		cs.windowStart = time.Time{}
		// Best-effort lifecycle emit — bypass to avoid lock recursion.
		go s.EmitLifecycle(coderadar.EventServiceHealthCheck, map[string]any{
			"status":                 "recovered",
			"coderadar_circuit_open": false,
		})
	}
	return false
}

func (s *CoderadarSubscriber) circuitRecordFailure() {
	cs := s.circuit
	cs.mu.Lock()
	defer cs.mu.Unlock()
	now := time.Now()
	if cs.windowStart.IsZero() || now.Sub(cs.windowStart) > cs.window {
		cs.windowStart = now
		cs.windowFailures = 1
		return
	}
	cs.windowFailures++
	if cs.windowFailures >= cs.threshold && cs.openUntil.IsZero() {
		cs.openUntil = now.Add(cs.openFor)
		go s.EmitLifecycle(coderadar.EventServiceHealthCheck, map[string]any{
			"status":                 "degraded",
			"coderadar_circuit_open": true,
		})
	}
}

// --- mapHubToCanonical ---

// mapHubToCanonical translates a hub.Event to a canonical CodeRadar
// Event. Returns ok=false for events outside the canonical 18.
//
// Per spec T1 the mapping is:
//   - mission.plan.done       → mission.plan
//   - mission.execute.start   → mission.execute
//   - verify.convergence_start → mission.verify
//   - verify.critic_review    → mission.review
//   - mission.converged       → mission.completed
//   - mission.failed          → mission.aborted (abort_reason=failed)
//   - mission.cancelled       → mission.aborted (abort_reason=cancelled)
//   - cortex.lobe.started     → cortex.lobe_invoked
//   - cortex.round.completed  → cortex.round_completed
//   - antitrunc.fired         → antitrunc.fired
//   - antitrunc.overridden    → antitrunc.overridden
//   - model.pre_call          → provider.call_started
//   - model.post_call         → provider.call_completed
//   - model.error             → provider.call_errored
//   - model.fallback          → provider.fallback
//   - tool.post_use           → tool.call_completed
func mapHubToCanonical(ev *hub.Event) (coderadar.Event, bool) {
	if ev == nil {
		return coderadar.Event{}, false
	}
	var canon coderadar.Event
	canon.Timestamp = ev.Timestamp
	if canon.Timestamp.IsZero() {
		canon.Timestamp = time.Now().UTC()
	}
	switch ev.Type {
	case hub.EventMissionPlanDone:
		canon.Name = coderadar.EventMissionPlan
		props := map[string]any{}
		if ev.MissionID != "" {
			props["mission_id"] = ev.MissionID
		}
		canon.Props = props

	case hub.EventMissionExecuteStart:
		canon.Name = coderadar.EventMissionExecute
		props := map[string]any{}
		if ev.MissionID != "" {
			props["mission_id"] = ev.MissionID
		}
		canon.Props = props

	case hub.EventVerifyConvergenceStart:
		canon.Name = coderadar.EventMissionVerify
		props := map[string]any{}
		if ev.MissionID != "" {
			props["mission_id"] = ev.MissionID
		}
		props["verify_kind"] = "convergence"
		canon.Props = props

	case hub.EventVerifyCriticReview:
		canon.Name = coderadar.EventMissionReview
		props := map[string]any{}
		if ev.MissionID != "" {
			props["mission_id"] = ev.MissionID
		}
		props["reviewer"] = "critic"
		canon.Props = props

	case hub.EventMissionConverged:
		canon.Name = coderadar.EventMissionCompleted
		props := map[string]any{}
		if ev.MissionID != "" {
			props["mission_id"] = ev.MissionID
		}
		if ev.Lifecycle != nil && ev.Lifecycle.Duration > 0 {
			props["duration_ms"] = ev.Lifecycle.Duration.Milliseconds()
		}
		if ev.Cost != nil {
			props["total_cost_usd"] = ev.Cost.TotalSpent
		}
		canon.Props = props

	case hub.EventMissionFailed:
		canon.Name = coderadar.EventMissionAborted
		props := map[string]any{"abort_reason": "failed"}
		if ev.MissionID != "" {
			props["mission_id"] = ev.MissionID
		}
		canon.Props = props

	case hub.EventMissionCancelled:
		canon.Name = coderadar.EventMissionAborted
		props := map[string]any{"abort_reason": "cancelled"}
		if ev.MissionID != "" {
			props["mission_id"] = ev.MissionID
		}
		canon.Props = props

	case hub.EventCortexLobeStarted:
		canon.Name = coderadar.EventCortexLobeInvoked
		props := map[string]any{}
		if ev.MissionID != "" {
			props["mission_id"] = ev.MissionID
		}
		if ev.Custom != nil {
			if v, ok := ev.Custom["lobe_name"]; ok {
				props["lobe_name"] = v
			}
		}
		canon.Props = props

	case hub.EventCortexRoundCompleted:
		canon.Name = coderadar.EventCortexRoundCompleted
		props := map[string]any{}
		if ev.Custom != nil {
			if v, ok := ev.Custom["round"]; ok {
				props["round"] = v
			}
			if v, ok := ev.Custom["notes_published"]; ok {
				props["notes_published"] = v
			}
		}
		canon.Props = props

	case hub.EventAntiTruncFired:
		canon.Name = coderadar.EventAntiTruncFired
		props := map[string]any{}
		if ev.Custom != nil {
			if v, ok := ev.Custom["pattern_matched"]; ok {
				props["pattern_matched"] = v
			}
			if v, ok := ev.Custom["phase"]; ok {
				props["phase"] = v
			}
			if v, ok := ev.Custom["evt_id"]; ok {
				props["evt_id"] = v
			}
		}
		canon.Props = props

	case hub.EventAntiTruncOverridden:
		canon.Name = coderadar.EventAntiTruncOverridden
		props := map[string]any{}
		if ev.Custom != nil {
			if v, ok := ev.Custom["override_actor"]; ok {
				props["override_actor"] = v
			}
			if v, ok := ev.Custom["justification_hash"]; ok {
				props["justification_hash"] = v
			}
		}
		canon.Props = props

	case hub.EventModelPreCall:
		canon.Name = coderadar.EventProviderCallStarted
		props := map[string]any{}
		if ev.Model != nil {
			if ev.Model.Provider != "" {
				props["provider"] = ev.Model.Provider
				props["gen_ai.system"] = ev.Model.Provider
			}
			if ev.Model.Model != "" {
				props["model"] = ev.Model.Model
				props["gen_ai.request.model"] = ev.Model.Model
			}
			if ev.Model.Role != "" {
				props["gen_ai.operation.name"] = ev.Model.Role
			}
		}
		canon.Props = props

	case hub.EventModelPostCall:
		canon.Name = coderadar.EventProviderCallCompleted
		props := map[string]any{}
		if ev.Model != nil {
			if ev.Model.Provider != "" {
				props["provider"] = ev.Model.Provider
				props["gen_ai.system"] = ev.Model.Provider
			}
			if ev.Model.Model != "" {
				props["model"] = ev.Model.Model
				props["gen_ai.request.model"] = ev.Model.Model
			}
			props["input_tokens"] = ev.Model.InputTokens
			props["output_tokens"] = ev.Model.OutputTokens
			props["cost_usd"] = ev.Model.CostUSD
			if ev.Model.CachedTokens > 0 {
				props["cached_tokens"] = ev.Model.CachedTokens
			}
			if ev.Model.StopReason != "" {
				props["stop_reason"] = ev.Model.StopReason
			}
			if ev.Model.Duration > 0 {
				props["duration_ms"] = ev.Model.Duration.Milliseconds()
			}
			if ev.Model.Role != "" {
				props["gen_ai.operation.name"] = ev.Model.Role
			}
			props["gen_ai.usage.input_tokens"] = ev.Model.InputTokens
			props["gen_ai.usage.output_tokens"] = ev.Model.OutputTokens
		}
		canon.Props = props

	case hub.EventModelError:
		canon.Name = coderadar.EventProviderCallErrored
		props := map[string]any{}
		if ev.Model != nil {
			if ev.Model.Provider != "" {
				props["provider"] = ev.Model.Provider
			}
			if ev.Model.Model != "" {
				props["model"] = ev.Model.Model
			}
		}
		if ev.Custom != nil {
			if v, ok := ev.Custom["error_class"]; ok {
				props["error_class"] = v
			}
			if v, ok := ev.Custom["http_status"]; ok {
				props["http_status"] = v
			}
			if v, ok := ev.Custom["error_message"]; ok {
				props["error_message"] = v
			}
		}
		canon.Props = props

	case hub.EventModelFallback:
		canon.Name = coderadar.EventProviderFallback
		props := map[string]any{}
		if ev.Custom != nil {
			if v, ok := ev.Custom["from_model"]; ok {
				props["from_model"] = v
			}
			if v, ok := ev.Custom["to_model"]; ok {
				props["to_model"] = v
			}
			if v, ok := ev.Custom["reason"]; ok {
				props["reason"] = v
			}
		}
		canon.Props = props

	case hub.EventToolPostUse:
		canon.Name = coderadar.EventToolCallCompleted
		props := map[string]any{}
		if ev.Tool != nil {
			if ev.Tool.Name != "" {
				props["tool_name"] = ev.Tool.Name
			}
			props["exit_code"] = ev.Tool.ExitCode
			if ev.Tool.Duration > 0 {
				props["duration_ms"] = ev.Tool.Duration.Milliseconds()
			}
			if ev.Tool.FilePath != "" {
				props["file_path_sha256"] = coderadar.HashFilePath(ev.Tool.FilePath)
			}
		}
		canon.Props = props

	default:
		return coderadar.Event{}, false
	}
	return canon, true
}

// subscribedEventTypes returns the set of hub event types the
// subscriber wants to receive. Single source of truth for both
// Register() and the canonical mapping above.
func (s *CoderadarSubscriber) subscribedEventTypes() []hub.EventType {
	return []hub.EventType{
		hub.EventMissionPlanDone,
		hub.EventMissionExecuteStart,
		hub.EventVerifyConvergenceStart,
		hub.EventVerifyCriticReview,
		hub.EventMissionConverged,
		hub.EventMissionFailed,
		hub.EventMissionCancelled,
		hub.EventCortexLobeStarted,
		hub.EventCortexRoundCompleted,
		hub.EventAntiTruncFired,
		hub.EventAntiTruncOverridden,
		hub.EventModelPreCall,
		hub.EventModelPostCall,
		hub.EventModelError,
		hub.EventModelFallback,
		hub.EventToolPostUse,
	}
}

// attachCorrelation pulls correlation IDs from ctx and merges them
// into the event's props + sets the CorrelationID envelope field.
func attachCorrelation(ctx context.Context, ev coderadar.Event) coderadar.Event {
	ids := correlation.FromContext(ctx)
	if ev.Props == nil {
		ev.Props = make(map[string]any, 3)
	}
	if ids.SessionID != "" {
		ev.Props[coderadar.PropR1SessionID] = ids.SessionID
	}
	if ids.AgentID != "" {
		ev.Props[coderadar.PropR1AgentID] = ids.AgentID
	}
	if ids.TaskID != "" {
		ev.Props[coderadar.PropR1TaskID] = ids.TaskID
	}
	ev.CorrelationID = buildCorrelationID(ev.Props)
	return ev
}

// attachCorrelationFromEvent fills correlation IDs from the hub.Event
// envelope (AgentID/TaskID/MissionID stays as mission_id prop). Only
// fills slots that ctx did not populate so ctx wins when both present.
func attachCorrelationFromEvent(ev *hub.Event, canon coderadar.Event) coderadar.Event {
	if ev == nil {
		return canon
	}
	if canon.Props == nil {
		canon.Props = make(map[string]any, 3)
	}
	if _, ok := canon.Props[coderadar.PropR1AgentID]; !ok && ev.AgentID != "" {
		canon.Props[coderadar.PropR1AgentID] = ev.AgentID
	}
	if _, ok := canon.Props[coderadar.PropR1TaskID]; !ok && ev.TaskID != "" {
		canon.Props[coderadar.PropR1TaskID] = ev.TaskID
	}
	// Mission ID is the canonical session-scoped trace key when no
	// explicit SessionID is on the ctx.
	if _, ok := canon.Props[coderadar.PropR1SessionID]; !ok && ev.MissionID != "" {
		canon.Props[coderadar.PropR1SessionID] = ev.MissionID
	}
	canon.CorrelationID = buildCorrelationID(canon.Props)
	return canon
}

// buildCorrelationID composes the canonical correlation_id string
// from whichever of the three id-props are populated.
func buildCorrelationID(props map[string]any) string {
	parts := make([]string, 0, 3)
	if v, ok := props[coderadar.PropR1SessionID].(string); ok && v != "" {
		parts = append(parts, "s:"+v)
	}
	if v, ok := props[coderadar.PropR1AgentID].(string); ok && v != "" {
		parts = append(parts, "a:"+v)
	}
	if v, ok := props[coderadar.PropR1TaskID].(string); ok && v != "" {
		parts = append(parts, "t:"+v)
	}
	if len(parts) == 0 {
		return ""
	}
	return joinKey(parts)
}

func joinKey(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "/"
		}
		out += p
	}
	return out
}

// newEventID returns a hex-encoded 16-byte random ID used for the
// per-event idempotency key (T14).
func newEventID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// --- sentinel errors ---

type errPanic struct{ val any }

func (e errPanic) Error() string { return "coderadar subscriber panic" }

var errQueueFull = errSentinel("coderadar subscriber queue full")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }
