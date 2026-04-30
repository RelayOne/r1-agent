package oneshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Dispatch smoke-tests: every supported verb produces a non-empty
// Response with the correct Verb echo and valid-JSON Data, regardless
// of whether the payload was sufficient to produce a real result.
// Contract check for the CloudSwarm supervisor.
//
// With the real decompose/verify/critique bodies wired (spec §5.6.1)
// a nil payload lands on the legacy-probe scaffold path for each verb,
// so Status remains StatusScaffold across the whole set. Once the
// CloudSwarm supervisor posts real payloads those return StatusOK.
func TestDispatch_AllSupportedVerbsReturnScaffold(t *testing.T) {
	for _, verb := range SupportedVerbs {
		verb := verb
		t.Run(verb, func(t *testing.T) {
			resp, err := Dispatch(verb, nil)
			if err != nil {
				t.Fatalf("Dispatch(%q): %v", verb, err)
			}
			if resp.Verb != verb {
				t.Errorf("Verb=%q want %q", resp.Verb, verb)
			}
			if resp.Status != StatusScaffold {
				t.Errorf("Status=%q want %q", resp.Status, StatusScaffold)
			}
			if len(resp.Data) == 0 {
				t.Errorf("Data should not be empty")
			}
			// Data must be valid JSON.
			var anyVal any
			if err := json.Unmarshal(resp.Data, &anyVal); err != nil {
				t.Errorf("Data not valid JSON: %v", err)
			}
		})
	}
}

func TestDispatch_RealPayloadsExposeRuntimeMetadata(t *testing.T) {
	tests := []struct {
		name    string
		verb    string
		payload string
	}{
		{name: "decompose", verb: "decompose", payload: `{"task":"design a landing page"}`},
		{name: "verify", verb: "verify", payload: `{"artifact":"landing page copy","acceptance_criteria":["landing"]}`},
		{name: "critique", verb: "critique", payload: `{"draft":"# Draft\n\nThis landing page targets dentists and explains the offer in detail."}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			resp, err := Dispatch(tt.verb, json.RawMessage(tt.payload))
			if err != nil {
				t.Fatalf("Dispatch(%q): %v", tt.verb, err)
			}
			if resp.Status != StatusOK {
				t.Fatalf("Status=%q want %q", resp.Status, StatusOK)
			}
			if resp.ProviderUsed != "r1_core" {
				t.Errorf("ProviderUsed=%q want r1_core", resp.ProviderUsed)
			}
			if resp.CostEstimateUSD != 0 {
				t.Errorf("CostEstimateUSD=%v want 0", resp.CostEstimateUSD)
			}
		})
	}
}

func TestDispatch_DecomposeShape(t *testing.T) {
	resp, err := Dispatch("decompose", nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	// Decompose wraps a plan object with the §5.6.1 triad.
	var wrapped struct {
		Plan struct {
			SubTasks           []string `json:"subTasks"`
			Dependencies       []string `json:"dependencies"`
			AcceptanceCriteria []string `json:"acceptanceCriteria"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(resp.Data, &wrapped); err != nil {
		t.Fatalf("Unmarshal data: %v (%s)", err, string(resp.Data))
	}
	// Must be non-nil slices (JSON `[]` not `null`) so CloudSwarm
	// can iterate without nil-checks.
	if wrapped.Plan.SubTasks == nil {
		t.Error("SubTasks should be [], not nil")
	}
	if wrapped.Plan.Dependencies == nil {
		t.Error("Dependencies should be [], not nil")
	}
	if wrapped.Plan.AcceptanceCriteria == nil {
		t.Error("AcceptanceCriteria should be [], not nil")
	}
}

func TestDispatch_VerifyShape(t *testing.T) {
	resp, err := Dispatch("verify", nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var v struct {
		Result string `json:"result"`
		Tier   string `json:"tier"`
	}
	if err := json.Unmarshal(resp.Data, &v); err != nil {
		t.Fatalf("Unmarshal: %v (%s)", err, string(resp.Data))
	}
	if v.Result != "soft-pass" {
		t.Errorf("Result=%q want soft-pass", v.Result)
	}
	if v.Tier != "T1" {
		t.Errorf("Tier=%q want T1", v.Tier)
	}
}

func TestDispatch_CritiqueShape(t *testing.T) {
	resp, err := Dispatch("critique", nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var c struct {
		Critique string `json:"critique"`
	}
	if err := json.Unmarshal(resp.Data, &c); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if c.Critique == "" {
		t.Error("Critique string should be non-empty in scaffold response")
	}
}

func TestDispatch_UnknownVerb(t *testing.T) {
	_, err := Dispatch("nonsense", nil)
	if err == nil {
		t.Fatal("expected error for unknown verb")
	}
	if !errors.Is(err, ErrUnknownVerb) {
		t.Errorf("want ErrUnknownVerb, got %v", err)
	}
}

func TestRun_WritesJSONResponseToWriter(t *testing.T) {
	var out bytes.Buffer
	// Empty input keeps decompose on the legacy scaffold path so
	// this test remains a contract smoke-check for the Run wrapper.
	// Real-payload decompose is exercised by dedicated tests in
	// decompose_test.go once that file lands.
	in := strings.NewReader("")
	if err := Run("decompose", in, &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Must be newline-terminated JSON (Encoder.Encode adds \n)
	if !strings.HasSuffix(out.String(), "\n") {
		t.Error("output should end with newline")
	}
	// Parse as a Response and validate fields.
	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("parse output: %v (%s)", err, out.String())
	}
	if resp.Verb != "decompose" || resp.Status != StatusScaffold {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestRun_EmptyInputStillProducesScaffold(t *testing.T) {
	var out bytes.Buffer
	if err := Run("verify", strings.NewReader(""), &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Status != StatusScaffold {
		t.Errorf("Status=%q want scaffold", resp.Status)
	}
}

func TestRun_UnknownVerbReturnsError(t *testing.T) {
	var out bytes.Buffer
	err := Run("unknown-verb", strings.NewReader(""), &out)
	if !errors.Is(err, ErrUnknownVerb) {
		t.Errorf("want ErrUnknownVerb, got %v", err)
	}
	// No JSON should have been written.
	if out.Len() != 0 {
		t.Errorf("no output expected on error, got: %s", out.String())
	}
}

func TestRunFromFile_StdinSentinels(t *testing.T) {
	// We can't easily feed stdin here, but we can verify the
	// file-open branch rejects a nonexistent path cleanly.
	var out bytes.Buffer
	err := RunFromFile("decompose", "/nonexistent/path/does/not/exist", &out)
	if err == nil {
		t.Fatal("expected error for nonexistent input file")
	}
	if !strings.Contains(err.Error(), "open input") {
		t.Errorf("error=%q should mention 'open input'", err.Error())
	}
}

func TestSupportedVerbsMatchDispatch(t *testing.T) {
	// Every entry in SupportedVerbs must dispatch without
	// returning ErrUnknownVerb — keeps the exported list and
	// the switch in Dispatch in lock-step.
	for _, v := range SupportedVerbs {
		if _, err := Dispatch(v, nil); errors.Is(err, ErrUnknownVerb) {
			t.Errorf("advertised verb %q not handled by Dispatch", v)
		}
	}
}
