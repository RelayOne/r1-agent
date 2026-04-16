package env

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// captureLogger is a FallbackLogger that records every message.
type captureLogger struct {
	mu   sync.Mutex
	msgs []string
}

func (c *captureLogger) Printf(format string, v ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, fmt.Sprintf(format, v...))
}

func (c *captureLogger) contains(sub string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range c.msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// helpers to install/uninstall probes without polluting the
// global availabilityChecks between tests.

func withAvailability(t *testing.T, b Backend, avail bool) {
	t.Helper()
	old, wasThere := availabilityChecks[b]
	availabilityChecks[b] = func(context.Context) bool { return avail }
	t.Cleanup(func() {
		if wasThere {
			availabilityChecks[b] = old
		} else {
			delete(availabilityChecks, b)
		}
	})
}

func TestResolveBackend_PreferredAvailable(t *testing.T) {
	withAvailability(t, BackendDocker, true)
	got, err := ResolveBackend(context.Background(), BackendDocker, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != BackendDocker {
		t.Errorf("got %q want %q", got, BackendDocker)
	}
}

func TestResolveBackend_PreferredUnavailable_FallsBack(t *testing.T) {
	withAvailability(t, BackendDocker, false)
	withAvailability(t, BackendInProc, true)
	logger := &captureLogger{}
	got, err := ResolveBackend(context.Background(), BackendDocker, nil, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != BackendInProc {
		t.Errorf("got %q want %q (fallback)", got, BackendInProc)
	}
	if !logger.contains("DEGRADED") {
		t.Errorf("expected DEGRADED log when falling back from docker to inproc, got msgs %v", logger.msgs)
	}
}

func TestResolveBackend_NoPreferred_UsesChainHead(t *testing.T) {
	withAvailability(t, BackendDocker, true)
	got, err := ResolveBackend(context.Background(), "", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != BackendDocker {
		t.Errorf("got %q want docker (chain head)", got)
	}
}

func TestResolveBackend_ExhaustedChain(t *testing.T) {
	withAvailability(t, BackendDocker, false)
	withAvailability(t, BackendInProc, false)
	_, err := ResolveBackend(context.Background(), BackendDocker, nil, nil)
	if !errors.Is(err, ErrNoUsableBackend) {
		t.Fatalf("want ErrNoUsableBackend, got %v", err)
	}
}

func TestResolveBackend_CustomChain(t *testing.T) {
	// Caller provides its own chain — DefaultChain isn't used.
	withAvailability(t, BackendDocker, false)
	withAvailability(t, BackendSSH, true)
	got, err := ResolveBackend(context.Background(), BackendDocker, []Backend{BackendSSH, BackendInProc}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != BackendSSH {
		t.Errorf("got %q want %q", got, BackendSSH)
	}
}

func TestBackendAvailable_Unregistered_DefaultsTrue(t *testing.T) {
	// A backend without a registered probe is assumed available.
	// Protects against over-eager fallback when a probe is missing.
	if !BackendAvailable(context.Background(), Backend("made-up")) {
		t.Error("unregistered backend should default to available=true")
	}
}

func TestIsolationRank(t *testing.T) {
	if IsolationRank(BackendDocker) <= IsolationRank(BackendInProc) {
		t.Errorf("Docker rank %d should be > InProc rank %d",
			IsolationRank(BackendDocker), IsolationRank(BackendInProc))
	}
	if IsolationRank(Backend("unknown")) != -1 {
		t.Error("unknown backend should return rank -1")
	}
}

func TestDefaultChain_OrderedByIsolation(t *testing.T) {
	chain := DefaultChain()
	for i := 1; i < len(chain); i++ {
		prev := IsolationRank(chain[i-1])
		cur := IsolationRank(chain[i])
		if prev < cur {
			t.Errorf("chain not ordered high-to-low: [%d]=%q(%d) < [%d]=%q(%d)",
				i-1, chain[i-1], prev, i, chain[i], cur)
		}
	}
}
