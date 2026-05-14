package builtin

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
	
)

// fakeClient records every call. Implements lifecycle.Client.
type fakeClient struct {
	mu        sync.Mutex
	tracks    []fakeTrack
	identifies []fakeIdentify
	deletes    []string
	enabled    bool
}

type fakeTrack struct {
	UserID, Event string
	Props         map[string]any
}

type fakeIdentify struct {
	UserID, Email string
	Traits        map[string]any
}

func (f *fakeClient) Enabled() bool { return f.enabled }
func (f *fakeClient) Identify(ctx context.Context, userID, email string, traits map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identifies = append(f.identifies, fakeIdentify{userID, email, traits})
	return nil
}
func (f *fakeClient) Track(ctx context.Context, userID, event string, props map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tracks = append(f.tracks, fakeTrack{userID, event, props})
	return nil
}
func (f *fakeClient) MergeIdentities(ctx context.Context, old, new string) error { return nil }
func (f *fakeClient) Delete(ctx context.Context, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, userID)
	return nil
}

func (f *fakeClient) tracksFor(event string) []fakeTrack {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []fakeTrack{}
	for _, t := range f.tracks {
		if t.Event == event {
			out = append(out, t)
		}
	}
	return out
}

// fakePolicy implements lifecyclePolicy.
type fakePolicy struct {
	disabled map[string]bool
}

func (p fakePolicy) LifecycleDisabled(tenantID string) bool {
	return p.disabled[tenantID]
}

// drainSDK polls the fake client until it has recorded `want` total
// SDK calls or the deadline expires.
func drainSDK(t *testing.T, fc *fakeClient, want int, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		fc.mu.Lock()
		got := len(fc.tracks) + len(fc.identifies) + len(fc.deletes)
		fc.mu.Unlock()
		if got >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	fc.mu.Lock()
	got := len(fc.tracks) + len(fc.identifies) + len(fc.deletes)
	fc.mu.Unlock()
	t.Fatalf("SDK drain timeout: got=%d want=%d", got, want)
}

func TestLifecycleSubscriber_RouteSessionInitProducesSessionStartedAndActivation(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{enabled: true}
	fs := mustOpenTempFlagStore(t)
	s := NewLifecycleSubscriber(fc, fs, fakePolicy{})
	b := hub.New()
	if err := s.Register(b); err != nil {
		t.Fatalf("register: %v", err)
	}
	defer s.Close()

	ev := &hub.Event{
		Type:   hub.EventSessionInit,
		Custom: map[string]any{"user_id": "alice", "tenant_id": "acme"},
	}
	emitLifecycleEvent(b, ev)

	drainSDK(t, fc, 2, time.Second)
	sessions := fc.tracksFor("session_started")
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session_started track, got %d", len(sessions))
	}
	if sessions[0].UserID != "alice" {
		t.Fatalf("expected userID=alice, got %s", sessions[0].UserID)
	}
	activation := fc.tracksFor("activation")
	if len(activation) != 1 {
		t.Fatalf("expected 1 activation track (first SessionInit), got %d", len(activation))
	}
}

func TestLifecycleSubscriber_ActivationFiresOnlyOnceAcrossSessions(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{enabled: true}
	fs := mustOpenTempFlagStore(t)
	s := NewLifecycleSubscriber(fc, fs, fakePolicy{})
	b := hub.New()
	if err := s.Register(b); err != nil {
		t.Fatalf("register: %v", err)
	}
	defer s.Close()

	for i := 0; i < 3; i++ {
		ev := &hub.Event{
			Type:   hub.EventSessionInit,
			Custom: map[string]any{"user_id": "bob", "tenant_id": "acme"},
		}
		emitLifecycleEvent(b, ev)
	}

	drainSDK(t, fc, 4, time.Second) // 3 session_started + 1 activation
	if got := len(fc.tracksFor("session_started")); got != 3 {
		t.Errorf("expected 3 session_started tracks, got %d", got)
	}
	if got := len(fc.tracksFor("activation")); got != 1 {
		t.Errorf("expected exactly 1 activation track (first-time gate), got %d", got)
	}
}

func TestLifecycleSubscriber_AnonymousUserIsFiltered(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{enabled: true}
	fs := mustOpenTempFlagStore(t)
	s := NewLifecycleSubscriber(fc, fs, fakePolicy{})
	b := hub.New()
	if err := s.Register(b); err != nil {
		t.Fatalf("register: %v", err)
	}
	defer s.Close()

	// no user_id in Custom — anonymous CLI run
	ev := &hub.Event{Type: hub.EventSessionInit, Custom: map[string]any{}}
	emitLifecycleEvent(b, ev)

	time.Sleep(100 * time.Millisecond) // wait for drain attempt
	if len(fc.tracks)+len(fc.identifies) != 0 {
		t.Fatalf("anonymous event should not reach Customer.io; got %d tracks + %d identifies", len(fc.tracks), len(fc.identifies))
	}
}

func TestLifecycleSubscriber_TenantOptOutSuppresses(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{enabled: true}
	fs := mustOpenTempFlagStore(t)
	pol := fakePolicy{disabled: map[string]bool{"opted-out-tenant": true}}
	s := NewLifecycleSubscriber(fc, fs, pol)
	b := hub.New()
	if err := s.Register(b); err != nil {
		t.Fatalf("register: %v", err)
	}
	defer s.Close()

	ev := &hub.Event{
		Type:   hub.EventSessionInit,
		Custom: map[string]any{"user_id": "carol", "tenant_id": "opted-out-tenant"},
	}
	emitLifecycleEvent(b, ev)

	time.Sleep(100 * time.Millisecond)
	if len(fc.tracks) != 0 {
		t.Fatalf("opted-out tenant should not reach Customer.io; got %d tracks", len(fc.tracks))
	}
}

func TestLifecycleSubscriber_BudgetAlertsAlwaysFire(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{enabled: true}
	fs := mustOpenTempFlagStore(t)
	s := NewLifecycleSubscriber(fc, fs, fakePolicy{})
	b := hub.New()
	if err := s.Register(b); err != nil {
		t.Fatalf("register: %v", err)
	}
	defer s.Close()

	for i := 0; i < 3; i++ {
		ev := &hub.Event{
			Type:   hub.EventCostBudget80,
			Custom: map[string]any{"user_id": "dan", "tenant_id": "acme", "cost_usd": float64(i + 1)},
		}
		emitLifecycleEvent(b, ev)
	}

	drainSDK(t, fc, 3, time.Second)
	if got := len(fc.tracksFor("budget_alert")); got != 3 {
		t.Errorf("expected 3 budget_alert tracks (no first-time gate), got %d", got)
	}
}

func TestLifecycleSubscriber_DisabledClientNeverEmits(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{enabled: false}
	fs := mustOpenTempFlagStore(t)
	s := NewLifecycleSubscriber(fc, fs, fakePolicy{})
	b := hub.New()
	if err := s.Register(b); err != nil {
		t.Fatalf("register: %v", err)
	}

	ev := &hub.Event{Type: hub.EventSessionInit, Custom: map[string]any{"user_id": "u", "tenant_id": "t"}}
	emitLifecycleEvent(b, ev)

	time.Sleep(50 * time.Millisecond)
	if len(fc.tracks) != 0 {
		t.Fatalf("disabled client should never record tracks; got %d", len(fc.tracks))
	}
	if len(fc.identifies) != 0 {
		t.Fatalf("disabled client should never record identifies; got %d", len(fc.identifies))
	}
}
