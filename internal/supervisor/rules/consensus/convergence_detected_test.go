package consensus

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
)

// loopNodeContent mirrors the unified loop schema (nodes.Loop /
// loops.loopContent — audit A066): artifact_ref + convened_partners.
type loopNodeContent struct {
	State            string   `json:"state"`
	LoopType         string   `json:"loop_type"`
	ArtifactRef      string   `json:"artifact_ref"`
	ConvenedPartners []string `json:"convened_partners,omitempty"`
}

// buildLoopFixture creates a draft node, a loop node whose artifact_ref
// points at the draft, and one agree/dissent node per entry in stances
// (each connected to the draft via an EdgeReferences edge — the
// mechanism loops.Tracker.countStances walks). Returns the loop node ID.
func buildLoopFixture(t *testing.T, l *ledger.Ledger, partners []string, stances []string) string {
	t.Helper()
	ctx := context.Background()

	draftContent, _ := json.Marshal(map[string]string{"body": "draft"})
	draftID, err := l.AddNode(ctx, ledger.Node{
		Type: "draft", SchemaVersion: 1, CreatedBy: "architect", MissionID: "m1",
		Content: draftContent,
	})
	if err != nil {
		t.Fatalf("add draft: %v", err)
	}

	loopJSON, _ := json.Marshal(loopNodeContent{
		State: "reviewing", LoopType: "prd",
		ArtifactRef: draftID, ConvenedPartners: partners,
	})
	loopID, err := l.AddNode(ctx, ledger.Node{
		Type: "loop", SchemaVersion: 1, CreatedBy: "supervisor", MissionID: "m1",
		Content: loopJSON,
	})
	if err != nil {
		t.Fatalf("add loop: %v", err)
	}

	for i, stance := range stances {
		stanceContent, _ := json.Marshal(map[string]string{"draft_ref": draftID})
		stanceID, err := l.AddNode(ctx, ledger.Node{
			Type: stance, SchemaVersion: 1, CreatedBy: partners[i%len(partners)], MissionID: "m1",
			Content: stanceContent,
		})
		if err != nil {
			t.Fatalf("add %s: %v", stance, err)
		}
		if err := l.AddEdge(ctx, ledger.Edge{From: stanceID, To: draftID, Type: ledger.EdgeReferences}); err != nil {
			t.Fatalf("add edge: %v", err)
		}
	}
	return loopID
}

func nodeAddedEvent(t *testing.T, loopID string) bus.Event {
	t.Helper()
	payload, _ := json.Marshal(nodeAddedPayload{
		NodeID:   "node-1",
		NodeType: "agree",
		LoopID:   loopID,
	})
	return bus.Event{
		ID:        "evt-1",
		Type:      bus.EvtLedgerNodeAdded,
		Timestamp: time.Now(),
		EmitterID: "reviewer-1",
		Scope:     bus.Scope{MissionID: "m1", LoopID: loopID},
		Payload:   payload,
	}
}

func TestConvergenceDetected_Evaluate_AllAgreed(t *testing.T) {
	ctx := context.Background()
	l, err := ledger.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	loopID := buildLoopFixture(t, l, []string{"reviewer-1"}, []string{"agree"})

	fired, err := NewConvergenceDetected().Evaluate(ctx, nodeAddedEvent(t, loopID), l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fired {
		t.Fatal("expected rule to fire when all partners agreed")
	}
}

func TestConvergenceDetected_Evaluate_OutstandingDissent(t *testing.T) {
	ctx := context.Background()
	l, err := ledger.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	loopID := buildLoopFixture(t, l, []string{"reviewer-1", "reviewer-2"}, []string{"agree", "dissent"})

	fired, err := NewConvergenceDetected().Evaluate(ctx, nodeAddedEvent(t, loopID), l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fired {
		t.Fatal("expected rule NOT to fire with outstanding dissent")
	}
}

func TestConvergenceDetected_Evaluate_MissingPartnerAgreement(t *testing.T) {
	ctx := context.Background()
	l, err := ledger.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Two convened partners but only one agree node: not converged.
	loopID := buildLoopFixture(t, l, []string{"reviewer-1", "reviewer-2"}, []string{"agree"})

	fired, err := NewConvergenceDetected().Evaluate(ctx, nodeAddedEvent(t, loopID), l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fired {
		t.Fatal("expected rule NOT to fire until every convened partner agreed")
	}
}

func TestConvergenceDetected_Evaluate_UnknownLoop(t *testing.T) {
	ctx := context.Background()
	l, err := ledger.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	fired, err := NewConvergenceDetected().Evaluate(ctx, nodeAddedEvent(t, "loop-does-not-exist"), l)
	if err != nil {
		t.Fatalf("Evaluate should not error on unknown loop: %v", err)
	}
	if fired {
		t.Fatal("expected rule NOT to fire for an unknown loop")
	}
}

func TestConvergenceDetected_Action(t *testing.T) {
	dir := t.TempDir()
	b, err := bus.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewConvergenceDetected()

	payload, _ := json.Marshal(nodeAddedPayload{
		NodeID:   "node-1",
		NodeType: "review.agree",
		LoopID:   "loop-1",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      bus.EvtLedgerNodeAdded,
		Timestamp: time.Now(),
		EmitterID: "reviewer-1",
		Scope:     bus.Scope{MissionID: "m1", LoopID: "loop-1"},
		Payload:   payload,
	}

	var published []bus.Event
	var mu sync.Mutex
	b.Subscribe(bus.Pattern{}, func(e bus.Event) {
		mu.Lock()
		published = append(published, e)
		mu.Unlock()
	})

	err = rule.Action(context.Background(), evt, b)
	if err != nil {
		t.Fatalf("Action: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(published)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()

	if len(published) != 1 {
		t.Fatalf("expected 1 published event, got %d", len(published))
	}
	if published[0].Type != "consensus.loop.state.changed" {
		t.Errorf("event type = %s, want consensus.loop.state.changed", published[0].Type)
	}
}
