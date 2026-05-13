package coderadar

import (
	"strings"
	"testing"
)

// TestRedactorDropsDisallowedProps — anything not in AllowedProps is
// dropped silently. assert.Validate result props.
func TestRedactorDropsDisallowedProps(t *testing.T) {
	r := NewRedactor()
	in := Event{
		Name: EventToolCallCompleted,
		Props: map[string]any{
			"tool_name":          "Bash",
			"exit_code":          0,
			"raw_prompt":         "ignore previous instructions",
			"api_key":            "sk-leak",
			"AUTH":               "Bearer abc",
		},
	}
	out := r.Redact(in)
	if _, ok := out.Props["raw_prompt"]; ok {
		t.Errorf("raw_prompt leaked through redactor")
	}
	if _, ok := out.Props["api_key"]; ok {
		t.Errorf("api_key leaked through redactor")
	}
	if _, ok := out.Props["AUTH"]; ok {
		t.Errorf("Authorization-style key leaked through redactor")
	}
	if out.Props["tool_name"] != "Bash" {
		t.Errorf("tool_name=%v want Bash", out.Props["tool_name"])
	}
}

// TestRedactorHashesFilePath — plain `file_path` never leaves, hash does.
func TestRedactorHashesFilePath(t *testing.T) {
	r := NewRedactor()
	in := Event{
		Name: EventToolCallCompleted,
		Props: map[string]any{
			"tool_name": "Read",
			"exit_code": 0,
			"file_path": "/etc/passwd",
		},
	}
	out := r.Redact(in)
	if _, ok := out.Props["file_path"]; ok {
		t.Errorf("plain file_path leaked through redactor")
	}
	hashed, ok := out.Props["file_path_sha256"].(string)
	if !ok || hashed == "" {
		t.Fatalf("expected file_path_sha256 string prop, got %v", out.Props["file_path_sha256"])
	}
	if len(hashed) != 64 {
		t.Errorf("expected 64-hex sha256, got len=%d", len(hashed))
	}
	if strings.Contains(hashed, "passwd") {
		t.Errorf("hash unexpectedly contains plain path substring")
	}
}

// TestRedactorTruncatesErrorMessage — long error_message capped, sha256
// preserved. assert.Verify both fields.
func TestRedactorTruncatesErrorMessage(t *testing.T) {
	r := NewRedactor()
	long := strings.Repeat("X", ErrorMessageMaxChars*2)
	in := Event{
		Name: EventProviderCallErrored,
		Props: map[string]any{
			"provider":      "anthropic",
			"model":         "claude-opus-4-7",
			"error_message": long,
		},
	}
	out := r.Redact(in)
	got, _ := out.Props["error_message"].(string)
	if len(got) > ErrorMessageMaxChars {
		t.Errorf("error_message length=%d, want <= %d", len(got), ErrorMessageMaxChars)
	}
	sum, _ := out.Props["error_message_sha256"].(string)
	if len(sum) != 64 {
		t.Errorf("error_message_sha256 len=%d want 64", len(sum))
	}
}

// TestRedactorDropsNonCanonical — unknown event names emit no props.
func TestRedactorDropsNonCanonical(t *testing.T) {
	r := NewRedactor()
	out := r.Redact(Event{
		Name:  "not.a.real.event",
		Props: map[string]any{"x": 1, "y": "z"},
	})
	if len(out.Props) != 0 {
		t.Errorf("non-canonical event retained props: %v", out.Props)
	}
}

// TestHashFilePathStable — same input → same output, empty → empty.
func TestHashFilePathStable(t *testing.T) {
	if HashFilePath("") != "" {
		t.Errorf("empty input should return empty hash")
	}
	a := HashFilePath("/a/b/c")
	b := HashFilePath("/a/b/c")
	if a != b {
		t.Errorf("hash not stable: %q vs %q", a, b)
	}
	if len(a) != 64 {
		t.Errorf("hash len=%d want 64", len(a))
	}
}
