package llm

import (
	"context"
	"testing"
)

func TestEscalator_RespectsAllowedFlag(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		allowed bool
		reason  string
		want    string
	}{
		{name: "allowed-with-reason", allowed: true, reason: "rule-failure", want: "claude-sonnet-4-6"},
		{name: "allowed-empty-reason", allowed: true, reason: "", want: ""},
		{name: "denied-with-reason", allowed: false, reason: "rule-failure", want: ""},
		{name: "denied-empty-reason", allowed: false, reason: "", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEscalator(tc.allowed)
			got := e(ctx, tc.reason)
			if got != tc.want {
				t.Fatalf("escalator returned %q, want %q", got, tc.want)
			}
		})
	}
}
