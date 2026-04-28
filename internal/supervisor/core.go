package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
)

// SupervisorType distinguishes the three supervisor tiers.
type SupervisorType string

const (
	// TypeMission supervises an entire mission lifecycle.
	TypeMission SupervisorType = "mission"
	// TypeBranch supervises a single branch within a mission.
	TypeBranch SupervisorType = "branch"
	// TypeSDM supervises stance-detection across branches.
	TypeSDM SupervisorType = "sdm"
)

// Structural events that trigger checkpoints.
const (
	EvtLoopConverged           bus.EventType = "loop.converged"
	EvtLoopEscalated           bus.EventType = "loop.escalated"
	EvtBranchCompletionAgreed  bus.EventType = "branch.completion.agreed"
	EvtTaskMilestoneReached    bus.EventType = "task.milestone.reached"
)

// Supervisor rule action failure event.
const (
	EvtSupervisorRuleActionFailed bus.EventType = "supervisor.rule.action_failed"
)

// Config holds the supervisor's runtime parameters.
type Config struct {
	// ID is a unique supervisor instance identifier.
	ID string
	// Type selects which rule manifest to load.
	Type SupervisorType
	// Scope filters events the supervisor processes.
	Scope bus.Scope
	// RuleOverrides apply wizard-specified configuration to named rules.
	RuleOverrides map[string]RuleConfig
	// Pattern is the event filter for the supervisor's subscription.
	// If empty, defaults to scope-only filtering.
	Pattern bus.Pattern
}

// Supervisor is the deterministic rules engine. It subscribes to bus events,
// evaluates rules in priority order, and publishes action events when rules fire.
//
// Design: no polling, no internal buffering. The supervisor processes events
// inline on its subscription goroutine (provided by the bus's async delivery).
// Checkpoints are driven by structural events, not wall-clock timers.
type Supervisor struct {
	config Config
	bus    *bus.Bus
	ledger *ledger.Ledger

	rules        []Rule // sorted by priority descending after RegisterRules
	sub          *bus.Subscription
	checkpointSub *bus.Subscription
	running      bool
	mu           sync.Mutex

	// stats tracks rule-fire counts for checkpoint reporting.
	stats map[string]uint64
	// startTime records when the supervisor was started.
	startTime time.Time
}

// New creates a supervisor with the given config, bus, and ledger.
func New(cfg Config, b *bus.Bus, l *ledger.Ledger) *Supervisor {
	return &Supervisor{
		config: cfg,
		bus:    b,
		ledger: l,
		stats:  make(map[string]uint64),
	}
}

// RegisterRules loads rules into the supervisor. Rules are sorted by priority
// (highest first) and filtered by wizard overrides (disabled rules are removed).
func (s *Supervisor) RegisterRules(rules ...Rule) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, r := range rules {
		if override, ok := s.config.RuleOverrides[r.Name()]; ok {
			if override.Enabled != nil && !*override.Enabled {
				continue // rule disabled by wizard
			}
		}
		// A3 preflight: if the rule declares a payload schema, verify
		// it is well-formed (non-nil + at least one field). Catches
		// schema-drift at registration rather than at replay — the
		// silent-failure mode where a rule emits a payload missing a
		// required field and the consumer has no schema to check against.
		if sp, ok := r.(PayloadSchemaProvider); ok {
			schema := sp.PayloadSchema()
			if schema == nil || len(schema.Fields) == 0 {
				log.Printf("supervisor %s: rule %s declares PayloadSchemaProvider but returns empty schema — payloads will not be validated at dispatch",
					s.config.ID, r.Name())
			}
		}
		s.rules = append(s.rules, r)
	}
	sort.SliceStable(s.rules, func(i, j int) bool {
		return s.rules[i].Priority() > s.rules[j].Priority()
	})
}

// Rules returns the currently registered rules (read-only snapshot).
func (s *Supervisor) Rules() []Rule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Rule, len(s.rules))
	copy(out, s.rules)
	return out
}

