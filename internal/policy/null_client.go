package policy

import "context"

// NullClient is a no-op policy backend used for standalone
// development when no real policy engine is configured. It
// always returns DecisionAllow with an explicit banner so
// operators can distinguish "allowed by policy" from "allowed
// because no policy is loaded" in logs and event payloads.
//
// NullClient MUST NOT be used in production — it disables the
// authorization gate entirely.
type NullClient struct{}

// Check always returns DecisionAllow with a fixed banner
// reason. It never returns an error and ignores ctx and req.
func (NullClient) Check(_ context.Context, _ Request) (Result, error) {
	return Result{
		Decision: DecisionAllow,
		Reasons:  []string{"null-client: no policy configured"},
		Errors:   nil,
	}, nil
}

// Compile-time assertion that *NullClient satisfies Client.
var _ Client = (*NullClient)(nil)
