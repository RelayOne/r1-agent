package provider

import "strings"

// ThinkingSpec requests extended thinking from the model. BudgetTokens
// is a hint, not a wire value: models on the adaptive-thinking API get
// {"type":"adaptive"} and choose their own depth, while legacy
// budget_tokens models receive the hint clamped to the API's
// [1024, max_tokens) constraint. A nil spec (or a non-positive budget)
// leaves the request wire bytes exactly as they are today.
type ThinkingSpec struct {
	BudgetTokens int
}

// minThinkingBudgetTokens is the API's floor for budget_tokens on
// legacy extended-thinking models.
const minThinkingBudgetTokens = 1024

// adaptiveThinkingModels lists lowercase substrings of model IDs that
// take thinking:{type:"adaptive"}. On these models budget_tokens is
// deprecated or rejected outright (400), so the numeric hint is
// intentionally dropped.
var adaptiveThinkingModels = []string{
	"opus-4-6",
	"opus-4-7",
	"opus-4-8",
	"sonnet-4-6",
	"sonnet-5",
	"fable",
	"mythos",
}

// legacyBudgetThinkingModels lists lowercase substrings of model IDs
// that still take thinking:{type:"enabled",budget_tokens:N}. Checked
// AFTER the adaptive table so the broader "sonnet-4-" entry cannot
// shadow sonnet-4-6.
var legacyBudgetThinkingModels = []string{
	"sonnet-4-5",
	"opus-4-5",
	"opus-4-1",
	"opus-4-0",
	"haiku-4-5",
	"sonnet-4-",
	"3-7-sonnet",
}

// anthropicThinkingParam returns the model-correct "thinking" request
// parameter for the given spec, or (nil, false) when no parameter
// should be emitted. Fail-closed by design: unknown models (LiteLLM
// aliases, RelayGate "tier:*" names, non-Claude IDs) get NO thinking
// parameter rather than a guessed one — a wrong shape is a hard 400 on
// every request, while no thinking is merely today's behavior.
func anthropicThinkingParam(model string, spec *ThinkingSpec, maxTokens int) (map[string]interface{}, bool) {
	if spec == nil || spec.BudgetTokens <= 0 {
		return nil, false
	}
	m := strings.ToLower(model)
	for _, s := range adaptiveThinkingModels {
		if strings.Contains(m, s) {
			return map[string]interface{}{"type": "adaptive"}, true
		}
	}
	for _, s := range legacyBudgetThinkingModels {
		if strings.Contains(m, s) {
			budget := spec.BudgetTokens
			if budget < minThinkingBudgetTokens {
				budget = minThinkingBudgetTokens
			}
			// budget_tokens must be strictly less than max_tokens.
			if budget >= maxTokens {
				budget = maxTokens - 1
			}
			if budget < minThinkingBudgetTokens {
				// max_tokens leaves no room for a valid budget —
				// emitting anyway would 400 the whole request.
				return nil, false
			}
			return map[string]interface{}{
				"type":          "enabled",
				"budget_tokens": budget,
			}, true
		}
	}
	return nil, false
}
