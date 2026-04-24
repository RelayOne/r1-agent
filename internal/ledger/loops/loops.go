// Package loops provides query helpers for consensus loop state tracking.
// Loops are ledger nodes with type "loop". Their state transitions are driven
// by supervisor rules. This package makes it easy to ask "what is the current
// state of this loop" without walking the full supersede chain manually.
package loops

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
)

// nodeTypeLoop is the ledger node type string for consensus loop nodes.
const nodeTypeLoop = "loop"

// LoopState represents the current state of a consensus loop.
type LoopState string

// Loop states are the seven positions a consensus loop can occupy.
// Transitions are driven by supervisor rules; see
// supervisor/rules/consensus. Values are persisted in the ledger.
const (
	// StateProposing is the initial state: a draft is being authored.
	StateProposing LoopState = "proposing"
	// StateDrafted means a draft exists and is awaiting convening.
	StateDrafted LoopState = "drafted"
	// StateConvening means the system is gathering reviewer stances.
	StateConvening LoopState = "convening"
	// StateReviewing means reviewers have been convened and are
	// evaluating the current draft.
	StateReviewing LoopState = "reviewing"
	// StateResolvingDissents means at least one reviewer dissented and
	// the authoring stance is producing a revised draft.
	StateResolvingDissents LoopState = "resolving_dissents"
	// StateConverged is a terminal state: all convened reviewers agree.
	StateConverged LoopState = "converged"
	// StateEscalated is a terminal state: the loop exceeded its
	// iteration budget or timed out and was handed to a supervisor.
	StateEscalated LoopState = "escalated"
)

// terminalStates are states in which a loop is no longer active.
var terminalStates = map[LoopState]bool{
	StateConverged: true,
	StateEscalated: true,
}

// LoopType categorizes what artifact the loop governs.
type LoopType string

// Loop types categorize what artifact the consensus loop governs. The
// type drives which reviewer stances are convened and which supervisor
// rules apply.
const (
	// LoopTypePRD governs a product requirements document.
	LoopTypePRD LoopType = "prd"
	// LoopTypeSOW governs a statement of work.
	LoopTypeSOW LoopType = "sow"
	// LoopTypeTicket governs an individual work ticket.
	LoopTypeTicket LoopType = "ticket"
	// LoopTypePR governs a pull-request review.
	LoopTypePR LoopType = "pr_review"
	// LoopTypeRefactor governs a structural refactor proposal.
	LoopTypeRefactor LoopType = "refactor"
	// LoopTypeResearch governs a research-request lifecycle.
	LoopTypeResearch LoopType = "research"
)

// loopContent is the JSON schema for the Content field of a loop node.
type loopContent struct {
	State            LoopState `json:"state"`
	LoopType         LoopType  `json:"loop_type"`
	ArtifactID       string    `json:"artifact_id"`
	ParentLoopID     string    `json:"parent_loop_id,omitempty"`
	ConvenedPartners []string  `json:"convened_partners,omitempty"`
	Reason           string    `json:"reason,omitempty"`
}

