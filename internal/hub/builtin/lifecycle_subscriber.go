package builtin

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/lifecycle"
	"github.com/RelayOne/r1/internal/metrics"
)

// LifecycleSubscriber bridges the in-process hub.Bus to Customer.io.
// Mirrors AnalyticsSubscriber in shape (ModeObserve, bounded queue,
// per-event drainer goroutine, panic-safe SDK wrapper). Maps R1 bus
// events to Customer.io tracks per spec §6.3.
//
// First-time milestones (signup, activation, first_mission,
// first_completion) are gated by FlagStore.IsFirstTime so each fires
// exactly once per (tenant, user). Recurring engagement signals
// (anti_trunc_fired, budget_alert) fire every time.
//
// SSOLoginSucceeded does not yet exist in this branch — see spec note;
// the subscriber subscribes defensively to SessionInit alone until A4
// merges and adds the SSO event. When the SSO event lands a second
// subscription line activates automatically (the bus.Subscribe call is
// already in Register).
//
// Anonymous users (no user_id on the event) are filtered out at the
// top of the handler — anonymous CLI sessions never reach Customer.io.
type LifecycleSubscriber struct {
	client    lifecycle.Client
	flags     *lifecycle.FlagStore
	policy    lifecyclePolicy
	queue     chan lifecycleItem
	closeOnce sync.Once
	wg        sync.WaitGroup
	dropped   atomic.Uint64
	queued    atomic.Uint64
	workers   int
}

// lifecyclePolicy is the narrow interface the subscriber needs from
// the policy package. Tests pass a fake; production wires
// lifecycle.Policy directly.
type lifecyclePolicy interface {
	LifecycleDisabled(tenantID string) bool
}

// passthroughPolicy is the default when no policy was wired.
type passthroughPolicy struct{}

func (passthroughPolicy) LifecycleDisabled(string) bool { return false }

type lifecycleItem struct {
	kind      string // "identify" | "track" | "delete"
	userID    string
	tenantID  string
	event     string
	traits    map[string]any
	props     map[string]any
}

// DefaultLifecycleQueueDepth is the bounded buffer between the bus and
// the Customer.io SDK. 4 096 events ≈ 5 minutes of moderate-traffic
// burst at 14 events/s.
const DefaultLifecycleQueueDepth = 4096

// DefaultLifecycleWorkers is the worker-goroutine count draining the
// queue. Four lets us absorb a transient Customer.io latency spike
// without head-of-line blocking on a single SDK call.
const DefaultLifecycleWorkers = 4

// NewLifecycleSubscriber constructs a subscriber but does NOT register
// it on the bus. Caller invokes Register(bus) after construction.
func NewLifecycleSubscriber(client lifecycle.Client, flags *lifecycle.FlagStore, policy lifecyclePolicy) *LifecycleSubscriber {
	if policy == nil {
		policy = passthroughPolicy{}
	}
	return &LifecycleSubscriber{
		client:  client,
		flags:   flags,
		policy:  policy,
		queue:   make(chan lifecycleItem, DefaultLifecycleQueueDepth),
		workers: DefaultLifecycleWorkers,
	}
}

// Register attaches the subscriber to bus and starts worker
// goroutines. Returns nil immediately when client.Enabled() is false
// so dev environments that haven't set CUSTOMERIO_SITE_ID never spawn
// goroutines they don't need.
func (s *LifecycleSubscriber) Register(bus *hub.Bus) error {
	if bus == nil {
		return nil
	}
	if s.client == nil || !s.client.Enabled() {
		return nil
	}
	for i := 0; i < s.workers; i++ {
		s.wg.Add(1)
		go s.drain()
	}

	// Six event types maps to six Customer.io tracks. SSOLoginSucceeded
	// is not yet a bus event in this branch — when A4 merges and adds
	// it, append it to the Events list and add a case in handle().
	bus.Register(hub.Subscriber{
		ID: "builtin.lifecycle",
		Events: []hub.EventType{
			hub.EventSessionInit,
			hub.EventMissionCreated,
			hub.EventMissionConverged,
			hub.EventCostBudget80,
			hub.EventCostBudget90,
			hub.EventCostBudgetExceeded,
		},
		Mode:     hub.ModeObserve,
		Priority: 9200, // after analytics (9100) and cost (lower)
		Handler:  s.handle,
	})
	return nil
}

