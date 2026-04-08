package jsonutil

import (
	"strings"
	"testing"
)

func TestExtractJSONObject_Plain(t *testing.T) {
	raw := `{"id": "x", "name": "y"}`
	blob, err := ExtractJSONObject(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(blob), `"id"`) {
		t.Errorf("blob missing id: %s", blob)
	}
}

func TestExtractJSONObject_MarkdownFence(t *testing.T) {
	raw := "```json\n{\"id\": \"x\"}\n```"
	blob, err := ExtractJSONObject(raw)
	if err != nil {
		t.Fatalf("fence extraction failed: %v", err)
	}
	if string(blob) != `{"id": "x"}` {
		t.Errorf("unexpected blob: %q", blob)
	}
}

func TestExtractJSONObject_BareFence(t *testing.T) {
	raw := "```\n{\"id\": \"x\"}\n```"
	_, err := ExtractJSONObject(raw)
	if err != nil {
		t.Fatalf("bare fence extraction failed: %v", err)
	}
}

func TestExtractJSONObject_PreamblePostamble(t *testing.T) {
	raw := `Sure! Here's the JSON you asked for:

{"id": "x", "value": 42}

Let me know if you want changes.`
	blob, err := ExtractJSONObject(raw)
	if err != nil {
		t.Fatalf("preamble strip failed: %v", err)
	}
	if !strings.Contains(string(blob), `"value": 42`) {
		t.Errorf("blob missing value: %s", blob)
	}
}

func TestExtractJSONObject_TrailingComma(t *testing.T) {
	raw := `{"a": 1, "b": 2,}`
	blob, err := ExtractJSONObject(raw)
	if err != nil {
		t.Fatalf("trailing comma not stripped: %v", err)
	}
	if strings.Contains(string(blob), "2,}") {
		t.Errorf("trailing comma still present: %s", blob)
	}
}

func TestExtractJSONObject_TrailingCommaInArray(t *testing.T) {
	raw := `{"items": [1, 2, 3,]}`
	_, err := ExtractJSONObject(raw)
	if err != nil {
		t.Fatalf("array trailing comma: %v", err)
	}
}

func TestExtractJSONObject_NestedBraces(t *testing.T) {
	raw := `{"outer": {"inner": {"deepest": 1}}, "other": "x"}`
	_, err := ExtractJSONObject(raw)
	if err != nil {
		t.Fatalf("nested braces: %v", err)
	}
}

func TestExtractJSONObject_BracesInString(t *testing.T) {
	raw := `{"template": "hello {world}"}`
	blob, err := ExtractJSONObject(raw)
	if err != nil {
		t.Fatalf("braces in string: %v", err)
	}
	if !strings.Contains(string(blob), "hello {world}") {
		t.Errorf("blob should preserve braces in string: %s", blob)
	}
}

func TestExtractJSONObject_EscapedQuotes(t *testing.T) {
	raw := `{"msg": "he said \"hi\""}`
	_, err := ExtractJSONObject(raw)
	if err != nil {
		t.Fatalf("escaped quotes: %v", err)
	}
}

func TestExtractJSONObject_BOM(t *testing.T) {
	raw := "\ufeff{\"id\": \"x\"}"
	_, err := ExtractJSONObject(raw)
	if err != nil {
		t.Fatalf("BOM not stripped: %v", err)
	}
}

func TestExtractJSONObject_NoObject_Errors(t *testing.T) {
	raw := `no json here at all`
	_, err := ExtractJSONObject(raw)
	if err == nil {
		t.Error("expected error for non-JSON input")
	}
}

func TestExtractJSONObject_MalformedJSON_Errors(t *testing.T) {
	raw := `{"id": "x"` // missing closing brace
	_, err := ExtractJSONObject(raw)
	if err == nil {
		t.Error("expected error for unbalanced JSON")
	}
}

func TestExtractJSONObject_InvalidJSONInsideBraces(t *testing.T) {
	// Balanced braces but not valid JSON (missing quotes)
	raw := `{id: x}`
	_, err := ExtractJSONObject(raw)
	if err == nil {
		t.Error("expected error for malformed JSON keys")
	}
}

func TestExtractJSONInto_HappyPath(t *testing.T) {
	raw := "```json\n{\"id\": \"abc\", \"n\": 42}\n```"
	var out struct {
		ID string `json:"id"`
		N  int    `json:"n"`
	}
	blob, err := ExtractJSONInto(raw, &out)
	if err != nil {
		t.Fatalf("ExtractJSONInto: %v", err)
	}
	if out.ID != "abc" || out.N != 42 {
		t.Errorf("unmarshal wrong: %+v", out)
	}
	if len(blob) == 0 {
		t.Error("blob should not be empty")
	}
}

func TestExtractJSONInto_TypeMismatch(t *testing.T) {
	raw := `{"n": "not a number"}`
	var out struct {
		N int `json:"n"`
	}
	_, err := ExtractJSONInto(raw, &out)
	if err == nil {
		t.Error("expected type mismatch error")
	}
}

func TestExtractError_ErrorMessage(t *testing.T) {
	e := &ExtractError{
		Raw:    "very long raw output that definitely exceeds two hundred chars " + strings.Repeat("x", 300),
		Reason: "test",
	}
	msg := e.Error()
	if !strings.Contains(msg, "test") {
		t.Error("error message should include reason")
	}
	if !strings.Contains(msg, "...") {
		t.Error("long raw output should be truncated")
	}
}

func TestRemoveTrailingCommas_IgnoresStringCommas(t *testing.T) {
	// Commas inside quoted strings should NOT be touched
	s := `{"msg": "hello, world", "count": 1}`
	out := removeTrailingCommas(s)
	if out != s {
		t.Errorf("string commas modified: %s", out)
	}
}

func TestFindBalancedObject_FirstObject(t *testing.T) {
	s := `garbage {"first": 1} more garbage {"second": 2} end`
	start, end := findBalancedObject(s)
	if start < 0 {
		t.Fatal("no object found")
	}
	first := s[start : end+1]
	if first != `{"first": 1}` {
		t.Errorf("expected first object, got %q", first)
	}
}

func TestFindBalancedObject_NoObject(t *testing.T) {
	s := `just some text`
	start, _ := findBalancedObject(s)
	if start >= 0 {
		t.Error("should return -1 for no object")
	}
}
