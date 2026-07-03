// LLM comparative selector: a provider-backed Selector that reads the
// N candidate patches (diffs, test output, plans, numeric signals)
// side by side and names the best one. It augments the deterministic
// Scorer — Run only honors its answer when it names a successful
// outcome, so a nil provider, a dead endpoint, or garbage output all
// degrade to the score-sorted winner.
package specexec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/jsonutil"
	"github.com/RelayOne/r1/internal/promptguard"
	"github.com/RelayOne/r1/internal/provider"
)

// Per-outcome prompt caps. Repo-derived text is untrusted and can be
// arbitrarily large; each block is sanitized and truncated before it
// reaches the judge so N outcomes stay within one bounded request.
const (
	selectorDiffCap = 8 * 1024
	selectorTestCap = 2 * 1024
	selectorPlanCap = 4 * 1024
)

// selectorVerdict is the JSON shape the selector model must return.
type selectorVerdict struct {
	Winner    string `json:"winner"`
	Rationale string `json:"rationale"`
}

const selectorInstruction = `You are selecting the best of several candidate implementations of the same task.
Each candidate block below carries its strategy id, deterministic score, test signals, and (when available) its diff, test output, and plan.
Prefer the candidate that is CORRECT first (tests pass, does what the task asks) and MINIMAL second (smallest coherent change). Ignore superficial style.
Output ONLY a JSON object, no prose, no markdown fences:
{"winner":"<strategy_id>","rationale":"one or two sentences"}
The winner value MUST be one of the strategy ids shown below.

`

// NewLLMSelector returns a Selector backed by the given provider, or
// nil when prov is nil (callers treat a nil Selector as disabled —
// the deterministic scorer then decides alone). model defaults to
// claude-sonnet-4-6 when empty.
func NewLLMSelector(prov provider.Provider, model string) Selector {
	if prov == nil {
		return nil
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	return func(ctx context.Context, outcomes []Outcome) (string, string, error) {
		prompt := buildSelectorPrompt(outcomes)
		userContent, err := json.Marshal([]map[string]interface{}{{"type": "text", "text": prompt}})
		if err != nil {
			return "", "", fmt.Errorf("marshal selector prompt: %w", err)
		}
		req := provider.ChatRequest{
			Model:     model,
			MaxTokens: 2000,
			Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
		}

		// provider.Provider.Chat carries no context; run it in a
		// goroutine and race the caller's deadline so a hung endpoint
		// cannot stall winner selection (Run's fallback then applies).
		type chatOut struct {
			resp *provider.ChatResponse
			err  error
		}
		ch := make(chan chatOut, 1)
		go func() {
			resp, chatErr := prov.Chat(req)
			ch <- chatOut{resp, chatErr}
		}()
		var resp *provider.ChatResponse
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case out := <-ch:
			if out.err != nil {
				return "", "", out.err
			}
			resp = out.resp
		}

		var v selectorVerdict
		if _, err := jsonutil.ExtractJSONInto(chatResponseText(resp), &v); err != nil {
			return "", "", fmt.Errorf("parse selector verdict: %w", err)
		}
		if strings.TrimSpace(v.Winner) == "" {
			return "", "", fmt.Errorf("selector verdict has empty winner")
		}
		return strings.TrimSpace(v.Winner), v.Rationale, nil
	}
}

// buildSelectorPrompt renders one bounded block per outcome. Exported
// signals (score, tests, diff size, duration) come first so the model
// sees the deterministic evidence before the free text.
func buildSelectorPrompt(outcomes []Outcome) string {
	var b strings.Builder
	b.WriteString(selectorInstruction)
	for _, o := range outcomes {
		fmt.Fprintf(&b, "--- candidate %s ---\n", o.StrategyID)
		fmt.Fprintf(&b, "success=%v score=%.3f tests_passed=%d tests_failed=%d diff_lines=%d duration=%s\n",
			o.Success, o.Score, o.TestsPassed, o.TestsFailed, o.DiffLines, o.Duration)
		// o.Error is repo/test-derived (a failed rollout's stderr, a build
		// log tail): untrusted like the diff/test/plan blocks, so it goes
		// through the same promptguard path. Interpolating it raw would let
		// an attacker-controlled error string steer the judge.
		if txt := sanitizedBlock("error:"+o.StrategyID, o.Error, selectorTestCap); txt != "" {
			fmt.Fprintf(&b, "error:\n%s\n", txt)
		}
		if txt := sanitizedBlock("diff:"+o.StrategyID, o.DiffText, selectorDiffCap); txt != "" {
			fmt.Fprintf(&b, "diff:\n%s\n", txt)
		}
		if txt := sanitizedBlock("tests:"+o.StrategyID, o.TestOutput, selectorTestCap); txt != "" {
			fmt.Fprintf(&b, "test output:\n%s\n", txt)
		}
		if txt := sanitizedBlock("plan:"+o.StrategyID, o.PlanText, selectorPlanCap); txt != "" {
			fmt.Fprintf(&b, "plan:\n%s\n", txt)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// sanitizedBlock routes repo-derived text through promptguard (verify
// phase disposition) and truncates it. A sanitize rejection drops the
// block entirely rather than feeding untrusted text to the judge.
func sanitizedBlock(source, text string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	sanitized, _, err := promptguard.Sanitize(text, promptguard.PhaseAction("verify"), "specexec-selector:"+source)
	if err != nil {
		return ""
	}
	return capText(sanitized, limit)
}

func capText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "\n... (truncated)"
}

func chatResponseText(resp *provider.ChatResponse) string {
	if resp == nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range resp.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}
