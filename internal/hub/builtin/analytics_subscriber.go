package builtin

import (
	"context"
	"sync"

	"github.com/RelayOne/r1/internal/analytics"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/metrics"
)

// AnalyticsSubscriber bridges the in-process hub.Bus to the PostHog
// client in internal/analytics/. It mirrors the registration pattern in
// cost_tracker.go: a single Register() call that attaches an observer
// to a defined set of bus events; each call routes through a per-event
// adapter (declared in analytics/taxonomy.go) and is enqueued onto a
// bounded channel that a single drainer goroutine forwards to the
// PostHog SDK.
//
// Design properties locked in by specs/posthog-analytics.md §8:
//
//   - Mode is ModeObserve. The subscriber never gates the bus.
//   - Subscription set is the bus side of analytics/taxonomy.go's
//     BusToAnalytics map — i.e. only events with a PostHog mapping
//     get routed. Adding a new event is a one-line change in the map.
//   - The drain channel is bounded (8 192 by default). On overflow the
//     subscriber drops the event and increments the analytics.dropped
//     counter — it NEVER blocks the bus delivery goroutine.
//   - The drainer goroutine recovers from panics in the SDK call so a
//     PostHog SDK regression never takes down the bus.
//
// Metrics surfaced via metrics.DefaultRegistry:
//
//	analytics.captured  — events that reached the SDK enqueue path
//	analytics.dropped   — events dropped on overflow
//	analytics.no_match  — events with no PostHog taxonomy mapping
type AnalyticsSubscriber struct {
	client      *analytics.Client
	queue       chan analyticsItem
	stop        chan struct{}
	wg          sync.WaitGroup
	contextLook *analytics.ContextLookup

	// distinctIDFn lets tests override how distinct_id is derived from a
	// bus event. Production always uses the default helper.
	distinctIDFn func(*hub.Event) string
}

type analyticsItem struct {
	ctx       context.Context
	name      analytics.EventName
	distinct  string
	props     map[string]any
}

// DefaultQueueDepth is the bounded buffer size between the bus and the
// PostHog SDK. Per spec §8 the default is 8 192; the spec also calls
// out that POSTHOG_QUEUE_DEPTH can override it at the FromEnv path.
const DefaultQueueDepth = 8192

// NewAnalyticsSubscriber constructs a subscriber bound to client. A nil
// client OR a !Enabled() client still produces a subscriber — but the
// drainer drops every item (the bus still emits, the SDK never sees
// the captures). This keeps the wiring uniform across services that
// have not been configured with POSTHOG_API_KEY.
func NewAnalyticsSubscriber(client *analytics.Client) *AnalyticsSubscriber {
	return NewAnalyticsSubscriberWithDepth(client, DefaultQueueDepth)
}

// NewAnalyticsSubscriberWithDepth is the same constructor with an
// explicit queue depth. Used by integration tests that want to assert
// drop behaviour under load.
func NewAnalyticsSubscriberWithDepth(client *analytics.Client, depth int) *AnalyticsSubscriber {
	if depth <= 0 {
		depth = DefaultQueueDepth
	}
	s := &AnalyticsSubscriber{
		client:       client,
		queue:        make(chan analyticsItem, depth),
		stop:         make(chan struct{}),
		contextLook:  analytics.DefaultContextLookup,
		distinctIDFn: defaultDistinctID,
	}
	s.wg.Add(1)
	go s.drain()
	return s
}

// Register attaches the subscriber to the bus. Subscribes once per
// hub.EventType in the analytics taxonomy — wildcards are intentionally
// avoided so the subscriber receives only events it has a mapping for.
//
// Priority is set to 9100 (just after the cost tracker at 9000) so the
// analytics subscriber always runs last in the observe phase — keeping
// it off the critical path.
func (s *AnalyticsSubscriber) Register(bus *hub.Bus) {
	if bus == nil {
		return
	}
	events := make([]hub.EventType, 0, len(analytics.BusToAnalytics))
	for t := range analytics.BusToAnalytics {
		events = append(events, t)
	}
	bus.Register(hub.Subscriber{
		ID:       "builtin.analytics",
		Events:   events,
		Mode:     hub.ModeObserve,
		Priority: 9100,
		Handler:  s.handle,
	})
}