// Start begins event processing. The supervisor subscribes to bus events and
// processes them inline on the subscription's goroutine (provided by the bus's
// async per-subscriber delivery). No polling, no internal goroutine, no
// buffered channel.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("supervisor %s: already running", s.config.ID)
	}
	s.running = true
	s.startTime = time.Now()

	// Build a scope-filtered pattern that matches all event types within scope.
	pattern := s.config.Pattern
	if pattern.Scope == nil {
		pattern.Scope = &s.config.Scope
	}

	// Subscribe for event processing. The bus delivers events asynchronously
	// on a per-subscriber goroutine, so processEvent runs without blocking
	// the bus or other subscribers.
	s.sub = s.bus.Subscribe(pattern, func(evt bus.Event) {
		s.processEvent(ctx, evt)
	})

	// R1-V1 audit Domain 9 P0 #1: register every rule that implements
	// HookRule as a privileged bus hook so it can gate (veto / inject)
	// on the publish path, not just observe. We unlock the supervisor
	// mutex first because RegisterHookRules takes its own snapshot.
	s.mu.Unlock()
	if _, err := s.RegisterHookRules(ctx); err != nil {
		log.Printf("supervisor %s: register hook rules: %v", s.config.ID, err)
	}
	s.mu.Lock()

	// Subscribe to structural events that trigger checkpoints.
	// This replaces the former time.NewTicker-based checkpoint loop.
	s.checkpointSub = s.bus.Subscribe(bus.Pattern{}, func(evt bus.Event) {
		switch evt.Type {
		case EvtLoopConverged, EvtLoopEscalated,
			EvtBranchCompletionAgreed, EvtTaskMilestoneReached:
			s.checkpoint(ctx)
		case bus.EvtWorkerSpawned,
			bus.EvtWorkerActionStarted, bus.EvtWorkerActionCompleted,
			bus.EvtWorkerDeclarationDone, bus.EvtWorkerDeclarationFix,
			bus.EvtWorkerDeclarationProblem,
			bus.EvtWorkerPaused, bus.EvtWorkerResumed, bus.EvtWorkerTerminated,
			bus.EvtLedgerNodeAdded, bus.EvtLedgerEdgeAdded,
			bus.EvtSupervisorRuleFired, bus.EvtSupervisorHookInjected,
			bus.EvtSupervisorCheckpoint,
			bus.EvtSkillLoaded, bus.EvtSkillApplied, bus.EvtSkillExtraction,
			bus.EvtMissionStarted, bus.EvtMissionCompleted, bus.EvtMissionAborted,
			bus.EvtBusHandlerPanic, bus.EvtBusSubscriberOverflow,
			bus.EvtBusHookActionFailed, bus.EvtBusHookInjectionFailed,
			bus.EvtDescentFileCapExceeded, bus.EvtDescentGhostWriteDetected,
			bus.EvtDescentBootstrapReinstalled, bus.EvtDescentPreCompletionGateFailed,
			bus.EvtWorkerEnvBlocked:
			// Other events do not trigger a supervisor checkpoint.
		default:
			// Any future bus event types fall through as no-op.
		}
	})

	s.mu.Unlock()
	return nil
}

// Stop gracefully stops the supervisor by cancelling its subscriptions
// and writing a final checkpoint.
func (s *Supervisor) Stop() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return fmt.Errorf("supervisor %s: not running", s.config.ID)
	}
	s.running = false
	s.mu.Unlock()

	// Cancel subscriptions to stop new event delivery.
	if s.sub != nil {
		s.sub.Cancel()
	}
	if s.checkpointSub != nil {
		s.checkpointSub.Cancel()
	}

	// Write a final checkpoint.
	s.checkpoint(context.Background())

	return nil
}

// processEvent handles a single event against all rules. Rules are evaluated
// in priority order (highest first). Each rule that matches and evaluates to
// true has its action executed. Evaluation or action errors are logged and
// published as observable events.
func (s *Supervisor) processEvent(ctx context.Context, evt bus.Event) {
	s.mu.Lock()
	rules := make([]Rule, len(s.rules))
	copy(rules, s.rules)
	s.mu.Unlock()

	for _, rule := range rules {
		// Check pattern match first (cheap, no I/O).
		if !rule.Pattern().Matches(evt) {
			continue
		}

		// Evaluate condition (may query ledger).
		fire, err := rule.Evaluate(ctx, evt, s.ledger)
		if err != nil {
			log.Printf("supervisor %s: rule %s evaluate error on evt %s (seq %d): %v",
				s.config.ID, rule.Name(), evt.Type, evt.Sequence, err)
			continue
		}
		if !fire {
			continue
		}

		// PR #24 codex-reverify HIGH: when a rule implements HookRule,
		// its side-effects (PauseWorker / ResumeWorker / SpawnWorker /
		// InjectEvents) are already delivered by the bus's hook path
		// before subscribers see the triggering event. Re-running the
		// subscribe-path Action() would double-publish worker.paused
		// and supervisor.spawn.requested for every match — doubling
		// pauses and spawning two CTO workers per snapshot violation.
		// Skip Action() for hook rules; observability (rule.fired) is
		// still published below.
		if _, isHook := rule.(HookRule); !isHook {
			if err := rule.Action(ctx, evt, s.bus); err != nil {
				log.Printf("supervisor %s: rule %s action error on evt %s (seq %d): %v",
					s.config.ID, rule.Name(), evt.Type, evt.Sequence, err)
				// Publish observable failure event (Fix #16).
				s.publishRuleActionFailed(evt, rule, err)
				continue
			}
		}

		// Record the firing.
		s.mu.Lock()
		s.stats[rule.Name()]++
		s.mu.Unlock()

		// Publish a rule-fired event for observability.
		s.publishRuleFired(evt, rule)
	}
}

