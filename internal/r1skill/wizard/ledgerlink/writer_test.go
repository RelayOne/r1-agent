package ledgerlink

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/r1skill/analyze"
	"github.com/RelayOne/r1/internal/r1skill/ir"
	"github.com/RelayOne/r1/internal/r1skill/wizard"
	"github.com/RelayOne/r1/internal/r1skill/wizard/adapter"
)

func TestWriterPersistStoresQueryableSession(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ledgerDir := filepath.Join(root, "ledger")
	artifactDir := filepath.Join(root, "artifacts")
	lg, err := ledger.New(ledgerDir)
	if err != nil {
		t.Fatalf("ledger.New() error = %v", err)
	}
	defer lg.Close()

	writer, err := NewWriter(lg, artifactDir)
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}
	sourcePath := filepath.Join(root, "source.md")
	if err := os.WriteFile(sourcePath, []byte("# source"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	result := &wizard.RunResult{
		Skill: &ir.Skill{
			SchemaVersion: ir.SchemaVersion,
			SkillID:       "demo-skill",
			SkillVersion:  1,
			Description:   "demo",
			Lineage:       ir.Lineage{Kind: "human"},
			Schemas: ir.Schemas{
				Inputs:  ir.TypeSpec{Type: "record", Fields: map[string]ir.TypeSpec{}},
				Outputs: ir.TypeSpec{Type: "record", Fields: map[string]ir.TypeSpec{"result": {Type: "string"}}},
			},
			Graph: ir.Graph{
				Nodes: map[string]ir.Node{
					"echo": {Kind: "pure_fn", Outputs: map[string]ir.TypeSpec{"result": {Type: "string"}}},
				},
				Return: ir.Expr{Kind: "ref", Ref: "echo"},
			},
		},
		Decisions: &wizard.SkillAuthoringDecisions{
			SessionID:      "wizard-session-1",
			SkillID:        "demo-skill",
			SkillVersion:   1,
			StartedAt:      time.Now().UTC(),
			Mode:           "interactive",
			QuestionPackID: "default",
			FinalStatus:    "registered",
			Version:        1,
		},
		Source: &adapter.SourceArtifact{
			Format:   "markdown",
			Path:     sourcePath,
			RawBytes: []byte("# source"),
		},
		SourcePath: sourcePath,
	}
	persisted, err := writer.Persist(context.Background(), result, &analyze.CompileProof{IRHash: "abc123"}, PersistOptions{
		MissionID: "mission-1",
		CreatedBy: "wizard-test",
		StanceID:  "wizard-test",
	})
	if err != nil {
		t.Fatalf("Persist() error = %v", err)
	}
	node, err := lg.Get(context.Background(), persisted.SessionNodeID)
	if err != nil {
		t.Fatalf("ledger.Get() error = %v", err)
	}
	if node.Type != "skill_authoring_decisions" {
		t.Fatalf("node.Type = %q", node.Type)
	}
	if result.Decisions.SourceArtifactRef == "" || result.Decisions.ProducedIRRef == "" || result.Decisions.AnalyzerProofRef == "" {
		t.Fatalf("decision refs were not populated: %+v", result.Decisions)
	}
}
