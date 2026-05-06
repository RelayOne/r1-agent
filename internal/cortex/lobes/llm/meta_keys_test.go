package llm

import (
	"encoding/json"
	"testing"
)

// TestMetaKeys_RoundTripJSON exercises a JSON round-trip of all four
// Note.Meta convention keys with representative value types: a plain
// string for action_kind, a nested object for action_payload, a number
// for expires_after_round (Go decodes JSON numbers into any as float64),
// and a string array for refs.
//
// Spec: specs/cortex-concerns.md item 4.
func TestMetaKeys_RoundTripJSON(t *testing.T) {
	meta := map[string]any{
		MetaActionKind:        "user-confirm",
		MetaActionPayload:     map[string]any{"adds": []string{"x"}},
		MetaExpiresAfterRound: float64(7),
		MetaRefs:              []any{"note-1", "evt-2"},
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got[MetaActionKind] != "user-confirm" {
		t.Fatalf("action_kind round-trip failed: %v", got[MetaActionKind])
	}
	if got[MetaExpiresAfterRound].(float64) != 7 {
		t.Fatalf("expires_after_round round-trip failed: %v", got[MetaExpiresAfterRound])
	}
	if payload, ok := got[MetaActionPayload].(map[string]any); !ok || payload == nil {
		t.Fatalf("action_payload round-trip lost object shape: %#v", got[MetaActionPayload])
	}
	if refs, ok := got[MetaRefs].([]any); !ok || len(refs) != 2 {
		t.Fatalf("refs round-trip lost array shape: %#v", got[MetaRefs])
	}
}
