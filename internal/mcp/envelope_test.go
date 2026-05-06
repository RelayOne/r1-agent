package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOKEnvelope_BasicShape(t *testing.T) {
	env := OKEnvelope("r1.session.start", map[string]any{"session_id": "s-1"})
	if !env.OK {
		t.Fatal("OK should be true for OKEnvelope")
	}
	if env.ErrorCode != "" || env.ErrorMessage != "" {
		t.Errorf("error fields must be empty on OK; got code=%q msg=%q",
			env.ErrorCode, env.ErrorMessage)
	}
	if env.Links == nil || env.Links.Self != "r1.session.start" {
		t.Errorf("Self link should be the tool name; got %+v", env.Links)
	}
	var data map[string]any
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("Data should be valid JSON: %v", err)
	}
	if data["session_id"] != "s-1" {
		t.Errorf("Data round-trip: got %v, want session_id=s-1", data)
	}
}

func TestOKEnvelope_NilDataOmitted(t *testing.T) {
	env := OKEnvelope("r1.session.cancel", nil)
	out, err := MarshalEnvelope(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// data field must be omitted from the JSON when nil.
	if strings.Contains(string(out), `"data"`) {
		t.Errorf("nil data should be omitted; got %s", out)
	}
	if !strings.Contains(string(out), `"ok":true`) {
		t.Errorf("ok:true should be present; got %s", out)
	}
}

func TestErrEnvelope_TaxonomyCodePresent(t *testing.T) {
	env := ErrEnvelope("r1.session.start", "INVALID_INPUT", "workdir is required")
	if env.OK {
		t.Fatal("ErrEnvelope must have OK=false")
	}
	if env.ErrorCode != "INVALID_INPUT" {
		t.Errorf("ErrorCode = %q, want INVALID_INPUT", env.ErrorCode)
	}
	if env.ErrorMessage != "workdir is required" {
		t.Errorf("ErrorMessage = %q, want %q", env.ErrorMessage, "workdir is required")
	}
}

func TestEnvelope_RelatedLinks(t *testing.T) {
	env := OKEnvelope("r1.session.start", nil,
		"r1.session.send", "r1.session.cancel")
	if env.Links == nil || len(env.Links.Related) != 2 {
		t.Fatalf("Related links should have 2 entries; got %+v", env.Links)
	}
	if env.Links.Related[0] != "r1.session.send" {
		t.Errorf("Related[0] = %q, want r1.session.send", env.Links.Related[0])
	}
}

func TestEnvelope_WithDeprecationAppends(t *testing.T) {
	env := OKEnvelope("r1.session.start", nil)
	env = env.WithDeprecation("stoke_build_from_sow is deprecated; use r1_build_from_sow")
	env = env.WithDeprecation("schema v1 will be removed in v2.0.0")
	if env.Links == nil || len(env.Links.Deprecations) != 2 {
		t.Fatalf("expected 2 deprecations; got %+v", env.Links)
	}
}

func TestEnvelope_MarshalRoundTrip(t *testing.T) {
	env := ErrEnvelope("r1.lanes.kill", "NOT_FOUND", "lane lane-9 not found")
	raw, err := MarshalEnvelope(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Envelope
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.OK != false || back.ErrorCode != "NOT_FOUND" {
		t.Errorf("round-trip lost taxonomy: %+v", back)
	}
}

func TestEnvelope_NoSelfNoLinksSection(t *testing.T) {
	env := Envelope{OK: true}
	out, err := MarshalEnvelope(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), `"links"`) {
		t.Errorf("empty Links should be omitted; got %s", out)
	}
}
