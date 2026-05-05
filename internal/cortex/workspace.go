package cortex

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/hub"
)

// Severity tags drive both supervisor injection priority and the
// PreEndTurnCheckFn gate. Critical Notes refuse end_turn until resolved.
type Severity string

const (
	SevInfo     Severity = "info"
	SevAdvice   Severity = "advice"
	SevWarning  Severity = "warning"
	SevCritical Severity = "critical"
)

// Note is the unit of Lobe output. Append-only; a Note is never mutated.
// Resolution is modeled as a follow-on Note with Resolves=parentID.
type Note struct {
	ID        string         // ULID-like; monotonic per Workspace
	LobeID    string         // who emitted (e.g. "memory-recall")
	Severity  Severity       // info|advice|warning|critical
	Title     string         // <=80 chars, single-line
	Body      string         // free-form markdown, no length cap
	Tags      []string       // free-form, sorted; e.g. ["plan-divergence","secret-shape"]
	Resolves  string         // optional: ID of a prior Note this resolves
	EmittedAt time.Time
	Round     uint64         // the Round in which this Note was published
	Meta      map[string]any // free-form structured payload
}

// Validate enforces the structural invariants of a Note. It rejects an
// empty LobeID, an empty Title, a Title longer than 80 runes, and any
// Severity that is not one of the four declared constants. A nil error
// indicates the Note is well-formed for publication into a Workspace.
func (n Note) Validate() error {
	if n.LobeID == "" {
		return errors.New("note: empty LobeID")
	}
	if n.Title == "" {
		return errors.New("note: empty Title")
	}
	if utf8.RuneCountInString(n.Title) > 80 {
		return errors.New("note: Title >80 runes")
	}
	switch n.Severity {
	case SevInfo, SevAdvice, SevWarning, SevCritical:
		// ok
	default:
		return errors.New("note: unknown Severity")
	}
	return nil
}

// Workspace is the append-only Note store that backs the cortex Round
// pipeline. Lobes Publish Notes; the supervisor and PreEndTurnCheckFn
// read snapshots; subscribers receive each Note as it lands.
//
// Concurrency model: a sync.RWMutex guards every field. Readers (Snapshot,
// Drain) take RLock and copy; writers (Publish, round bumps) take Lock.
// The pattern mirrors internal/conversation/runtime.go:67-99.
//
// The events handle is the in-process typed hub used for live UI/log
// updates. The durable handle is the WAL-backed bus used for crash
// recovery and post-mortem replay; it is allowed to be nil, in which
// case the Workspace runs in pure in-memory mode (per spec item 22).
type Workspace struct {
	mu           sync.RWMutex
	notes        []Note
	seq          uint64
	drainedUpTo  uint64
	currentRound uint64
	events       *hub.Bus
	durable      *bus.Bus
	subs         map[uint64]func(Note)
	subsSeq      uint64
	spotlight    *Spotlight

	// --- Lanes protocol fields (specs/lanes-protocol.md §8) ---
	//
	// sessionID is the session identifier stamped on every emitted lane
	// event. Empty until SetSessionID is called; lane events emitted with
	// an empty sessionID still validate but surfaces will see them as
	// orphaned. Set once at session bind time.
	sessionID string

	// lanes is the canonical store of every Lane created in this
	// Workspace. Keyed by Lane.ID. Mutated under w.mu. Lanes are never
	// removed; terminal lanes remain so r1.lanes.list can return them.
	lanes map[string]*Lane

	// laneSeqAlloc is the per-session single-writer seq allocator
	// goroutine. Lazily started on first lane-event emission via
	// startSeqAllocator (see seq_allocator.go).
	//
	// seq=0 is reserved for the synthetic session.bound event per spec
	// §5.5; the first allocated lane-event seq is 1.
	laneSeqAlloc *seqAllocator
}

// NewWorkspace constructs a Workspace bound to the given event hub and
// durable bus. Either argument may be nil: a nil events hub disables
// live notifications, and a nil durable bus selects in-memory mode
// with no WAL persistence (per TASK-22 contract).
//
// A Spotlight is constructed alongside the Workspace and wired so that
// every successful Publish updates the spotlight ranking (TASK-7).
func NewWorkspace(events *hub.Bus, durable *bus.Bus) *Workspace {
	return &Workspace{
		events:    events,
		durable:   durable,
		subs:      make(map[uint64]func(Note)),
		spotlight: NewSpotlight(events),
	}
}

