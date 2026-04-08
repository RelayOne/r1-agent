package errtaxonomy

import (
	"testing"
)

func TestClassifyRateLimit(t *testing.T) {
	tests := []string{
		"rate limit exceeded",
		"HTTP 429 Too Many Requests",
		"request throttled, please wait",
	}
	for _, msg := range tests {
		if Classify(msg) != ClassRateLimit {
			t.Errorf("expected rate_limit for %q, got %s", msg, Classify(msg))
		}
	}
}

func TestClassifyAuth(t *testing.T) {
	tests := []string{
		"HTTP 401 Unauthorized",
		"invalid API token",
		"authentication failed",
	}
	for _, msg := range tests {
		if Classify(msg) != ClassAuth {
			t.Errorf("expected auth for %q, got %s", msg, Classify(msg))
		}
	}
}

func TestClassifyTransient(t *testing.T) {
	tests := []string{
		"connection timeout after 30s",
		"connection refused",
		"ECONNRESET",
		"HTTP 503 Service Unavailable",
	}
	for _, msg := range tests {
		if Classify(msg) != ClassTransient {
			t.Errorf("expected transient for %q, got %s", msg, Classify(msg))
		}
	}
}

func TestClassifySyntax(t *testing.T) {
	tests := []string{
		"syntax error: unexpected token",
		"parse error: expected ';'",
		"unterminated string literal",
	}
	for _, msg := range tests {
		if Classify(msg) != ClassSyntax {
			t.Errorf("expected syntax for %q, got %s", msg, Classify(msg))
		}
	}
}

func TestClassifyRuntime(t *testing.T) {
	tests := []string{
		"panic: runtime error: nil pointer dereference",
		"index out of range [5] with length 3",
		"stack overflow",
	}
	for _, msg := range tests {
		if Classify(msg) != ClassRuntime {
			t.Errorf("expected runtime for %q, got %s", msg, Classify(msg))
		}
	}
}

func TestClassifyResource(t *testing.T) {
	tests := []string{
		"out of memory",
		"disk full",
		"context too large: exceeds token limit",
		"no space left on device",
	}
	for _, msg := range tests {
		if Classify(msg) != ClassResource {
			t.Errorf("expected resource for %q, got %s", msg, Classify(msg))
		}
	}
}

func TestClassifyLogic(t *testing.T) {
	tests := []string{
		"--- FAIL: TestFoo",
		"assertion failed: expected 5, got 3",
		"test failed: mismatch",
	}
	for _, msg := range tests {
		if Classify(msg) != ClassLogic {
			t.Errorf("expected logic for %q, got %s", msg, Classify(msg))
		}
	}
}

func TestClassifyPermission(t *testing.T) {
	tests := []string{
		"permission denied",
		"EACCES: access denied",
		"sandbox restriction",
	}
	for _, msg := range tests {
		if Classify(msg) != ClassPermission {
			t.Errorf("expected permission for %q, got %s", msg, Classify(msg))
		}
	}
}

func TestClassifyNotFound(t *testing.T) {
	tests := []string{
		"file not found: config.yaml",
		"ENOENT: no such file or directory",
		"module 'foo' not found",
	}
	for _, msg := range tests {
		if Classify(msg) != ClassNotFound {
			t.Errorf("expected not_found for %q, got %s", msg, Classify(msg))
		}
	}
}

func TestClassifyConflict(t *testing.T) {
	tests := []string{
		"merge conflict in main.go",
		"lock held by another process",
	}
	for _, msg := range tests {
		if Classify(msg) != ClassConflict {
			t.Errorf("expected conflict for %q, got %s", msg, Classify(msg))
		}
	}
}

func TestClassifyUnknown(t *testing.T) {
	if Classify("something weird happened") != ClassUnknown {
		t.Error("should classify as unknown")
	}
}

func TestStrategy(t *testing.T) {
	s := Strategy("connection timeout")
	if !s.ShouldRetry {
		t.Error("transient errors should retry")
	}
	if s.BackoffMs < 1000 {
		t.Error("should have backoff")
	}
}

func TestShouldRetry(t *testing.T) {
	if !ShouldRetry("connection timeout") {
		t.Error("transient should retry")
	}
	if ShouldRetry("permission denied") {
		t.Error("permission should not retry")
	}
}

func TestNeedsHuman(t *testing.T) {
	if !NeedsHuman("401 unauthorized") {
		t.Error("auth failure should need human")
	}
	if NeedsHuman("syntax error") {
		t.Error("syntax error should not need human")
	}
}

func TestClassifyMultiple(t *testing.T) {
	msgs := []string{
		"connection timeout",
		"permission denied",
		"syntax error",
	}
	worst := ClassifyMultiple(msgs)
	if worst != ClassPermission {
		t.Errorf("expected permission (most severe), got %s", worst)
	}
}

func TestStrategyRequiresFix(t *testing.T) {
	s := Strategy("syntax error: unexpected token")
	if !s.RequiresFix {
		t.Error("syntax errors should require fix")
	}
}

func TestStrategyReduceScope(t *testing.T) {
	s := Strategy("out of memory")
	if !s.ReduceScope {
		t.Error("resource errors should reduce scope")
	}
}
