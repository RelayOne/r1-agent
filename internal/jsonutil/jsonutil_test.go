package jsonutil

import (
	"testing"
)

func TestExtractFromMarkdown_Plain(t *testing.T) {
	var out map[string]string
	err := ExtractFromMarkdown(`{"key":"value"}`, &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["key"] != "value" {
		t.Errorf("got %q, want %q", out["key"], "value")
	}
}

func TestExtractFromMarkdown_CodeFence(t *testing.T) {
	raw := "```json\n{\"ship\": true}\n```"
	var out struct {
		Ship bool `json:"ship"`
	}
	if err := ExtractFromMarkdown(raw, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Ship {
		t.Error("expected ship=true")
	}
}

func TestExtractFromMarkdown_EmbeddedJSON(t *testing.T) {
	raw := "Here is the result:\n{\"count\": 42}\nEnd of output."
	var out struct {
		Count int `json:"count"`
	}
	if err := ExtractFromMarkdown(raw, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Count != 42 {
		t.Errorf("got %d, want 42", out.Count)
	}
}

func TestExtractFromMarkdown_Invalid(t *testing.T) {
	var out map[string]string
	err := ExtractFromMarkdown("no json here at all", &out)
	if err == nil {
		t.Error("expected error for non-JSON input")
	}
}

func TestExtractFromMarkdown_Empty(t *testing.T) {
	var out map[string]string
	err := ExtractFromMarkdown("", &out)
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestMarshalIndent(t *testing.T) {
	data, err := MarshalIndent(map[string]int{"a": 1}, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty data")
	}
}

func TestMarshalIndent_Error(t *testing.T) {
	// channels can't be marshaled
	_, err := MarshalIndent(make(chan int), "channel")
	if err == nil {
		t.Error("expected error for unmarshalable type")
	}
}

func TestSafeUnmarshal(t *testing.T) {
	var out struct {
		Name string `json:"name"`
	}
	err := SafeUnmarshal([]byte(`{"name":"stoke"}`), &out, "config")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Name != "stoke" {
		t.Errorf("got %q, want %q", out.Name, "stoke")
	}
}

func TestSafeUnmarshal_Invalid(t *testing.T) {
	var out struct{}
	err := SafeUnmarshal([]byte("not json"), &out, "config")
	if err == nil {
		t.Error("expected error")
	}
}

func TestMustMarshal_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for unmarshalable type")
		}
	}()
	MustMarshal(make(chan int))
}

func TestMustMarshal_Success(t *testing.T) {
	data := MustMarshal(map[string]int{"a": 1})
	if len(data) == 0 {
		t.Error("expected non-empty data")
	}
}
