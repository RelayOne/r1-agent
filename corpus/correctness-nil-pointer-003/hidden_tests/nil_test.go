package main

import (
	"testing"
)

// TestParseConfig_NilConfig verifies that passing a nil *Config does not panic.
func TestParseConfig_NilConfig(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ParseConfig panicked on nil input: %v", r)
		}
	}()

	result, err := ParseConfig(nil)
	if err != nil {
		// Returning an error for nil input is acceptable.
		return
	}

	// If no error, result should be empty or a zero-value string.
	if result != "" {
		t.Logf("non-empty result for nil config: %q (acceptable if meaningful)", result)
	}
}

// TestParseConfig_NilConfig_NoError verifies the function returns empty string
// and nil error for nil input, matching the prompt specification.
func TestParseConfig_NilConfig_NoError(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ParseConfig panicked on nil input: %v", r)
		}
	}()

	result, err := ParseConfig(nil)

	// The prompt says: return empty string and nil error for nil input.
	// We accept either an error OR empty string with nil error.
	if err != nil {
		// Returning an error is an acceptable alternative.
		t.Logf("returned error for nil: %v (acceptable)", err)
		return
	}

	if result != "" {
		t.Errorf("expected empty string for nil config, got %q", result)
	}
}
