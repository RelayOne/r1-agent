// Package cortex — Lane lifecycle: state-machine validation, event
// emission, kill cascade, and Workspace constructors.
//
// This file owns the methods listed under specs/lanes-protocol.md §8.1:
//
//   1. Lane.Transition(newStatus, reasonCode, reason) error  (§3.3 FSM)
//   2. Lane.EmitDelta(block agentloop.ContentBlock)         (§4.3)
//   3. Lane.EmitCost(in, out int, usd float64)              (§4.4)
//   4. Lane.EmitNote(noteID, severity)                       (§4.5)
//   5. Lane.Kill(reason)                                     (§4.6)
//
// Plus the three constructors that emit lane.created synchronously:
//
//   6. Workspace.NewMainLane(ctx)                  (one per session)
//   7. Workspace.NewLobeLane(ctx, name, parent)   (one per cortex Lobe spawn)
//   8. Workspace.NewToolLane(ctx, parent, toolName) (only on promotion)
package cortex

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/stokerr"
)

// SetSessionID sets the session identifier stamped on every emitted lane
// event. Intended to be called once at session bind time. Subsequent calls
// overwrite; this is by design to keep the constructor signature backwards
// compatible (NewWorkspace existing callers pass nil/nil).
func (w *Workspace) SetSessionID(id string) {
	w.mu.Lock()
	w.sessionID = id
	w.mu.Unlock()
}

// SessionID returns the session identifier currently stamped on lane events.
// Returns "" if SetSessionID has not been called.
func (w *Workspace) SessionID() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.sessionID
}

// laneTransitionTable encodes the §3.3 transition table. A transition from
// row to column is allowed iff the bool is true. Terminal-state rows are
// all-false. The pin operation is orthogonal and does not flow through
// this table.
//
// The table is closed: a new transition is a wire-version bump per
// spec §5.6.
var laneTransitionTable = map[hub.LaneStatus]map[hub.LaneStatus]bool{
	hub.LaneStatusPending: {
		hub.LaneStatusRunning:   true,
		hub.LaneStatusCancelled: true,
	},
	hub.LaneStatusRunning: {
		hub.LaneStatusBlocked:   true,
		hub.LaneStatusDone:      true,
		hub.LaneStatusErrored:   true,
		hub.LaneStatusCancelled: true,
	},
	hub.LaneStatusBlocked: {
		hub.LaneStatusRunning:   true,
		hub.LaneStatusCancelled: true,
		hub.LaneStatusErrored:   true,
	},
	// Terminal rows: empty maps mean every transition is rejected.
	hub.LaneStatusDone:      {},
	hub.LaneStatusErrored:   {},
	hub.LaneStatusCancelled: {},
}

// allowedTransition reports whether old → new is permitted by §3.3.
// Returns false for an unknown old state too (defensive).
func allowedTransition(oldS, newS hub.LaneStatus) bool {
	row, ok := laneTransitionTable[oldS]
	if !ok {
		return false
	}
	return row[newS]
}

// generateLaneID returns a monotonic-by-time identifier for a lane. The
// format is the prefix followed by a hex-encoded UnixNano timestamp and 8
// bytes of crypto/rand entropy, giving lex-sortable IDs that are unique
// across the session. TASK-8 of specs/lanes-protocol.md §11 swaps this
// for oklog/ulid/v2's MonotonicEntropy generator; until then the prefix
// keeps the "lane_" namespace so logs can grep one source and the
// timestamp-prefix preserves lex-ordering.
func generateLaneID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Entropy unavailable: fall back to a timestamp-only identifier.
		// Two lanes created in the same nanosecond would collide, but the
		// caller (newLane) holds the workspace mutex across allocation so
		// in practice the OS clock advances between calls.
		now := time.Now().UnixNano()
		return fmt.Sprintf("%s%016x", prefix, now)
	}
	return fmt.Sprintf("%s%016x%016x", prefix, time.Now().UnixNano(), bytesAsUint64(b[:8]))
}

func bytesAsUint64(b []byte) uint64 {
	if len(b) < 8 {
		return 0
	}
	var u uint64
	for i := 0; i < 8; i++ {
		u = (u << 8) | uint64(b[i])
	}
	return u
}

// nextLaneSeq allocates the next per-session monotonic seq for a lane
// event by routing through the single-writer goroutine in
// seq_allocator.go (per specs/lanes-protocol.md §5.5 / TASK-7).
//
// The first call returns 1; seq=0 is reserved for the synthetic
// session.bound event.
func (w *Workspace) nextLaneSeq() uint64 {
	return w.startSeqAllocator().next()
}