// Spotlight returns the Spotlight tracker bound to this Workspace. The
// returned pointer is stable for the lifetime of the Workspace, so callers
// may cache it. The Spotlight is updated on every successful Publish via
// maybeUpdate; see TASK-7 in specs/cortex-core.md.
func (w *Workspace) Spotlight() *Spotlight {
	return w.spotlight
}

// SetRound updates the round stamp applied to subsequent Publish calls.
// Called by Cortex.MidturnNote (TASK-14) at the start of each round.
//
// Per spec item 11, Round.Open is the only writer that should invoke this,
// so that every Note published within a round carries that round's ID
// (Publish reads w.currentRound while holding the write lock; see item 4).
func (w *Workspace) SetRound(roundID uint64) {
	w.mu.Lock()
	w.currentRound = roundID
	w.mu.Unlock()
}

// Subscribe registers fn to receive every Published Note. Subscribers fire
// SYNCHRONOUSLY inside Publish after the workspace mutex is released;
// subscribers MUST return within ~1ms or they will block subsequent
// publishes for all callers. For long-running consumers, use Subscribe to
// enqueue onto a channel and process asynchronously elsewhere.
//
// The returned cancel closure removes fn from the subscriber set in O(1).
// It is safe to call cancel multiple times: the second and subsequent
// calls are no-ops. cancel is safe to call from any goroutine, including
// from inside fn itself, because it acquires the same write mutex that
// Publish releases before invoking subscribers.
func (w *Workspace) Subscribe(fn func(Note)) (cancel func()) {
	w.mu.Lock()
	key := w.subsSeq
	w.subsSeq++
	if w.subs == nil {
		w.subs = make(map[uint64]func(Note))
	}
	w.subs[key] = fn
	w.mu.Unlock()

	return func() {
		w.mu.Lock()
		delete(w.subs, key)
		w.mu.Unlock()
	}
}

// Publish validates the supplied Note, assigns it a Workspace-monotonic
// ID, stamps EmittedAt and Round, appends it to the Workspace, persists
// it through the durable bus (TASK-22), and finally fans the Note out to
// every registered subscriber and the event hub.
//
// Per spec item 4: the write-side mutex is acquired only across the
// validate/assign/append/persist critical section. Hub emission and
// subscriber fan-out happen *after* the lock has been released so that
// downstream handlers cannot deadlock callers blocked behind the same
// mutex (e.g. a subscriber calling Snapshot).
//
// The first published Note in a Workspace receives ID "note-0"; the
// counter is post-incremented after the assignment so that seq always
// equals len(notes) after a successful Publish.
func (w *Workspace) Publish(n Note) error {
	if err := n.Validate(); err != nil {
		return err
	}

	w.mu.Lock()
	n.ID = fmt.Sprintf("note-%d", w.seq)
	w.seq++
	if n.EmittedAt.IsZero() {
		n.EmittedAt = time.Now().UTC()
	}
	n.Round = w.currentRound
	w.notes = append(w.notes, n)
	if err := writeNote(w.durable, n); err != nil {
		w.mu.Unlock()
		return err
	}
	subs := make([]func(Note), 0, len(w.subs))
	for _, fn := range w.subs {
		subs = append(subs, fn)
	}
	w.mu.Unlock()

	// TASK-7: Spotlight update happens AFTER persistence and BEFORE the
	// subs fan-out. maybeUpdate is responsible for emitting its own
	// "cortex.spotlight.changed" event when an upgrade occurs.
	if w.spotlight != nil {
		w.spotlight.maybeUpdate(n)
	}

	if w.events != nil {
		w.events.EmitAsync(&hub.Event{
			Type:   hub.EventCortexNotePublished,
			Custom: map[string]any{"note": n},
		})
	}
	for _, sub := range subs {
		sub(n)
	}
	return nil
}

