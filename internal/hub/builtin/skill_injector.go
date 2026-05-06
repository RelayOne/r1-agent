package builtin

import (
	"context"
	"encoding/json"
	"time"

	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/ledger/nodes"
	"github.com/RelayOne/r1/internal/skill"
)

// SkillInjector is a transform subscriber that injects the skill block into
// plan/execute/review prompts. It replaces the direct InjectPromptBudgeted
// calls in workflow.go with a bus-native approach.
//
// When Ledger is non-nil, a SkillLoaded ledger node is appended for each
// selected skill — this is what the v2 dashboard reads to render the 🧬
// glyph in the waterfall and the active-state predicate in the time
// scrubber. Spec r1-server-ui-v2-event-rendering §5.1.
// SkillLoadNoter is the minimum surface SkillInjector needs from the
// skill-load tracker (internal/skilltracker.Tracker). Declared here as
// an interface so this package doesn't import skilltracker — that
// would create a cycle since skilltracker already imports hub/builtin
// for EmitSkillUnloaded.
//
// Implementer note: the method is named NoteLoadInfo (not Note) so a
// concrete Tracker can keep an in-package `Note(LoadInfo)` method
// for tests and offer this distinct entry point for the cross-package
// interface boundary. See internal/skilltracker.Tracker.NoteLoadInfo.
type SkillLoadNoter interface {
	NoteLoadInfo(LoadInfoNote) error
}

// LoadInfoNote is the structural copy of skilltracker.LoadInfo used at
// the SkillInjector → Tracker boundary. Defined separately so the two
// packages don't share a struct type and the dependency arrow stays
// one-way (skilltracker imports hub/builtin, not the reverse).
type LoadInfoNote struct {
	LoadID     string
	StanceID   string
	StanceRole string
	SkillRef   string
	TaskScope  string
	LoadedAt   time.Time
	Tokens     int
}

type SkillInjector struct {
	Registry     *skill.Registry
	StackMatches []string
	TokenBudget  int
	// Ledger, when non-nil, enables skill_loaded ledger emission per
	// Spec 3 §5.1. Off by default so existing tests + offline runs
	// don't suddenly start writing to the chain tier.
	Ledger *ledger.Ledger
	// Tracker, when non-nil, gets a Note() call for every successfully
	// emitted SkillLoaded so the compactor + scope manager can later
	// emit the matching SkillUnloaded via skilltracker.Tracker.
	Tracker SkillLoadNoter
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

	// Spec r1-server-ui-v2-event-rendering §5.1: emit a SkillLoaded
	// ledger node per selected skill so the v2 dashboard can render
	// the load lifecycle. Best-effort; ledger errors are logged but
	// don't fail the hook (the prompt still gets the augmentation).
	if s.Ledger != nil {
		s.emitSkillLoadedNodes(ctx, ev, selections)
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

// emitSkillLoadedNodes appends one SkillLoaded ledger node per
// injected skill. Fields that aren't carried on hub.Event today are
// filled from ev.Custom keys when present, otherwise from sane
// defaults documented in Spec 3 §5.1. This keeps the emission
// best-effort: missing context never blocks the prompt mutation
// path.
func (s *SkillInjector) emitSkillLoadedNodes(ctx context.Context, ev *hub.Event, selections []skill.SkillSelection) {
	stanceID := ev.AgentID
	if stanceID == "" {
		if v, ok := ev.Custom["stance_id"].(string); ok && v != "" {
			stanceID = v
		} else {
			stanceID = "unknown"
		}
	}
	stanceRole := stringFromCustom(ev, "stance_role", "agent")
	concernField := stringFromCustom(ev, "concern_field_template", "default")
	taskScope := ev.TaskID
	if taskScope == "" {
		taskScope = "default"
	}
	loopRef := stringFromCustom(ev, "loop_id", ev.MissionID)
	if loopRef == "" {
		loopRef = "default"
	}
	now := time.Now().UTC()
	for _, sel := range selections {
		// The skill registry's Skill struct doesn't carry the
		// nodes.SkillLoaded "Applicability" field directly — keywords
		// stand in as the closest-matching trigger condition.
		appl := "*"
		if len(sel.Skill.Keywords) > 0 {
			appl = sel.Skill.Keywords[0]
		} else if len(sel.Skill.Triggers) > 0 {
			appl = sel.Skill.Triggers[0]
		}
		n := &nodes.SkillLoaded{
			SkillRef:              sel.Skill.Name,
			LoadingStanceID:       stanceID,
			LoadingStanceRole:     stanceRole,
			ConcernFieldTemplate:  concernField,
			MatchingApplicability: appl,
			TaskDAGScope:          taskScope,
			LoopRef:               loopRef,
			CreatedAt:             now,
			Version:               1,
		}
		if err := n.Validate(); err != nil {
			continue
		}
		payload, err := json.Marshal(n)
		if err != nil {
			continue
		}
		nodeID, _ := s.Ledger.AddNode(ctx, ledger.Node{
			Type:          n.NodeType(),
			SchemaVersion: n.SchemaVersion(),
			CreatedBy:     "builtin.skill_injector",
			Content:       payload,
		})
		// Note the load in the tracker so the compactor + scope
		// manager can later emit a matching SkillUnloaded with the
		// right LoadRef. Best-effort: tracker errors are ignored so
		// they don't break the prompt mutation path.
		if s.Tracker != nil && nodeID != "" {
			_ = s.Tracker.NoteLoadInfo(LoadInfoNote{
				LoadID:     string(nodeID),
				StanceID:   stanceID,
				StanceRole: stanceRole,
				SkillRef:   sel.Skill.Name,
				TaskScope:  taskScope,
				LoadedAt:   now,
				Tokens:     sel.Skill.EstTokens,
			})
		}
	}
}

func stringFromCustom(ev *hub.Event, key, fallback string) string {
	if ev == nil || ev.Custom == nil {
		return fallback
	}
	if v, ok := ev.Custom[key].(string); ok && v != "" {
		return v
	}
	return fallback
}
