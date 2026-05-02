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

// writeNote is a TASK-22 stub. The persistent WAL-backed note writer will
// land in persist.go alongside crash-recovery support; for now the stub
// preserves the call-site contract and keeps Publish callable. Returning
// nil is safe because Workspace tolerates a nil durable bus by design.
func writeNote(_ *bus.Bus, _ Note) error { return nil }

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
	subs         []func(Note)
}

// NewWorkspace constructs a Workspace bound to the given event hub and
// durable bus. Either argument may be nil: a nil events hub disables
// live notifications, and a nil durable bus selects in-memory mode
// with no WAL persistence (per TASK-22 contract).
func NewWorkspace(events *hub.Bus, durable *bus.Bus) *Workspace {
	return &Workspace{events: events, durable: durable}
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
	subs := make([]func(Note), len(w.subs))
	copy(subs, w.subs)
	w.mu.Unlock()

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