// Snapshot returns a deep copy of every Note currently stored in the
// Workspace. The caller is free to mutate the returned slice or any
// element header without affecting the Workspace's internal state,
// because each Note is a value type that is copied by the builtin copy.
//
// Per spec item 5: readers acquire RLock so concurrent Publishers (which
// take the write lock) cannot observe a torn slice header. Returning a
// fresh slice means callers can sort, filter, or truncate the result
// without coordinating with other readers.
func (w *Workspace) Snapshot() []Note {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]Note, len(w.notes))
	copy(out, w.notes)
	return out
}

// UnresolvedCritical returns the subset of Notes with Severity==SevCritical
// whose ID is not referenced by any later Note's Resolves field. The
// PreEndTurnCheckFn (TASK-9) consults this list to refuse end_turn until
// the model addresses every outstanding critical concern.
//
// Resolution is direction-agnostic in storage but order-sensitive in
// intent: a follow-on Note declares Resolves=parentID, so any Note whose
// Resolves field is non-empty is treated as resolving its parent. We
// build the resolved-ID set in a single pass over w.notes and then
// filter critical Notes against it.
func (w *Workspace) UnresolvedCritical() []Note {
	w.mu.RLock()
	defer w.mu.RUnlock()
	resolved := make(map[string]bool, len(w.notes))
	for _, n := range w.notes {
		if n.Resolves != "" {
			resolved[n.Resolves] = true
		}
	}
	out := make([]Note, 0)
	for _, n := range w.notes {
		if n.Severity == SevCritical && !resolved[n.ID] {
			out = append(out, n)
		}
	}
	return out
}

// Drain returns every Note whose Round >= sinceRound and advances the
// internal drainedUpTo cursor to sinceRound+1 (clamped non-decreasing).
// MidturnCheckFn (TASK-9) calls Drain to format the supervisor injection
// block: each turn drains everything emitted in the round just past so
// the next prompt sees fresh Notes exactly once.
//
// Drain takes the write lock because it mutates drainedUpTo. The returned
// slice is freshly allocated; callers may mutate it freely.
func (w *Workspace) Drain(sinceRound uint64) ([]Note, uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]Note, 0)
	for _, n := range w.notes {
		if n.Round >= sinceRound {
			out = append(out, n)
		}
	}
	if next := sinceRound + 1; next > w.drainedUpTo {
		w.drainedUpTo = next
	}
	return out, w.drainedUpTo
}

// severityRank maps a Severity to a numeric ordering. Higher values rank
// higher: SevCritical(4) > SevWarning(3) > SevAdvice(2) > SevInfo(1).
// Unknown severities collapse to 0, which intentionally never out-ranks
// any well-formed Note (Validate would have rejected such a Note before
// publication, so this branch is defensive only).
func severityRank(s Severity) int {
	switch s {
	case SevCritical:
		return 4
	case SevWarning:
		return 3
	case SevAdvice:
		return 2
	case SevInfo:
		return 1
	default:
		return 0
	}
}

// Spotlight tracks the single "loudest" unresolved Note in a Workspace.
// At each Publish the Workspace calls maybeUpdate, which compares the new
// Note against the current spotlight and upgrades when the new Note ranks
// higher (Critical > Warning > Advice > Info; ties broken by EmittedAt
// desc) OR when the current spotlight has been resolved by a follow-on
// Note.
//
// The TUI/UI surface reads Current() to display the most pressing concern
// the model should address; the orchestrator (TASK-25 integration) wires
// Subscribe to react when the spotlight changes.
//
// Concurrency: a single sync.Mutex guards every field. The hub emit and
// subscriber fan-out are performed AFTER the lock has been released so
// downstream handlers cannot deadlock callers blocked behind the same
// mutex (mirrors the Workspace.Publish pattern).
type Spotlight struct {
	mu          sync.Mutex
	current     Note
	subs        map[uint64]func(Note)
	subsSeq     uint64
	bus         *hub.Bus
	resolvedIDs map[string]bool
}

