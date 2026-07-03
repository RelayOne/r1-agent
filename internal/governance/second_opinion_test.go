package governance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/ledger"
)

// pollNodes queries the ledger for nodes of the given type until at
// least want exist or the deadline passes (rule dispatch is async).
func pollNodes(t *testing.T, g *Governor, nodeType string, want int) []ledger.Node {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		nodes, err := g.Ledger().Query(context.Background(), ledger.QueryFilter{Type: nodeType})
		if err != nil {
			t.Fatalf("ledger query %q: %v", nodeType, err)
		}
		if len(nodes) >= want || time.Now().After(deadline) {
			return nodes
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitEvents(snapshot func() []bus.Event, want int) []bus.Event {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if evs := snapshot(); len(evs) >= want {
			return evs
		}
		time.Sleep(10 * time.Millisecond)
	}
	return snapshot()
}

// TestGovernorSecondOpinionDissentWakesConsensus drives a blocking
// second-opinion dissent through the hub bridge and asserts the FULL
// consensus chain fires: dissent ledger node -> ledger.node.added bus
// event (first production publisher) -> DissentRequiresAddress ->
// loop state resolving_dissents + dissent notification.
func TestGovernorSecondOpinionDissentWakesConsensus(t *testing.T) {
	g := newTestGovernor(t, 0)

	nodeAdded, cancelNA := collect(t, g.Bus(), string(bus.EvtLedgerNodeAdded))
	defer cancelNA()
	stateChanges, cancelSC := collect(t, g.Bus(), "consensus.loop.state.changed")
	defer cancelSC()
	notifications, cancelN := collect(t, g.Bus(), "consensus.dissent.notification")
	defer cancelN()

	handler := g.HubSubscriber().Handler
	handler(context.Background(), &hub.Event{
		Type:   hub.EventVerifySecondOpinion,
		TaskID: "T1",
		Phase:  "review",
		Lifecycle: &hub.LifecycleEvent{
			Entity: "second_opinion",
			State:  "dissent",
		},
		Custom: map[string]any{
			"severity":         "blocking",
			"reasoning":        "edge case broken in token refresh",
			"requested_change": "handle expiry",
		},
	})

	// (a) dissent ledger node with the critic's reasoning.
	nodes := pollNodes(t, g, "dissent", 1)
	if len(nodes) != 1 {
		t.Fatalf("dissent nodes = %d, want 1", len(nodes))
	}
	content := string(nodes[0].Content)
	if !strings.Contains(content, "edge case broken in token refresh") {
		t.Errorf("dissent node missing reasoning: %s", content)
	}
	if !strings.Contains(content, "governance.second-critic") {
		t.Errorf("dissent node missing stance id: %s", content)
	}

	// (b) ledger.node.added published with the consensus payload shape.
	added := waitEvents(nodeAdded, 1)
	if len(added) < 1 {
		t.Fatal("no ledger.node.added event published")
	}
	if !strings.Contains(string(added[0].Payload), `"node_type":"dissent"`) {
		t.Errorf("payload missing node_type dissent: %s", added[0].Payload)
	}
	if added[0].Scope.LoopID != "T1" {
		t.Errorf("LoopID = %q, want T1", added[0].Scope.LoopID)
	}

	// (c) DissentRequiresAddress fired: loop transitions to
	// resolving_dissents and the worker is notified.
	scs := waitEvents(stateChanges, 1)
	found := false
	for _, ev := range scs {
		if strings.Contains(string(ev.Payload), "resolving_dissents") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no resolving_dissents transition observed in %d state changes", len(scs))
	}
	if evs := waitEvents(notifications, 1); len(evs) < 1 {
		t.Error("consensus.dissent.notification never published")
	}
}

// TestGovernorSecondOpinionAgreeWritesReviewAgree asserts an agree
// verdict records a second review.agree node (trust rules' literal
// query string) and does NOT wake the dissent machinery.
func TestGovernorSecondOpinionAgreeWritesReviewAgree(t *testing.T) {
	g := newTestGovernor(t, 0)

	notifications, cancelN := collect(t, g.Bus(), "consensus.dissent.notification")
	defer cancelN()

	handler := g.HubSubscriber().Handler
	handler(context.Background(), &hub.Event{
		Type:      hub.EventVerifySecondOpinion,
		TaskID:    "T2",
		Phase:     "review",
		Lifecycle: &hub.LifecycleEvent{Entity: "second_opinion", State: "agree"},
	})

	nodes := pollNodes(t, g, "review.agree", 1)
	if len(nodes) != 1 {
		t.Fatalf("review.agree nodes = %d, want 1", len(nodes))
	}
	if nodes[0].CreatedBy != "governance.second-critic" {
		t.Errorf("CreatedBy = %q, want governance.second-critic", nodes[0].CreatedBy)
	}
	if evs := notifications(); len(evs) != 0 {
		t.Errorf("agree must not publish dissent notifications, got %d", len(evs))
	}
}
