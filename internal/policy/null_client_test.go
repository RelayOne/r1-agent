package policy

import (
	"context"
	"testing"
)

// Compile-time assertion mirrored in the test file so the
// interface contract is covered even if the production file
// drops its own assertion.
var _ Client = (*NullClient)(nil)

func TestNullClient_CheckAllowsWithBanner(t *testing.T) {
	t.Parallel()

	var c NullClient
	res, err := c.Check(context.Background(), Request{
		Principal: `Stoke::"test-session"`,
		Action:    "bash",
		Resource:  "echo hello",
		Context:   map[string]any{"trust_level": 3},
	})
	if err != nil {
		t.Fatalf("NullClient.Check returned error: %v", err)
	}
	if res.Decision != DecisionAllow {
		t.Fatalf("Decision = %v, want DecisionAllow", res.Decision)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("Errors = %v, want nil/empty", res.Errors)
	}
	if len(res.Reasons) != 1 {
		t.Fatalf("len(Reasons) = %d, want 1", len(res.Reasons))
	}
	const wantReason = "null-client: no policy configured"
	if res.Reasons[0] != wantReason {
		t.Fatalf("Reasons[0] = %q, want %q", res.Reasons[0], wantReason)
	}
}

func TestNullClient_CheckIgnoresNilRequestFields(t *testing.T) {
	t.Parallel()

	var c NullClient
	res, err := c.Check(context.Background(), Request{})
	if err != nil {
		t.Fatalf("NullClient.Check returned error on zero Request: %v", err)
	}
	if res.Decision != DecisionAllow {
		t.Fatalf("Decision = %v, want DecisionAllow", res.Decision)
	}
}

func TestNullClient_SatisfiesClientInterface(t *testing.T) {
	t.Parallel()

	// Assigning to a Client-typed variable proves the interface
	// is satisfied at both the value and pointer receiver level.
	var _ Client = NullClient{}
	var _ Client = (*NullClient)(nil)
}
