// Package stances defines system prompt templates and configuration for the 11
// stance roles used in Stoke's team harness.
package stances

// StanceTemplate defines the prompt template and configuration for a stance role.
type StanceTemplate struct {
	Role             string
	DisplayName      string
	DefaultModel     string // e.g. "claude-opus-4-6", "claude-sonnet-4-6"
	ConsensusPosture string // "absolute_completion_and_quality", "balanced", "pragmatic"
	SystemPrompt     string
}

var registry map[string]StanceTemplate

func init() {
	registry = make(map[string]StanceTemplate, 11)
	for _, fn := range []func() StanceTemplate{
		poTemplate,
		leadEngineerTemplate,
		leadDesignerTemplate,
		vpEngTemplate,
		ctoTemplate,
		sdmTemplate,
		qaLeadTemplate,
		devTemplate,
		reviewerTemplate,
		judgeTemplate,
		stakeholderTemplate,
	} {
		t := fn()
		registry[t.Role] = t
	}
}

// All returns the full registry of stance templates.
func All() map[string]StanceTemplate {
	out := make(map[string]StanceTemplate, len(registry))
	for k, v := range registry {
		out[k] = v
	}
	return out
}

// Get returns a specific stance template by role name.
func Get(role string) (StanceTemplate, bool) {
	t, ok := registry[role]
	return t, ok
}
