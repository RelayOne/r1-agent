// Package artifact — poll.go
//
// Poller is the supervisor-side surface a worker calls at every safe
// point (next tool-call boundary) to read annotations that arrived since
// the last poll. It returns annotations grouped by action so the worker
// can dispatch:
//
//   - reject  → halt current step; emit error stance and stop
//   - amend   → read amendment; produce successor artifact via
//               Builder.Amend; continue from current step
//   - comment → log; continue
//   - accept  → mark artifact sealed; downstream gates may treat as
//               approved
//
// The poll loop is the answer to Antigravity's "Google-Doc-style inline
// feedback that gets incorporated live without restarting the run." R1's
// implementation is structurally better in three ways:
//
//   - polls are bounded: the worker reads at known safe points, not
//     mid-tool-call, so partial state never leaks
//   - annotations are append-only; consumed annotations are recorded via
//     a follow-up "consumed" annotation that cites the consumer's stance,
//     so the operator's contribution is itself a verifiable artifact
//   - the Constitution can refuse annotations that would violate
//     protected-path rules; an annotation amending r1.constitution.yaml
//     from an unprivileged annotator is rejected at read time and
//     surfaces as a constitutional violation event

package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/ledger/nodes"
)

// LedgerReader is the subset of *ledger.Ledger that Poller needs.
// Defining it as an interface keeps Poller testable without a full
// Ledger dependency tree.
type LedgerReader interface {
	QueryNodes(filter ledger.QueryFilter) ([]ledger.NodeID, error)
	ReadNode(id ledger.NodeID) (ledger.Node, error)
}

// Poller fetches new annotations for the artifacts a worker is
// responsible for.
type Poller struct {
	ledger LedgerReader

	// readWatermark records the most-recent annotation node CreatedAt
	// the worker has seen, per mission. Annotations created before or at
	// this watermark are not returned again. Concurrent missions don't
	// share watermarks; the map is keyed by missionID.
	//
	// Because annotations are immutable, a watermark is sufficient: a
	// new annotation has CreatedAt strictly after every prior CreatedAt
	// in the same mission (the ledger's monotonic clock guarantees this
	// per index ordering).
	watermarks map[string]time.Time
}

// NewPoller constructs a Poller against a ledger reader.
func NewPoller(l LedgerReader) *Poller {
	if l == nil {
		panic("artifact: NewPoller: ledger is nil")
	}
	return &Poller{
		ledger:     l,
		watermarks: make(map[string]time.Time),
	}
}

// Pending is the result of one Poll call. Annotations are pre-grouped
// by action so the worker can dispatch with a switch.
type Pending struct {
	Comments []nodes.ArtifactAnnotation // informational
	Rejects  []nodes.ArtifactAnnotation // halt and revise
	Accepts  []nodes.ArtifactAnnotation // sealed
	Amends   []nodes.ArtifactAnnotation // produce successor artifact
}

// HasAny returns true if any group is non-empty. Cheap pre-check before
// the worker spends cycles in any handler.
func (p Pending) HasAny() bool {
	return len(p.Comments) > 0 || len(p.Rejects) > 0 || len(p.Accepts) > 0 || len(p.Amends) > 0
}

// HasBlocking returns true if any annotation requires the worker to
// halt the current step (rejects). Used as a fast check at safe points.
func (p Pending) HasBlocking() bool {
	return len(p.Rejects) > 0
}

// Poll returns annotations for artifacts within the given mission that
// have not yet been seen by this Poller instance. After a successful
// Poll, the watermark advances; subsequent Poll calls return only
// strictly newer annotations.
//
// The artifactIDs filter scopes the result to specific artifacts. If
// empty, all annotations in the mission are returned.
func (p *Poller) Poll(ctx context.Context, missionID string, artifactIDs []string) (Pending, error) {
	if missionID == "" {
		return Pending{}, errors.New("artifact: Poll: missionID required")
	}

	// Build a fast-path ID filter set
	wanted := make(map[string]bool, len(artifactIDs))
	for _, id := range artifactIDs {
		wanted[id] = true
	}
	scopeAll := len(wanted) == 0

	prev := p.watermarks[missionID]

	since := prev
	filter := ledger.QueryFilter{
		Type:      "artifact_annotation",
		MissionID: missionID,
	}
	if !since.IsZero() {
		filter.Since = &since
	}

	ids, err := p.ledger.QueryNodes(filter)
	if err != nil {
		return Pending{}, fmt.Errorf("artifact: poll query: %w", err)
	}

	var anns []nodes.ArtifactAnnotation
	for _, id := range ids {
		n, err := p.ledger.ReadNode(id)
		if err != nil {
			return Pending{}, fmt.Errorf("artifact: read annotation %q: %w", id, err)
		}
		var ann nodes.ArtifactAnnotation
		if err := json.Unmarshal(n.Content, &ann); err != nil {
			return Pending{}, fmt.Errorf("artifact: unmarshal annotation %q: %w", id, err)
		}
		// Apply the artifact-ID filter
		if !scopeAll && !wanted[ann.ArtifactRef] {
			continue
		}
		// Strict-after check (handle the QueryFilter's Since semantics
		// being inclusive rather than exclusive)
		if !since.IsZero() && !n.CreatedAt.After(since) {
			continue
		}
		anns = append(anns, ann)
	}

	// Sort by When ascending so handlers process in order
	sort.SliceStable(anns, func(i, j int) bool {
		return anns[i].When.Before(anns[j].When)
	})

	pending := groupByAction(anns)

	// Advance the watermark to the latest annotation's When
	if len(anns) > 0 {
		latest := anns[len(anns)-1].When
		if latest.After(p.watermarks[missionID]) {
			p.watermarks[missionID] = latest
		}
	}
	return pending, nil
}

// PollAll is a convenience wrapper for "all annotations in mission, no
// artifact filter."
func (p *Poller) PollAll(ctx context.Context, missionID string) (Pending, error) {
	return p.Poll(ctx, missionID, nil)
}

// ResetWatermark forces the next Poll for the given mission to return
// every annotation since the start of time. Used by tests and by a
// future "operator wants to re-review everything" surface.
func (p *Poller) ResetWatermark(missionID string) {
	delete(p.watermarks, missionID)
}

// Watermark returns the current high-water mark for a mission. Useful
// for telemetry and for the dashboard's "last sync" indicator.
func (p *Poller) Watermark(missionID string) time.Time {
	return p.watermarks[missionID]
}

func groupByAction(anns []nodes.ArtifactAnnotation) Pending {
	var p Pending
	for _, a := range anns {
		switch a.Action {
		case "comment":
			p.Comments = append(p.Comments, a)
		case "reject":
			p.Rejects = append(p.Rejects, a)
		case "accept":
			p.Accepts = append(p.Accepts, a)
		case "amend":
			p.Amends = append(p.Amends, a)
		default:
			// Unknown action: surface as comment so it isn't silently
			// dropped. The validator would have rejected this at write
			// time, so this branch is defense-in-depth.
			p.Comments = append(p.Comments, a)
		}
	}
	return p
}
