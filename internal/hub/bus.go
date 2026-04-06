package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// HandlerFunc is the signature for in-process hook handlers.
type HandlerFunc func(ctx context.Context, ev *Event) *HookResponse

// Subscriber represents a registered hook consumer.
type Subscriber struct {
	ID       string      // unique subscriber identifier
	Events   []EventType // events to subscribe to ("*" = all)
	Mode     Mode        // gate, transform, observe
	Priority int         // lower = runs first (default 1000)
	Handler  HandlerFunc // in-process handler (nil for external)

	// External transport (mutually exclusive with Handler)
	Webhook *WebhookConfig // HTTP webhook
	Script  *ScriptConfig  // CLI script

	// Runtime state
	enabled bool
}

// WebhookConfig defines an HTTP webhook subscriber.
type WebhookConfig struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Timeout time.Duration     `json:"timeout"`
	Retries int               `json:"retries"`
}

// ScriptConfig defines a CLI script subscriber.
type ScriptConfig struct {
	Command    string        `json:"command"`
	Timeout    time.Duration `json:"timeout"`
	InputJSON  bool          `json:"input_json"`
	OutputJSON bool          `json:"output_json"`
}

// Bus is the central event bus that dispatches events to subscribers.
type Bus struct {
	mu          sync.RWMutex
	subscribers map[EventType][]*Subscriber
	wildcards   []*Subscriber // subscribers to "*" (all events)
	breakers    map[string]*CircuitBreaker
	audit       *AuditLog
	log         *slog.Logger
}

// New creates a new event bus.
func New() *Bus {
	return &Bus{
		subscribers: make(map[EventType][]*Subscriber),
		breakers:    make(map[string]*CircuitBreaker),
		audit:       NewAuditLog(10000),
		log:         slog.Default().With("component", "hub"),
	}
}

// Register adds a subscriber to the bus. Thread-safe.
func (b *Bus) Register(sub Subscriber) {
	b.mu.Lock()
	defer b.mu.Unlock()

	sub.enabled = true
	if sub.Priority == 0 {
		sub.Priority = 1000
	}

	s := &sub
	for _, et := range sub.Events {
		if et == "*" {
			b.wildcards = append(b.wildcards, s)
			sort.Slice(b.wildcards, func(i, j int) bool {
				return b.wildcards[i].Priority < b.wildcards[j].Priority
			})
		} else {
			b.subscribers[et] = append(b.subscribers[et], s)
			sort.Slice(b.subscribers[et], func(i, j int) bool {
				return b.subscribers[et][i].Priority < b.subscribers[et][j].Priority
			})
		}
	}

	// Initialize circuit breaker for this subscriber
	b.breakers[sub.ID] = &CircuitBreaker{
		MaxFailures:   3,
		ResetTimeout:  30 * time.Second,
		HalfOpenMax:   1,
		state:         CircuitClosed,
	}
}

// Unregister removes a subscriber by ID.
func (b *Bus) Unregister(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for et, subs := range b.subscribers {
		filtered := subs[:0]
		for _, s := range subs {
			if s.ID != id {
				filtered = append(filtered, s)
			}
		}
		b.subscribers[et] = filtered
	}

	filtered := b.wildcards[:0]
	for _, s := range b.wildcards {
		if s.ID != id {
			filtered = append(filtered, s)
		}
	}
	b.wildcards = filtered

	delete(b.breakers, id)
}

// Emit fires an event through all registered subscribers.
// Gate subscribers run synchronously in priority order.
// Transform subscribers run synchronously, modifications accumulate.
// Observe subscribers run asynchronously.
func (b *Bus) Emit(ctx context.Context, ev *Event) *HookResponse {
	b.ensureID(ev)

	subs := b.subscribersFor(ev.Type)
	if len(subs) == 0 {
		return &HookResponse{Decision: Allow}
	}

	entry := AuditEntry{
		EventID:   ev.ID,
		EventType: ev.Type,
		Timestamp: ev.Timestamp,
	}
	start := time.Now()

	// Phase 1: Gate hooks (sync, can block)
	var gateResult *HookResponse
	for _, sub := range subs {
		if sub.Mode != ModeGate || !sub.enabled {
			continue
		}
		if !b.circuitAllows(sub.ID) {
			continue
		}
		resp := b.invoke(ctx, sub, ev)
		entry.Subscribers = append(entry.Subscribers, sub.ID)
		entry.Decisions = append(entry.Decisions, AuditDecision{
			SubscriberID: sub.ID, Decision: resp.Decision, Reason: resp.Reason,
		})
		if resp.Decision == Deny {
			entry.FinalResult = Deny
			entry.LatencyMs = time.Since(start).Milliseconds()
			b.audit.Record(entry)
			return resp
		}
		if gateResult == nil {
			gateResult = resp
		}
	}

	// Phase 2: Transform hooks (sync, can modify)
	var allInjections []Injection
	for _, sub := range subs {
		if sub.Mode != ModeTransform || !sub.enabled {
			continue
		}
		if !b.circuitAllows(sub.ID) {
			continue
		}
		resp := b.invoke(ctx, sub, ev)
		entry.Subscribers = append(entry.Subscribers, sub.ID)
		if resp.Suppress {
			break
		}
		allInjections = append(allInjections, resp.Injections...)
		entry.Injections += len(resp.Injections)
	}

	// Phase 3: Observe hooks (async, fire-and-forget)
	for _, sub := range subs {
		if sub.Mode != ModeObserve || !sub.enabled {
			continue
		}
		s := sub // capture
		go func() {
			if b.circuitAllows(s.ID) {
				b.invoke(context.Background(), s, ev)
			}
		}()
		entry.Subscribers = append(entry.Subscribers, s.ID)
	}

	entry.FinalResult = Allow
	entry.LatencyMs = time.Since(start).Milliseconds()
	b.audit.Record(entry)

	result := &HookResponse{Decision: Allow, Injections: allInjections}
	if gateResult != nil {
		result.Metadata = gateResult.Metadata
	}
	return result
}

