package dispatcher

import (
	"strings"
	"testing"
)

func TestInterpolate_BoundVariable(t *testing.T) {
	got, err := Interpolate(`session ${SESSION_ID} active`, Bindings{
		"SESSION_ID": "s-42",
	})
	if err != nil {
		t.Fatalf("Interpolate: %v", err)
	}
	if got != "session s-42 active" {
		t.Errorf("got %q", got)
	}
}

func TestInterpolate_MultipleVars(t *testing.T) {
	got, err := Interpolate(
		`mission ${MISSION_ID} on session ${SESSION_ID}`,
		Bindings{"MISSION_ID": "m-1", "SESSION_ID": "s-1"})
	if err != nil {
		t.Fatalf("Interpolate: %v", err)
	}
	if got != "mission m-1 on session s-1" {
		t.Errorf("got %q", got)
	}
}

func TestInterpolate_UnboundErrors(t *testing.T) {
	_, err := Interpolate(`session ${SESSION_ID}`, Bindings{})
	if err == nil {
		t.Fatal("unbound var should error")
	}
	if !strings.Contains(err.Error(), "SESSION_ID") {
		t.Errorf("error should mention the var name; got %v", err)
	}
}

func TestInterpolate_FallbackUsedWhenUnbound(t *testing.T) {
	got, err := Interpolate(`session ${SESSION_ID:-default}`, Bindings{})
	if err != nil {
		t.Fatalf("fallback should suppress error; got %v", err)
	}
	if got != "session default" {
		t.Errorf("got %q", got)
	}
}

func TestInterpolate_FallbackIgnoredWhenBound(t *testing.T) {
	got, err := Interpolate(`session ${SESSION_ID:-default}`,
		Bindings{"SESSION_ID": "s-99"})
	if err != nil {
		t.Fatalf("Interpolate: %v", err)
	}
	if got != "session s-99" {
		t.Errorf("got %q", got)
	}
}

func TestInterpolate_EmptyFallbackIsRespected(t *testing.T) {
	got, err := Interpolate(`x=${MISSING:-}`, Bindings{})
	if err != nil {
		t.Fatalf("empty fallback should suppress error; got %v", err)
	}
	if got != "x=" {
		t.Errorf("got %q, want \"x=\"", got)
	}
}

func TestInterpolate_NoVarReturnsUnchanged(t *testing.T) {
	got, err := Interpolate(`a plain string`, Bindings{})
	if err != nil {
		t.Fatalf("no var should not error; got %v", err)
	}
	if got != "a plain string" {
		t.Errorf("got %q", got)
	}
}

func TestInterpolate_NoMultiPassRecursion(t *testing.T) {
	// Substituting ${NAME} -> "${OTHER}" must NOT recurse.
	got, err := Interpolate(`a=${NAME}`, Bindings{"NAME": "${OTHER}", "OTHER": "should-not-appear"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "a=${OTHER}" {
		t.Errorf("got %q, want literal ${OTHER}", got)
	}
}

func TestInterpolateAll_AggregatesFirstError(t *testing.T) {
	out, err := InterpolateAll(
		[]string{"a=${A}", "b=${B}", "c=${C}"},
		Bindings{"A": "1"},
	)
	if err == nil {
		t.Fatal("missing B and C should error")
	}
	// First error is for B.
	if !strings.Contains(err.Error(), "B") {
		t.Errorf("first error should be for B; got %v", err)
	}
	// All entries are still processed.
	if len(out) != 3 {
		t.Errorf("output length = %d, want 3", len(out))
	}
	if out[0] != "a=1" {
		t.Errorf("first entry should still substitute; got %q", out[0])
	}
}

func TestInterpolate_VariableNamesMustBeAllCaps(t *testing.T) {
	// lower-case variables are not matched by the regex; they pass
	// through unchanged.
	got, err := Interpolate(`x=${foo}`, Bindings{})
	if err != nil {
		t.Fatalf("lower-case var should pass through, not error; got %v", err)
	}
	if got != "x=${foo}" {
		t.Errorf("got %q", got)
	}
}
