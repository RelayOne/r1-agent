package main

// sow_env_issue.go wires the report_env_issue worker tool for spec-1
// item 6 (descent-hardening). Workers invoke this tool when they hit
// an environment blocker they cannot fix — missing binary, network
// outage, credential, protected file. The tool records the blocker
// in an in-process scratch keyed by (sessionID, acID); the descent
// engine's T3 classifier consults the scratch BEFORE running the
// 5-LLM-call multi-analyst reasoning loop.
//
// Net effect: when a worker honestly declares "I can't fix this — it's
// the environment", the descent engine trusts the signal and jumps
// straight to T5 env-fix, saving ~$0.10/AC.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/engine"
	"github.com/RelayOne/r1/internal/plan"
	"github.com/RelayOne/r1/internal/provider"
)

// envIssueToolName is the canonical name advertised to the model.
const envIssueToolName = "report_env_issue"

const envIssueToolDescription = "Report an environment blocker you cannot fix (missing binary, network outage, credential, protected file). The descent engine classifies this AC as 'environment' and skips multi-analyst reasoning. Use this when you've tried reasonable workarounds and are certain the failure is NOT a code bug."

// envIssueToolInput matches the JSON schema advertised to workers.
type envIssueToolInput struct {
	Issue               string `json:"issue"`
	WorkaroundAttempted string `json:"workaround_attempted"`
	Suggestion          string `json:"suggestion"`
}

// buildEnvIssueToolDef returns the provider-facing tool definition.
// Required field: issue. Optional: workaround_attempted, suggestion.
func buildEnvIssueToolDef() provider.ToolDef {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"issue": map[string]any{
				"type":        "string",
				"description": "One-line concrete problem (e.g., 'pnpm not on PATH', 'npm registry 503').",
			},
			"workaround_attempted": map[string]any{
				"type":        "string",
				"description": "What you tried before giving up (e.g., 'tried apt install, not root').",
			},
			"suggestion": map[string]any{
				"type":        "string",
				"description": "Actionable next step for the human operator (e.g., 'run ci as root or preinstall pnpm in the image').",
			},
		},
		"required": []string{"issue"},
	}
	raw, _ := json.Marshal(schema)
	return provider.ToolDef{
		Name:        envIssueToolName,
		Description: envIssueToolDescription,
		InputSchema: raw,
	}
}

// buildEnvIssueExtraTool builds the engine.ExtraTool for the current
// (session, task, currentAC) triple. The handler:
//
//   1. Decodes the tool input.
//   2. Validates issue is non-empty.
//   3. Records the report in DefaultEnvBlockerScratch().
//   4. Logs a user-visible line: the event-emitting subscriber wires
//      descent.worker_env_blocked onto the bus separately.
//   5. Returns "reported" as the tool_result so the worker's loop
//      ends cleanly instead of spinning.
//
// currentACID is typically "" when the worker hasn't yet targeted a
// specific AC — in that case the marker is stored under the empty ac
// id and the T3 classifier falls back to a per-session check.
func buildEnvIssueExtraTool(sessionID, taskID, currentACID string) engine.ExtraTool {
	return engine.ExtraTool{
		Def: buildEnvIssueToolDef(),
		Handler: func(ctx context.Context, input json.RawMessage) (string, error) {
			var ei envIssueToolInput
			if err := json.Unmarshal(input, &ei); err != nil {
				return "", fmt.Errorf("%s: invalid input: %w", envIssueToolName, err)
			}
			issue := strings.TrimSpace(ei.Issue)
			if issue == "" {
				return "", fmt.Errorf("%s: issue is required", envIssueToolName)
			}
			report := plan.EnvBlockerReport{
				SessionID:           sessionID,
				TaskID:              taskID,
				ACID:                currentACID,
				Issue:               issue,
				WorkaroundAttempted: strings.TrimSpace(ei.WorkaroundAttempted),
				Suggestion:          strings.TrimSpace(ei.Suggestion),
			}
			plan.DefaultEnvBlockerScratch().Record(report)
			fmt.Printf("    🚧 worker.env_blocked: session=%s task=%s ac=%s issue=%q workaround=%q suggestion=%q\n",
				sessionID, taskID, currentACID, issue, report.WorkaroundAttempted, report.Suggestion)
			return "reported", nil
		},
	}
}
