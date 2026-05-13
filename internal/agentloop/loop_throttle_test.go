package agentloop

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/RelayOne/r1/internal/throttle"
	throttlepolicy "github.com/RelayOne/r1/internal/throttle/policy"
)

// TestAgentloopThrottle covers T20: configure throttle at
// rate=1/s burst=2; feed 5 tool-use blocks in a single turn;
// assert exactly 2 real calls and 3 synthetic throttled
// tool_result blocks visible in results.
func TestAgentloopThrottle(t *testing.T) {
	cfg, err := throttlepolicy.Validate(throttlepolicy.Config{
		Defaults: throttlepolicy.Scoped{
			PerSession: throttlepolicy.Limit{Rate: "1/s", Burst: 2},
			PerTenant:  throttlepolicy.Limit{Rate: "100/s", Burst: 100},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	limiter := throttle.New(cfg)

	var realCalls int64
	var mu sync.Mutex
	var capturedNames []string

	handler := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		atomic.AddInt64(&realCalls, 1)
		mu.Lock()
		capturedNames = append(capturedNames, name)
		mu.Unlock()
		return "ok", nil
	}

	loop := New(nil, Config{
		Model:     "stub",
		SessionID: "session-1",
		TenantID:  "tenant-1",
		Throttler: limiter,
	}, nil, handler)

	// Build 5 tool_use blocks with distinct ids.
	blocks := make([]ContentBlock, 5)
	for i := 0; i < 5; i++ {
		blocks[i] = ContentBlock{
			Type:  "tool_use",
			ID:    fmtToolID(i),
			Name:  "agentloop.tool",
			Input: json.RawMessage(`{}`),
		}
	}

	results, hasErr := loop.executeTools(context.Background(), blocks)
	if !hasErr {
		t.Fatalf("expected hasError=true due to throttled blocks")
	}
	if got := len(results); got != 5 {
		t.Fatalf("expected 5 results, got %d", got)
	}
	if got := atomic.LoadInt64(&realCalls); got != 2 {
		t.Fatalf("expected exactly 2 handler calls (burst=2), got %d", got)
	}
	throttledCount := 0
	for _, r := range results {
		if r.IsError && len(r.Content) > 0 && r.Content[:9] == "Throttled" {
			throttledCount++
		}
	}
	if throttledCount != 3 {
		t.Fatalf("expected 3 synthetic-throttled results, got %d", throttledCount)
	}
}

// TestAgentloopNilThrottlerIsOpen covers T15: a nil Throttler means
// every call is admitted, matching the --one-shot bypass.
func TestAgentloopNilThrottlerIsOpen(t *testing.T) {
	var calls int64
	handler := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		atomic.AddInt64(&calls, 1)
		return "ok", nil
	}
	loop := New(nil, Config{
		Model:     "stub",
		SessionID: "session-1",
		TenantID:  "tenant-1",
		Throttler: nil, // explicitly nil
	}, nil, handler)

	blocks := make([]ContentBlock, 5)
	for i := 0; i < 5; i++ {
		blocks[i] = ContentBlock{
			Type:  "tool_use",
			ID:    fmtToolID(i),
			Name:  "agentloop.tool",
			Input: json.RawMessage(`{}`),
		}
	}
	_, hasErr := loop.executeTools(context.Background(), blocks)
	if hasErr {
		t.Fatalf("nil-throttler path should not produce errors")
	}
	if got := atomic.LoadInt64(&calls); got != 5 {
		t.Fatalf("expected all 5 handler calls, got %d", got)
	}
}

func fmtToolID(i int) string {
	const hex = "0123456789abcdef"
	return "tu-" + string([]byte{hex[i&15]})
}
