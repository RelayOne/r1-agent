// research_llm_decomposer.go: LLM-backed query decomposer for the
// ResearchExecutor. This replaces the heuristic keyword splitter with
// a model call that produces 1..N focused sub-questions sized by the
// effort level. When the provider is unreachable or returns garbage,
// the decomposer falls back to research.HeuristicDecomposer so the
// executor never loses the ability to run single-agent.
//
// The shape of the returned SubQuestions matches what the orchestrator
// + the executor's URL routing already consume — ID, Text, Hints.
// Hints are populated from the LLM's "expected_source_type" field
// (falling back to the raw question text) so the executor's
// collectURLs helper can still route Extra["urls_by_hint"] matches.

package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/provider"
	"github.com/ericmacdougall/stoke/internal/research"
)

// LLMDecomposer decomposes a research query into SubQuestions using an
// LLM provider. It is the production replacement for
// research.HeuristicDecomposer.
//
// Zero-value is not usable — Provider must be set. Construct via
// NewLLMDecomposer or assign fields directly.
type LLMDecomposer struct {
	// Provider is the LLM provider used for the decomposition call.
	// Required. When nil, Decompose returns an error and callers fall
	// back to the heuristic path.
	Provider provider.Provider

	// Model is the model name passed to Provider.Chat. Defaults to
	// "claude-sonnet-4-6" when empty.
	Model string

	// MaxSubQuestions caps the sub-question count regardless of
	// effort. 0 means use the per-effort ceiling (Minimal=1,
	// Standard=5, Thorough=10).
	MaxSubQuestions int
}

// NewLLMDecomposer returns an LLMDecomposer ready to call. Provider
// must be non-nil; a nil provider is accepted here but Decompose will
// reject it at call time so the fallback chain in ResearchExecutor
// engages.
func NewLLMDecomposer(p provider.Provider) *LLMDecomposer {
	return &LLMDecomposer{Provider: p}
}

// llmSubQuestion is the raw JSON shape we parse from the model. The
// executor's SubQuestion has a different set of fields (ID, Text,
// Hints) — we map llmSubQuestion → SubQuestion in Decompose.
type llmSubQuestion struct {
	ID                 string `json:"id"`
	Question           string `json:"question"`
	ExpectedSourceType string `json:"expected_source_type,omitempty"`
}

// decomposerEnvelope is the object wrapper we prompt the model to
// emit. ExtractJSONObject only handles top-level objects, so asking
// the model to return {"questions": [...]} rather than a bare array
// lets jsonutil robustly recover from preamble / code-fence / truncation
// drift. The model is still told to produce the array semantically.
type decomposerEnvelope struct {
	Questions []llmSubQuestion `json:"questions"`
}