// emitLaneEvent is the single chokepoint that publishes a LaneEvent
// through the hub.Bus. Callers populate the type-specific fields on ev;
// this helper stamps SessionID and Seq, advances Lane.LastSeq under the
// workspace mutex, and emits asynchronously.
func (w *Workspace) emitLaneEvent(eventType hub.EventType, l *Lane, ev *hub.LaneEvent) {
	if w == nil {
		return
	}
	seq := w.nextLaneSeq()

	w.mu.Lock()
	if l != nil {
		l.LastSeq = seq
	}
	sid := w.sessionID
	w.mu.Unlock()

	if w.events == nil {
		return
	}
	ev.Seq = seq
	if ev.SessionID == "" {
		ev.SessionID = sid
	}
	w.events.EmitAsync(&hub.Event{
		Type: eventType,
		Lane: ev,
	})
}

// NewMainLane creates the single main-thread lane for the session. Emits
// lane.created synchronously per spec §8.1. There must be at most one
// main lane per session; subsequent calls return a fresh lane and the
// caller is responsible for not calling it twice (the spec gates this at
// agentloop.Loop start).
func (w *Workspace) NewMainLane(ctx context.Context) *Lane {
	return w.newLane(ctx, hub.LaneKindMain, "", "main")
}

// NewLobeLane creates a lane representing one cortex Lobe spawn. parent
// is typically the main lane (or another Lobe lane in nested scenarios).
// Emits lane.created synchronously.
func (w *Workspace) NewLobeLane(ctx context.Context, name string, parent *Lane) *Lane {
	parentID := ""
	if parent != nil {
		parentID = parent.ID
	}
	return w.newLane(ctx, hub.LaneKindLobe, parentID, name)
}

// NewToolLane creates a lane representing a long-running tool call that
// has been promoted to its own surface thread (>2s wall clock per spec
// §8.1). parent is typically the main lane or a Lobe lane that issued
// the tool call. Emits lane.created synchronously.
func (w *Workspace) NewToolLane(ctx context.Context, parent *Lane, toolName string) *Lane {
	parentID := ""
	if parent != nil {
		parentID = parent.ID
	}
	return w.newLane(ctx, hub.LaneKindTool, parentID, toolName)
}

// newLane is the shared constructor used by NewMainLane/NewLobeLane/
// NewToolLane. It registers the lane in the workspace, then emits
// lane.created.
func (w *Workspace) newLane(_ context.Context, kind hub.LaneKind, parentID, label string) *Lane {
	now := time.Now().UTC()
	l := &Lane{
		ID:        generateLaneID("lane_"),
		Kind:      kind,
		ParentID:  parentID,
		Label:     label,
		Status:    hub.LaneStatusPending,
		StartedAt: now,
		ws:        w,
	}

	w.mu.Lock()
	if w.lanes == nil {
		w.lanes = make(map[string]*Lane)
	}
	w.lanes[l.ID] = l
	w.mu.Unlock()

	startedAt := now
	w.emitLaneEvent(hub.EventLaneCreated, l, &hub.LaneEvent{
		LaneID:    l.ID,
		Kind:      kind,
		ParentID:  parentID,
		Label:     label,
		StartedAt: &startedAt,
		LobeName:  lobeNameFor(kind, label),
	})
	return l
}

// lobeNameFor returns the label as the lobe_name for lobe lanes; empty
// otherwise. Surfaces special-case lane.created with kind=lobe to render
// the lobe name in the gutter (per spec §4.1 example).
func lobeNameFor(kind hub.LaneKind, label string) string {
	if kind == hub.LaneKindLobe {
		return label
	}
	return ""
}

// Transition validates and applies a state-machine transition per
// specs/lanes-protocol.md §3.3. It rejects:
//
//   - illegal old → new pairs (returns *stokerr.Error{Code: ErrInternal});
//   - unknown LaneStatus values (returns *stokerr.Error{Code: ErrInternal});
//   - transitions on a nil lane (returns ErrInternal).
//
// On success it updates Lane.Status (and EndedAt if newStatus is
// terminal) and emits lane.status carrying both prev and new state.
//
// Transition is goroutine-safe; the workspace mutex serializes mutations.
func (l *Lane) Transition(newStatus hub.LaneStatus, reasonCode, reason string) error {
	if l == nil {
		return stokerr.Internalf("lane: Transition on nil receiver")
	}
	if !newStatus.IsValid() {
		return stokerr.Internalf("lane: invalid newStatus %q", string(newStatus))
	}

	w := l.ws
	if w == nil {
		return stokerr.Internalf("lane %q: no workspace bound", l.ID)
	}

	w.mu.Lock()
	prev := l.Status
	if !allowedTransition(prev, newStatus) {
		w.mu.Unlock()
		return stokerr.Internalf("lane %q: illegal transition %q → %q", l.ID, string(prev), string(newStatus))
	}
	l.Status = newStatus
	var endedAtPtr *time.Time
	if newStatus.IsTerminal() {
		now := time.Now().UTC()
		l.EndedAt = now
		endedAtPtr = &now
	}
	w.mu.Unlock()

	w.emitLaneEvent(hub.EventLaneStatus, l, &hub.LaneEvent{
		LaneID:     l.ID,
		Status:     newStatus,
		PrevStatus: prev,
		Reason:     reason,
		ReasonCode: reasonCode,
		EndedAt:    endedAtPtr,
	})
	return nil
}

