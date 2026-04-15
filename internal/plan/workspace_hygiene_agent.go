// Package plan — workspace_hygiene_agent.go
//
// LLM-agent fallback for hygiene findings that the deterministic
// ScanAndAutoFix pipeline could not resolve on its own. Typical
// examples: a script references a helper file that doesn't exist, a
// requirements.txt setup needs a venv decision, a poetry install
// failed because the network was flaky and a retry + selective fix is
// warranted.
//
// The agent has a single tool — bash — with a deny list for the
// obvious catastrophic commands. It loops for up to 30 turns (or 10
// minutes wall-clock) and is expected to emit a final JSON verdict
// naming which findings it fixed and which it has concluded are
// unfixable in the current repo/scope.
package plan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/provider"
)

// agentVerdict is the JSON shape the repair agent must emit once it
// has decided each finding is fixed or unfixable.
type agentVerdict struct {
	// Fixed is the list of finding identifiers the agent resolved.
	Fixed []string `json:"fixed"`

	// Unfixable is the list of findings the agent judged impossible
	// to fix with the repo and scope available.
	Unfixable []agentUnfixable `json:"unfixable"`

	// Summary is a one-line description of what the agent did.
	Summary string `json:"summary,omitempty"`
}

// agentUnfixable captures the reason a single finding could not be
// resolved.
type agentUnfixable struct {
	Finding string `json:"finding"`
	Reason  string `json:"reason"`
}

// hygieneBashDeny contains substring patterns the agent is never
// allowed to run. Anything matching here is refused pre-exec and the
// tool_result reports the refusal so the model can adjust.
var hygieneBashDeny = []string{
	"rm -rf /",
	"rm -rf /*",
	"sudo ",
	"curl | sh",
	"curl | bash",
	"wget | sh",
	"wget | bash",
	":(){:|:&};:",
	"mkfs",
	"dd if=",
	"> /dev/sda",
}

