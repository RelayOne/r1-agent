package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/RelayOne/r1-agent/internal/plan"
)

// TestEnvIssueTool_RecordsMarker verifies buildEnvIssueExtraTool's
// handler parses input, records the marker via the scratch, and
// returns "reported".
func TestEnvIssueTool_RecordsMarker(t *testing.T) {
	// Isolate the scratch for this test.
	plan.DefaultEnvBlockerScratch().Clear("S-TEST", "AC-1")
	defer plan.DefaultEnvBlockerScratch().Clear("S-TEST", "AC-1")

	tool := buildEnvIssueExtraTool("S-TEST", "T1", "AC-1")
	if tool.Def.Name != "report_env_issue" {
		t.Errorf("tool name=%q, want report_env_issue", tool.Def.Name)
	}
	if !strings.Contains(tool.Def.Description, "environment blocker") {
		t.Errorf("description should mention environment blocker")
	}

	// Handler: normal success path.
	input, _ := json.Marshal(map[string]string{
		"issue":                "pnpm not on PATH",
		"workaround_attempted": "tried apt install",
		"suggestion":           "pre-install pnpm in the image",
	})
	result, err := tool.Handler(context.Background(), input)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if result != "reported" {
		t.Errorf("result=%q, want %q", result, "reported")
	}
	report, ok := plan.DefaultEnvBlockerScratch().Get("S-TEST", "AC-1")
	if !ok {
		t.Fatalf("expected marker in scratch")
	}
	if report.Issue != "pnpm not on PATH" {
		t.Errorf("issue=%q", report.Issue)
	}
	if report.Suggestion != "pre-install pnpm in the image" {
		t.Errorf("suggestion=%q", report.Suggestion)
	}
}

// TestEnvIssueTool_RequiresIssue verifies the handler rejects
// requests with an empty issue field (the tool's only required input).
func TestEnvIssueTool_RequiresIssue(t *testing.T) {
	tool := buildEnvIssueExtraTool("S", "T", "AC")
	input, _ := json.Marshal(map[string]string{
		"issue": "",
	})
	_, err := tool.Handler(context.Background(), input)
	if err == nil {
		t.Fatalf("expected error for empty issue")
	}
	if !strings.Contains(err.Error(), "issue is required") {
		t.Errorf("error=%q, want contains 'issue is required'", err.Error())
	}
}

// TestEnvIssueTool_MalformedInput verifies the handler rejects non-JSON.
func TestEnvIssueTool_MalformedInput(t *testing.T) {
	tool := buildEnvIssueExtraTool("S", "T", "AC")
	_, err := tool.Handler(context.Background(), []byte(`not-json`))
	if err == nil {
		t.Fatalf("expected error for bad json")
	}
}
