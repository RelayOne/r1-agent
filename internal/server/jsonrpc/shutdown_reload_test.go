package jsonrpc

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/stokerr"
)

// TestHubHandler_DaemonShutdown_InvokesCallback exercises the wired
// shutdown path: SetShutdownFunc installs a callback that fires
// asynchronously (so the RPC reply lands before the listener tears
// down).
func TestHubHandler_DaemonShutdown_InvokesCallback(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	var graceSeen atomic.Int64
	called := make(chan struct{}, 1)
	h.SetShutdownFunc(func(g int) {
		graceSeen.Store(int64(g))
		called <- struct{}{}
	})

	resp, err := h.DaemonShutdown(context.Background(), DaemonShutdownRequest{GraceSeconds: 17})
	if err != nil {
		t.Fatalf("DaemonShutdown: %v", err)
	}
	if resp.AcceptedAt == "" {
		t.Errorf("AcceptedAt empty")
	}
	if _, perr := time.Parse(time.RFC3339Nano, resp.AcceptedAt); perr != nil {
		t.Errorf("AcceptedAt not RFC3339Nano: %v", perr)
	}

	// Callback fires async; wait for it.
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatalf("shutdown callback did not fire within 2s")
	}
	if got := graceSeen.Load(); got != 17 {
		t.Errorf("callback received grace=%d, want 17", got)
	}
}

// TestHubHandler_DaemonReloadConfig_InvokesCallback exercises the
// wired reload path: SetReloadConfigFunc installs a callback that
// receives the operator's path and returns the absolute path applied.
func TestHubHandler_DaemonReloadConfig_InvokesCallback(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	var pathSeen atomic.Value
	h.SetReloadConfigFunc(func(path string) (string, error) {
		pathSeen.Store(path)
		return "/abs/applied/" + path, nil
	})

	resp, err := h.DaemonReloadConfig(context.Background(), DaemonReloadConfigRequest{Path: "x.yaml"})
	if err != nil {
		t.Fatalf("DaemonReloadConfig: %v", err)
	}
	if resp.Source != "/abs/applied/x.yaml" {
		t.Errorf("Source = %q, want /abs/applied/x.yaml", resp.Source)
	}
	if resp.ReloadedAt == "" {
		t.Errorf("ReloadedAt empty")
	}
	if _, perr := time.Parse(time.RFC3339Nano, resp.ReloadedAt); perr != nil {
		t.Errorf("ReloadedAt not RFC3339Nano: %v", perr)
	}
	if got, _ := pathSeen.Load().(string); got != "x.yaml" {
		t.Errorf("callback received path %q, want x.yaml", got)
	}
}

// TestHubHandler_DaemonReloadConfig_CallbackError surfaces a callback
// error as ErrInternal.
func TestHubHandler_DaemonReloadConfig_CallbackError(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	wantErr := errors.New("config syntax error at line 42")
	h.SetReloadConfigFunc(func(string) (string, error) {
		return "", wantErr
	})

	_, err := h.DaemonReloadConfig(context.Background(), DaemonReloadConfigRequest{})
	if err == nil {
		t.Fatal("expected callback error to propagate")
	}
	var se *stokerr.Error
	if !errors.As(err, &se) || se.Code != stokerr.ErrInternal {
		t.Errorf("expected ErrInternal wrap, got %v", err)
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("expected errors.Is(err, wantErr); got %v", err)
	}
}

// TestHubHandler_DaemonShutdown_AsyncDoesNotBlock asserts the
// handler returns immediately even when the callback blocks. Critical
// for graceful shutdown: the RPC reply must flush before the listener
// closes.
func TestHubHandler_DaemonShutdown_AsyncDoesNotBlock(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	blocked := make(chan struct{})
	defer close(blocked)
	h.SetShutdownFunc(func(int) {
		<-blocked // never wakes during the test — verifies async
	})

	start := time.Now()
	if _, err := h.DaemonShutdown(context.Background(), DaemonShutdownRequest{}); err != nil {
		t.Fatalf("DaemonShutdown: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("handler blocked for %v despite async callback", elapsed)
	}
}
