package models

import "context"

// MockProvider is a test double that returns canned responses without
// making any real API calls.
type MockProvider struct {
	// Response is returned by every Chat call. If nil, a default empty
	// response is returned.
	Response *ChatResponse
	// Err is returned by every Chat call when non-nil.
	Err error
	// Calls records every ChatRequest received.
	Calls []ChatRequest
}

// Name implements Provider.
func (m *MockProvider) Name() string { return "mock" }

// Chat implements Provider. It appends the request to Calls and returns
// the configured Response/Err.
func (m *MockProvider) Chat(_ context.Context, req ChatRequest) (*ChatResponse, error) {
	m.Calls = append(m.Calls, req)
	if m.Err != nil {
		return nil, m.Err
	}
	if m.Response != nil {
		return m.Response, nil
	}
	return &ChatResponse{
		Content:   "ok",
		TokensIn:  10,
		TokensOut: 5,
		CostUSD:   0.001,
	}, nil
}
