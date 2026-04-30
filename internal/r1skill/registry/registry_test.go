package registry

import (
	"path/filepath"
	"testing"

	"github.com/RelayOne/r1/internal/r1skill/analyze"
	"github.com/RelayOne/r1/internal/r1skill/ir"
)

func TestSaveEntryRoundTrip(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	entry, err := SaveEntry(root, &ir.Skill{
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
	}, &analyze.CompileProof{IRHash: "abc123"})
	if err != nil {
		t.Fatalf("SaveEntry() error = %v", err)
	}
	if got, want := entry.SourcePath, filepath.Join(root, "demo-skill", "skill.r1.json"); got != want {
		t.Fatalf("SourcePath = %q, want %q", got, want)
	}
	if got, want := entry.ProofPath, filepath.Join(root, "demo-skill", "skill.r1.proof.json"); got != want {
		t.Fatalf("ProofPath = %q, want %q", got, want)
	}
	loaded, err := LoadEntry(entry.SourcePath)
	if err != nil {
		t.Fatalf("LoadEntry() error = %v", err)
	}
	if loaded.Skill.SkillID != "demo-skill" {
		t.Fatalf("loaded skill_id = %q", loaded.Skill.SkillID)
	}
	if loaded.Proof.IRHash != "abc123" {
		t.Fatalf("loaded proof ir_hash = %q", loaded.Proof.IRHash)
	}
}
