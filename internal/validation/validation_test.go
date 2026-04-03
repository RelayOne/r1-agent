package validation

import (
	"testing"
	"time"
)

func TestNonEmpty(t *testing.T) {
	tests := []struct {
		input string
		fail  bool
	}{
		{"hello", false},
		{"  hello  ", false},
		{"", true},
		{"   ", true},
		{"\t\n", true},
	}
	for _, tt := range tests {
		err := NonEmpty(tt.input, "field")
		if (err != nil) != tt.fail {
			t.Errorf("NonEmpty(%q) error=%v, wantFail=%v", tt.input, err, tt.fail)
		}
	}
}

func TestPositive(t *testing.T) {
	if err := Positive(1, "count"); err != nil {
		t.Errorf("1 should be positive: %v", err)
	}
	if err := Positive(0, "count"); err == nil {
		t.Error("0 should fail")
	}
	if err := Positive(-1, "count"); err == nil {
		t.Error("-1 should fail")
	}
}

func TestInRange(t *testing.T) {
	if err := InRange(5, 1, 10, "n"); err != nil {
		t.Errorf("5 in [1,10]: %v", err)
	}
	if err := InRange(1, 1, 10, "n"); err != nil {
		t.Errorf("1 in [1,10]: %v", err)
	}
	if err := InRange(10, 1, 10, "n"); err != nil {
		t.Errorf("10 in [1,10]: %v", err)
	}
	if err := InRange(0, 1, 10, "n"); err == nil {
		t.Error("0 not in [1,10]")
	}
	if err := InRange(11, 1, 10, "n"); err == nil {
		t.Error("11 not in [1,10]")
	}
}

func TestPositiveDuration(t *testing.T) {
	if err := PositiveDuration(time.Second, "timeout"); err != nil {
		t.Errorf("1s should be valid: %v", err)
	}
	if err := PositiveDuration(0, "timeout"); err == nil {
		t.Error("0 should fail")
	}
	if err := PositiveDuration(-time.Second, "timeout"); err == nil {
		t.Error("negative should fail")
	}
}

func TestOneOf(t *testing.T) {
	allowed := []string{"mode1", "mode2"}
	if err := OneOf("mode1", allowed, "mode"); err != nil {
		t.Errorf("mode1 should be allowed: %v", err)
	}
	if err := OneOf("mode3", allowed, "mode"); err == nil {
		t.Error("mode3 should not be allowed")
	}
}

func TestValidURL(t *testing.T) {
	if err := ValidURL("https://example.com", "endpoint"); err != nil {
		t.Errorf("valid URL: %v", err)
	}
	if err := ValidURL("not-a-url", "endpoint"); err == nil {
		t.Error("invalid URL should fail")
	}
	if err := ValidURL("", "endpoint"); err == nil {
		t.Error("empty URL should fail")
	}
	if err := ValidURL("/just/a/path", "endpoint"); err == nil {
		t.Error("path without scheme/host should fail")
	}
}

func TestSafeID(t *testing.T) {
	if err := SafeID("task-123.v2", "id"); err != nil {
		t.Errorf("valid ID: %v", err)
	}
	if err := SafeID("task_name", "id"); err != nil {
		t.Errorf("underscore ID: %v", err)
	}
	if err := SafeID("", "id"); err == nil {
		t.Error("empty should fail")
	}
	if err := SafeID("../etc/passwd", "id"); err == nil {
		t.Error("path traversal chars should fail")
	}
	if err := SafeID("task with spaces", "id"); err == nil {
		t.Error("spaces should fail")
	}
}

func TestAll(t *testing.T) {
	// All pass
	if err := All(NonEmpty("ok", "a"), Positive(1, "b")); err != nil {
		t.Errorf("all valid: %v", err)
	}
	// First fails
	err := All(NonEmpty("", "a"), Positive(1, "b"))
	if err == nil {
		t.Error("expected first check to fail")
	}
	// Second fails
	err = All(NonEmpty("ok", "a"), Positive(0, "b"))
	if err == nil {
		t.Error("expected second check to fail")
	}
}

func TestPositiveFloat(t *testing.T) {
	if err := PositiveFloat(1.5, "cost"); err != nil {
		t.Errorf("1.5 should be valid: %v", err)
	}
	if err := PositiveFloat(0, "cost"); err == nil {
		t.Error("0 should fail")
	}
	if err := PositiveFloat(-1.0, "cost"); err == nil {
		t.Error("-1 should fail")
	}
}