// Decompose asks the LLM to split query into 1..N focused sub-questions
// sized by effort. Returns the sub-questions or an error when the
// provider fails or returns unparseable content. Callers should fall
// back to research.HeuristicDecomposer on error.
func (d *LLMDecomposer) Decompose(ctx context.Context, query string, effort EffortLevel) ([]research.SubQuestion, error) {
	if d == nil {
		return nil, fmt.Errorf("llm decomposer: nil receiver")
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	if d.Provider == nil {
		return nil, fmt.Errorf("llm decomposer: no Provider configured")
	}

	low, high := effortBounds(effort)
	cap := d.MaxSubQuestions
	if cap > 0 && cap < high {
		high = cap
	}
	if low > high {
		low = high
	}

	model := d.Model
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	prompt := buildLLMDecomposerPrompt(q, low, high)
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": prompt}})
	resp, err := d.Provider.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 2000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, fmt.Errorf("llm decomposer: chat: %w", err)
	}
	raw := collectDecomposerText(resp)
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("llm decomposer: empty response")
	}

	var env decomposerEnvelope
	if _, perr := jsonutil.ExtractJSONInto(raw, &env); perr != nil {
		return nil, fmt.Errorf("llm decomposer: parse: %w", perr)
	}
	if len(env.Questions) == 0 {
		return nil, fmt.Errorf("llm decomposer: zero questions returned")
	}

	// Clamp to [low, high]. If the model returned more than we want,
	// truncate — trusting its own ordering as priority. If it returned
	// fewer than the low bound for Standard/Thorough, we still accept
	// the shorter list (better than failing over a soft minimum).
	if len(env.Questions) > high {
		env.Questions = env.Questions[:high]
	}

	out := make([]research.SubQuestion, 0, len(env.Questions))
	for i, s := range env.Questions {
		text := strings.TrimSpace(s.Question)
		if text == "" {
			continue
		}
		id := strings.TrimSpace(s.ID)
		if id == "" {
			id = fmt.Sprintf("SQ-%d", i+1)
		}
		hints := []string{text}
		if est := strings.TrimSpace(s.ExpectedSourceType); est != "" {
			hints = append(hints, est)
		}
		out = append(out, research.SubQuestion{
			ID:    id,
			Text:  text,
			Hints: hints,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("llm decomposer: all questions empty after trim")
	}
	return out, nil
}

// effortBounds returns the [low, high] sub-question count for an
// effort level. These match the task spec's numbers:
//
//	Minimal  → [1, 1]
//	Standard → [3, 5]
//	Thorough → [5, 10]
//	Critical → [5, 10] (alias for Thorough in this ladder)
func effortBounds(e EffortLevel) (int, int) {
	switch e {
	case EffortMinimal:
		return 1, 1
	case EffortThorough, EffortCritical:
		return 5, 10
	case EffortStandard:
		return 3, 5
	default:
		return 3, 5
	}
}

// buildLLMDecomposerPrompt renders the prompt text. Kept as a small
// pure helper so tests can assert on its contents without the chat
// round-trip.
func buildLLMDecomposerPrompt(query string, low, high int) string {
	var nDirective string
	switch {
	case low == high:
		nDirective = fmt.Sprintf("Produce exactly %d sub-question.", low)
	default:
		nDirective = fmt.Sprintf("Produce between %d and %d sub-questions.", low, high)
	}
	return fmt.Sprintf(`You are a research planner. Decompose the user's research query into focused sub-questions, each answerable by reading a small number of authoritative sources.

QUERY:
%s

INSTRUCTIONS:
- %s
- Each sub-question must stand on its own: answerable without the other sub-questions in scope.
- Prefer sub-questions whose answers will meaningfully differ depending on the source consulted.
- "expected_source_type" is a short tag describing the kind of source best suited to answer it (e.g. "official-docs", "benchmark", "academic-paper", "vendor-blog", "standards-body"). Optional.

Output ONLY a single JSON object — no prose, no backticks — matching this shape:

{
  "questions": [
    {"id": "SQ-1", "question": "<one focused sub-question>", "expected_source_type": "<optional tag>"},
    {"id": "SQ-2", "question": "...", "expected_source_type": "..."}
  ]
}
`, query, nDirective)
}

// collectDecomposerText pulls assistant text out of a ChatResponse.
// Duplicates the tiny extraction used elsewhere in the codebase rather
// than pulling the full plan.collectModelText (which lives in a
// package we must not import from here to avoid a cycle — executor
// depends on plan for AC types only, and plan depends on research-
// adjacent helpers that would loop back).
func collectDecomposerText(resp *provider.ChatResponse) string {
	if resp == nil {
		return ""
	}
	var text, thinking strings.Builder
	for _, c := range resp.Content {
		switch c.Type {
		case "text":
			text.WriteString(c.Text)
		case "thinking", "redacted_thinking":
			if c.Thinking != "" {
				thinking.WriteString(c.Thinking)
				thinking.WriteString("\n")
			}
		}
	}
	if text.Len() > 0 {
		return text.String()
	}
	return thinking.String()
}