// AgentRepair dispatches an LLM agent to resolve hygiene findings the
// deterministic auto-fixer could not handle. The agent is granted
// bash execution authority rooted at repoRoot (read, write, edit, run
// arbitrary commands subject to the deny list) and is expected to
// either resolve each finding or explicitly mark it unfixable with a
// reason.
//
// The agent runs via provider.Provider.Chat in a tool-use loop. The
// total session is capped at 30 turns and 10 minutes wall-clock.
// Each individual bash invocation is capped at 120 seconds.
//
// Returns nil when the agent emits a verdict (fixed or unfixable) or
// when prov is nil. Returns an error on transport / protocol failure.
func AgentRepair(ctx context.Context, prov provider.Provider, model string, repoRoot string, findings []HygieneFinding) error {
	if prov == nil {
		return nil
	}
	if len(findings) == 0 {
		return nil
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	sessionCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	system := agentRepairSystemPrompt
	userText := buildAgentUserPrompt(findings, repoRoot)
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": userText}})

	messages := []provider.ChatMessage{{Role: "user", Content: userContent}}

	tool := provider.ToolDef{
		Name:        "bash",
		Description: "Run a shell command in the repository root. Use for reading files (cat/head), editing (tee/sed), running build/install commands, and any other Unix tooling.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "command":         {"type": "string",  "description": "the shell command to run, passed to bash -lc"},
    "cwd":             {"type": "string",  "description": "optional subdirectory of the repo root"},
    "timeout_seconds": {"type": "integer", "description": "optional per-command timeout, default 60, max 120"}
  },
  "required": ["command"]
}`),
	}

	const maxTurns = 30
	for turn := 0; turn < maxTurns; turn++ {
		if sessionCtx.Err() != nil {
			return fmt.Errorf("hygiene agent timed out: %w", sessionCtx.Err())
		}
		resp, err := prov.Chat(provider.ChatRequest{
			Model:     model,
			System:    system,
			Messages:  messages,
			MaxTokens: 6000,
			Tools:     []provider.ToolDef{tool},
		})
		if err != nil {
			return fmt.Errorf("hygiene agent chat: %w", err)
		}
		if resp == nil {
			return fmt.Errorf("hygiene agent: nil response")
		}

		// Append the assistant turn verbatim (we need the tool_use
		// IDs on subsequent tool_result messages).
		assistantBlocks := marshalAssistantBlocks(resp.Content)
		messages = append(messages, provider.ChatMessage{Role: "assistant", Content: assistantBlocks})

		// If there are no tool_use blocks, treat this as the final
		// verdict turn.
		toolUses := extractToolUses(resp.Content)
		if len(toolUses) == 0 {
			raw, _ := collectModelText(resp)
			var v agentVerdict
			if _, err := jsonutil.ExtractJSONInto(raw, &v); err != nil {
				// Model stopped without a verdict — treat as best-
				// effort completion and return nil so the caller
				// doesn't escalate. We log the raw text so an
				// operator can see what it said.
				fmt.Printf("  🔧 hygiene-agent: (no JSON verdict, treating as done) %s\n", firstLine(raw))
				return nil
			}
			if strings.TrimSpace(v.Summary) != "" {
				fmt.Printf("  🔧 hygiene-agent: %s\n", v.Summary)
			} else {
				fmt.Printf("  🔧 hygiene-agent: %d fixed, %d unfixable\n", len(v.Fixed), len(v.Unfixable))
			}
			return nil
		}

		// Execute each tool_use and collate tool_result blocks into a
		// single user-turn content array.
		toolResults := make([]map[string]interface{}, 0, len(toolUses))
		for _, tu := range toolUses {
			result := execBashTool(sessionCtx, tu.Input, repoRoot)
			toolResults = append(toolResults, map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": tu.ID,
				"content":     result,
			})
		}
		resultJSON, _ := json.Marshal(toolResults)
		messages = append(messages, provider.ChatMessage{Role: "user", Content: resultJSON})
	}

	fmt.Printf("  🔧 hygiene-agent: turn cap reached (%d) — aborting\n", maxTurns)
	return nil
}

// toolUse is a convenience struct mirroring the Anthropic tool_use
// content block.
type toolUse struct {
	ID    string
	Name  string
	Input map[string]interface{}
}

// extractToolUses filters tool_use content blocks out of a response.
func extractToolUses(blocks []provider.ResponseContent) []toolUse {
	var out []toolUse
	for _, b := range blocks {
		if b.Type != "tool_use" {
			continue
		}
		out = append(out, toolUse{ID: b.ID, Name: b.Name, Input: b.Input})
	}
	return out
}

// marshalAssistantBlocks re-serializes the assistant response content
// blocks into the JSON shape the Anthropic Messages API expects on
// subsequent request bodies. Thinking / redacted_thinking blocks are
// included so caching stays aligned.
func marshalAssistantBlocks(blocks []provider.ResponseContent) json.RawMessage {
	out := make([]map[string]interface{}, 0, len(blocks))
	for _, b := range blocks {
		m := map[string]interface{}{"type": b.Type}
		switch b.Type {
		case "text":
			m["text"] = b.Text
		case "tool_use":
			m["id"] = b.ID
			m["name"] = b.Name
			if b.Input != nil {
				m["input"] = b.Input
			} else {
				m["input"] = map[string]interface{}{}
			}
		case "thinking":
			m["thinking"] = b.Thinking
			if b.Signature != "" {
				m["signature"] = b.Signature
			}
		case "redacted_thinking":
			// Best-effort passthrough.
			m["data"] = b.Text
		default:
			continue
		}
		out = append(out, m)
	}
	buf, _ := json.Marshal(out)
	return buf
}

// execBashTool runs a single bash tool invocation after deny-list
// screening and output truncation. The returned string is what we
// send back to the model as tool_result content.
func execBashTool(ctx context.Context, input map[string]interface{}, repoRoot string) string {
	cmdStr, _ := input["command"].(string)
	if strings.TrimSpace(cmdStr) == "" {
		return "error: empty command"
	}
	// Deny-list screening.
	lower := strings.ToLower(cmdStr)
	for _, bad := range hygieneBashDeny {
		if strings.Contains(lower, strings.ToLower(bad)) {
			fmt.Printf("    🔧 hygiene-agent: DENIED %s\n", truncate(cmdStr, 80))
			return fmt.Sprintf("refused: command matches deny pattern %q", bad)
		}
	}

	cwd := repoRoot
	if sub, ok := input["cwd"].(string); ok && strings.TrimSpace(sub) != "" {
		// Keep the agent rooted at the repo — don't allow it to cd
		// out via absolute paths or `..` traversal. Always resolve
		// sub against repoRoot, then Clean the result, then require
		// the cleaned path still sits under repoRoot. Any escape
		// attempt snaps cwd back to repoRoot and logs a refusal.
		candidate := sub
		if strings.HasPrefix(candidate, "/") {
			// Absolute paths are never honored — treat as relative
			// from repoRoot by stripping the leading slashes.
			candidate = strings.TrimLeft(candidate, "/")
		}
		joined := filepath.Clean(filepath.Join(repoRoot, candidate))
		rootAbs, _ := filepath.Abs(repoRoot)
		candAbs, _ := filepath.Abs(joined)
		// Require candAbs == rootAbs OR candAbs starts with rootAbs + separator.
		if candAbs == rootAbs || strings.HasPrefix(candAbs, rootAbs+string(filepath.Separator)) {
			cwd = candAbs
		} else {
			fmt.Printf("    🔧 hygiene-agent: REFUSED cwd outside repo: %s\n", truncate(sub, 80))
			return fmt.Sprintf("refused: cwd %q escapes repo root", sub)
		}
	}

	timeoutSec := 60
	switch v := input["timeout_seconds"].(type) {
	case float64:
		timeoutSec = int(v)
	case int:
		timeoutSec = v
	}
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	if timeoutSec > 120 {
		timeoutSec = 120
	}

	fmt.Printf("    🔧 hygiene-agent: %s\n", truncate(cmdStr, 80))
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	c := exec.CommandContext(cctx, "bash", "-lc", cmdStr)
	c.Dir = cwd
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	err := c.Run()
	out := buf.String()
	if len(out) > 4000 {
		out = out[:4000] + "\n...[truncated]"
	}
	if err != nil {
		return fmt.Sprintf("exit error: %v\n---output---\n%s", err, out)
	}
	return fmt.Sprintf("exit 0\n---output---\n%s", out)
}

// buildAgentUserPrompt renders the opening user message describing
// the findings the agent must address.
func buildAgentUserPrompt(findings []HygieneFinding, repoRoot string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repository root: %s\n\n", repoRoot)
	fmt.Fprintf(&b, "The deterministic workspace-hygiene scanner could not auto-fix the following %d finding(s).\n", len(findings))
	b.WriteString("Resolve each finding using the bash tool, or explicitly mark it unfixable with a reason.\n\n")
	for i, f := range findings {
		fmt.Fprintf(&b, "FINDING %d\n", i+1)
		fmt.Fprintf(&b, "  executor:  %s\n", f.Executor)
		fmt.Fprintf(&b, "  package:   %s\n", f.Package)
		fmt.Fprintf(&b, "  kind:      %s\n", f.Kind)
		fmt.Fprintf(&b, "  detail:    %s\n", f.Detail)
		if f.Suggested != "" {
			fmt.Fprintf(&b, "  suggested: %s\n", f.Suggested)
		}
		b.WriteString("\n")
	}
	b.WriteString("When every finding is either resolved or marked unfixable, emit a final assistant turn containing ONLY this JSON object (no prose, no backticks):\n\n")
	b.WriteString(`{"fixed": ["finding 1", ...], "unfixable": [{"finding": "...", "reason": "..."}], "summary": "one-line description"}`)
	b.WriteString("\n")
	return b.String()
}

// truncate caps s to n runes, appending an ellipsis when truncation
// actually happens. Used only for log output.
func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > 160 {
				return line[:160] + "…"
			}
			return line
		}
	}
	return ""
}

// agentRepairSystemPrompt is the persistent system prompt for the
// hygiene-repair agent. The tone is intentionally terse — the agent
// doesn't need to explain its thinking, it needs to run bash and
// ultimately emit a JSON verdict.
const agentRepairSystemPrompt = `You are a workspace hygiene repairer. You have one tool: bash. You will be given a list of findings from a deterministic scanner that could not auto-fix them. Your job:

  1. For each finding, use bash to inspect the repo and either FIX it (write missing files, edit package.json, run install commands, etc.) or determine it is genuinely UNFIXABLE with the current scope.
  2. Do not make speculative changes. Touch only what the findings describe.
  3. Do not run destructive commands. Do not attempt network-heavy operations unless a finding requires one.
  4. When every finding is resolved or declared unfixable, emit a FINAL assistant turn containing ONLY this JSON (no prose, no markdown fences):

     {"fixed": ["..."], "unfixable": [{"finding": "...", "reason": "..."}], "summary": "..."}

The JSON verdict is how you signal completion. Emit it exactly once, at the end.`
