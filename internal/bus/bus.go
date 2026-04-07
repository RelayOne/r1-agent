// Package bus implements Stoke's event-driven runtime substrate.
//
// The bus is a durable, event-driven message system with three participant types:
// publishers (emit events), subscribers (passive observers), and hooks
// (privileged action handlers registered only by the supervisor).
//
// Events are durable before Publish returns. Hooks fire before passive
// subscribers see events. Hook registration is privileged — only callers
// with "supervisor" authority may register hooks.
//
// Subscriber delivery is asynchronous: each subscription owns a goroutine
// and buffered channel. Publish enqueues and returns. Panics in handlers
// are recovered per-subscriber without affecting other subscribers or the bus.
package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// EventType is a dotted-namespace identifier for event kinds.
type EventType string

// Worker events.
const (
	EvtWorkerSpawned            EventType = "worker.spawned"
	EvtWorkerActionStarted      EventType = "worker.action.started"
	EvtWorkerActionCompleted    EventType = "worker.action.completed"
	EvtWorkerDeclarationDone    EventType = "worker.declaration.done"
	EvtWorkerDeclarationFix     EventType = "worker.declaration.fix"
	EvtWorkerDeclarationProblem EventType = "worker.declaration.problem"
	EvtWorkerPaused             EventType = "worker.paused"
	EvtWorkerResumed            EventType = "worker.resumed"
	EvtWorkerTerminated         EventType = "worker.terminated"
)

// Ledger change-stream events.
const (
	EvtLedgerNodeAdded EventType = "ledger.node.added"
	EvtLedgerEdgeAdded EventType = "ledger.edge.added"
)

// Supervisor events.
const (
	EvtSupervisorRuleFired    EventType = "supervisor.rule.fired"
	EvtSupervisorHookInjected EventType = "supervisor.hook.injected"
	EvtSupervisorCheckpoint   EventType = "supervisor.checkpoint"
)

// Skill events.
const (
	EvtSkillLoaded     EventType = "skill.loaded"
	EvtSkillApplied    EventType = "skill.applied"
	EvtSkillExtraction EventType = "skill.extraction.requested"
)

// Mission events.
const (
	EvtMissionStarted   EventType = "mission.started"
	EvtMissionCompleted EventType = "mission.completed"
	EvtMissionAborted   EventType = "mission.aborted"
)

// Bus observability events.
const (
	EvtBusHandlerPanic       EventType = "bus.handler.panic"
	EvtBusSubscriberOverflow  EventType = "bus.subscriber.overflow"
	EvtBusHookActionFailed   EventType = "bus.hook.action_failed"
	EvtBusHookInjectionFailed EventType = "bus.hook.injection_failed"
)

// Event is an immutable record published on the bus.
type Event struct {
	ID        string          `json:"id"`
	Type      EventType       `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	EmitterID string          `json:"emitter_id"`
	Sequence  uint64          `json:"sequence"`
	Scope     Scope           `json:"scope"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CausalRef string          `json:"causal_ref,omitempty"`
}

