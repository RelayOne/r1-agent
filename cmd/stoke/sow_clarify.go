// Clarification round-trip wiring for stoke workers.
//
// A worker that hits genuine ambiguity in its SOW emits the
// request_clarification tool call. This file builds that tool's
// schema, enforces the per-task rate limit, routes the request to the
// configured plan.ClarifyResponder, and feeds the answer back into the
// worker's tool-use loop as a tool_result. Chat mode wires a user-
// prompting ChatResponder; headless mode synthesizes a SupervisorResponder
// from the SOW and ReasoningProvider. When no responder is available,
// UNKNOWN is returned immediately so the worker abandons rather than
// guesses.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/engine"
	"github.com/RelayOne/r1/internal/plan"
	"github.com/RelayOne/r1/internal/provider"
)

// clarifyToolDescription is the tool-use guidance the worker sees. It
// is deliberately blunt about when to use this tool (genuine SOW
// ambiguity, not minor preferences) and what UNKNOWN means (abandon,
// do not guess).
const clarifyToolDescription = `Pause work and ask the operator (or a supervisor LLM synthesized from the SOW) a scoped question. Use this ONLY when you cannot proceed because of a genuine ambiguity in the SOW or spec — e.g. the spec says "use OAuth" without specifying 2.0 vs 1.0a, or names a function without defining its signature. Do NOT use this for minor preferences (naming, formatting, which standard library to use) — those you decide yourself.

When you invoke this tool, stop all other work until the tool_result arrives. The result will be either a concrete answer you must obey, or the literal string "UNKNOWN — escalate" which means the SOW cannot answer the question. On UNKNOWN you MUST abandon this task, cite the ambiguity in your completion summary, and exit cleanly — DO NOT guess.

You may invoke this tool at most ` + "`" + `3` + "`" + ` times per task; the 4th attempt returns an immediate rate-limit answer.`

// buildClarifyToolDef returns the request_clarification tool schema.
// The schema mirrors plan.ClarifyRequest: question (required), context
// (optional excerpt, max 800 chars), options (optional 2-5 candidates).
// TaskID is injected by the handler closure — the worker never sees it
// in the schema.
func buildClarifyToolDef() provider.ToolDef {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"question": map[string]interface{}{
				"type":        "string",
				"description": "The specific question you need answered. One sentence ending in a question mark. MUST cite the exact ambiguity — e.g. \"The SOW says 'use OAuth' — OAuth 2.0 or OAuth 1.0a?\"",
			},
			"context": map[string]interface{}{
				"type":        "string",
				"description": "Short excerpt (max 800 chars) of the code or spec you were reading when you got stuck. Lets the responder anchor their answer in the same material you saw.",
			},
			"options": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Optional 2-5 candidate answers. When present, the responder may pick one by index instead of free-form answering. Phrase each option as a concrete choice you could implement.",
			},
		},
		"required": []string{"question"},
	}
	raw, _ := json.Marshal(schema)
	return provider.ToolDef{
		Name:        "request_clarification",
		Description: clarifyToolDescription,
		InputSchema: raw,
	}
}

// clarifyToolInput is the decoded shape of a request_clarification call.
type clarifyToolInput struct {
	Question string   `json:"question"`
	Context  string   `json:"context"`
	Options  []string `json:"options"`
}

