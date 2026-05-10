package jsonrpc

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/RelayOne/r1/internal/stokerr"
)

// fakeSubscriptionRegistry implements SubscriptionRegistry for tests.
// Mirrors the real *ws.Conn implementation but is local so we don't
// have to import ws (which would create a cycle).
type fakeSubscriptionRegistry struct {
	mu       sync.Mutex
	stored   map[string]interface{ Close() }
	registers atomic.Int64
	unregisters atomic.Int64
}

func newFakeRegistry() *fakeSubscriptionRegistry {
	return &fakeSubscriptionRegistry{stored: map[string]interface{ Close() }{}}
}

func (f *fakeSubscriptionRegistry) RegisterSubscription(subID string, closer interface{ Close() }) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stored[subID] = closer
	f.registers.Add(1)
}

func (f *fakeSubscriptionRegistry) UnregisterSubscription(subID string) interface{ Close() } {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.stored[subID]
	if !ok {
		return nil
	}
	delete(f.stored, subID)
	f.unregisters.Add(1)
	return c
}

func (f *fakeSubscriptionRegistry) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.stored)
}

// withFakeConnRegistry installs a ConnFromContextFunc that pulls the
// fake registry off the test ctx. Restores prior on cleanup.
func withFakeConnRegistry(t *testing.T) (*fakeSubscriptionRegistry, context.Context) {
	t.Helper()
	reg := newFakeRegistry()
	prior := ConnFromContextFunc
	type ctxKey struct{}
	key := ctxKey{}
	ConnFromContextFunc = func(ctx context.Context) SubscriptionRegistry {
		v := ctx.Value(key)
		if v == nil {
			return nil
		}
		return v.(SubscriptionRegistry)
	}
	t.Cleanup(func() { ConnFromContextFunc = prior })
	ctx := context.WithValue(context.Background(), key, SubscriptionRegistry(reg))
	return reg, ctx
}

// TestHubHandler_SubscribeUnsubscribe_HappyPath asserts the full
// round-trip: subscribe mints a SubID + registers a closer; unsubscribe
// finds it by SubID and removes it.
func TestHubHandler_SubscribeUnsubscribe_HappyPath(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()
	reg, ctx := withFakeConnRegistry(t)

	wd := t.TempDir()
	startResp, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: wd})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	subResp, err := h.DaemonSessionSubscribe(ctx, SessionSubscribeRequest{
		SessionID: startResp.SessionID,
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if subResp.SubID == "" {
		t.Fatalf("subscribe returned empty SubID")
	}
	if reg.count() != 1 {
		t.Errorf("expected 1 registered subscription, got %d", reg.count())
	}
	if reg.registers.Load() != 1 {
		t.Errorf("expected 1 register call, got %d", reg.registers.Load())
	}

	unsubResp, err := h.DaemonSessionUnsubscribe(ctx, SessionUnsubscribeRequest{
		SubID: subResp.SubID,
	})
	if err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if unsubResp.UnsubscribedAt == "" {
		t.Errorf("unsubscribe response missing UnsubscribedAt")
	}
	if reg.count() != 0 {
		t.Errorf("expected 0 registered subscriptions after unsubscribe, got %d", reg.count())
	}
}

// TestHubHandler_Subscribe_NoConnContext returns ErrInternal when
// invoked outside a WS dispatch path (no Conn on ctx).
func TestHubHandler_Subscribe_NoConnContext(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()
	_, _ = withFakeConnRegistry(t)

	wd := t.TempDir()
	startResp, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: wd})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// Use a bare ctx (no fake registry on it) so ConnFromContextFunc returns nil.
	_, err = h.DaemonSessionSubscribe(context.Background(), SessionSubscribeRequest{
		SessionID: startResp.SessionID,
	})
	if err == nil {
		t.Fatal("expected error when ctx has no conn")
	}
	var se *stokerr.Error
	if !errors.As(err, &se) || se.Code != stokerr.ErrInternal {
		t.Errorf("expected ErrInternal, got %v", err)
	}
}

// TestHubHandler_Unsubscribe_UnknownSubID returns ErrNotFound.
func TestHubHandler_Unsubscribe_UnknownSubID(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()
	_, ctx := withFakeConnRegistry(t)

	_, err := h.DaemonSessionUnsubscribe(ctx, SessionUnsubscribeRequest{SubID: "sub-does-not-exist"})
	if err == nil {
		t.Fatal("expected error for unknown SubID")
	}
	var se *stokerr.Error
	if !errors.As(err, &se) || se.Code != stokerr.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestHubHandler_Unsubscribe_EmptySubID returns ErrValidation.
func TestHubHandler_Unsubscribe_EmptySubID(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()
	_, ctx := withFakeConnRegistry(t)

	_, err := h.DaemonSessionUnsubscribe(ctx, SessionUnsubscribeRequest{SubID: ""})
	if err == nil {
		t.Fatal("expected error for empty SubID")
	}
	var se *stokerr.Error
	if !errors.As(err, &se) || se.Code != stokerr.ErrValidation {
		t.Errorf("expected ErrValidation, got %v", err)
	}
}

// TestHubHandler_Subscribe_UnknownSession returns ErrNotFound (the
// session-validation gate fires before the Conn-context check).
func TestHubHandler_Subscribe_UnknownSession(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()
	_, ctx := withFakeConnRegistry(t)

	_, err := h.DaemonSessionSubscribe(ctx, SessionSubscribeRequest{SessionID: "s-unknown"})
	if err == nil {
		t.Fatal("expected error for unknown session")
	}
	var se *stokerr.Error
	if !errors.As(err, &se) || se.Code != stokerr.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
