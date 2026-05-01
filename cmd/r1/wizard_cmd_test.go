package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/r1skill/wizard"
)

func TestLoadDecisionLogFromLedger(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ledgerDir := filepath.Join(root, "ledger")
	lg, err := ledger.New(ledgerDir)
	if err != nil {
		t.Fatalf("ledger.New() error = %v", err)
	}
	defer lg.Close()

	body, err := json.Marshal(&wizard.SkillAuthoringDecisions{
		SessionID:      "session-1",
		SkillID:        "demo-skill",
		SkillVersion:   1,
		StartedAt:      time.Now().UTC(),
		Mode:           "interactive",
		QuestionPackID: "default",
		FinalStatus:    "registered",
		Version:        1,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	id, err := lg.AddNode(context.Background(), ledger.Node{
		Type:          "skill_authoring_decisions",
		SchemaVersion: 1,
		CreatedBy:     "test",
		Content:       body,
	})
	if err != nil {
		t.Fatalf("AddNode() error = %v", err)
	}

	log, err := loadDecisionLog(ledgerDir, id, "")
	if err != nil {
		t.Fatalf("loadDecisionLog() error = %v", err)
	}
	if log.SkillID != "demo-skill" {
		t.Fatalf("log.SkillID = %q", log.SkillID)
	}
}
