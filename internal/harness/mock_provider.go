package harness

import (
	"context"
	"sync"
)

// MockProvider is a deterministic offline Provider for tests and for bench
// runs that must not touch the network. It is safe for concurrent use.
type MockProvider struct {
	// Responses are returned by successive Chat calls in order. When the
	// queue is exhausted the LAST response repeats (sticky last), so a
	// looping runner keeps receiving a well-defined reply. When empty, a
	// default zero-cost final response is returned.
	Responses []*ChatResponse
	// Err, when non-nil, is returned by every Chat call.
	Err error
	// ChatFn, when non-nil, overrides the canned-response behavior
	// entirely (Calls are still recorded). Tests use this to gate turns
	// on external signals (e.g. pause/resume choreography).
	ChatFn func(ctx context.Context, req ChatRequest) (*ChatResponse, error)

	mu    sync.Mutex
	calls []ChatRequest
	next  int
}

// Name implements Provider.
func (m *MockProvider) Name() string { return "mock" }

// Chat implements Provider. It records the request and returns the next
// configured response (sticky last), the configured error, or the ChatFn
// override's result.
func (m *MockProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	m.mu.Lock()
	m.calls = append(m.calls, req)
	var resp *ChatResponse
	if len(m.Responses) > 0 {
		idx := m.next
		if idx >= len(m.Responses) {
			idx = len(m.Responses) - 1
		}
		resp = m.Responses[idx]
		m.next++
	}
	m.mu.Unlock()

	if m.ChatFn != nil {
		return m.ChatFn(ctx, req)
	}
	if m.Err != nil {
		return nil, m.Err
	}
	if resp != nil {
		return resp, nil
	}
	return &ChatResponse{Content: "ok"}, nil
}

// Calls returns a snapshot of every ChatRequest received.
func (m *MockProvider) Calls() []ChatRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ChatRequest, len(m.calls))
	copy(out, m.calls)
	return out
}

// CallCount returns how many Chat calls have been received.
func (m *MockProvider) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}
