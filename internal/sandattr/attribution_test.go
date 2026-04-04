package sandattr

import (
	"testing"
)

func TestAttributeCodeBug(t *testing.T) {
	tests := []struct {
		stderr string
	}{
		{"./main.go:10: undefined: fooBar"},
		{"syntax error: unexpected token '}'"},
		{"cannot convert string to int"},
		{"nil pointer dereference"},
		{"build failed: compilation errors"},
	}
	for _, tc := range tests {
		attr := Attribute(tc.stderr)
		if attr.Cause != CauseCodeBug {
			t.Errorf("%q: expected code_bug, got %s", tc.stderr, attr.Cause)
		}
		if !attr.Retryable {
			t.Errorf("%q: code bugs should be retryable", tc.stderr)
		}
	}
}

func TestAttributeSandboxDeny(t *testing.T) {
	tests := []struct {
		stderr string
	}{
		{"error: sandbox denied the operation"},
		{"seatbelt denied(1) file-write"},
		{"bubblewrap error: permission denied"},
		{"seccomp blocked syscall 59"},
	}
	for _, tc := range tests {
		attr := Attribute(tc.stderr)
		if attr.Cause != CauseSandboxDeny {
			t.Errorf("%q: expected sandbox_deny, got %s", tc.stderr, attr.Cause)
		}
		if !attr.NeedsEscalation {
			t.Error("sandbox deny should need escalation")
		}
	}
}

func TestAttributeNetworkBlock(t *testing.T) {
	attr := Attribute("network unreachable sandbox policy denied")
	if attr.Cause != CauseNetworkBlock {
		t.Errorf("expected network_block, got %s", attr.Cause)
	}
}

func TestAttributeFileBlock(t *testing.T) {
	attr := Attribute("read-only file system: /etc/config")
	if attr.Cause != CauseFileBlock {
		t.Errorf("expected file_block, got %s", attr.Cause)
	}
}

func TestAttributeResourceLimit(t *testing.T) {
	tests := []string{
		"fatal error: out of memory",
		"no space left on device",
		"too many open files",
	}
	for _, stderr := range tests {
		attr := Attribute(stderr)
		if attr.Cause != CauseResourceLimit {
			t.Errorf("%q: expected resource_limit, got %s", stderr, attr.Cause)
		}
	}
}

func TestAttributeTimeout(t *testing.T) {
	attr := Attribute("context deadline exceeded after 5m0s")
	if attr.Cause != CauseTimeout {
		t.Errorf("expected timeout, got %s", attr.Cause)
	}
}

func TestAttributeEmpty(t *testing.T) {
	attr := Attribute("")
	if attr.Cause != CauseUnknown {
		t.Errorf("expected unknown for empty input, got %s", attr.Cause)
	}
}

func TestAttributeUnknown(t *testing.T) {
	attr := Attribute("something weird happened 12345")
	if attr.Cause != CauseUnknown {
		t.Errorf("expected unknown, got %s", attr.Cause)
	}
}

func TestSuggestRetryCodeBug(t *testing.T) {
	attr := Attribute("syntax error: unexpected }")
	rs := SuggestRetry(attr)
	if !rs.ShouldRetry {
		t.Error("code bugs should suggest retry")
	}
	if !rs.AddContext {
		t.Error("code bugs should add error context")
	}
}

func TestSuggestRetrySandbox(t *testing.T) {
	attr := Attribute("sandbox denied the operation")
	rs := SuggestRetry(attr)
	if !rs.ShouldRetry {
		t.Error("sandbox deny should suggest retry")
	}
	if !rs.AdjustSandbox {
		t.Error("should suggest sandbox adjustment")
	}
	if len(rs.SuggestedPerms) == 0 {
		t.Error("should suggest permissions")
	}
}

func TestSuggestRetryNetwork(t *testing.T) {
	attr := Attribute("network unreachable sandbox blocked")
	rs := SuggestRetry(attr)
	if !rs.SkipTool {
		t.Error("network block should suggest skipping tool")
	}
}

func TestSuggestRetryResource(t *testing.T) {
	attr := Attribute("out of memory killed signal 9")
	rs := SuggestRetry(attr)
	if !rs.ReduceScope {
		t.Error("resource limit should suggest reducing scope")
	}
}

func TestIsSandboxCaused(t *testing.T) {
	if !IsSandboxCaused("sandbox denied file-write") {
		t.Error("should detect sandbox cause")
	}
	if IsSandboxCaused("syntax error in main.go") {
		t.Error("syntax error is not sandbox-caused")
	}
}

func TestIsCodeBug(t *testing.T) {
	if !IsCodeBug("undefined: myVariable") {
		t.Error("should detect code bug")
	}
	if IsCodeBug("sandbox denied") {
		t.Error("sandbox deny is not a code bug")
	}
}

func TestHigherConfidenceWins(t *testing.T) {
	// "permission denied" matches both sandbox and OS permission patterns
	// but sandbox pattern with "sandbox" keyword should win
	attr := Attribute("sandbox denied: permission denied on /tmp/test")
	if attr.Cause != CauseSandboxDeny {
		t.Errorf("sandbox-specific pattern should win, got %s", attr.Cause)
	}
	if attr.Confidence < 0.85 {
		t.Errorf("should have high confidence, got %f", attr.Confidence)
	}
}

func TestEvidence(t *testing.T) {
	attr := Attribute("error: seatbelt denied(1) file-read-data /etc/secret")
	if len(attr.Evidence) == 0 {
		t.Error("should capture evidence")
	}
}
