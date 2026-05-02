package cortex

import (
	"errors"
	"time"
	"unicode/utf8"
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
