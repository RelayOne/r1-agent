package tui

import (
	"encoding/json"
	"strings"
	"testing"
)

const fixture = `{
  "name": "demo",
  "counter": 7,
  "lanes": [
    {"id": "alpha", "status": "running"},
    {"id": "beta",  "status": "done"},
    {"id": "gamma", "status": "errored"}
  ],
  "meta": {"size": {"w": 120, "h": 40}}
}`

func TestEvalJSONPath_RootDollar(t *testing.T) {
	got, err := EvalJSONPath(json.RawMessage(fixture), "$")
	if err != nil {
		t.Fatalf("EvalJSONPath($): %v", err)
	}
	if string(got) != fixture {
		t.Error("$ should return the whole document unchanged")
	}
}

func TestEvalJSONPath_TopLevelField(t *testing.T) {
	got, err := EvalJSONPath(json.RawMessage(fixture), "$.counter")
	if err != nil {
		t.Fatalf("$.counter: %v", err)
	}
	if string(got) != "7" {
		t.Errorf("got %q, want 7", got)
	}
}

func TestEvalJSONPath_NestedFields(t *testing.T) {
	got, err := EvalJSONPath(json.RawMessage(fixture), "$.meta.size.w")
	if err != nil {
		t.Fatalf("$.meta.size.w: %v", err)
	}
	if string(got) != "120" {
		t.Errorf("got %q, want 120", got)
	}
}

func TestEvalJSONPath_ArrayIndex(t *testing.T) {
	got, err := EvalJSONPath(json.RawMessage(fixture), "$.lanes[1]")
	if err != nil {
		t.Fatalf("$.lanes[1]: %v", err)
	}
	var lane map[string]string
	if err := json.Unmarshal(got, &lane); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if lane["id"] != "beta" || lane["status"] != "done" {
		t.Errorf("lanes[1] = %v, want beta/done", lane)
	}
}

func TestEvalJSONPath_ArrayIndexThenField(t *testing.T) {
	got, err := EvalJSONPath(json.RawMessage(fixture), "$.lanes[2].status")
	if err != nil {
		t.Fatalf("$.lanes[2].status: %v", err)
	}
	// Strings come back JSON-encoded with quotes.
	if string(got) != `"errored"` {
		t.Errorf("got %q, want \"errored\"", got)
	}
}

func TestEvalJSONPath_WildcardArray(t *testing.T) {
	got, err := EvalJSONPath(json.RawMessage(fixture), "$.lanes[*]")
	if err != nil {
		t.Fatalf("$.lanes[*]: %v", err)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(got, &arr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(arr) != 3 {
		t.Errorf("len(lanes[*]) = %d, want 3", len(arr))
	}
}

func TestEvalJSONPath_FieldNotFound(t *testing.T) {
	_, err := EvalJSONPath(json.RawMessage(fixture), "$.nonexistent")
	if err == nil {
		t.Error("missing field should error")
	}
	if !strings.Contains(err.Error(), "field not found") {
		t.Errorf("error message should mention 'field not found'; got %v", err)
	}
}

func TestEvalJSONPath_IndexOutOfRange(t *testing.T) {
	_, err := EvalJSONPath(json.RawMessage(fixture), "$.lanes[99]")
	if err == nil {
		t.Error("OOB index should error")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("error should mention out of range; got %v", err)
	}
}

func TestEvalJSONPath_FilterExpressionRejected(t *testing.T) {
	_, err := EvalJSONPath(json.RawMessage(fixture), `$.lanes[?(@.status=="run")]`)
	if err == nil {
		t.Error("filter syntax should error with helpful message")
	}
	if !strings.Contains(err.Error(), "filter") {
		t.Errorf("error should mention filter; got %v", err)
	}
}

func TestEvalJSONPath_LeadingDollarRequired(t *testing.T) {
	_, err := EvalJSONPath(json.RawMessage(fixture), "counter")
	if err == nil {
		t.Error("path without leading $ should error")
	}
}

func TestEvalJSONPath_InvalidBracketExpression(t *testing.T) {
	_, err := EvalJSONPath(json.RawMessage(fixture), "$.lanes[abc]")
	if err == nil {
		t.Error("non-integer non-* bracket expr should error")
	}
}

func TestEvalJSONPath_NotAnObjectError(t *testing.T) {
	// Try to walk into a scalar field.
	_, err := EvalJSONPath(json.RawMessage(fixture), "$.counter.foo")
	if err == nil {
		t.Error("walking into a scalar should error")
	}
}

func TestEvalJSONPath_NotAnArrayError(t *testing.T) {
	_, err := EvalJSONPath(json.RawMessage(fixture), "$.counter[0]")
	if err == nil {
		t.Error("indexing into a scalar should error")
	}
}
