package nodes

import (
	"strings"
	"testing"
	"time"
)

func TestHITLRequest_Validate(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		req     HITLRequest
		wantErr string
	}{
		{"ok", HITLRequest{StanceRole: "Reviewer", SessionID: "S1", Kind: "approval", Question: "proceed?", When: now, Version: 1}, ""},
		{"missing role", HITLRequest{SessionID: "S1", Kind: "approval", Question: "q", When: now}, "stance_role"},
		{"missing session", HITLRequest{StanceRole: "R", Kind: "approval", Question: "q", When: now}, "session_id"},
		{"missing kind", HITLRequest{StanceRole: "R", SessionID: "S1", Question: "q", When: now}, "kind"},
		{"bad kind", HITLRequest{StanceRole: "R", SessionID: "S1", Kind: "invalid", Question: "q", When: now}, "invalid kind"},
		{"missing question", HITLRequest{StanceRole: "R", SessionID: "S1", Kind: "approval", When: now}, "question"},
		{"negative deadline", HITLRequest{StanceRole: "R", SessionID: "S1", Kind: "approval", Question: "q", When: now, DeadlineSeconds: -1}, "negative"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.req.Validate()
			if c.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestHITLResponse_Validate(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		resp    HITLResponse
		wantErr string
	}{
		{"ok approved", HITLResponse{RequestID: "r1", Decision: "approved", ResponderID: "u1", When: now}, ""},
		{"ok timed_out no responder", HITLResponse{RequestID: "r1", Decision: "timed_out", When: now}, ""},
		{"modified needs scope", HITLResponse{RequestID: "r1", Decision: "modified", ResponderID: "u1", When: now}, "modified_scope"},
		{"approved needs responder", HITLResponse{RequestID: "r1", Decision: "approved", When: now}, "responder_id"},
		{"bad decision", HITLResponse{RequestID: "r1", Decision: "maybe", ResponderID: "u1", When: now}, "invalid decision"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.resp.Validate()
			if c.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestIntervention_Validate(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		intv    Intervention
		wantErr string
	}{
		{"ok abort silent", Intervention{SessionID: "S1", Kind: "abort", InitiatorID: "u1", When: now}, ""},
		{"ok redirect with directive", Intervention{SessionID: "S1", Kind: "redirect", Directive: "focus on security first", InitiatorID: "u1", When: now}, ""},
		{"redirect needs directive", Intervention{SessionID: "S1", Kind: "redirect", InitiatorID: "u1", When: now}, "directive is required"},
		{"inject needs directive", Intervention{SessionID: "S1", Kind: "inject", InitiatorID: "u1", When: now}, "directive is required"},
		{"bad kind", Intervention{SessionID: "S1", Kind: "nuke", InitiatorID: "u1", When: now}, "invalid kind"},
		{"missing initiator", Intervention{SessionID: "S1", Kind: "abort", When: now}, "initiator_id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.intv.Validate()
			if c.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestReplanning_Validate(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		rp      Replanning
		wantErr string
	}{
		{"ok", Replanning{SessionID: "S1", Trigger: "terminal_failure", NewPlanRef: "plan-x", Rationale: "S1 escalated", NodesAffected: 4, When: now}, ""},
		{"missing rationale", Replanning{SessionID: "S1", Trigger: "terminal_failure", NewPlanRef: "plan-x", When: now}, "rationale"},
		{"bad trigger", Replanning{SessionID: "S1", Trigger: "because", NewPlanRef: "plan-x", Rationale: "r", When: now}, "invalid trigger"},
		{"negative affected", Replanning{SessionID: "S1", Trigger: "spec_drift", NewPlanRef: "plan-x", Rationale: "r", NodesAffected: -1, When: now}, "negative"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.rp.Validate()
			if c.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestVerificationEvidence_Validate(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		v       VerificationEvidence
		wantErr string
	}{
		{"ok", VerificationEvidence{SubjectRef: "n1", SubjectKind: "task", ProducerModel: "claude-sonnet-4-6", VerifierModel: "gpt-5-codex", Verdict: "agree", When: now}, ""},
		{"same producer verifier rejected", VerificationEvidence{SubjectRef: "n1", SubjectKind: "task", ProducerModel: "x", VerifierModel: "x", Verdict: "agree", When: now}, "must differ"},
		{"bad verdict", VerificationEvidence{SubjectRef: "n1", SubjectKind: "task", ProducerModel: "a", VerifierModel: "b", Verdict: "sorta", When: now}, "invalid verdict"},
		{"missing subject", VerificationEvidence{SubjectKind: "task", ProducerModel: "a", VerifierModel: "b", Verdict: "agree", When: now}, "subject_ref"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.v.Validate()
			if c.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestNewNodeTypes_Registered(t *testing.T) {
	for _, name := range []string{"hitl_request", "hitl_response", "intervention", "replanning", "verification_evidence"} {
		n, err := New(name)
		if err != nil {
			t.Fatalf("New(%q): %v", name, err)
		}
		if n.NodeType() != name {
			t.Fatalf("NodeType()=%q want %q", n.NodeType(), name)
		}
		if n.SchemaVersion() != 1 {
			t.Fatalf("SchemaVersion()=%d want 1", n.SchemaVersion())
		}
	}
}
