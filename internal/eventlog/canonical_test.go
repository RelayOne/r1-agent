package eventlog

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCanonical_Deterministic(t *testing.T) {
	in := map[string]any{
		"z": 1,
		"a": "hello",
		"m": map[string]any{"k2": 2, "k1": 1},
	}
	b1, err := Marshal(in)
	if err != nil {
		t.Fatalf("marshal1: %v", err)
	}
	b2, err := Marshal(in)
	if err != nil {
		t.Fatalf("marshal2: %v", err)
	}
	if !bytes.Equal(b1, b2) {
		t.Fatalf("not deterministic: %q vs %q", b1, b2)
	}
}

func TestCanonical_SortsKeysRecursively(t *testing.T) {
	in := map[string]any{
		"z": 1,
		"a": map[string]any{"c": 3, "b": 2, "a": 1},
	}
	got, err := Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"a":{"a":1,"b":2,"c":3},"z":1}`
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCanonical_NoHTMLEscape(t *testing.T) {
	in := map[string]any{"s": "<script>&amp;</script>"}
	got, err := Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"s":"<script>&amp;</script>"}`
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCanonical_PreservesArrayOrder(t *testing.T) {
	in := []any{3, 1, 2}
	got, err := Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != `[3,1,2]` {
		t.Fatalf("got %q, want [3,1,2]", got)
	}
}

func TestCanonical_Nil(t *testing.T) {
	got, err := Marshal(nil)
	if err != nil {
		t.Fatalf("marshal nil: %v", err)
	}
	if string(got) != "null" {
		t.Fatalf("got %q, want null", got)
	}
}

func TestCanonical_Unicode(t *testing.T) {
	in := map[string]any{"greeting": "héllo 世界"}
	got, err := Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Round-trip via stdlib to confirm the bytes still parse as the same map.
	var back map[string]any
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if back["greeting"] != "héllo 世界" {
		t.Fatalf("round-trip mismatch: %v", back)
	}
}

func TestCanonical_RawMessageReparsed(t *testing.T) {
	// Two RawMessages with the same keys in different order must produce
	// the same canonical bytes.
	a := json.RawMessage(`{"b":2,"a":1}`)
	b := json.RawMessage(`{"a":1,"b":2}`)
	ab, err := Marshal(a)
	if err != nil {
		t.Fatalf("marshal a: %v", err)
	}
	bb, err := Marshal(b)
	if err != nil {
		t.Fatalf("marshal b: %v", err)
	}
	if !bytes.Equal(ab, bb) {
		t.Fatalf("RawMessage not re-parsed: %q vs %q", ab, bb)
	}
}
