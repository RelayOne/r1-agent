package builtin

import (
	"context"

	"github.com/RelayOne/r1-agent/internal/hub"
	"github.com/RelayOne/r1-agent/internal/skill"
)

// SkillInjector is a transform subscriber that injects the skill block into
// plan/execute/review prompts. It replaces the direct InjectPromptBudgeted
// calls in workflow.go with a bus-native approach.
type SkillInjector struct {
	Registry     *skill.Registry
	StackMatches []string
	TokenBudget  int
}

// Register adds the skill injector to the bus.
func (s *SkillInjector) Register(bus *hub.Bus) {
	bus.Register(hub.Subscriber{
		ID: "builtin.skill_injector",
		Events: []hub.EventType{
			hub.EventPromptBuilding,
			hub.EventPromptSkillsMatched,
		},
		Mode:     hub.ModeTransform,
		Priority: 200,
		Handler:  s.handle,
	})
}

func (s *SkillInjector) handle(ctx context.Context, ev *hub.Event) *hub.HookResponse {
	if s.Registry == nil {
		return &hub.HookResponse{Decision: hub.Allow}
	}

	// Extract prompt from event
	prompt := ""
	if ev.Prompt != nil {
		if p, ok := ev.Custom["prompt"]; ok {
			if str, ok := p.(string); ok {
				prompt = str
			}
		}
	}
	if prompt == "" {
		return &hub.HookResponse{Decision: hub.Allow}
	}

	budget := s.TokenBudget
	if budget == 0 {
		budget = 3000
	}

	augmented, selections := s.Registry.InjectPromptBudgeted(prompt, s.StackMatches, budget)

	// Report which skills were injected
	skillNames := make([]string, 0, len(selections))
	for _, sel := range selections {
		skillNames = append(skillNames, sel.Skill.Name)
	}

	return &hub.HookResponse{
		Decision: hub.Allow,
		Injections: []hub.Injection{
			{
				Position: "system",
				Content:  augmented,
				Label:    "skills",
				Priority: 200,
				Budget:   budget,
			},
		},
		Metadata: map[string]any{
			"skills_injected": skillNames,
			"skill_count":     len(selections),
		},
	}
}