// EmitAsync fires an event without waiting for any response.
func (b *Bus) EmitAsync(ev *Event) {
	b.ensureID(ev)
	go func() {
		b.Emit(context.Background(), ev)
	}()
}

// Gate is a convenience for emitting a gate event and checking the decision.
func (b *Bus) Gate(ctx context.Context, ev *Event) (allowed bool, reason string) {
	resp := b.Emit(ctx, ev)
	return resp.Decision != Deny, resp.Reason
}

// Transform is a convenience for emitting a transform event and getting injections.
func (b *Bus) Transform(ctx context.Context, ev *Event) []Injection {
	resp := b.Emit(ctx, ev)
	return resp.Injections
}

// SubscriberCount returns the total number of registered subscribers.
func (b *Bus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	total := len(b.wildcards)
	for _, subs := range b.subscribers {
		total += len(subs)
	}
	return total
}

// AuditEntries returns recent audit entries.
func (b *Bus) AuditEntries(limit int) []AuditEntry {
	return b.audit.Recent(limit)
}

// --- internal ---

func (b *Bus) subscribersFor(et EventType) []*Subscriber {
	b.mu.RLock()
	defer b.mu.RUnlock()

	var result []*Subscriber
	result = append(result, b.subscribers[et]...)
	result = append(result, b.wildcards...)

	// Also match wildcard prefix: "security.*" matches "security.scan_result"
	prefix := string(et)
	if idx := strings.LastIndex(prefix, "."); idx > 0 {
		wildcard := EventType(prefix[:idx] + ".*")
		result = append(result, b.subscribers[wildcard]...)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Priority < result[j].Priority
	})
	return result
}

func (b *Bus) invoke(ctx context.Context, sub *Subscriber, ev *Event) *HookResponse {
	if sub.Handler != nil {
		return b.invokeHandler(ctx, sub, ev)
	}
	if sub.Script != nil {
		return b.invokeScript(ctx, sub, ev)
	}
	if sub.Webhook != nil {
		return b.invokeWebhook(ctx, sub, ev)
	}
	return &HookResponse{Decision: Abstain}
}

func (b *Bus) invokeHandler(ctx context.Context, sub *Subscriber, ev *Event) *HookResponse {
	// Apply timeout
	timeout := 5 * time.Second
	if sub.Mode == ModeObserve {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan *HookResponse, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				b.log.Error("hook panic", "subscriber", sub.ID, "event", ev.Type, "panic", r)
				b.recordFailure(sub.ID)
				done <- &HookResponse{Decision: Abstain, Reason: "panic"}
			}
		}()
		done <- sub.Handler(ctx, ev)
	}()

	select {
	case resp := <-done:
		if resp == nil {
			return &HookResponse{Decision: Abstain}
		}
		if resp.Reason != "panic" {
			b.recordSuccess(sub.ID)
		}
		return resp
	case <-ctx.Done():
		b.log.Warn("hook timeout", "subscriber", sub.ID, "event", ev.Type)
		b.recordFailure(sub.ID)
		return &HookResponse{Decision: Abstain, Reason: "timeout"}
	}
}

func (b *Bus) circuitAllows(id string) bool {
	b.mu.RLock()
	cb, ok := b.breakers[id]
	b.mu.RUnlock()
	if !ok {
		return true
	}
	return cb.Allow()
}

func (b *Bus) recordSuccess(id string) {
	b.mu.RLock()
	cb, ok := b.breakers[id]
	b.mu.RUnlock()
	if ok {
		cb.RecordSuccess()
	}
}

func (b *Bus) recordFailure(id string) {
	b.mu.RLock()
	cb, ok := b.breakers[id]
	b.mu.RUnlock()
	if ok {
		cb.RecordFailure()
	}
}

func (b *Bus) ensureID(ev *Event) {
	if ev.ID == "" {
		ev.ID = generateID()
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}
}

func generateID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return fmt.Sprintf("%d-%s", time.Now().UnixMilli(), hex.EncodeToString(b))
}