// NewSpotlight constructs a Spotlight bound to the given event bus. The
// bus argument may be nil, in which case Spotlight upgrades emit no hub
// events but still fan out to subscribers registered via Subscribe.
func NewSpotlight(bus *hub.Bus) *Spotlight {
	return &Spotlight{
		subs:        make(map[uint64]func(Note)),
		bus:         bus,
		resolvedIDs: make(map[string]bool),
	}
}

// Current returns a copy of the Note currently held in the spotlight.
// If no Note has ever been spotlighted, the zero-value Note is returned.
// The returned value is a copy: callers may mutate it without affecting
// internal state.
func (sp *Spotlight) Current() Note {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return sp.current
}

// Subscribe registers fn to receive every spotlight Note that becomes the
// new current value (i.e. fires only on upgrade, not on every Publish).
// Subscribers fire SYNCHRONOUSLY after the Spotlight mutex is released;
// they MUST return promptly or they will block subsequent Publishes for
// all callers.
//
// The returned cancel closure removes fn from the subscriber set in O(1).
// It is safe to call cancel multiple times: the second and subsequent
// calls are no-ops.
func (sp *Spotlight) Subscribe(fn func(Note)) (cancel func()) {
	sp.mu.Lock()
	key := sp.subsSeq
	sp.subsSeq++
	if sp.subs == nil {
		sp.subs = make(map[uint64]func(Note))
	}
	sp.subs[key] = fn
	sp.mu.Unlock()

	return func() {
		sp.mu.Lock()
		delete(sp.subs, key)
		sp.mu.Unlock()
	}
}

// maybeUpdate is invoked by Workspace.Publish after persistence. It
// implements the spotlight upgrade rules:
//
//  1. If n.Resolves is non-empty, mark that ID as resolved. If the
//     currently-spotlighted Note was the one being resolved, treat the
//     spotlight as empty so the new Note (or any future candidate) can
//     take its place.
//  2. Compute candidate-vs-current rank:
//     - higher rank wins outright;
//     - same rank: newer EmittedAt wins (ties broken by emit order in
//       practice because Workspace stamps EmittedAt monotonically);
//     - if the current spotlight is now resolved, the new candidate
//       takes over regardless of rank, provided n itself is unresolved
//       (a resolution Note can carry SevInfo and still legitimately
//       become the spotlight as the "next-best unresolved" Note).
//
// On upgrade the previous and new IDs are captured; after the lock is
// released we emit "cortex.spotlight.changed" via the hub (if non-nil)
// and fan out to subscribers synchronously.
func (sp *Spotlight) maybeUpdate(n Note) {
	sp.mu.Lock()

	// Step 1: track resolutions.
	if n.Resolves != "" {
		sp.resolvedIDs[n.Resolves] = true
	}

	// Decide whether to upgrade. Three independent conditions can trigger
	// an upgrade; we evaluate them in order of decreasing strength.
	currentResolved := sp.current.ID != "" && sp.resolvedIDs[sp.current.ID]
	newRank := severityRank(n.Severity)
	curRank := severityRank(sp.current.Severity)

	upgrade := false
	switch {
	case sp.current.ID == "":
		// Spotlight is empty (no Note has ever held it). Take the new one.
		upgrade = true
	case currentResolved:
		// The current spotlight has been resolved out from under us. The
		// new Note becomes the spotlight as long as it itself is not the
		// one that resolved an already-empty pointer (n.Resolves field
		// targets some other Note, not n.ID, so this is safe).
		upgrade = true
	case newRank > curRank:
		upgrade = true
	case newRank == curRank && n.EmittedAt.After(sp.current.EmittedAt):
		upgrade = true
	}

	if !upgrade {
		sp.mu.Unlock()
		return
	}

	prev := sp.current.ID
	sp.current = n

	// Snapshot subscribers under the lock; fire after release.
	subs := make([]func(Note), 0, len(sp.subs))
	for _, fn := range sp.subs {
		subs = append(subs, fn)
	}
	bus := sp.bus
	sp.mu.Unlock()

	if bus != nil {
		bus.EmitAsync(&hub.Event{
			Type: hub.EventCortexSpotlightChanged,
			Custom: map[string]any{
				"from": prev,
				"to":   n.ID,
				"note": n,
			},
		})
	}
	for _, sub := range subs {
		sub(n)
	}
}
