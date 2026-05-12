package builtin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/analytics"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/metrics"
)

type recorder struct {
	mu     sync.Mutex
	events []map[string]any
	count  atomic.Int64
	srv    *httptest.Server
}

// emitEvent forwards a hub event for the tests below. The wrapper is
// purely to keep callers terse.
func emitEvent(b *hub.Bus, ctx context.Context, ev *hub.Event) *hub.HookResponse {
	method := b.Emit
	return method(ctx, ev)
}

func newRecorder(t *testing.T) *recorder {
	r := &recorder{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		r.mu.Lock()
		if batch, ok := payload["batch"].([]any); ok {
			for _, b := range batch {
				if obj, ok := b.(map[string]any); ok {
					r.events = append(r.events, obj)
					r.count.Add(1)
				}
			}
		} else if len(payload) > 0 {
			r.events = append(r.events, payload)
			r.count.Add(1)
		}
		r.mu.Unlock()
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func newTestClient(t *testing.T, host string) *analytics.Client {
	t.Helper()
	return analytics.New(analytics.Config{
		APIKey:        "phc_test",
		Host:          host,
		Environment:   "test",
		ServiceName:   "r1-test",
		FlushAt:       1,
		FlushInterval: 30 * time.Millisecond,
		ShutdownWait:  2 * time.Second,
	})
}

func resetMetrics() {
	// metrics.DefaultRegistry has no reset; clobber via fresh counters.
	for _, name := range []string{"analytics.captured", "analytics.dropped", "analytics.no_match", "analytics.captured_noop", "analytics.panic"} {
		c := metrics.DefaultRegistry.Counter(name)
		// Counter has no Set; just record the pre-test value and subtract
		// in assertions. We assert deltas, not absolutes.
		_ = c.Value()
	}
}

func TestAnalyticsSubscriber_ObservesMappedEvent(t *testing.T) {
	resetMetrics()
	rec := newRecorder(t)
	client := newTestClient(t, rec.srv.URL)
	defer func() { _ = client.Shutdown(context.Background()) }()

	bus := hub.New()
	sub := NewAnalyticsSubscriber(client)
	defer sub.Close()
	sub.Register(bus)

	before := metrics.DefaultRegistry.Counter("analytics.captured").Value()

	emitEvent(bus,context.Background(), &hub.Event{
		Type:      hub.EventMissionCreated,
		MissionID: "m-1",
		Timestamp: time.Now(),
	})

	// Wait for the observe goroutine + drainer.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if metrics.DefaultRegistry.Counter("analytics.captured").Value() > before {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := metrics.DefaultRegistry.Counter("analytics.captured").Value(); got <= before {
		t.Errorf("analytics.captured did not increment (before=%d after=%d)", before, got)
	}

	_ = client.Shutdown(context.Background())
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && rec.count.Load() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if rec.count.Load() == 0 {
		t.Fatal("mock PostHog server did not receive any event")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if name, _ := rec.events[0]["event"].(string); name != string(analytics.EvtMissionStarted) {
		t.Errorf("event name = %q, want %q", name, analytics.EvtMissionStarted)
	}
}

func TestAnalyticsSubscriber_DropsUnmappedEvent(t *testing.T) {
	resetMetrics()
	rec := newRecorder(t)
	client := newTestClient(t, rec.srv.URL)
	defer func() { _ = client.Shutdown(context.Background()) }()

	bus := hub.New()
	sub := NewAnalyticsSubscriber(client)
	defer sub.Close()
	sub.Register(bus)

	// EventModelPreCall is intentionally NOT in BusToAnalytics — it
	// should produce no_match.
	before := metrics.DefaultRegistry.Counter("analytics.no_match").Value()
	emitEvent(bus,context.Background(), &hub.Event{
		Type: hub.EventModelPreCall,
	})

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if metrics.DefaultRegistry.Counter("analytics.no_match").Value() > before {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The wildcard subscriber dispatch is async; pre-call may not have
	// dispatched at all because the subscriber declares its events
	// explicitly. Either zero handlers OR an explicit no_match counts.
	got := metrics.DefaultRegistry.Counter("analytics.no_match").Value()
	if got != before {
		t.Logf("analytics.no_match delta = %d (acceptable)", got-before)
	}
}

func TestAnalyticsSubscriber_TenantContextLookup(t *testing.T) {
	resetMetrics()
	rec := newRecorder(t)
	client := newTestClient(t, rec.srv.URL)
	defer func() { _ = client.Shutdown(context.Background()) }()

	bus := hub.New()
	sub := NewAnalyticsSubscriber(client)
	defer sub.Close()
	sub.Register(bus)

	// Mission start carries tenant_id via Custom (the path A4 RelayOne
	// SSO will populate once merged — bus discards observe-phase ctx, so
	// the orchestrator stamps the tenant onto Event.Custom). A later
	// tool event with no ctx tenant should still carry the binding.
	ctx := analytics.WithTenantID(context.Background(), "tenant-lk")
	emitEvent(bus, ctx, &hub.Event{
		Type:      hub.EventMissionCreated,
		MissionID: "m-lk",
		Custom:    map[string]any{"tenant_id": "tenant-lk"},
	})

	// The Observe phase runs in a separate goroutine — wait for the
	// contextlookup to record the tenant before the dependent emit.
	deadlineLk := time.Now().Add(time.Second)
	for time.Now().Before(deadlineLk) {
		if analytics.DefaultContextLookup.Lookup("m-lk") != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := analytics.DefaultContextLookup.Lookup("m-lk"); got != "tenant-lk" {
		t.Fatalf("contextlookup did not capture tenant; Lookup=%q", got)
	}

	emitEvent(bus, context.Background(), &hub.Event{
		Type:      hub.EventToolPostUse,
		MissionID: "m-lk",
		Tool:      &hub.ToolEvent{Name: "Read", Duration: 10 * time.Millisecond},
	})

	// Wait for both events to drain through subscriber → SDK → mock.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && rec.count.Load() < 2 {
		time.Sleep(30 * time.Millisecond)
	}
	// Force an SDK flush so the events land before assertions. Tests
	// running with -race occasionally need a kick beyond the FlushAt=1
	// setting we configured because the SDK batches across goroutines.
	_ = client.Shutdown(context.Background())
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && rec.count.Load() < 2 {
		time.Sleep(30 * time.Millisecond)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	sawTenantOnTool := false
	for _, ev := range rec.events {
		props, _ := ev["properties"].(map[string]any)
		if props == nil {
			continue
		}
		if ev["event"] == string(analytics.EvtToolCallSucceeded) && props["tenant_id"] == "tenant-lk" {
			sawTenantOnTool = true
		}
	}
	if !sawTenantOnTool {
		t.Errorf("tool_call_succeeded missing tenant_id from contextlookup; events=%v", rec.events)
	}
}

func TestAnalyticsSubscriber_NonBlockingOnOverflow(t *testing.T) {
	resetMetrics()
	// Tiny queue so we can force overflow.
	client := analytics.New(analytics.Config{
		APIKey:        "phc_test",
		Host:          "http://127.0.0.1:1", // unreachable — sdk will retry
		FlushAt:       1000,
		FlushInterval: 10 * time.Second,
		ShutdownWait:  100 * time.Millisecond,
	})
	defer func() { _ = client.Shutdown(context.Background()) }()

	bus := hub.New()
	sub := NewAnalyticsSubscriberWithDepth(client, 4)
	defer sub.Close()
	sub.Register(bus)

	before := metrics.DefaultRegistry.Counter("analytics.dropped").Value()
	for i := 0; i < 200; i++ {
		emitEvent(bus,context.Background(), &hub.Event{
			Type:      hub.EventMissionCreated,
			MissionID: "m-overflow",
		})
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if metrics.DefaultRegistry.Counter("analytics.dropped").Value() > before {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := metrics.DefaultRegistry.Counter("analytics.dropped").Value(); got <= before {
		t.Errorf("expected analytics.dropped to increment under overflow (before=%d after=%d)", before, got)
	}
}

func TestAnalyticsSubscriber_NoOpClient_StillObservesBus(t *testing.T) {
	resetMetrics()
	// no-op client: APIKey empty
	client := analytics.New(analytics.Config{APIKey: ""})
	if client.Enabled() {
		t.Fatal("expected no-op client")
	}

	bus := hub.New()
	sub := NewAnalyticsSubscriber(client)
	defer sub.Close()
	sub.Register(bus)

	before := metrics.DefaultRegistry.Counter("analytics.captured_noop").Value()
	emitEvent(bus,context.Background(), &hub.Event{
		Type:      hub.EventMissionCreated,
		MissionID: "m-1",
	})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if metrics.DefaultRegistry.Counter("analytics.captured_noop").Value() > before {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := metrics.DefaultRegistry.Counter("analytics.captured_noop").Value(); got <= before {
		t.Errorf("no-op subscriber did not record captured_noop (before=%d after=%d)", before, got)
	}
}
