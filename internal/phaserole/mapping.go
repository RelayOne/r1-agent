// Package phaserole implements phase-based agent role assignment.
// Inspired by OmX's phase→role mapping where different execution phases
// require different agent configurations:
// - Plan phase: architect-class model (high reasoning, low speed)
// - Implement phase: editor-class model (balanced)
// - Test phase: fast model with tool access
// - Review phase: different model than implementer (cross-model review)
// - Fix phase: same model as implement but with failure context
//
// This ensures the right model is used at each stage, optimizing for both
// quality and cost. Planning doesn't need speed; testing doesn't need genius.
package phaserole

// Phase is an execution phase.
type Phase string

const (
	PhasePlan      Phase = "plan"
	PhaseImplement Phase = "implement"
	PhaseTest      Phase = "test"
	PhaseReview    Phase = "review"
	PhaseFix       Phase = "fix"
	PhaseLint      Phase = "lint"
	PhaseDocument  Phase = "document"
	PhaseRefactor  Phase = "refactor"
)

// Role defines the agent configuration for a phase.
type Role struct {
	Phase         Phase    `json:"phase"`
	ModelClass    string   `json:"model_class"`    // "architect", "editor", "fast", "reviewer"
	Provider      string   `json:"provider"`       // "claude", "codex", "openrouter"
	MaxTokens     int      `json:"max_tokens"`
	Temperature   float64  `json:"temperature"`
	Tools         []string `json:"tools"`          // allowed tool categories
	SystemPrompt  string   `json:"system_prompt"`
	TimeoutSec    int      `json:"timeout_sec"`
}

// Mapping holds the phase→role configuration.
type Mapping struct {
	roles    map[Phase]Role
	fallback Role
}

// DefaultMapping returns a production-ready phase→role mapping.
func DefaultMapping() *Mapping {
	return &Mapping{
		roles: map[Phase]Role{
			PhasePlan: {
				Phase:        PhasePlan,
				ModelClass:   "architect",
				Provider:     "claude",
				MaxTokens:    16000,
				Temperature:  0.3,
				Tools:        []string{"read", "glob", "grep"},
				SystemPrompt: "You are a software architect. Analyze the task, identify the files that need changes, and create a detailed implementation plan. Do NOT write code.",
				TimeoutSec:   300,
			},
			PhaseImplement: {
				Phase:        PhaseImplement,
				ModelClass:   "editor",
				Provider:     "claude",
				MaxTokens:    32000,
				Temperature:  0.1,
				Tools:        []string{"read", "write", "edit", "glob", "grep", "bash"},
				SystemPrompt: "Implement the changes described in the plan. Write clean, tested code. Follow existing patterns in the codebase.",
				TimeoutSec:   600,
			},
			PhaseTest: {
				Phase:        PhaseTest,
				ModelClass:   "fast",
				Provider:     "claude",
				MaxTokens:    8000,
				Temperature:  0.0,
				Tools:        []string{"read", "bash"},
				SystemPrompt: "Run the test suite and report results. Do not modify code.",
				TimeoutSec:   180,
			},
			PhaseReview: {
				Phase:        PhaseReview,
				ModelClass:   "reviewer",
				Provider:     "codex",
				MaxTokens:    16000,
				Temperature:  0.2,
				Tools:        []string{"read", "glob", "grep"},
				SystemPrompt: "Review the code changes for correctness, security, and style. You did NOT write this code — review it critically.",
				TimeoutSec:   300,
			},
			PhaseFix: {
				Phase:        PhaseFix,
				ModelClass:   "editor",
				Provider:     "claude",
				MaxTokens:    16000,
				Temperature:  0.1,
				Tools:        []string{"read", "write", "edit", "bash"},
				SystemPrompt: "Fix the issue described in the failure context. Make minimal, targeted changes.",
				TimeoutSec:   300,
			},
			PhaseLint: {
				Phase:        PhaseLint,
				ModelClass:   "fast",
				Provider:     "claude",
				MaxTokens:    4000,
				Temperature:  0.0,
				Tools:        []string{"bash"},
				SystemPrompt: "Run linters and formatters. Report any issues.",
				TimeoutSec:   120,
			},
			PhaseDocument: {
				Phase:        PhaseDocument,
				ModelClass:   "editor",
				Provider:     "claude",
				MaxTokens:    8000,
				Temperature:  0.3,
				Tools:        []string{"read", "write", "edit"},
				SystemPrompt: "Write clear documentation for the changes made. Focus on the why, not just the what.",
				TimeoutSec:   180,
			},
			PhaseRefactor: {
				Phase:        PhaseRefactor,
				ModelClass:   "architect",
				Provider:     "claude",
				MaxTokens:    32000,
				Temperature:  0.2,
				Tools:        []string{"read", "write", "edit", "glob", "grep", "bash"},
				SystemPrompt: "Refactor the code to improve structure and maintainability. Preserve all existing behavior.",
				TimeoutSec:   600,
			},
		},
		fallback: Role{
			ModelClass:   "editor",
			Provider:     "claude",
			MaxTokens:    16000,
			Temperature:  0.1,
			Tools:        []string{"read", "write", "edit", "glob", "grep", "bash"},
			SystemPrompt: "Complete the assigned task.",
			TimeoutSec:   300,
		},
	}
}

// NewMapping creates an empty mapping.
func NewMapping() *Mapping {
	return &Mapping{
		roles: make(map[Phase]Role),
	}
}

// Set configures the role for a phase.
func (m *Mapping) Set(phase Phase, role Role) {
	role.Phase = phase
	m.roles[phase] = role
}

// SetFallback configures the default role for unmapped phases.
func (m *Mapping) SetFallback(role Role) {
	m.fallback = role
}

// Resolve returns the role for a phase, falling back to default if unmapped.
func (m *Mapping) Resolve(phase Phase) Role {
	if role, ok := m.roles[phase]; ok {
		return role
	}
	r := m.fallback
	r.Phase = phase
	return r
}

// Phases returns all configured phases.
func (m *Mapping) Phases() []Phase {
	var phases []Phase
	for p := range m.roles {
		phases = append(phases, p)
	}
	return phases
}

// HasPhase returns true if the phase has a configured role.
func (m *Mapping) HasPhase(phase Phase) bool {
	_, ok := m.roles[phase]
	return ok
}

// ModelClassForPhase returns just the model class for a phase.
func (m *Mapping) ModelClassForPhase(phase Phase) string {
	return m.Resolve(phase).ModelClass
}

// ProviderForPhase returns the provider for a phase.
func (m *Mapping) ProviderForPhase(phase Phase) string {
	return m.Resolve(phase).Provider
}

// IsCrossModel returns true if the review phase uses a different provider than implement.
func (m *Mapping) IsCrossModel() bool {
	impl := m.Resolve(PhaseImplement)
	review := m.Resolve(PhaseReview)
	return impl.Provider != review.Provider || impl.ModelClass != review.ModelClass
}

// ToolsAllowed returns the allowed tools for a phase.
func (m *Mapping) ToolsAllowed(phase Phase) []string {
	return m.Resolve(phase).Tools
}

// CanWrite returns true if the phase allows write operations.
func (m *Mapping) CanWrite(phase Phase) bool {
	for _, t := range m.Resolve(phase).Tools {
		if t == "write" || t == "edit" {
			return true
		}
	}
	return false
}