// Close drains in-flight work and shuts down workers. Idempotent —
// calling more than once is safe.
func (s *LifecycleSubscriber) Close() {
	s.closeOnce.Do(func() {
		close(s.queue)
	})
	s.wg.Wait()
}

// Snapshot returns the queued + dropped counters. Used by metrics.
func (s *LifecycleSubscriber) Snapshot() (queued, dropped uint64) {
	return s.queued.Load(), s.dropped.Load()
}

// handle is the bus-side observer. Maps the event type to a
// Customer.io track + (if applicable) a first-time milestone.
func (s *LifecycleSubscriber) handle(ctx context.Context, e *hub.Event) *hub.HookResponse {
	if e == nil {
		return &hub.HookResponse{Decision: hub.Allow}
	}

	userID := extractLifecycleUserID(e)
	if userID == "" {
		// Anonymous CLI run — never reaches Customer.io.
		return &hub.HookResponse{Decision: hub.Allow}
	}
	tenantID := extractLifecycleTenantID(e)
	if s.policy.LifecycleDisabled(tenantID) {
		return &hub.HookResponse{Decision: hub.Allow}
	}

	switch e.Type {
	case hub.EventSessionInit:
		s.emitTrack(ctx, userID, tenantID, "session_started", propsFromEvent(e))
		if s.firstTime(ctx, tenantID, "activation", userID) {
			s.emitTrack(ctx, userID, tenantID, "activation", nil)
		}
	case hub.EventMissionCreated:
		s.emitTrack(ctx, userID, tenantID, "mission_started", propsFromEvent(e))
		if s.firstTime(ctx, tenantID, "first_mission", userID) {
			s.emitTrack(ctx, userID, tenantID, "first_mission", nil)
		}
	case hub.EventMissionConverged:
		s.emitTrack(ctx, userID, tenantID, "mission_completed", propsFromEvent(e))
		if s.firstTime(ctx, tenantID, "first_completion", userID) {
			s.emitTrack(ctx, userID, tenantID, "first_completion", nil)
		}
	case hub.EventCostBudget80, hub.EventCostBudget90, hub.EventCostBudgetExceeded:
		s.emitTrack(ctx, userID, tenantID, "budget_alert", propsFromEvent(e))
	}
	return &hub.HookResponse{Decision: hub.Allow}
}

// firstTime is the FlagStore-gated check. Failures (flagstore unset,
// SQLite IO error) log a warning and return false to avoid double-fire.
func (s *LifecycleSubscriber) firstTime(ctx context.Context, tenantID, event, userID string) bool {
	if s.flags == nil {
		return false
	}
	ok, err := s.flags.IsFirstTime(ctx, tenantID, event, userID)
	if err != nil {
		metrics.DefaultRegistry.Counter("lifecycle.flagstore_error").Inc()
		return false
	}
	return ok
}

// emitTrack enqueues a Customer.io Track call onto the worker queue.
// Drops on overflow rather than blocking the bus. Safe after Close()
// thanks to the defer-recover on the closed-channel send.
func (s *LifecycleSubscriber) emitTrack(ctx context.Context, userID, tenantID, event string, props map[string]any) {
	defer func() {
		if r := recover(); r != nil {
			// queue closed (Close() was called); count as dropped.
			s.dropped.Add(1)
		}
	}()
	it := lifecycleItem{
		kind:     "track",
		userID:   userID,
		tenantID: tenantID,
		event:    event,
		props:    props,
	}
	select {
	case s.queue <- it:
		s.queued.Add(1)
	default:
		s.dropped.Add(1)
	}
}

// drain pulls items off the queue and forwards them to the Customer.io
// SDK with a 5-second per-call context timeout. Close() closes the
// queue; this loop returns when the queue is closed and drained.
func (s *LifecycleSubscriber) drain() {
	defer s.wg.Done()
	for it := range s.queue {
		s.forward(it)
	}
}

func (s *LifecycleSubscriber) forward(it lifecycleItem) {
	defer func() {
		// Don't let an SDK panic kill the worker goroutine.
		if r := recover(); r != nil {
			s.dropped.Add(1)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch it.kind {
	case "track":
		if err := s.client.Track(ctx, it.userID, it.event, it.props); err != nil {
			s.dropped.Add(1)
		}
	case "identify":
		if err := s.client.Identify(ctx, it.userID, it.userID, it.traits); err != nil {
			s.dropped.Add(1)
		}
	case "delete":
		if err := s.client.Delete(ctx, it.userID); err != nil {
			s.dropped.Add(1)
		}
	}
}
