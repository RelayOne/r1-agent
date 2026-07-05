package agentloop

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/RelayOne/r1/internal/provider"
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

	results, hasErr, hasHandlerErr := loop.executeTools(context.Background(), blocks)
	if !hasErr {
		t.Fatalf("expected hasError=true due to throttled blocks")
	}
	// Throttle denials are NOT genuine handler execution errors, so they must
	// not be reported as such (they don't count toward the max_errors budget).
	if hasHandlerErr {
		t.Fatalf("throttled blocks must not report hasHandlerError=true")
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
	_, hasErr, hasHandlerErr := loop.executeTools(context.Background(), blocks)
	if hasErr {
		t.Fatalf("nil-throttler path should not produce errors")
	}
	if hasHandlerErr {
		t.Fatalf("nil-throttler path should not report handler errors")
	}
	if got := atomic.LoadInt64(&calls); got != 5 {
		t.Fatalf("expected all 5 handler calls, got %d", got)
	}
}

func fmtToolID(i int) string {
	const hex = "0123456789abcdef"
	return "tu-" + string([]byte{hex[i&15]})
}

// denyAllThrottle is a ThrottleGate stub that denies every tool call.
type denyAllThrottle struct{}

func (denyAllThrottle) AllowAgentloop(ctx context.Context, sessionID, tenantID, tool string) throttle.Decision {
	return throttle.Decision{Allowed: false, Tool: tool, RetryAfter: 1}
}

// TestThrottleDenialsDoNotTripMaxErrors is the regression for the gap where a
// denied tool (throttle / policy / promptguard) incremented consecutiveErrors
// and aborted the whole run with max_errors after MaxConsecutiveErrs turns — a
// denied tool is not an execution error and a rate-limited session must be
// allowed to keep trying rather than being killed.
func TestThrottleDenialsDoNotTripMaxErrors(t *testing.T) {
	// Provider always asks for one tool, forever — every turn's single tool
	// gets throttle-denied. With MaxConsecutiveErrs=3 the OLD behavior aborted
	// on turn 3 with StopReason=max_errors.
	oneToolResp := &provider.ChatResponse{
		Content: []provider.ResponseContent{
			{Type: "tool_use", ID: "toolu_x", Name: "agentloop.tool",
				Input: map[string]interface{}{}},
		},
		StopReason: "tool_use",
	}
	resps := make([]*provider.ChatResponse, 10)
	for i := range resps {
		resps[i] = oneToolResp
	}
	mock := &mockProvider{responses: resps}

	var handlerCalls int64
	handler := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		atomic.AddInt64(&handlerCalls, 1)
		return "ok", nil
	}

	loop := New(mock, Config{
		Model:              "stub",
		SessionID:          "s1",
		TenantID:           "t1",
		Throttler:          denyAllThrottle{},
		MaxTurns:           8,
		MaxConsecutiveErrs: 3,
	}, []provider.ToolDef{{Name: "agentloop.tool"}}, handler)

	result, err := loop.Run(context.Background(), "go")

	// The run must NOT abort with max_errors. It exhausts the mock responses
	// / max_turns instead, because throttle denials never count as errors.
	if result.StopReason == "max_errors" {
		t.Fatalf("throttle denials tripped max_errors abort: %v", err)
	}
	if atomic.LoadInt64(&handlerCalls) != 0 {
		t.Fatalf("throttled tools must never reach the handler, got %d calls", handlerCalls)
	}
	if result.Turns < 4 {
		t.Fatalf("run aborted early after %d turns; denials should not stop it", result.Turns)
	}
}
