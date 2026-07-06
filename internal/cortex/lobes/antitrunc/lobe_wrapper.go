// lobe_wrapper.go — AntiTruncLobe satisfies the real cortex.Lobe contract.
//
// Until cortex-core merged this file declared LOCAL versions of the Lobe
// interface, LobeKind, LobeInput, NoteSeverity, Workspace, and Note types
// because the cortex package did not yet exist. Cortex-core has since
// landed (internal/cortex/lobe.go, internal/cortex/workspace.go), and
// keeping the shadow types around was a known drift hazard documented at
// audit/scan-governance-gaps.md item #2. This file now imports the real
// cortex package directly so that:
//
//   - The AntiTruncLobe satisfies cortex.Lobe and can be registered with
//     the production cortex.Cortex.
//   - Notes are published into the production cortex.Workspace and
//     therefore visible to cortex.PreEndTurnGate via UnresolvedCritical.
//   - cortex-side renames or type changes break this file at compile
//     time rather than silently diverging.
//
// The Detector itself is unchanged — it consumes []string and a few
// filesystem paths. AntiTruncLobe.Run extracts the []string from
// cortex.LobeInput.History (which is []agentloop.Message) by walking
// each assistant Message's text ContentBlocks.

package antitrunclobe

import (
	"context"
	"path/filepath"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/antitrunc"
	"github.com/RelayOne/r1/internal/cortex"
)

// AntiTruncLobe is the cortex Lobe implementation for the
// anti-truncation defense. It wraps Detector and publishes a
// SevCritical Note for every finding into a real cortex.Workspace.
//
// Construct with NewAntiTruncLobe(ws, planPath, specGlob).
type AntiTruncLobe struct {
	ws       *cortex.Workspace
	planPath string
	specGlob string
	gitLog   func(n int) ([]string, error)
}

// NewAntiTruncLobe constructs the Lobe. ws is the production
// cortex.Workspace the Lobe will Publish Notes into; nil disables
// publication (the Lobe still satisfies the interface and Run is a
// no-op so misconfiguration does not crash the cortex round). gitLog
// is optional — leave nil to disable commit-body scanning.
func NewAntiTruncLobe(ws *cortex.Workspace, planPath, specGlob string) *AntiTruncLobe {
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

// ID satisfies cortex.Lobe. Stable identifier used as LobeID on Notes.
func (l *AntiTruncLobe) ID() string { return "antitrunc" }

// Description satisfies cortex.Lobe.
func (l *AntiTruncLobe) Description() string {
	return "publishes critical Notes when the model attempts to truncate or stop work prematurely"
}

// Kind satisfies cortex.Lobe. Deterministic — runs purely on regex /
// file reads / git log without an LLM call.
func (l *AntiTruncLobe) Kind() cortex.LobeKind { return cortex.KindDeterministic }

// Run scans the conversation history + plan + specs + recent commits
// for anti-truncation signals and publishes a SevCritical Note for
// every finding. Returns nil error in all cases — the Lobe never fails
// fatally because that would let a model self-truncate past a
// transient io error.
func (l *AntiTruncLobe) Run(ctx context.Context, in cortex.LobeInput) error {
	if l.ws == nil {
		return nil
	}
	d := &Detector{
		History:  assistantTextHistory(in.History),
		PlanPath: l.planPath,
		SpecGlob: l.specGlob,
		GitLog:   l.gitLog,
	}
	findings := d.Run(ctx)

	// Dedupe against already-open findings. The anti-truncation Lobe
	// re-runs every cortex Round against the same conversation / plan /
	// spec / commit state, so a finding that is still open would be
	// re-published as a fresh SevCritical Note on every Round. Because
	// each Note gets a unique monotonic ID and nothing resolves them, the
	// workspace's UnresolvedCritical set (and the PreEndTurnGate that
	// reads it) would fill with escalating duplicates the loop can never
	// clear. Key each finding by its rendered (Title, Body) pair — the
	// same pair a prior Round would have produced — and skip any finding
	// that already has an unresolved critical Note from this Lobe. The
	// map is also consulted within this Round so a Detector that returns
	// the same finding twice publishes it only once.
	seen := make(map[string]bool)
	for _, n := range l.ws.UnresolvedCritical() {
		if n.LobeID == l.ID() {
			seen[n.Title+"\x00"+n.Body] = true
		}
	}

	for _, f := range findings {
		title := formatNoteTitle(f)
		key := title + "\x00" + f.Detail
		if seen[key] {
			continue
		}
		seen[key] = true
		_ = l.ws.Publish(cortex.Note{
			LobeID:    l.ID(),
			Severity:  cortex.SevCritical,
			Title:     title,
			Body:      f.Detail,
			Tags:      []string{"antitrunc", f.Source},
			EmittedAt: time.Now(),
			Round:     in.Round,
		})
	}
	return nil
}

// assistantTextHistory extracts the assistant-output text from the
// agentloop messages handed to Run. The Detector consumes []string so
// this is the bridge between the cortex.LobeInput contract and the
// existing Detector contract.
func assistantTextHistory(msgs []agentloop.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		for _, block := range m.Content {
			if block.Type == "text" && block.Text != "" {
				out = append(out, block.Text)
			}
		}
	}
	return out
}

// formatNoteTitle renders a Finding as a Note's user-facing Title
// (single-line, <=80 runes per cortex.Note.Validate). Detail goes into
// Body unchanged.
func formatNoteTitle(f antitrunc.Finding) string {
	const prefix = "[ANTI-TRUNCATION] "
	switch f.Source {
	case "assistant_output":
		return truncateRunes(prefix+"phrase '"+f.PhraseID+"' detected in assistant output", 80)
	case "plan_unchecked":
		return truncateRunes(prefix+f.Detail, 80)
	case "spec_unchecked":
		return truncateRunes(prefix+f.Detail, 80)
	case "commit_body":
		return truncateRunes(prefix+"recent commit body claims false completion", 80)
	default:
		return truncateRunes(prefix+f.Detail, 80)
	}
}

// truncateRunes returns at most n runes from s. cortex.Note.Validate
// rejects Titles >80 runes; this guard is defensive.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// AssertLobeContract is a compile-time check that AntiTruncLobe
// satisfies cortex.Lobe. If the cortex Lobe interface changes, this
// assertion will fail at build time so the wrapper cannot silently
// drift out of sync.
var _ cortex.Lobe = (*AntiTruncLobe)(nil)

// _ = filepath is used in Detector.Run; suppress unused import warning
// by referencing it here in a no-op.
var _ = filepath.Glob
