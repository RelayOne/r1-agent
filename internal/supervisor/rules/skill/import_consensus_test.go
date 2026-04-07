package skill

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
)

func TestImportConsensus_Evaluate_ImportProposal(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewImportConsensus()
	payload, _ := json.Marshal(importProposalPayload{
		NodeID:   "node-1",
		NodeType: "skill_import_proposal",
	})
	evt := bus.Event{
		ID:        "nodeadd-1",
		Type:      bus.EvtLedgerNodeAdded,
		Timestamp: time.Now(),
		Scope:     bus.Scope{MissionID: "m1"},
		Payload:   payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fire {
		t.Fatal("expected rule to fire on skill_import_proposal")
	}
}

func TestImportConsensus_Evaluate_OtherNode(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewImportConsensus()
	payload, _ := json.Marshal(importProposalPayload{
		NodeID:   "node-2",
		NodeType: "draft",
	})
	evt := bus.Event{
		ID:      "nodeadd-2",
		Type:    bus.EvtLedgerNodeAdded,
		Scope:   bus.Scope{MissionID: "m1"},
		Payload: payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fire {
		t.Fatal("expected rule NOT to fire for non-import node types")
	}
}

func TestImportConsensus_Action(t *testing.T) {
	bDir := t.TempDir()
	b, err := bus.New(bDir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	var published []bus.Event
	b.Subscribe(bus.Pattern{}, func(e bus.Event) {
		published = append(published, e)
	})

	rule := NewImportConsensus()
	payload, _ := json.Marshal(map[string]any{
		"node_id":         "node-1",
		"node_type":       "skill_import_proposal",
		"risk_assessment": "high",
	})
	evt := bus.Event{
		ID:      "nodeadd-3",
		Type:    bus.EvtLedgerNodeAdded,
		Scope:   bus.Scope{MissionID: "m1"},
		Payload: payload,
	}

	if err := rule.Action(context.Background(), evt, b); err != nil {
		t.Fatalf("Action: %v", err)
	}
	if len(published) < 1 {
		t.Fatal("expected spawn event")
	}
	if published[0].Type != "supervisor.spawn.requested" {
		t.Errorf("type = %s, want supervisor.spawn.requested", published[0].Type)
	}

	// Verify auto_escalate is true for high risk.
	var spawnData map[string]any
	if err := json.Unmarshal(published[0].Payload, &spawnData); err != nil {
		t.Fatal(err)
	}
	if ae, ok := spawnData["auto_escalate"].(bool); !ok || !ae {
		t.Errorf("auto_escalate = %v, want true", spawnData["auto_escalate"])
	}
}
