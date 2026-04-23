package policy

import (
	"errors"
	"fmt"
	"testing"
)

// TestDecisionZeroValue asserts that the zero value of Decision is
// DecisionDeny so any mis-constructed Result fails closed.
func TestDecisionZeroValue(t *testing.T) {
	var d Decision
	if d != DecisionDeny {
		t.Fatalf("zero-value Decision = %v, want DecisionDeny", d)
	}
	var r Result
	if r.Decision != DecisionDeny {
		t.Fatalf("zero-value Result.Decision = %v, want DecisionDeny", r.Decision)
	}
}

// TestDecisionString covers DecisionAllow, DecisionDeny, and any
// unknown integer value (which must default to "deny").
func TestDecisionString(t *testing.T) {
	if got := DecisionAllow.String(); got != "allow" {
		t.Errorf("DecisionAllow.String() = %q, want %q", got, "allow")
	}
	if got := DecisionDeny.String(); got != "deny" {
		t.Errorf("DecisionDeny.String() = %q, want %q", got, "deny")
	}
	// Any other integer value must fall through to "deny" for
	// fail-closed logging semantics.
	for _, v := range []Decision{-1, 2, 42, 1 << 20} {
		if got := v.String(); got != "deny" {
			t.Errorf("Decision(%d).String() = %q, want %q", int(v), got, "deny")
		}
	}
}

// TestErrPolicyUnavailableIs verifies that errors.Is unwraps through
// fmt.Errorf %w wrapping to the sentinel ErrPolicyUnavailable.
func TestErrPolicyUnavailableIs(t *testing.T) {
	wrapped := fmt.Errorf("http get: %w", ErrPolicyUnavailable)
	if !errors.Is(wrapped, ErrPolicyUnavailable) {
		t.Fatalf("errors.Is(wrapped, ErrPolicyUnavailable) = false, want true")
	}
	// Double-wrap still resolves.
	doubleWrapped := fmt.Errorf("outer: %w", wrapped)
	if !errors.Is(doubleWrapped, ErrPolicyUnavailable) {
		t.Fatalf("errors.Is(doubleWrapped, ErrPolicyUnavailable) = false, want true")
	}
	// Unrelated error does not match.
	unrelated := errors.New("something else")
	if errors.Is(unrelated, ErrPolicyUnavailable) {
		t.Fatalf("errors.Is(unrelated, ErrPolicyUnavailable) = true, want false")
	}
}

// TestRequestResultZeroValues sanity-checks the zero-value shapes of
// the exported structs so downstream callers can rely on them.
func TestRequestResultZeroValues(t *testing.T) {
	var req Request
	if req.Principal != "" || req.Action != "" || req.Resource != "" || req.Context != nil {
		t.Fatalf("zero-value Request not empty: %+v", req)
	}
	var res Result
	if res.Decision != DecisionDeny || res.Reasons != nil || res.Errors != nil {
		t.Fatalf("zero-value Result not fail-closed: %+v", res)
	}
}
