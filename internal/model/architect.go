// architect.go implements the Architect/Editor two-model pipeline.
// Inspired by Aider: separate reasoning from code editing into a two-model pipeline.
// An Architect model (powerful, expensive) describes HOW to solve the problem.
// An Editor model (cheaper, faster) converts that into specific file edits.
//
// Aider found this improved pass rates by 3-8 percentage points.
// Best result: 85% (o1-preview architect + o1-mini editor).
//
// This maps to Stoke's existing plan/execute phase separation but makes it
// explicit at the model routing level.
package model

// PipelineRole identifies a model's role in the architect/editor pipeline.
type PipelineRole string

const (
	RoleArchitect PipelineRole = "architect" // reasons about approach, no format constraints
	RoleEditor    PipelineRole = "editor"    // converts architect output to specific edits
	RoleReviewer  PipelineRole = "reviewer"  // reviews changes (cross-model)
	RoleFull      PipelineRole = "full"      // single model does everything (no pipeline)
)

// PipelineConfig defines the model pairing for architect/editor split.
type PipelineConfig struct {
	ArchitectProvider Provider     `json:"architect_provider"` // powerful model for reasoning
	EditorProvider    Provider     `json:"editor_provider"`    // fast model for code edits
	ReviewerProvider  Provider     `json:"reviewer_provider"`  // cross-model reviewer
	Enabled           bool         `json:"enabled"`            // false = single-model mode
}

// DefaultPipelineConfig returns the recommended model pairing.
// Claude (Opus) for architecture, Codex for editing, cross-model review.
func DefaultPipelineConfig() PipelineConfig {
	return PipelineConfig{
		ArchitectProvider: ProviderClaude,
		EditorProvider:    ProviderCodex,
		ReviewerProvider:  ProviderCodex, // different from architect for cross-model review
		Enabled:           true,
	}
}

// SingleModelConfig returns a config that uses one provider for everything.
func SingleModelConfig(provider Provider) PipelineConfig {
	return PipelineConfig{
		ArchitectProvider: provider,
		EditorProvider:    provider,
		ReviewerProvider:  provider,
		Enabled:           false,
	}
}

// ResolveRole returns the provider for a specific pipeline role.
func (pc PipelineConfig) ResolveRole(role PipelineRole, isAvailable func(Provider) bool) Provider {
	if !pc.Enabled {
		// Single model: use architect for everything
		if isAvailable(pc.ArchitectProvider) {
			return pc.ArchitectProvider
		}
		return ProviderLintOnly
	}

	var preferred Provider
	switch role {
	case RoleArchitect:
		preferred = pc.ArchitectProvider
	case RoleEditor:
		preferred = pc.EditorProvider
	case RoleReviewer:
		preferred = pc.ReviewerProvider
	default:
		preferred = pc.ArchitectProvider
	}

	if isAvailable(preferred) {
		return preferred
	}

	// Fallback: try the other provider
	for _, p := range []Provider{pc.ArchitectProvider, pc.EditorProvider, pc.ReviewerProvider} {
		if p != preferred && isAvailable(p) {
			return p
		}
	}

	return ProviderLintOnly
}

// ArchitectPrompt generates the system prompt for the architect phase.
// The architect should describe the approach in natural language without
// format constraints — no diff syntax, no line numbers.
func ArchitectPrompt(taskDescription string) string {
	return `You are the ARCHITECT. Your job is to analyze the task and describe HOW to solve it.

## Rules
- Describe the approach in natural language
- Identify which files need to change and why
- Explain the logic of each change
- Do NOT write code or diffs — that's the Editor's job
- Do NOT use any edit format syntax
- Focus on correctness, edge cases, and design tradeoffs

## Task
` + taskDescription
}

// EditorPrompt generates the system prompt for the editor phase.
// The editor takes the architect's description and produces specific code edits.
func EditorPrompt(architectOutput, taskDescription string) string {
	return `You are the EDITOR. The Architect has analyzed the task and described the approach.
Your job is to convert the Architect's description into specific, correct code changes.

## Architect's Analysis
` + architectOutput + `

## Rules
- Implement exactly what the Architect described
- Make minimal, focused changes
- Do not add features or cleanup beyond what was specified
- If the Architect's approach has a flaw, note it but implement as described

## Original Task
` + taskDescription
}

// ShouldUsePipeline decides if a task benefits from the architect/editor split.
// Small, mechanical tasks don't need the split; complex tasks do.
func ShouldUsePipeline(taskType TaskType) bool {
	switch taskType {
	case TaskTypeArchitecture, TaskTypeSecurity, TaskTypeConcurrency:
		return true // complex tasks benefit from split
	case TaskTypeDocs:
		return false // docs don't need architect
	default:
		return true // default to pipeline for safety
	}
}