// handle is the bus-side entry point. It runs on the bus's observe
// goroutine — keep it cheap: derive the analytics shape, enqueue, return.
func (s *AnalyticsSubscriber) handle(ctx context.Context, ev *hub.Event) *hub.HookResponse {
	if ev == nil {
		return &hub.HookResponse{Decision: hub.Allow}
	}
	name, adapter, ok := analytics.LookupTaxonomy(ev.Type)
	if !ok {
		metrics.DefaultRegistry.Counter("analytics.no_match").Inc()
		return &hub.HookResponse{Decision: hub.Allow}
	}

	// Mission-start events teach the contextlookup about a tenant; later
	// per-mission events read it back when the bus delivery has no ctx
	// tenant of its own.
	//
	// hub.Bus discards the emitter ctx on the observe phase (every
	// observe handler runs with context.Background — see internal/hub/bus.go
	// phase-3 dispatch). The emitter therefore conveys tenant identity
	// via two channels and the subscriber accepts whichever arrives:
	//
	//   1. ctx — used when the bus is invoked synchronously (gate /
	//      transform mode) or when a test wires the subscriber directly.
	//   2. ev.Custom["tenant_id"] — populated by the orchestrator on
	//      EventMissionCreated when the emitter ctx had a TenantID.
	//      This is the path A4 RelayOne SSO will use once merged.
	if ev.Type == hub.EventMissionCreated && ev.MissionID != "" {
		tenantID := analytics.TenantIDFromContext(ctx)
		if tenantID == "" {
			if v, ok := ev.Custom["tenant_id"].(string); ok {
				tenantID = v
			}
		}
		if tenantID != "" {
			s.contextLook.Remember(ev.MissionID, tenantID)
		}
	}
	if ev.Type == hub.EventMissionConverged || ev.Type == hub.EventMissionFailed || ev.Type == hub.EventMissionCancelled {
		s.contextLook.Forget(ev.MissionID)
	}

	props := adapter(ev)
	item := analyticsItem{
		ctx:      s.hydrateTenantCtx(ctx, ev),
		name:     name,
		distinct: s.distinctIDFn(ev),
		props:    props,
	}
	select {
	case s.queue <- item:
		// queued for the drainer
	default:
		metrics.DefaultRegistry.Counter("analytics.dropped").Inc()
	}
	return &hub.HookResponse{Decision: hub.Allow}
}

// hydrateTenantCtx returns a context whose correlation.TenantID is set
// from the bus event's mission_id binding when the inbound ctx has
// none. Until A4 RelayOne SSO lands, this lookup will normally return
// the empty string and the returned ctx is unchanged.
func (s *AnalyticsSubscriber) hydrateTenantCtx(ctx context.Context, ev *hub.Event) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if analytics.TenantIDFromContext(ctx) != "" {
		return ctx
	}
	tenantID := s.contextLook.Lookup(ev.MissionID)
	if tenantID == "" {
		return ctx
	}
	return analytics.WithTenantID(ctx, tenantID)
}

// drain reads items off the queue and pushes each to the analytics
// client. Recovers from SDK panics so a regression in posthog-go never
// takes down the bus's observe-phase goroutine.
func (s *AnalyticsSubscriber) drain() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stop:
			return
		case item, ok := <-s.queue:
			if !ok {
				return
			}
			s.dispatch(item)
		}
	}
}

func (s *AnalyticsSubscriber) dispatch(item analyticsItem) {
	defer func() {
		if r := recover(); r != nil {
			metrics.DefaultRegistry.Counter("analytics.panic").Inc()
		}
	}()
	if s.client == nil || !s.client.Enabled() {
		// Still count "would-have-captured" so tests can observe the
		// bus → adapter path independently of the SDK wiring.
		metrics.DefaultRegistry.Counter("analytics.captured_noop").Inc()
		return
	}
	s.client.Capture(item.ctx, item.distinct, item.name.String(), item.props)
	metrics.DefaultRegistry.Counter("analytics.captured").Inc()
}

// Close stops the drainer goroutine. Pending items still in the queue
// are abandoned — call client.Shutdown() separately to flush the SDK's
// own batch buffer.
func (s *AnalyticsSubscriber) Close() {
	close(s.stop)
	s.wg.Wait()
}

// defaultDistinctID returns the most stable identifier we can derive
// from a bus event for PostHog's distinct_id field. Preference order:
// AgentID → TaskID → MissionID → "anon". An empty distinct_id is
// rejected at the Capture layer.
func defaultDistinctID(ev *hub.Event) string {
	if ev == nil {
		return "anon"
	}
	if ev.AgentID != "" {
		return ev.AgentID
	}
	if ev.TaskID != "" {
		return ev.TaskID
	}
	if ev.MissionID != "" {
		return "mission_" + ev.MissionID
	}
	return "anon"
}