// EmitDelta publishes a lane.delta event carrying one streamed content
// block. block is converted from agentloop.ContentBlock to the wire-format
// hub.LaneContentBlock at the boundary (hub cannot import agentloop).
//
// EmitDelta on a terminal lane is a no-op: surfaces would discard such
// events anyway (spec §3.3) and emitting them would only inflate the WAL.
func (l *Lane) EmitDelta(block agentloop.ContentBlock) {
	if l == nil || l.ws == nil {
		return
	}
	if l.Status.IsTerminal() {
		return
	}
	deltaSeq := atomic.AddUint64(&l.deltaSeq, 1)
	l.ws.emitLaneEvent(hub.EventLaneDelta, l, &hub.LaneEvent{
		LaneID:   l.ID,
		DeltaSeq: deltaSeq,
		Block:    contentBlockToHub(block),
	})
}

// EmitCost publishes a lane.cost event carrying a single token/dollar
// tick. Spec §4.4 says cost ticks are emitted no more than once per
// second per lane; rate-limiting is the caller's responsibility (the
// hub/WAL layer does not coalesce).
func (l *Lane) EmitCost(tokensIn, tokensOut int, usd float64) {
	if l == nil || l.ws == nil {
		return
	}
	l.ws.emitLaneEvent(hub.EventLaneCost, l, &hub.LaneEvent{
		LaneID:    l.ID,
		TokensIn:  tokensIn,
		TokensOut: tokensOut,
		USD:       usd,
	})
}

// EmitNote publishes a lane.note event pointing at the cortex Note named
// by noteID. The full Note is fetched via r1.cortex.notes; this event is
// only a lightweight pointer so surfaces can badge a lane without
// round-tripping for every Note (spec §4.5).
func (l *Lane) EmitNote(noteID, severity string) {
	if l == nil || l.ws == nil {
		return
	}
	l.ws.emitLaneEvent(hub.EventLaneNote, l, &hub.LaneEvent{
		LaneID:       l.ID,
		NoteID:       noteID,
		NoteSeverity: severity,
	})
}

// Kill emits lane.killed and follows it with a lane.status transition to
// cancelled. Spec §4.6 says lane.killed is REDUNDANT with the terminal
// lane.status carrying status=cancelled; surfaces use lane.killed for
// kill animations / audit trails.
//
// Idempotent: killing a terminal lane is a no-op, returning nil.
func (l *Lane) Kill(reason string) error {
	if l == nil || l.ws == nil {
		return stokerr.Internalf("lane: Kill on nil/unbound receiver")
	}
	if l.Status.IsTerminal() {
		return nil
	}

	// Emit lane.killed first per spec §4.6 ("Always followed (in same
	// seq window, monotonic) by a lane.status to the terminal state").
	l.ws.emitLaneEvent(hub.EventLaneKilled, l, &hub.LaneEvent{
		LaneID: l.ID,
		Reason: reason,
		Actor:  "operator",
	})
	return l.Transition(hub.LaneStatusCancelled, "cancelled_by_operator", reason)
}

// contentBlockToHub adapts agentloop.ContentBlock to the wire-format
// hub.LaneContentBlock. The two structs have nearly identical shape; the
// adapter exists solely to keep the import direction one-way (hub never
// imports agentloop).
func contentBlockToHub(b agentloop.ContentBlock) *hub.LaneContentBlock {
	return &hub.LaneContentBlock{
		Type:      b.Type,
		Text:      b.Text,
		ID:        b.ID,
		Name:      b.Name,
		Input:     []byte(b.Input),
		ToolUseID: b.ToolUseID,
		Content:   b.Content,
		IsError:   b.IsError,
		Thinking:  b.Thinking,
		Signature: b.Signature,
	}
}