// buildClarifyExtraTool produces an engine.ExtraTool bound to the given
// taskID and responder. The handler:
//
//  1. Decodes the model's tool input.
//  2. Enforces plan.MaxClarificationsPerTask per task via the counter.
//  3. Invokes responder.Respond with the ClarifyRequest.
//  4. Logs the round-trip (log callback — nil is fine).
//  5. Formats the ClarifyAnswer as the tool_result content string.
//
// The handler never returns an error on a normal round-trip, including
// UNKNOWN — the tool itself succeeded; the answer is just informative.
// True errors (malformed JSON, responder RPC failure) surface as tool
// errors so the agentloop's consecutive-error tracker sees them.
func buildClarifyExtraTool(taskID string, responder plan.ClarifyResponder, counter *plan.ClarifyCounter, onLog func(plan.ClarifyLogEntry)) engine.ExtraTool {
	return engine.ExtraTool{
		Def: buildClarifyToolDef(),
		Handler: func(ctx context.Context, input json.RawMessage) (string, error) {
			var ci clarifyToolInput
			if err := json.Unmarshal(input, &ci); err != nil {
				return "", fmt.Errorf("request_clarification: invalid input: %w", err)
			}
			if strings.TrimSpace(ci.Question) == "" {
				return "", fmt.Errorf("request_clarification: question is required")
			}

			// Rate limit. The 4th call returns immediately without
			// consulting the responder.
			n := counter.Inc()
			if n > plan.MaxClarificationsPerTask {
				entry := plan.ClarifyLogEntry{
					TaskID:   taskID,
					Question: ci.Question,
					Options:  ci.Options,
					Answer:   plan.ClarifyRateLimitAnswer,
					Source:   "rate-limit",
					Selected: -1,
				}
				if onLog != nil {
					onLog(entry)
				}
				fmt.Printf("    ⚠ request_clarification: task %s exceeded limit (%d) — returning rate-limit answer\n", taskID, plan.MaxClarificationsPerTask)
				return formatClarifyResult(entry.Answer, entry.Source, -1), nil
			}

			req := plan.ClarifyRequest{
				Question: ci.Question,
				Context:  ci.Context,
				Options:  ci.Options,
				TaskID:   taskID,
			}

			if responder == nil {
				responder = plan.NoopResponder{}
			}
			ans, err := responder.Respond(ctx, req)
			if err != nil {
				return "", fmt.Errorf("request_clarification: responder: %w", err)
			}
			if ans == nil {
				ans = &plan.ClarifyAnswer{
					Answer:         plan.ClarifyUnknownAnswer,
					SelectedOption: -1,
					Source:         "none",
				}
			}

			entry := plan.ClarifyLogEntry{
				TaskID:   taskID,
				Question: ci.Question,
				Options:  ci.Options,
				Answer:   ans.Answer,
				Source:   ans.Source,
				Selected: ans.SelectedOption,
			}
			if onLog != nil {
				onLog(entry)
			}
			fmt.Println(plan.FormatClarifyForLog(entry))

			return formatClarifyResult(ans.Answer, ans.Source, ans.SelectedOption), nil
		},
	}
}

// formatClarifyResult renders the tool_result body the worker sees.
// Including the source lets the worker reason about confidence
// ("supervisor-llm" vs "user"), and a UNKNOWN marker is restated
// explicitly so the model cannot mis-parse it.
func formatClarifyResult(answer, source string, selected int) string {
	var b strings.Builder
	b.WriteString("ANSWER: ")
	b.WriteString(answer)
	b.WriteString("\n")
	fmt.Fprintf(&b, "SOURCE: %s\n", source)
	if selected >= 0 {
		fmt.Fprintf(&b, "SELECTED_OPTION: %d\n", selected)
	}
	if strings.Contains(answer, plan.ClarifyUnknownAnswer) {
		b.WriteString("\nThe responder could not answer from the SOW. Abandon this task — do NOT guess. State the ambiguity in your final completion message so the operator sees it.\n")
	}
	return b.String()
}

// activeClarifyResponder is a process-scoped override that chat mode
// installs before calling sowCmd so that every worker the SOW spawns
// routes request_clarification to the chat ChatResponder instead of
// the synthetic supervisor-LLM. Set to nil after the dispatch returns.
//
// Why a package variable: sowCmd is a CLI entry point that re-reads
// flags on every invocation. Threading a new flag for "is this a
// chat-dispatched run? here's the ChatResponder" would leak chat
// internals into the CLI surface. The override is scoped to the chat
// dispatch call and cleared immediately after.
var activeClarifyResponder plan.ClarifyResponder

// SetActiveClarifyResponder installs a process-scoped responder the
// next runSessionNative will pick up. Chat mode calls this with its
// ChatResponder before dispatching a SOW and clears it (by calling
// with nil) when the dispatch returns. Non-chat runs never call this
// and the override stays nil, which is the headless default.
func SetActiveClarifyResponder(r plan.ClarifyResponder) {
	activeClarifyResponder = r
}

// resolveClarifyResponder picks the responder for a task run. Precedence:
//
//  1. cfg.ClarifyResponder (chat mode installs this).
//  2. A fresh SupervisorResponder built from cfg.ReasoningProvider + cfg.RawSOWText.
//  3. NoopResponder — the worker sees UNKNOWN on any request_clarification
//     and should abandon.
//
// The returned responder's Respond never returns nil, nil.
func resolveClarifyResponder(cfg sowNativeConfig) plan.ClarifyResponder {
	if cfg.ClarifyResponder != nil {
		return cfg.ClarifyResponder
	}
	if activeClarifyResponder != nil {
		return activeClarifyResponder
	}
	if cfg.ReasoningProvider != nil {
		model := cfg.ReasoningModel
		if model == "" {
			model = cfg.Model
		}
		return &plan.SupervisorResponder{
			Provider: cfg.ReasoningProvider,
			Model:    model,
			RepoRoot: cfg.RepoRoot,
			RawSOW:   cfg.RawSOWText,
		}
	}
	return plan.NoopResponder{}
}
