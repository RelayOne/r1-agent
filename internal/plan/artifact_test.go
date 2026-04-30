package plan

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
)

func TestSaveWithLedgerWritesPlanAndApprovalNodes(t *testing.T) {
	repo := t.TempDir()
	ledgerDir := filepath.Join(repo, ".r1", "ledger")
	lg, err := ledger.New(ledgerDir)
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	lg.Close()

	p := &Plan{
		ID:          "plan-test",
		Description: "test plan",
		Tasks: []Task{
			{ID: "TASK-1", Description: "first"},
		},
	}
	approval := &Approval{Actor: "eric", Mode: "cli", At: time.Now().UTC(), EventID: "evt-1"}
	out := filepath.Join(repo, "stoke-plan.json")
	if err := SaveWithLedger(context.Background(), repo, out, p, approval); err != nil {
		t.Fatalf("SaveWithLedger: %v", err)
	}

	lg, err = ledger.New(ledgerDir)
	if err != nil {
		t.Fatalf("ledger.New reopen: %v", err)
	}
	defer lg.Close()
	artifacts, err := lg.QueryNodes(ledger.QueryFilter{Type: "plan_artifact"})
	if err != nil {
		t.Fatalf("QueryNodes artifact: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("plan_artifact count = %d, want 1", len(artifacts))
	}
	approvals, err := lg.QueryNodes(ledger.QueryFilter{Type: "plan_approval"})
	if err != nil {
		t.Fatalf("QueryNodes approval: %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("plan_approval count = %d, want 1", len(approvals))
	}
}