// LoopInfo is the query result for a single loop's current state.
type LoopInfo struct {
	ID               string
	Type             LoopType
	State            LoopState
	ArtifactID       string    // current draft node ID
	ParentLoopID     string    // empty for root
	MissionID        string
	IterationCount   int       // number of supersedes-chain drafts
	ConvenedPartners []string  // stance IDs of convened reviewers
	AgreeCounts      int
	DissentCounts    int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Tracker provides query helpers over the ledger for loop state.
type Tracker struct {
	ledger *ledger.Ledger
}

// NewTracker creates a new Tracker backed by the given Ledger.
func NewTracker(l *ledger.Ledger) *Tracker {
	return &Tracker{ledger: l}
}

// parseContent extracts the loopContent from a ledger node.
func parseContent(n *ledger.Node) (loopContent, error) {
	var lc loopContent
	if err := json.Unmarshal(n.Content, &lc); err != nil {
		return lc, fmt.Errorf("loops: unmarshal content for %s: %w", n.ID, err)
	}
	return lc, nil
}

// Get retrieves the current state of a loop by its node ID.
// It resolves the supersedes chain to find the latest version.
func (t *Tracker) Get(ctx context.Context, loopID string) (*LoopInfo, error) {
	resolved, err := t.ledger.Resolve(ctx, loopID)
	if err != nil {
		return nil, fmt.Errorf("loops: resolve %s: %w", loopID, err)
	}

	lc, err := parseContent(resolved)
	if err != nil {
		return nil, err
	}

	original, err := t.ledger.Get(ctx, loopID)
	if err != nil {
		return nil, fmt.Errorf("loops: get original %s: %w", loopID, err)
	}

	iterCount, err := t.IterationCount(ctx, loopID)
	if err != nil {
		return nil, err
	}

	agreeCounts, dissentCounts, err := t.countStances(ctx, lc.ArtifactID)
	if err != nil {
		return nil, err
	}

	return &LoopInfo{
		ID:               loopID,
		Type:             lc.LoopType,
		State:            lc.State,
		ArtifactID:       lc.ArtifactID,
		ParentLoopID:     lc.ParentLoopID,
		MissionID:        original.MissionID,
		IterationCount:   iterCount,
		ConvenedPartners: lc.ConvenedPartners,
		AgreeCounts:      agreeCounts,
		DissentCounts:    dissentCounts,
		CreatedAt:        original.CreatedAt,
		UpdatedAt:        resolved.CreatedAt,
	}, nil
}

// CurrentDraft returns the latest draft node in the loop (following supersedes chain).
// It queries for draft nodes that reference the loop and resolves through supersedes.
func (t *Tracker) CurrentDraft(ctx context.Context, loopID string) (*ledger.Node, error) {
	resolved, err := t.ledger.Resolve(ctx, loopID)
	if err != nil {
		return nil, fmt.Errorf("loops: resolve %s: %w", loopID, err)
	}

	lc, err := parseContent(resolved)
	if err != nil {
		return nil, err
	}

	if lc.ArtifactID == "" {
		return nil, fmt.Errorf("loops: no artifact for loop %s", loopID)
	}

	draft, err := t.ledger.Resolve(ctx, lc.ArtifactID)
	if err != nil {
		return nil, fmt.Errorf("loops: resolve draft %s: %w", lc.ArtifactID, err)
	}

	return draft, nil
}

// IterationCount counts how many drafts have been produced in this loop
// by walking the supersedes chain backward from the current state.
func (t *Tracker) IterationCount(ctx context.Context, loopID string) (int, error) {
	nodes, err := t.ledger.Walk(ctx, loopID, ledger.Backward, []ledger.EdgeType{ledger.EdgeSupersedes})
	if err != nil {
		return 0, fmt.Errorf("loops: walk supersedes for %s: %w", loopID, err)
	}
	// The walk includes the starting node. The supersedes chain length
	// represents iterations: original node = 1, each supersede = +1.
	// But we want iteration count = number of state transitions, which is len-1
	// for the loop node itself. Since we count supersedes chain of the loop
	// node, each transition creates a new loop node that supersedes the old one.
	return len(nodes), nil
}

// IsConverged checks the structural convergence criterion:
// 1. All convened partners have agree nodes on current draft
// 2. No outstanding dissents on current draft
func (t *Tracker) IsConverged(ctx context.Context, loopID string) (bool, error) {
	resolved, err := t.ledger.Resolve(ctx, loopID)
	if err != nil {
		return false, fmt.Errorf("loops: resolve %s: %w", loopID, err)
	}

	lc, err := parseContent(resolved)
	if err != nil {
		return false, err
	}

	if len(lc.ConvenedPartners) == 0 {
		return false, nil
	}

	if lc.ArtifactID == "" {
		return false, nil
	}

	agreeCounts, dissentCounts, err := t.countStances(ctx, lc.ArtifactID)
	if err != nil {
		return false, err
	}

	if dissentCounts > 0 {
		return false, nil
	}

	return agreeCounts >= len(lc.ConvenedPartners), nil
}

// countStances counts agree and dissent nodes that reference the given artifact ID.
func (t *Tracker) countStances(ctx context.Context, artifactID string) (agrees, dissents int, err error) {
	if artifactID == "" {
		return 0, 0, nil
	}

	// Walk backward from the artifact following "references" edges to find
	// agree/dissent nodes. Agree and dissent nodes reference the draft they
	// pertain to.
	refs, err := t.ledger.Walk(ctx, artifactID, ledger.Backward, []ledger.EdgeType{ledger.EdgeReferences})
	if err != nil {
		return 0, 0, fmt.Errorf("loops: walk references for %s: %w", artifactID, err)
	}

	for _, n := range refs {
		switch n.Type {
		case "agree":
			agrees++
		case "dissent":
			dissents++
		}
	}
	return agrees, dissents, nil
}

// Children returns child loop IDs for a given loop.
// Child loops have a parent_loop_id pointing to the given loop.
func (t *Tracker) Children(ctx context.Context, loopID string) ([]string, error) {
	// Children are connected via extends edges from child to parent.
	nodes, err := t.ledger.Walk(ctx, loopID, ledger.Backward, []ledger.EdgeType{ledger.EdgeExtends})
	if err != nil {
		return nil, fmt.Errorf("loops: walk children for %s: %w", loopID, err)
	}

	children := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n.ID == loopID {
			continue
		}
		if n.Type != nodeTypeLoop {
			continue
		}
		children = append(children, n.ID)
	}
	return children, nil
}