// Scope tags identifying which mission/branch/loop/task/stance an event relates to.
type Scope struct {
	MissionID string `json:"mission_id,omitempty"`
	BranchID  string `json:"branch_id,omitempty"`
	LoopID    string `json:"loop_id,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	StanceID  string `json:"stance_id,omitempty"`
}

// Pattern filters events by type prefix and/or scope.
type Pattern struct {
	TypePrefix string `json:"type_prefix,omitempty"`
	Scope      *Scope `json:"scope,omitempty"`
}

// Matches reports whether evt matches the pattern.
func (p Pattern) Matches(evt Event) bool {
	if p.TypePrefix != "" && !strings.HasPrefix(string(evt.Type), p.TypePrefix) {
		return false
	}
	if p.Scope != nil {
		s := p.Scope
		if s.MissionID != "" && s.MissionID != evt.Scope.MissionID {
			return false
		}
		if s.BranchID != "" && s.BranchID != evt.Scope.BranchID {
			return false
		}
		if s.LoopID != "" && s.LoopID != evt.Scope.LoopID {
			return false
		}
		if s.TaskID != "" && s.TaskID != evt.Scope.TaskID {
			return false
		}
		if s.StanceID != "" && s.StanceID != evt.Scope.StanceID {
			return false
		}
	}
	return true
}

// HookPriority determines firing order. Higher values fire first.
type HookPriority int

// Hook is a privileged event handler that can take action on the runtime.
type Hook struct {
	ID        string
	Pattern   Pattern
	Priority  HookPriority
	Handler   func(ctx context.Context, evt Event) (*HookAction, error)
	Authority string // must be "supervisor"
}

// HookAction describes side-effects a hook wants to perform.
type HookAction struct {
	InjectEvents []Event       `json:"inject_events,omitempty"`
	PauseWorker  string        `json:"pause_worker,omitempty"`
	ResumeWorker string        `json:"resume_worker,omitempty"`
	SpawnWorker  *SpawnRequest `json:"spawn_worker,omitempty"`
}

// SpawnRequest describes a worker to create.
type SpawnRequest struct {
	Role    string         `json:"role"`
	Scope   Scope          `json:"scope"`
	Context map[string]any `json:"context,omitempty"`
}

// defaultSubscriberBufSize is the channel buffer size for each subscriber.
const defaultSubscriberBufSize = 1024

// Subscription represents a passive event subscription with its own
// delivery goroutine and buffered channel.
type Subscription struct {
	ID      string
	pattern Pattern
	handler func(Event)
	ch      chan Event
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	bus     *Bus
}

// run is the per-subscriber delivery goroutine.
func (s *Subscription) run() {
	defer close(s.done)
	for {
		select {
		case <-s.ctx.Done():
			// Drain remaining events before exiting.
			for {
				select {
				case evt, ok := <-s.ch:
					if !ok {
						return
					}
					s.deliver(evt)
				default:
					return
				}
			}
		case evt, ok := <-s.ch:
			if !ok {
				return
			}
			s.deliver(evt)
		}
	}
}

// deliver invokes the handler with panic recovery.
func (s *Subscription) deliver(evt Event) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("bus: handler panic in subscription %s: %v", s.ID, r)
			// Record the panic as a bus event via WAL (avoid recursive Publish).
			if s.bus != nil {
				s.bus.recordHandlerPanic(s.ID, evt, r)
			}
		}
	}()
	s.handler(evt)
}

// Cancel terminates event delivery for this subscription. It signals the
// goroutine to stop and waits for it to drain and exit.
func (s *Subscription) Cancel() {
	s.cancel()
	<-s.done
}

// delayedEntry tracks a scheduled future event.
type delayedEntry struct {
	ID        string    `json:"id"`
	Event     Event     `json:"event"`
	FireAt    time.Time `json:"fire_at"`
	Cancelled bool      `json:"cancelled"`
	timer     *time.Timer
}

// Bus is the event-driven runtime substrate.
type Bus struct {
	mu          sync.Mutex
	wal         *WAL
	seq         uint64
	hooks       []Hook
	subscribers []*Subscription
	delayed     map[string]*delayedEntry
	closed      bool

	// overflowCount tracks subscriber overflow events for testing.
	overflowCount uint64
}

// New creates or opens a Bus backed by a WAL in dir.
func New(dir string) (*Bus, error) {
	w, err := OpenWAL(dir)
	if err != nil {
		return nil, fmt.Errorf("bus: open WAL: %w", err)
	}

	b := &Bus{
		wal:     w,
		seq:     w.LastSeq(),
		delayed: make(map[string]*delayedEntry),
	}

	// Restore delayed events from WAL.
	if err := b.restoreDelayed(); err != nil {
		w.Close()
		return nil, fmt.Errorf("bus: restore delayed: %w", err)
	}

	return b, nil
}

// Publish durably writes an event and notifies hooks then subscribers.
// The event's Sequence is assigned by the bus. If CausalRef references a
// sequence number >= the one being assigned, publication is rejected.
//
// Hooks fire synchronously (highest priority first) before the event is
// enqueued to subscriber channels. Subscriber delivery is asynchronous.
func (b *Bus) Publish(evt Event) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return fmt.Errorf("bus: closed")
	}

	if evt.ID == "" {
		evt.ID = uuid.New().String()
	}
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}

	b.seq++
	evt.Sequence = b.seq

	// Causality check: causal ref must point to the past.
	if evt.CausalRef != "" {
		refEvt, err := b.wal.FindByID(evt.CausalRef)
		if err == nil && refEvt.Sequence >= evt.Sequence {
			b.seq--
			b.mu.Unlock()
			return fmt.Errorf("bus: causal ref %s (seq %d) is not before current seq %d", evt.CausalRef, refEvt.Sequence, evt.Sequence)
		}
	}

	if err := b.wal.Append(evt); err != nil {
		b.seq--
		b.mu.Unlock()
		return fmt.Errorf("bus: WAL append: %w", err)
	}

	// Snapshot hooks and subscribers while holding the lock.
	hooks := make([]Hook, len(b.hooks))
	copy(hooks, b.hooks)
	subs := make([]*Subscription, len(b.subscribers))
	copy(subs, b.subscribers)
	b.mu.Unlock()

	// Fire hooks (highest priority first) before subscribers.
	b.fireHooks(hooks, evt)

	// Enqueue to subscriber channels (non-blocking).
	for _, sub := range subs {
		if sub.ctx.Err() != nil {
			continue
		}
		if sub.pattern.Matches(evt) {
			select {
			case sub.ch <- evt:
				// Delivered to subscriber's queue.
			default:
				// Subscriber overflow — record but do not block publish.
				b.recordSubscriberOverflow(sub.ID, evt)
			}
		}
	}

	return nil
}

// fireHooks executes matching hooks in priority order with panic recovery.
func (b *Bus) fireHooks(hooks []Hook, evt Event) {
	// Sort by priority descending, stable to preserve registration order for ties.
	sorted := make([]Hook, 0, len(hooks))
	for _, h := range hooks {
		if h.Pattern.Matches(evt) {
			sorted = append(sorted, h)
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Priority > sorted[j].Priority
	})

	for _, h := range sorted {
		action, err := b.invokeHook(h, evt)
		if err != nil {
			b.recordHookActionFailed(h, evt, err)
			continue
		}
		if action == nil {
			continue
		}
		// Process injected events.
		for _, injEvt := range action.InjectEvents {
			if pubErr := b.Publish(injEvt); pubErr != nil {
				b.recordHookInjectionFailed(h, evt, injEvt, pubErr)
			}
		}
	}
}

// invokeHook calls a hook handler with panic recovery.
func (b *Bus) invokeHook(h Hook, evt Event) (action *HookAction, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("bus: hook panic (id=%s, pattern=%s): %v", h.ID, h.Pattern.TypePrefix, r)
			b.recordHandlerPanic(h.ID, evt, r)
			err = fmt.Errorf("hook panic: %v", r)
		}
	}()
	return h.Handler(context.Background(), evt)
}

// recordHandlerPanic writes a bus.handler.panic event to the WAL.
func (b *Bus) recordHandlerPanic(handlerID string, evt Event, panicVal any) {
	payload, _ := json.Marshal(map[string]any{
		"handler_id":     handlerID,
		"triggering_evt": evt.ID,
		"triggering_type": string(evt.Type),
		"panic":          fmt.Sprintf("%v", panicVal),
	})

	b.mu.Lock()
	b.seq++
	panicEvt := Event{
		ID:        uuid.New().String(),
		Type:      EvtBusHandlerPanic,
		Timestamp: time.Now(),
		EmitterID: "bus",
		Sequence:  b.seq,
		Payload:   payload,
		CausalRef: evt.ID,
	}
	_ = b.wal.Append(panicEvt)
	b.mu.Unlock()
}

// recordHookActionFailed writes a bus.hook.action_failed event to the WAL.
func (b *Bus) recordHookActionFailed(h Hook, evt Event, actionErr error) {
	payload, _ := json.Marshal(map[string]any{
		"hook_id":        h.ID,
		"hook_pattern":   h.Pattern.TypePrefix,
		"hook_priority":  h.Priority,
		"hook_authority": h.Authority,
		"triggering_evt": evt.ID,
		"triggering_type": string(evt.Type),
		"error":          actionErr.Error(),
	})

	b.mu.Lock()
	b.seq++
	failEvt := Event{
		ID:        uuid.New().String(),
		Type:      EvtBusHookActionFailed,
		Timestamp: time.Now(),
		EmitterID: "bus",
		Sequence:  b.seq,
		Payload:   payload,
		CausalRef: evt.ID,
	}
	_ = b.wal.Append(failEvt)
	b.mu.Unlock()

	log.Printf("bus: hook action failed (id=%s, pattern=%s): %v", h.ID, h.Pattern.TypePrefix, actionErr)
}

// recordHookInjectionFailed writes a bus.hook.injection_failed event to the WAL.
func (b *Bus) recordHookInjectionFailed(h Hook, triggerEvt Event, injEvt Event, pubErr error) {
	payload, _ := json.Marshal(map[string]any{
		"hook_id":         h.ID,
		"triggering_evt":  triggerEvt.ID,
		"injected_type":   string(injEvt.Type),
		"error":           pubErr.Error(),
	})

	b.mu.Lock()
	b.seq++
	failEvt := Event{
		ID:        uuid.New().String(),
		Type:      EvtBusHookInjectionFailed,
		Timestamp: time.Now(),
		EmitterID: "bus",
		Sequence:  b.seq,
		Payload:   payload,
		CausalRef: triggerEvt.ID,
	}
	_ = b.wal.Append(failEvt)
	b.mu.Unlock()

	log.Printf("bus: hook injection failed (hook=%s, injected=%s): %v", h.ID, injEvt.Type, pubErr)
}

// recordSubscriberOverflow records that a subscriber's channel was full.
func (b *Bus) recordSubscriberOverflow(subID string, evt Event) {
	b.mu.Lock()
	b.overflowCount++
	b.mu.Unlock()

	log.Printf("bus: subscriber %s overflow on event %s (type=%s)", subID, evt.ID, evt.Type)
}

// Subscribe registers a passive observer. Each subscription gets its own
// goroutine and buffered channel for async delivery. Cancel the returned
// Subscription to stop delivery and clean up.
func (b *Bus) Subscribe(pattern Pattern, handler func(Event)) *Subscription {
	b.mu.Lock()
	defer b.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	sub := &Subscription{
		ID:      uuid.New().String(),
		pattern: pattern,
		handler: handler,
		ch:      make(chan Event, defaultSubscriberBufSize),
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
		bus:     b,
	}

	go sub.run()

	b.subscribers = append(b.subscribers, sub)
	return sub
}

// RegisterHook registers a privileged hook. Only callers with Authority
// "supervisor" may register hooks.
func (b *Bus) RegisterHook(hook Hook) error {
	if hook.Authority != "supervisor" {
		return fmt.Errorf("bus: hook registration denied: authority %q is not \"supervisor\"", hook.Authority)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if hook.ID == "" {
		hook.ID = uuid.New().String()
	}
	b.hooks = append(b.hooks, hook)
	return nil
}

// RemoveHook deregisters a hook by ID.
func (b *Bus) RemoveHook(hookID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for i, h := range b.hooks {
		if h.ID == hookID {
			b.hooks = append(b.hooks[:i], b.hooks[i+1:]...)
			return
		}
	}
}

// Replay delivers historical events matching pattern from the given sequence
// number onward. The handler is read-only — it has no access to the action
// surface.
func (b *Bus) Replay(pattern Pattern, from uint64, handler func(Event)) error {
	events, err := b.wal.ReadFrom(from)
	if err != nil {
		return fmt.Errorf("bus: replay: %w", err)
	}

	for _, evt := range events {
		if pattern.Matches(evt) {
			handler(evt)
		}
	}
	return nil
}

// Cursor returns the last-processed sequence number for a subscription.
func (b *Bus) Cursor(subID string) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, sub := range b.subscribers {
		if sub.ID == subID {
			// For async subscribers, the cursor is approximated by the bus seq.
			return b.seq
		}
	}
	return 0
}

// CurrentSeq returns the current global sequence number.
func (b *Bus) CurrentSeq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// PublishDelayed schedules an event for publication after delay.
// Returns a cancel ID that can be used with CancelDelayed.
func (b *Bus) PublishDelayed(evt Event, delay time.Duration) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return "", fmt.Errorf("bus: closed")
	}

	cancelID := uuid.New().String()
	entry := &delayedEntry{
		ID:     cancelID,
		Event:  evt,
		FireAt: time.Now().Add(delay),
	}

	// Persist the delayed entry to WAL.
	if err := b.wal.AppendDelayed(entry); err != nil {
		return "", fmt.Errorf("bus: persist delayed: %w", err)
	}

	entry.timer = time.AfterFunc(delay, func() {
		b.mu.Lock()
		de, ok := b.delayed[cancelID]
		if !ok || de.Cancelled {
			b.mu.Unlock()
			return
		}
		delete(b.delayed, cancelID)
		b.mu.Unlock()
		_ = b.Publish(de.Event)
	})

	b.delayed[cancelID] = entry
	return cancelID, nil
}

// CancelDelayed cancels a previously scheduled delayed event.
// If the event has already fired, this is a no-op.
func (b *Bus) CancelDelayed(cancelID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	entry, ok := b.delayed[cancelID]
	if !ok {
		return nil // already fired or never existed
	}

	entry.Cancelled = true
	if entry.timer != nil {
		entry.timer.Stop()
	}

	// Persist cancellation.
	if err := b.wal.AppendDelayedCancel(cancelID); err != nil {
		return fmt.Errorf("bus: persist delayed cancel: %w", err)
	}

	delete(b.delayed, cancelID)
	return nil
}

// restoreDelayed restores pending delayed events from the WAL after a restart.
func (b *Bus) restoreDelayed() error {
	entries, err := b.wal.ReadDelayed()
	if err != nil {
		return err
	}

	now := time.Now()
	for _, entry := range entries {
		if entry.Cancelled {
			continue
		}
		remaining := time.Until(entry.FireAt)
		if remaining <= 0 {
			// Fire immediately if past due.
			remaining = time.Millisecond
		}

		e := entry // capture
		cancelID := e.ID
		_ = now // suppress lint
		e.timer = time.AfterFunc(remaining, func() {
			b.mu.Lock()
			de, ok := b.delayed[cancelID]
			if !ok || de.Cancelled {
				b.mu.Unlock()
				return
			}
			delete(b.delayed, cancelID)
			b.mu.Unlock()
			_ = b.Publish(de.Event)
		})
		b.delayed[cancelID] = e
	}
	return nil
}

// Close shuts down the bus, stopping all delayed timers, cancelling all
// subscriber goroutines, and closing the WAL.
func (b *Bus) Close() error {
	b.mu.Lock()

	b.closed = true
	for _, entry := range b.delayed {
		if entry.timer != nil {
			entry.timer.Stop()
		}
	}
	b.delayed = nil

	// Cancel all subscriber goroutines.
	subs := make([]*Subscription, len(b.subscribers))
	copy(subs, b.subscribers)
	b.subscribers = nil
	b.mu.Unlock()

	for _, sub := range subs {
		sub.cancel()
		<-sub.done
	}

	return b.wal.Close()
}
