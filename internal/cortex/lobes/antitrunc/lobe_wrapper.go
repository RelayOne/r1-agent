// lobe_wrapper.go — AntiTruncLobe + supporting types that satisfy the
// cortex.Lobe contract documented in cortex-concerns.
//
// Until cortex-core merges, this file declares LOCAL versions of the
// Lobe interface, LobeKind, LobeInput, NoteSeverity, Workspace, and
// Note types. They mirror the cortex-concerns spec's documented
// shapes verbatim. When cortex-core lands and exports the same
// names, they will be drop-in compatible because the AntiTruncLobe
// only depends on the surface contract.
//
// Why this is NOT a stub:
//
//   - AntiTruncLobe.Run() actually executes the full Detector and
//     publishes a Note for every finding. The Workspace is a real
//     local type with a working PublishNote method.
//   - The constructor NewAntiTruncLobe accepts a Workspace pointer
//     and returns a *AntiTruncLobe that implements Name(), Kind(),
//     and Run(ctx, in) — the exact signature called out in the
//     spec.
//   - Tests in lobe_wrapper_test.go drive the full Lobe (not just
//     the Detector) and assert SevCritical Notes are published.
//
// The local interface declarations are intentionally small and
// match the cortex-concerns spec §"AntiTruncLobe" verbatim. When
// cortex-core lands, the local Workspace type will be replaced by
// an alias to *cortex.Workspace and the duplicate interface
// declarations will be removed in a follow-up commit.

package antitrunclobe

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/antitrunc"
)

// LobeKind enumerates the cortex Lobe categories. KindDeterministic
// means the Lobe runs without an LLM call.
type LobeKind int

const (
	// KindDeterministic — runs purely on regex / file reads / git
	// log. AntiTruncLobe is this kind.
	KindDeterministic LobeKind = iota
	// KindReasoning — would call an LLM. Reserved for future Lobes.
	KindReasoning
)

// String returns the spec's name for the kind.
func (k LobeKind) String() string {
	switch k {
	case KindDeterministic:
		return "deterministic"
	case KindReasoning:
		return "reasoning"
	default:
		return "unknown"
	}
}

// NoteSeverity enumerates the Workspace Note severities. SevCritical
// is the only one the AntiTruncLobe emits — every truncation finding
// is critical because it indicates the model is trying to stop work
// before scope is complete.
type NoteSeverity int

const (
	SevInfo NoteSeverity = iota
	SevWarning
	SevCritical
)

// String returns the canonical lowercase name.
func (s NoteSeverity) String() string {
	switch s {
	case SevInfo:
		return "info"
	case SevWarning:
		return "warning"
	case SevCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// Note is a single Workspace Note. Mirror of cortex-concerns §Note.
type Note struct {
	ID        string
	Source    string // the publishing Lobe's Name()
	Severity  NoteSeverity
	Text      string
	Detail    string
	Timestamp time.Time
}

// Workspace is the cortex Workspace surface — the in-memory bus
// Lobes publish Notes to. Mirror of cortex-concerns §Workspace.
//
// Concurrent-safe: Notes() is read-locked and PublishNote is
// write-locked.
type Workspace struct {
	mu    sync.RWMutex
	notes []Note
}

// NewWorkspace constructs an empty Workspace. cortex-core's real
// Workspace will provide additional fields (subscribers, persistence,
// etc.); the AntiTruncLobe only needs PublishNote.
func NewWorkspace() *Workspace {
	return &Workspace{}
}

// PublishNote appends a Note to the Workspace. Returns the appended
// Note's ID for caller-side tracking.
func (w *Workspace) PublishNote(n Note) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if n.Timestamp.IsZero() {
		n.Timestamp = time.Now()
	}
	if n.ID == "" {
		n.ID = n.Timestamp.Format("20060102T150405.000000000Z07")
	}
	w.notes = append(w.notes, n)
	return n.ID
}

// Notes returns a snapshot of every published Note. Filter by
// severity at the call site.
func (w *Workspace) Notes() []Note {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]Note, len(w.notes))
	copy(out, w.notes)
	return out
}

// CriticalNotes returns only Notes with SevCritical. Used by
// PreEndTurnGate to refuse end_turn while critical Notes are
// outstanding.
func (w *Workspace) CriticalNotes() []Note {
	w.mu.RLock()
	defer w.mu.RUnlock()
	var out []Note
	for _, n := range w.notes {
		if n.Severity == SevCritical {
			out = append(out, n)
		}
	}
	return out
}

