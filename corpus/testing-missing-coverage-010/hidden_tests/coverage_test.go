package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestAgentWroteTests verifies the agent created a test file with actual test functions.
func TestAgentWroteTests(t *testing.T) {
	// Look for test files written by the agent (not this file).
	files := []string{"util_test.go"}
	found := false
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		src := string(data)
		// Check for at least one Test function.
		testFuncRe := regexp.MustCompile(`func\s+Test\w+\(`)
		if testFuncRe.MatchString(src) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("agent must write test functions in util_test.go")
	}
}

// TestAgentCoversValidSeconds verifies ParseDuration works for seconds.
func TestAgentCoversValidSeconds(t *testing.T) {
	d, err := ParseDuration("30s")
	if err != nil {
		t.Fatalf("ParseDuration(\"30s\") returned error: %v", err)
	}
	if d != 30*time.Second {
		t.Fatalf("expected 30s, got %v", d)
	}
}

// TestAgentCoversValidMinutes verifies ParseDuration works for minutes.
func TestAgentCoversValidMinutes(t *testing.T) {
	d, err := ParseDuration("5m")
	if err != nil {
		t.Fatalf("ParseDuration(\"5m\") returned error: %v", err)
	}
	if d != 5*time.Minute {
		t.Fatalf("expected 5m, got %v", d)
	}
}

// TestAgentCoversValidHours verifies ParseDuration works for hours.
func TestAgentCoversValidHours(t *testing.T) {
	d, err := ParseDuration("2h")
	if err != nil {
		t.Fatalf("ParseDuration(\"2h\") returned error: %v", err)
	}
	if d != 2*time.Hour {
		t.Fatalf("expected 2h, got %v", d)
	}
}

// TestAgentCoversEmptyString verifies empty input returns an error.
func TestAgentCoversEmptyString(t *testing.T) {
	_, err := ParseDuration("")
	if err == nil {
		t.Fatal("ParseDuration(\"\") should return an error")
	}
}

// TestAgentCoversInvalidSuffix verifies unsupported suffix returns error.
func TestAgentCoversInvalidSuffix(t *testing.T) {
	_, err := ParseDuration("10x")
	if err == nil {
		t.Fatal("ParseDuration(\"10x\") should return an error for invalid suffix")
	}
}

// TestAgentCoversNonNumeric verifies non-numeric input returns error.
func TestAgentCoversNonNumeric(t *testing.T) {
	_, err := ParseDuration("abcs")
	if err == nil {
		t.Fatal("ParseDuration(\"abcs\") should return an error for non-numeric value")
	}
}

// TestAgentCoversNegative verifies negative duration returns error.
func TestAgentCoversNegative(t *testing.T) {
	_, err := ParseDuration("-5s")
	if err == nil {
		t.Fatal("ParseDuration(\"-5s\") should return an error for negative value")
	}
}

// TestAgentCoversZero verifies zero duration is valid.
func TestAgentCoversZero(t *testing.T) {
	d, err := ParseDuration("0s")
	if err != nil {
		t.Fatalf("ParseDuration(\"0s\") returned error: %v", err)
	}
	if d != 0 {
		t.Fatalf("expected 0, got %v", d)
	}
}

// TestAgentTestsHaveAssertions ensures the agent's tests actually assert something
// (not just empty test functions).
func TestAgentTestsHaveAssertions(t *testing.T) {
	data, err := os.ReadFile("util_test.go")
	if err != nil {
		t.Fatal("cannot read util_test.go:", err)
	}
	src := string(data)

	// Must contain at least one assertion or error check.
	hasAssertion := strings.Contains(src, "t.Fatal") ||
		strings.Contains(src, "t.Fatalf") ||
		strings.Contains(src, "t.Error") ||
		strings.Contains(src, "t.Errorf") ||
		strings.Contains(src, "t.Fail")

	if !hasAssertion {
		t.Fatal("agent's tests must contain assertions (t.Fatal, t.Error, etc.)")
	}
}

// TestAgentTestsCoverMultipleSuffixes ensures the agent tested more than one suffix type.
func TestAgentTestsCoverMultipleSuffixes(t *testing.T) {
	data, err := os.ReadFile("util_test.go")
	if err != nil {
		t.Fatal("cannot read util_test.go:", err)
	}
	src := string(data)

	suffixCount := 0
	for _, suffix := range []string{`"s"`, `s"`, `"m"`, `m"`, `"h"`, `h"`} {
		if strings.Contains(src, suffix) {
			suffixCount++
			break
		}
	}
	// Check for actual duration strings with different suffixes.
	hasS := strings.Contains(src, `s"`) || strings.Contains(src, `s'`)
	hasM := strings.Contains(src, `m"`) || strings.Contains(src, `m'`)
	hasH := strings.Contains(src, `h"`) || strings.Contains(src, `h'`)

	covered := 0
	if hasS {
		covered++
	}
	if hasM {
		covered++
	}
	if hasH {
		covered++
	}

	if covered < 2 {
		t.Fatalf("agent's tests should cover at least 2 different suffixes (s, m, h), found evidence of %d", covered)
	}
}
