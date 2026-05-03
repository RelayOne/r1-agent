// Haiku-call implementation for PlanUpdateLobe (spec item 18).
//
// haikuCall composes a cache-aligned ChatRequest via LobePromptBuilder,
// invokes the bound provider, and returns the raw assistant text. JSON
// parsing and Note publication land in TASK-19; until then haikuCall
// is exposed as a method that production code wires from Run via the
// onTrigger hook (TASK-18 commit installs the wiring; TASK-19 commit
// adds the parse-and-apply layer the wiring depends on).
package planupdate

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/provider"
)

// readFileBytes is a thin wrapper around os.ReadFile so callers can
// keep their import surface small. Returns an error for any IO
// failure including os.IsNotExist; loadPlanForContext interprets that
// as "plan file not present yet" and returns a fallback notice.
func readFileBytes(path string) ([]byte, error) {
	return os.ReadFile(path) // #nosec G304 -- planPath is a config-controlled path.
}

// haikuCall executes one Haiku request for the supplied LobeInput. The
// system prompt is the verbatim planUpdateSystemPrompt; the user
// message is buildPlanContext(in) — a compact summary of the current
// plan.json plus the recent conversation excerpt the model should
// reason over.
//
// The function returns the raw assistant text on success, or "" + a
// non-nil error on any provider/IO failure. Callers (TASK-19's parse
// layer) treat any error as "skip this trigger" rather than retry —
// the next tick will fire on its own cadence.
func (l *PlanUpdateLobe) haikuCall(ctx context.Context, in cortex.LobeInput) (string, error) {
	if l.client == nil {
		return "", fmt.Errorf("plan-update: nil provider")
	}

	pb := llm.LobePromptBuilder{
		Model:        planUpdateModel,
		SystemPrompt: planUpdateSystemPrompt,
		MaxTokens:    planUpdateMaxTokens,
	}

	userMsg := buildPlanContext(l.planPath, in)
	req := pb.Build(userMsg, nil)

	resp, err := l.client.ChatStream(req, nil)
	if err != nil {
		slog.Warn("plan-update: chat stream failed", "err", err, "lobe", l.ID())
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("plan-update: nil response")
	}

	return concatTextContent(resp.Content), nil
}

// buildPlanContext renders the user-message context for haikuCall: a
// short header line plus the JSON-encoded current plan (or a fallback
// notice if plan.json does not exist) plus the most recent conversation
// slice.
//
// The function is intentionally simple — Haiku reads the system prompt
// (which contains the schema) and the user message in a single request,
// so the user message just needs to provide the inputs the prompt asks
// for. The slice depth (last 10 messages of in.History) is bounded to
// keep the per-call output well below the budget set by spec §G.
func buildPlanContext(planPath string, in cortex.LobeInput) string {
	planText := loadPlanForContext(planPath)
	convoText := renderHistoryTail(in, 10)
	return fmt.Sprintf("Current plan.json (path: %s):\n%s\n\nRecent conversation:\n%s",
		planPath, planText, convoText)
}

// concatTextContent collapses a Provider response's content blocks into
// a single string of text. Tool-use, thinking, and signature blocks are
// skipped — the spec mandates the Lobe is tool-free and emits ONLY a
// JSON object as text.
func concatTextContent(blocks []provider.ResponseContent) string {
	var out string
	for _, blk := range blocks {
		if blk.Type != "" && blk.Type != "text" {
			continue
		}
		out += blk.Text
	}
	return out
}

// loadPlanForContext reads planPath as raw bytes and returns the
// contents (or a fallback notice if the file is missing / malformed).
// We pass raw bytes rather than re-marshalling a *plan.Plan because the
// model is more cache-friendly with stable byte layouts and because
// the plan format may evolve in fields the Lobe does not know about.
//
// Defined here so haikuCall has a self-contained dependency layer in
// TASK-18; tests inject the file via t.TempDir() + os.WriteFile.
func loadPlanForContext(path string) string {
	if path == "" {
		return "(no plan path configured)"
	}
	b, err := readFileBytes(path)
	if err != nil {
		return fmt.Sprintf("(plan.json not yet present at %s)", path)
	}
	if len(b) == 0 {
		return "(plan.json empty)"
	}
	return string(b)
}

// renderHistoryTail formats the last n agentloop.Messages from
// in.History into a plain-text transcript. Messages are tagged with a
// short role prefix (USER:/ASSISTANT:) so the model can disambiguate
// without needing structured input.
func renderHistoryTail(in cortex.LobeInput, n int) string {
	if len(in.History) == 0 {
		return "(no conversation yet)"
	}
	start := len(in.History) - n
	if start < 0 {
		start = 0
	}
	out := ""
	for _, m := range in.History[start:] {
		text := joinTextBlocks(m.Content)
		if text == "" {
			continue
		}
		role := "USER"
		if m.Role == "assistant" {
			role = "ASSISTANT"
		}
		if out != "" {
			out += "\n"
		}
		out += role + ": " + text
	}
	if out == "" {
		out = "(no text turns in window)"
	}
	return out
}

