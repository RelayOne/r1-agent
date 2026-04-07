package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
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
	// CheckpointInterval controls how often state is checkpointed.
	// Zero means use the default (30 seconds).
	CheckpointInterval time.Duration
}

// defaultCheckpointInterval is used when Config.CheckpointInterval is zero.
const defaultCheckpointInterval = 30 * time.Second

// Supervisor is the deterministic rules engine. It subscribes to bus events,
// evaluates rules in priority order, and publishes action events when rules fire.
type Supervisor struct {
	config Config
	bus    *bus.Bus
	ledger *ledger.Ledger

	rules   []Rule // sorted by priority descending after RegisterRules
	sub     *bus.Subscription
	running bool
	stopCh  chan struct{}
	doneCh  chan struct{}
	mu      sync.Mutex

	// stats tracks rule-fire counts for checkpoint reporting.
	stats map[string]uint64
	// lastCheckpoint records when the last checkpoint was written.
	lastCheckpoint time.Time
}

// New creates a supervisor with the given config, bus, and ledger.
func New(cfg Config, b *bus.Bus, l *ledger.Ledger) *Supervisor {
	if cfg.CheckpointInterval == 0 {
		cfg.CheckpointInterval = defaultCheckpointInterval
	}
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

// Start begins the event processing loop. It is non-blocking — the loop runs
// in a background goroutine. Start returns an error if the supervisor is
// already running.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("supervisor %s: already running", s.config.ID)
	}
	s.running = true
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	s.lastCheckpoint = time.Now()

	// Build a scope-filtered pattern that matches all event types within scope.
	pattern := bus.Pattern{
		Scope: &s.config.Scope,
	}

	// Channel to receive events from the subscription handler.
	evtCh := make(chan bus.Event, 256)

	s.sub = s.bus.Subscribe(pattern, func(evt bus.Event) {
		select {
		case evtCh <- evt:
		default:
			// Drop event if channel is full — better than blocking the bus.
			log.Printf("supervisor %s: event channel full, dropping %s seq=%d",
				s.config.ID, evt.Type, evt.Sequence)
		}
	})
	s.mu.Unlock()

	go s.loop(ctx, evtCh)
	return nil
}

// loop is the main event processing goroutine.
func (s *Supervisor) loop(ctx context.Context, evtCh <-chan bus.Event) {
	defer close(s.doneCh)

	ticker := time.NewTicker(s.config.CheckpointInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.checkpoint(ctx)
			return
		case <-s.stopCh:
			s.checkpoint(ctx)
			return
		case evt := <-evtCh:
			s.processEvent(ctx, evt)
		case <-ticker.C:
			s.checkpoint(ctx)
		}
	}
}

// Stop gracefully stops the supervisor. It blocks until the event loop exits.
func (s *Supervisor) Stop() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return fmt.Errorf("supervisor %s: not running", s.config.ID)
	}
	s.running = false
	s.mu.Unlock()

	// Cancel the subscription first to stop new event delivery.
	if s.sub != nil {
		s.sub.Cancel()
	}

	close(s.stopCh)
	<-s.doneCh // wait for loop to exit

	return nil
}

// processEvent handles a single event against all rules. Rules are evaluated
// in priority order (highest first). Each rule that matches and evaluates to
// true has its action executed. Evaluation or action errors are logged but do
// not halt processing of remaining rules.
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

		// Execute action.
		if err := rule.Action(ctx, evt, s.bus); err != nil {
			log.Printf("supervisor %s: rule %s action error on evt %s (seq %d): %v",
				s.config.ID, rule.Name(), evt.Type, evt.Sequence, err)
			continue
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
	s.lastCheckpoint = time.Now()
	s.mu.Unlock()

	cpPayload := checkpointPayload{
		SupervisorID:   s.config.ID,
		SupervisorType: string(s.config.Type),
		RuleCount:      ruleCount,
		FireCounts:     fireCounts,
		Uptime:         time.Since(s.lastCheckpoint).String(),
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
