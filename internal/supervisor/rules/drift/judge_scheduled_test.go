package drift

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
)

func TestJudgeScheduled_Timeout_DraftNotSuperseded(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Create a draft node that has NOT been superseded.
	draftContent, _ := json.Marshal(map[string]string{"text": "draft v1"})
	draftID, err := l.AddNode(ctx, ledger.Node{
		Type:          "draft",
		SchemaVersion: 1,
		CreatedBy:     "worker-1",
		MissionID:     "m1",
		Content:       draftContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	rule := NewJudgeScheduled()
	payload, _ := json.Marshal(judgeTimeoutPayload{DraftNodeID: draftID})
	evt := bus.Event{
		ID:        "timeout-1",
		Type:      "drift.judge.timeout",
		Timestamp: time.Now(),
		Scope:     bus.Scope{MissionID: "m1"},
		Payload:   payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fire {
		t.Fatal("expected rule to fire when draft is not superseded")
	}
}

func TestJudgeScheduled_Timeout_DraftSuperseded(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	draftContent, _ := json.Marshal(map[string]string{"text": "draft v1"})
	draftID, err := l.AddNode(ctx, ledger.Node{
		Type:          "draft",
		SchemaVersion: 1,
		CreatedBy:     "worker-1",
		MissionID:     "m1",
		Content:       draftContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Supersede the draft.
	v2Content, _ := json.Marshal(map[string]string{"text": "draft v2"})
	_, err = l.AddNode(ctx, ledger.Node{
		Type:          "draft",
		SchemaVersion: 1,
		CreatedBy:     "worker-1",
		MissionID:     "m1",
		Content:       v2Content,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Add supersedes edge from v2 to v1. The Resolve API follows edges where
	// From supersedes To, meaning "From is the newer version of To".
	// We need v2 -> v1 with edge type supersedes so Resolve(draftID) returns v2.
	// However the ledger Resolve follows EdgesTo (edges pointing TO the current node
	// as the "To" field). So we need an edge where To = draftID.
	// Actually looking at resolve: it calls EdgesTo(current, supersedes) which finds
	// edges where To == current. Then follows From (the successor).
	// So edge: From=v2ID, To=draftID means "v2 supersedes draft".
	// But we don't have v2ID easily. Let's query for it.
	nodes, err := l.Query(ctx, ledger.QueryFilter{Type: "draft", MissionID: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	var v2ID string
	for _, n := range nodes {
		if n.ID != draftID {
			v2ID = n.ID
			break
		}
	}
	if v2ID == "" {
		t.Fatal("could not find v2 node")
	}
	err = l.AddEdge(ctx, ledger.Edge{From: v2ID, To: draftID, Type: ledger.EdgeSupersedes})
	if err != nil {
		t.Fatal(err)
	}

	rule := NewJudgeScheduled()
	payload, _ := json.Marshal(judgeTimeoutPayload{DraftNodeID: draftID})
	evt := bus.Event{
		ID:        "timeout-2",
		Type:      "drift.judge.timeout",
		Timestamp: time.Now(),
		Scope:     bus.Scope{MissionID: "m1"},
		Payload:   payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fire {
		t.Fatal("expected rule NOT to fire when draft was superseded")
	}
}

func TestJudgeScheduled_Action(t *testing.T) {
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

	rule := NewJudgeScheduled()
	evt := bus.Event{
		ID:    "trigger-1",
		Type:  "drift.judge.timeout",
		Scope: bus.Scope{MissionID: "m1", LoopID: "loop-1"},
	}

	err = rule.Action(context.Background(), evt, b)
	if err != nil {
		t.Fatalf("Action: %v", err)
	}
	if len(published) < 1 {
		t.Fatal("expected at least 1 published event")
	}
	if published[0].Type != "supervisor.spawn.requested" {
		t.Errorf("event type = %s, want supervisor.spawn.requested", published[0].Type)
	}
}
