package loops

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
)

func newTestLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "ledger")
	l, err := ledger.New(root)
	if err != nil {
		t.Fatalf("New ledger: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

// createLoop creates a loop node in the ledger and returns its ID.
func createLoop(t *testing.T, l *ledger.Ledger, missionID string, lt LoopType, state LoopState, artifactID string, partners []string, parentLoopID string) string {
	t.Helper()
	ctx := context.Background()

	lc := loopContent{
		State:            state,
		LoopType:         lt,
		ArtifactID:       artifactID,
		ParentLoopID:     parentLoopID,
		ConvenedPartners: partners,
	}
	content, err := json.Marshal(lc)
	if err != nil {
		t.Fatalf("marshal loop content: %v", err)
	}

	node := ledger.Node{
		Type:          "loop",
		SchemaVersion: 1,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     "supervisor",
		MissionID:     missionID,
		Content:       content,
	}
	id, err := l.AddNode(ctx, node)
	if err != nil {
		t.Fatalf("AddNode loop: %v", err)
	}
	return id
}

// createDraft creates a draft node referencing a loop.
func createDraft(t *testing.T, l *ledger.Ledger, loopID string) string {
	t.Helper()
	ctx := context.Background()

	content, _ := json.Marshal(map[string]string{
		"draft_type":        "prd",
		"loop_ref":          loopID,
		"proposing_stance":  "architect",
		"body":              "draft content",
	})

	node := ledger.Node{
		Type:          "draft",
		SchemaVersion: 1,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     "architect",
		Content:       content,
	}
	id, err := l.AddNode(ctx, node)
	if err != nil {
		t.Fatalf("AddNode draft: %v", err)
	}
	return id
}

// createAgree creates an agree node referencing a draft.
func createAgree(t *testing.T, l *ledger.Ledger, draftID, stanceID string) string {
	t.Helper()
	ctx := context.Background()

	content, _ := json.Marshal(map[string]string{
		"draft_ref":          draftID,
		"agreeing_stance_id": stanceID,
		"reasoning":          "looks good",
	})

	node := ledger.Node{
		Type:          "agree",
		SchemaVersion: 1,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     stanceID,
		Content:       content,
	}
	id, err := l.AddNode(ctx, node)
	if err != nil {
		t.Fatalf("AddNode agree: %v", err)
	}

	// agree references the draft
	err = l.AddEdge(ctx, ledger.Edge{
		From: id,
		To:   draftID,
		Type: ledger.EdgeReferences,
	})
	if err != nil {
		t.Fatalf("AddEdge agree->draft: %v", err)
	}
	return id
}

// createDissent creates a dissent node referencing a draft.
func createDissent(t *testing.T, l *ledger.Ledger, draftID, stanceID string) string {
	t.Helper()
	ctx := context.Background()

	content, _ := json.Marshal(map[string]string{
		"draft_ref":            draftID,
		"dissenting_stance_id": stanceID,
		"reasoning":            "needs changes",
		"requested_change":     "fix section 3",
		"severity":             "blocking",
	})

	node := ledger.Node{
		Type:          "dissent",
		SchemaVersion: 1,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     stanceID,
		Content:       content,
	}
	id, err := l.AddNode(ctx, node)
	if err != nil {
		t.Fatalf("AddNode dissent: %v", err)
	}

	err = l.AddEdge(ctx, ledger.Edge{
		From: id,
		To:   draftID,
		Type: ledger.EdgeReferences,
	})
	if err != nil {
		t.Fatalf("AddEdge dissent->draft: %v", err)
	}
	return id
}

func TestGetLoopState(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	tr := NewTracker(l)

	draftID := createDraft(t, l, "")
	loopID := createLoop(t, l, "mission-1", LoopTypePRD, StateProposing, draftID, []string{"reviewer-1"}, "")

	info, err := tr.Get(ctx, loopID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info.ID != loopID {
		t.Errorf("expected ID %q, got %q", loopID, info.ID)
	}
	if info.State != StateProposing {
		t.Errorf("expected state %q, got %q", StateProposing, info.State)
	}
	if info.Type != LoopTypePRD {
		t.Errorf("expected type %q, got %q", LoopTypePRD, info.Type)
	}
	if info.MissionID != "mission-1" {
		t.Errorf("expected mission %q, got %q", "mission-1", info.MissionID)
	}
	if info.ArtifactID != draftID {
		t.Errorf("expected artifact %q, got %q", draftID, info.ArtifactID)
	}
}

func TestFullLifecycleTransition(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	tr := NewTracker(l)

	draftID := createDraft(t, l, "")
	loopID := createLoop(t, l, "mission-1", LoopTypePRD, StateProposing, draftID, []string{"reviewer-1"}, "")

	// proposing -> drafted
	if err := tr.TransitionState(ctx, loopID, StateDrafted, "draft complete"); err != nil {
		t.Fatalf("transition to drafted: %v", err)
	}
	info, _ := tr.Get(ctx, loopID)
	if info.State != StateDrafted {
		t.Fatalf("expected drafted, got %s", info.State)
	}

	// drafted -> convening
	if err := tr.TransitionState(ctx, loopID, StateConvening, "inviting reviewers"); err != nil {
		t.Fatalf("transition to convening: %v", err)
	}
	info, _ = tr.Get(ctx, loopID)
	if info.State != StateConvening {
		t.Fatalf("expected convening, got %s", info.State)
	}

	// convening -> reviewing
	if err := tr.TransitionState(ctx, loopID, StateReviewing, "all reviewers joined"); err != nil {
		t.Fatalf("transition to reviewing: %v", err)
	}
	info, _ = tr.Get(ctx, loopID)
	if info.State != StateReviewing {
		t.Fatalf("expected reviewing, got %s", info.State)
	}

	// reviewing -> converged
	if err := tr.TransitionState(ctx, loopID, StateConverged, "all agree"); err != nil {
		t.Fatalf("transition to converged: %v", err)
	}
	info, _ = tr.Get(ctx, loopID)
	if info.State != StateConverged {
		t.Fatalf("expected converged, got %s", info.State)
	}
}

func TestDissentLifecycle(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	tr := NewTracker(l)

	draftID := createDraft(t, l, "")
	loopID := createLoop(t, l, "mission-1", LoopTypeSOW, StateReviewing, draftID, []string{"r1", "r2"}, "")

	// reviewing -> resolving_dissents
	if err := tr.TransitionState(ctx, loopID, StateResolvingDissents, "dissent received"); err != nil {
		t.Fatalf("transition to resolving_dissents: %v", err)
	}
	info, _ := tr.Get(ctx, loopID)
	if info.State != StateResolvingDissents {
		t.Fatalf("expected resolving_dissents, got %s", info.State)
	}

	// resolving_dissents -> drafted (new draft produced)
	if err := tr.TransitionState(ctx, loopID, StateDrafted, "revised draft"); err != nil {
		t.Fatalf("transition to drafted: %v", err)
	}
	info, _ = tr.Get(ctx, loopID)
	if info.State != StateDrafted {
		t.Fatalf("expected drafted, got %s", info.State)
	}

	// drafted -> reviewing
	if err := tr.TransitionState(ctx, loopID, StateReviewing, "re-review"); err != nil {
		t.Fatalf("transition to reviewing: %v", err)
	}
	info, _ = tr.Get(ctx, loopID)
	if info.State != StateReviewing {
		t.Fatalf("expected reviewing, got %s", info.State)
	}

	// reviewing -> converged
	if err := tr.TransitionState(ctx, loopID, StateConverged, "all agree on revision"); err != nil {
		t.Fatalf("transition to converged: %v", err)
	}
	info, _ = tr.Get(ctx, loopID)
	if info.State != StateConverged {
		t.Fatalf("expected converged, got %s", info.State)
	}
}

func TestIterationCountIncreases(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	tr := NewTracker(l)

	draftID := createDraft(t, l, "")
	loopID := createLoop(t, l, "mission-1", LoopTypePRD, StateProposing, draftID, []string{"r1"}, "")

	count, err := tr.IterationCount(ctx, loopID)
	if err != nil {
		t.Fatalf("IterationCount: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected initial iteration count 1, got %d", count)
	}

	// Each transition creates a new node in the supersedes chain.
	if err := tr.TransitionState(ctx, loopID, StateDrafted, "drafted"); err != nil {
		t.Fatalf("transition: %v", err)
	}
	count, err = tr.IterationCount(ctx, loopID)
	if err != nil {
		t.Fatalf("IterationCount: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected iteration count 2 after one transition, got %d", count)
	}

	if err := tr.TransitionState(ctx, loopID, StateConvening, "convening"); err != nil {
		t.Fatalf("transition: %v", err)
	}
	count, err = tr.IterationCount(ctx, loopID)
	if err != nil {
		t.Fatalf("IterationCount: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected iteration count 3 after two transitions, got %d", count)
	}
}

func TestIsConvergedWithDissents(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	tr := NewTracker(l)

	draftID := createDraft(t, l, "")
	loopID := createLoop(t, l, "mission-1", LoopTypePRD, StateReviewing, draftID, []string{"r1", "r2"}, "")

	// Add one agree and one dissent.
	createAgree(t, l, draftID, "r1")
	createDissent(t, l, draftID, "r2")

	converged, err := tr.IsConverged(ctx, loopID)
	if err != nil {
		t.Fatalf("IsConverged: %v", err)
	}
	if converged {
		t.Fatal("expected not converged with outstanding dissent")
	}
}

func TestIsConvergedAllAgree(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	tr := NewTracker(l)

	draftID := createDraft(t, l, "")
	loopID := createLoop(t, l, "mission-1", LoopTypePRD, StateReviewing, draftID, []string{"r1", "r2"}, "")

	createAgree(t, l, draftID, "r1")
	createAgree(t, l, draftID, "r2")

	converged, err := tr.IsConverged(ctx, loopID)
	if err != nil {
		t.Fatalf("IsConverged: %v", err)
	}
	if !converged {
		t.Fatal("expected converged when all partners agree")
	}
}

func TestChildrenAndParentChain(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	tr := NewTracker(l)

	draftID := createDraft(t, l, "")
	parentID := createLoop(t, l, "mission-1", LoopTypePRD, StateReviewing, draftID, []string{"r1"}, "")

	childDraftID := createDraft(t, l, "")
	childID := createLoop(t, l, "mission-1", LoopTypeTicket, StateProposing, childDraftID, []string{"r1"}, parentID)

	// Connect child to parent via extends edge.
	err := l.AddEdge(ctx, ledger.Edge{
		From: childID,
		To:   parentID,
		Type: ledger.EdgeExtends,
	})
	if err != nil {
		t.Fatalf("AddEdge extends: %v", err)
	}

	// Test Children.
	children, err := tr.Children(ctx, parentID)
	if err != nil {
		t.Fatalf("Children: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(children))
	}
	if children[0] != childID {
		t.Fatalf("expected child %q, got %q", childID, children[0])
	}

	// Test ParentChain.
	parents, err := tr.ParentChain(ctx, childID)
	if err != nil {
		t.Fatalf("ParentChain: %v", err)
	}
	if len(parents) != 1 {
		t.Fatalf("expected 1 parent, got %d", len(parents))
	}
	if parents[0] != parentID {
		t.Fatalf("expected parent %q, got %q", parentID, parents[0])
	}
}

func TestActiveLoopsFiltersTerminal(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	tr := NewTracker(l)

	draftID1 := createDraft(t, l, "")
	createLoop(t, l, "mission-1", LoopTypePRD, StateReviewing, draftID1, []string{"r1"}, "")

	draftID2 := createDraft(t, l, "")
	createLoop(t, l, "mission-1", LoopTypeSOW, StateConverged, draftID2, []string{"r1"}, "")

	draftID3 := createDraft(t, l, "")
	createLoop(t, l, "mission-1", LoopTypeTicket, StateEscalated, draftID3, []string{"r1"}, "")

	draftID4 := createDraft(t, l, "")
	createLoop(t, l, "mission-1", LoopTypeRefactor, StateProposing, draftID4, []string{"r1"}, "")

	active, err := tr.ActiveLoops(ctx, "mission-1")
	if err != nil {
		t.Fatalf("ActiveLoops: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("expected 2 active loops, got %d", len(active))
	}

	for _, a := range active {
		if terminalStates[a.State] {
			t.Errorf("terminal loop %s (state %s) should not be in active list", a.ID, a.State)
		}
	}
}

func TestTransitionStateWritesCorrectNodes(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	tr := NewTracker(l)

	draftID := createDraft(t, l, "")
	loopID := createLoop(t, l, "mission-1", LoopTypePRD, StateProposing, draftID, []string{"r1"}, "")

	if err := tr.TransitionState(ctx, loopID, StateDrafted, "draft ready"); err != nil {
		t.Fatalf("TransitionState: %v", err)
	}

	// The original node should resolve to the new state.
	resolved, err := l.Resolve(ctx, loopID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if resolved.ID == loopID {
		t.Fatal("expected resolve to return a different node after transition")
	}
	if resolved.Type != "loop" {
		t.Fatalf("expected type loop, got %q", resolved.Type)
	}

	var lc loopContent
	if err := json.Unmarshal(resolved.Content, &lc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if lc.State != StateDrafted {
		t.Fatalf("expected state %q, got %q", StateDrafted, lc.State)
	}
	if lc.Reason != "draft ready" {
		t.Fatalf("expected reason %q, got %q", "draft ready", lc.Reason)
	}
	if lc.ArtifactID != draftID {
		t.Fatalf("expected artifact preserved as %q, got %q", draftID, lc.ArtifactID)
	}
}

func TestGetAfterMultipleTransitions(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	tr := NewTracker(l)

	draftID := createDraft(t, l, "")
	loopID := createLoop(t, l, "mission-1", LoopTypeResearch, StateProposing, draftID, nil, "")

	transitions := []LoopState{StateDrafted, StateConvening, StateReviewing}
	for _, s := range transitions {
		if err := tr.TransitionState(ctx, loopID, s, "progressing"); err != nil {
			t.Fatalf("TransitionState to %s: %v", s, err)
		}
	}

	info, err := tr.Get(ctx, loopID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info.State != StateReviewing {
		t.Fatalf("expected final state reviewing, got %s", info.State)
	}
	if info.ID != loopID {
		t.Fatalf("expected original ID %q preserved, got %q", loopID, info.ID)
	}
}
