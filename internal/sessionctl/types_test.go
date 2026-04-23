package sessionctl

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	orig := Request{
		Verb:      VerbBudgetAdd,
		RequestID: "req-123",
		Payload:   json.RawMessage(`{"amount_usd":5}`),
		Token:     "tok-abc",
		KeepAlive: true,
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Request
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Verb != orig.Verb {
		t.Errorf("Verb: got %q want %q", got.Verb, orig.Verb)
	}
	if got.RequestID != orig.RequestID {
		t.Errorf("RequestID: got %q want %q", got.RequestID, orig.RequestID)
	}
	if got.Token != orig.Token {
		t.Errorf("Token: got %q want %q", got.Token, orig.Token)
	}
	if got.KeepAlive != orig.KeepAlive {
		t.Errorf("KeepAlive: got %v want %v", got.KeepAlive, orig.KeepAlive)
	}
	// Compare Payload semantically (bytes may normalize whitespace).
	var a, c any
	if err := json.Unmarshal(orig.Payload, &a); err != nil {
		t.Fatalf("unmarshal orig payload: %v", err)
	}
	if err := json.Unmarshal(got.Payload, &c); err != nil {
		t.Fatalf("unmarshal got payload: %v", err)
	}
	ab, _ := json.Marshal(a)
	cb, _ := json.Marshal(c)
	if !bytes.Equal(ab, cb) {
		t.Errorf("Payload: got %s want %s", cb, ab)
	}
}

func TestResponseRoundTrip(t *testing.T) {
	orig := Response{
		RequestID: "req-456",
		OK:        true,
		Data:      json.RawMessage(`{"state":"paused"}`),
		Error:     "",
		EventID:   "evt-789",
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Response
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.RequestID != orig.RequestID {
		t.Errorf("RequestID: got %q want %q", got.RequestID, orig.RequestID)
	}
	if got.OK != orig.OK {
		t.Errorf("OK: got %v want %v", got.OK, orig.OK)
	}
	if got.EventID != orig.EventID {
		t.Errorf("EventID: got %q want %q", got.EventID, orig.EventID)
	}
	if got.Error != orig.Error {
		t.Errorf("Error: got %q want %q", got.Error, orig.Error)
	}
	if string(got.Data) == "" {
		t.Errorf("Data empty after round-trip")
	}
}

func TestReadRequestRejectsUnknownFields(t *testing.T) {
	// DisallowUnknownFields is a correctness guard -- clients must keep to
	// the documented schema.
	r := bytes.NewReader([]byte(`{"verb":"status","request_id":"x","bogus":1}` + "\n"))
	_, err := ReadRequest(r)
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}

func TestWriteResponseIsNDJSON(t *testing.T) {
	var buf bytes.Buffer
	err := WriteResponse(&buf, Response{RequestID: "r", OK: true})
	if err != nil {
		t.Fatalf("WriteResponse: %v", err)
	}
	b := buf.Bytes()
	if len(b) == 0 || b[len(b)-1] != '\n' {
		t.Fatalf("WriteResponse should terminate with newline, got %q", b)
	}
	// Only one newline -- single line.
	if bytes.Count(b, []byte{'\n'}) != 1 {
		t.Fatalf("expected exactly one newline, got %d in %q", bytes.Count(b, []byte{'\n'}), b)
	}
}