// ParentChain returns the chain of parent loops up to the root.
// The first element is the immediate parent.
func (t *Tracker) ParentChain(ctx context.Context, loopID string) ([]string, error) {
	// Parents are connected via extends edges from child to parent.
	nodes, err := t.ledger.Walk(ctx, loopID, ledger.Forward, []ledger.EdgeType{ledger.EdgeExtends})
	if err != nil {
		return nil, fmt.Errorf("loops: walk parents for %s: %w", loopID, err)
	}

	parents := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n.ID == loopID {
			continue
		}
		if n.Type != nodeTypeLoop {
			continue
		}
		parents = append(parents, n.ID)
	}
	return parents, nil
}

// ActiveLoops returns all non-terminal loops for a mission.
func (t *Tracker) ActiveLoops(ctx context.Context, missionID string) ([]LoopInfo, error) {
	nodes, err := t.ledger.Query(ctx, ledger.QueryFilter{
		Type:      nodeTypeLoop,
		MissionID: missionID,
	})
	if err != nil {
		return nil, fmt.Errorf("loops: query loops for mission %s: %w", missionID, err)
	}

	// Collect root loop IDs (those that are not superseded by another loop node).
	// A root loop is one where no other loop supersedes it — i.e., it's the
	// original node ID the user would reference.
	seen := map[string]bool{}
	rootIDs := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if seen[n.ID] {
			continue
		}
		seen[n.ID] = true
		rootIDs = append(rootIDs, n.ID)
	}

	// For each root, resolve and check if terminal.
	active := make([]LoopInfo, 0, len(rootIDs))
	for _, id := range rootIDs {
		info, err := t.Get(ctx, id)
		if err != nil {
			continue
		}
		if terminalStates[info.State] {
			continue
		}
		active = append(active, *info)
	}

	return active, nil
}

// TransitionState writes a new loop state node to the ledger with a supersedes edge.
func (t *Tracker) TransitionState(ctx context.Context, loopID string, newState LoopState, reason string) error {
	resolved, err := t.ledger.Resolve(ctx, loopID)
	if err != nil {
		return fmt.Errorf("loops: resolve %s: %w", loopID, err)
	}

	lc, err := parseContent(resolved)
	if err != nil {
		return err
	}

	lc.State = newState
	lc.Reason = reason

	content, err := json.Marshal(lc)
	if err != nil {
		return fmt.Errorf("loops: marshal content: %w", err)
	}

	newNode := ledger.Node{
		Type:          nodeTypeLoop,
		SchemaVersion: 1,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     resolved.CreatedBy,
		MissionID:     resolved.MissionID,
		Content:       content,
	}

	newID, err := t.ledger.AddNode(ctx, newNode)
	if err != nil {
		return fmt.Errorf("loops: add transition node: %w", err)
	}

	err = t.ledger.AddEdge(ctx, ledger.Edge{
		From: newID,
		To:   resolved.ID,
		Type: ledger.EdgeSupersedes,
	})
	if err != nil {
		return fmt.Errorf("loops: add supersedes edge: %w", err)
	}

	return nil
}