// ruleFiredPayload is the schema for supervisor.rule.fired events.
type ruleFiredPayload struct {
	SupervisorID   string `json:"supervisor_id"`
	SupervisorType string `json:"supervisor_type"`
	RuleName       string `json:"rule_name"`
	RulePriority   int    `json:"rule_priority"`
	TriggerEventID string `json:"trigger_event_id"`
	TriggerType    string `json:"trigger_type"`
	Rationale      string `json:"rationale"`
}

// publishRuleFired emits a supervisor.rule.fired event on the bus.
func (s *Supervisor) publishRuleFired(trigger bus.Event, rule Rule) {
	payload, err := json.Marshal(ruleFiredPayload{
		SupervisorID:   s.config.ID,
		SupervisorType: string(s.config.Type),
		RuleName:       rule.Name(),
		RulePriority:   rule.Priority(),
		TriggerEventID: trigger.ID,
		TriggerType:    string(trigger.Type),
		Rationale:      rule.Rationale(),
	})
	if err != nil {
		log.Printf("supervisor %s: marshal rule-fired payload: %v", s.config.ID, err)
		return
	}

	evt := bus.Event{
		Type:      bus.EvtSupervisorRuleFired,
		EmitterID: s.config.ID,
		Scope:     s.config.Scope,
		Payload:   payload,
		CausalRef: trigger.ID,
	}
	if pubErr := s.bus.Publish(evt); pubErr != nil {
		log.Printf("supervisor %s: publish rule-fired: %v", s.config.ID, pubErr)
	}
}

// publishRuleActionFailed emits a supervisor.rule.action_failed event.
func (s *Supervisor) publishRuleActionFailed(trigger bus.Event, rule Rule, actionErr error) {
	payload, _ := json.Marshal(map[string]any{
		"supervisor_id":   s.config.ID,
		"supervisor_type": string(s.config.Type),
		"rule_name":       rule.Name(),
		"triggering_evt":  trigger.ID,
		"error":           actionErr.Error(),
	})
	evt := bus.Event{
		Type:      EvtSupervisorRuleActionFailed,
		EmitterID: s.config.ID,
		Scope:     s.config.Scope,
		Payload:   payload,
		CausalRef: trigger.ID,
	}
	if pubErr := s.bus.Publish(evt); pubErr != nil {
		log.Printf("supervisor %s: publish rule-action-failed: %v", s.config.ID, pubErr)
	}
}

// checkpointPayload is the schema for supervisor.checkpoint events.
type checkpointPayload struct {
	SupervisorID   string            `json:"supervisor_id"`
	SupervisorType string            `json:"supervisor_type"`
	RuleCount      int               `json:"rule_count"`
	FireCounts     map[string]uint64 `json:"fire_counts"`
	Uptime         string            `json:"uptime"`
}

// checkpoint writes supervisor state to the ledger and publishes a checkpoint
// event on the bus.
func (s *Supervisor) checkpoint(ctx context.Context) {
	s.mu.Lock()
	fireCounts := make(map[string]uint64, len(s.stats))
	for k, v := range s.stats {
		fireCounts[k] = v
	}
	ruleCount := len(s.rules)
	s.mu.Unlock()

	cpPayload := checkpointPayload{
		SupervisorID:   s.config.ID,
		SupervisorType: string(s.config.Type),
		RuleCount:      ruleCount,
		FireCounts:     fireCounts,
		Uptime:         time.Since(s.startTime).String(),
	}

	payload, err := json.Marshal(cpPayload)
	if err != nil {
		log.Printf("supervisor %s: marshal checkpoint: %v", s.config.ID, err)
		return
	}

	// Write checkpoint node to the ledger.
	node := ledger.Node{
		Type:          "supervisor.checkpoint",
		SchemaVersion: 1,
		CreatedBy:     s.config.ID,
		MissionID:     s.config.Scope.MissionID,
		Content:       payload,
	}
	if _, err := s.ledger.AddNode(ctx, node); err != nil {
		log.Printf("supervisor %s: ledger checkpoint: %v", s.config.ID, err)
	}

	// Publish checkpoint event on the bus.
	evt := bus.Event{
		Type:      bus.EvtSupervisorCheckpoint,
		EmitterID: s.config.ID,
		Scope:     s.config.Scope,
		Payload:   payload,
	}
	if pubErr := s.bus.Publish(evt); pubErr != nil {
		log.Printf("supervisor %s: publish checkpoint: %v", s.config.ID, pubErr)
	}
}
