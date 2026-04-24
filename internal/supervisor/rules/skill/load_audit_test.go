package skill

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
)

func TestLoadAudit_Evaluate(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewLoadAudit()
	evt := bus.Event{
		ID:        "load-1",
		Type:      bus.EvtSkillLoaded,
		Timestamp: time.Now(),
		EmitterID: "worker-1",
		Scope:     bus.Scope{MissionID: "m1"},
		Payload:   json.RawMessage(`{"skill_id":"sk-1"}`),
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fire {
		t.Fatal("expected rule to always fire on skill.loaded")
	}
}

func TestLoadAudit_Action(t *testing.T) {
	bDir := t.TempDir()
	b, err := bus.New(bDir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	var published []bus.Event
	var mu sync.Mutex
	b.Subscribe(bus.Pattern{}, func(e bus.Event) {
		mu.Lock()
		published = append(published, e)
		mu.Unlock()
	})

	rule := NewLoadAudit()
	evt := bus.Event{
		ID:        "load-2",
		Type:      bus.EvtSkillLoaded,
		EmitterID: "worker-1",
		Scope:     bus.Scope{MissionID: "m1"},
	}

	if err := rule.Action(context.Background(), evt, b); err != nil {
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

	if len(published) < 1 {
		t.Fatal("expected audit event")
	}
	if published[0].Type != "supervisor.audit.skill_load" {
		t.Errorf("type = %s, want supervisor.audit.skill_load", published[0].Type)
	}
}