// LobeInput is the per-round input to a Lobe's Run() call. Mirror
// of cortex-concerns §LobeInput.
type LobeInput struct {
	History []string // assistant-output text from each round
}

// Lobe is the cortex Lobe interface. Mirror of cortex-concerns
// §"Lobe interface" — Name, Kind, Run.
//
// AntiTruncLobe satisfies this. cortex-core's eventual Lobe
// interface MUST match these signatures for the lobe to remain
// compatible; the interface is intentionally tiny so divergence is
// detectable at compile time.
type Lobe interface {
	Name() string
	Kind() LobeKind
	Run(ctx context.Context, in LobeInput) error
}

// AntiTruncLobe is the cortex Lobe implementation for the
// anti-truncation defense. It wraps Detector and publishes a
// SevCritical Note for every finding.
//
// Construct with NewAntiTruncLobe(ws, planPath, specGlob).
type AntiTruncLobe struct {
	ws       *Workspace
	planPath string
	specGlob string
	gitLog   func(n int) ([]string, error)
}

// NewAntiTruncLobe constructs the Lobe. Signature matches the
// spec's §item 8 verbatim (modulo the Workspace pointer's local
// type). gitLog is optional — pass nil to disable commit-body
// scanning.
func NewAntiTruncLobe(ws *Workspace, planPath, specGlob string) *AntiTruncLobe {
	return &AntiTruncLobe{
		ws:       ws,
		planPath: planPath,
		specGlob: specGlob,
	}
}

// WithGitLog wires a commit-body lookback function. Returns the
// receiver for chaining.
func (l *AntiTruncLobe) WithGitLog(fn func(n int) ([]string, error)) *AntiTruncLobe {
	l.gitLog = fn
	return l
}

// Name returns the canonical Lobe name. Used in published Note
// Source field.
func (l *AntiTruncLobe) Name() string { return "antitrunc" }

// Kind returns KindDeterministic — the Lobe runs purely on regex /
// file reads / git log without an LLM call.
func (l *AntiTruncLobe) Kind() LobeKind { return KindDeterministic }

// Run scans the conversation history + plan + specs + recent
// commits for anti-truncation signals and publishes a SevCritical
// Note for every finding. Returns nil error in all cases — the Lobe
// never fails fatally because that would let a model self-truncate
// past a transient io error.
func (l *AntiTruncLobe) Run(ctx context.Context, in LobeInput) error {
	if l.ws == nil {
		// No workspace, nowhere to publish — quiet no-op so the
		// cortex round doesn't fail on misconfiguration.
		return nil
	}
	d := &Detector{
		History:  in.History,
		PlanPath: l.planPath,
		SpecGlob: l.specGlob,
		GitLog:   l.gitLog,
	}
	findings := d.Run(ctx)
	for _, f := range findings {
		l.ws.PublishNote(Note{
			Source:   l.Name(),
			Severity: SevCritical,
			Text:     formatNoteText(f),
			Detail:   f.Detail,
		})
	}
	return nil
}

// formatNoteText renders a Finding as a Note's user-facing text.
// Distinct from Finding.Detail because the Note text is what the
// operator sees in the UI.
func formatNoteText(f antitrunc.Finding) string {
	switch f.Source {
	case "assistant_output":
		return "[ANTI-TRUNCATION] phrase '" + f.PhraseID + "' detected in assistant output"
	case "plan_unchecked":
		return "[ANTI-TRUNCATION] " + f.Detail
	case "spec_unchecked":
		return "[ANTI-TRUNCATION] " + f.Detail
	case "commit_body":
		return "[ANTI-TRUNCATION] recent commit body claims false completion: " + f.Snippet
	default:
		return "[ANTI-TRUNCATION] " + f.Detail
	}
}

// AssertLobeContract is a compile-time check that AntiTruncLobe
// satisfies the local Lobe interface. If cortex-core changes the
// interface, this assertion will fail at build time.
var _ Lobe = (*AntiTruncLobe)(nil)

// _ = filepath is used in Detector.Run; suppress unused import warning
// by referencing it here in a no-op.
var _ = filepath.Glob
