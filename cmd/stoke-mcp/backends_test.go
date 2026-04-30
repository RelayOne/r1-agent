package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/r1skill/analyze"
	"github.com/RelayOne/r1/internal/r1skill/ir"
	"github.com/RelayOne/r1/internal/skillmfr"
)

func TestInvokeDeterministicSkill(t *testing.T) {
	tmp := t.TempDir()
	skillPath := filepath.Join(tmp, "skill.r1.json")
	proofPath := filepath.Join(tmp, "skill.r1.proof.json")

	skill := &ir.Skill{
		SchemaVersion: ir.SchemaVersion,
		SkillID:       "deterministic-echo",
		SkillVersion:  1,
		Lineage:       ir.Lineage{Kind: "human", AuthoredAt: time.Now().UTC()},
		Schemas: ir.Schemas{
			Inputs:  ir.TypeSpec{Type: "record", Fields: map[string]ir.TypeSpec{"message": {Type: "string"}}},
			Outputs: ir.TypeSpec{Type: "record", Fields: map[string]ir.TypeSpec{"value": {Type: "string"}}},
		},
		Graph: ir.Graph{
			Nodes: map[string]ir.Node{
				"echo": {
					Kind: "pure_fn",
					Config: json.RawMessage(`{
						"registry_ref":"stdlib:echo",
						"input":{"kind":"ref","ref":"inputs.message"}
					}`),
				},
			},
			Return: ir.Expr{Kind: "ref", Ref: "echo"},
		},
	}
	proof, err := analyze.Analyze(skill, analyze.Constitution{Hash: "sha256:test"}, analyze.DefaultOptions())
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	writeJSON(t, skillPath, skill)
	writeJSON(t, proofPath, proof)

	backends, err := NewBackends(filepath.Join(tmp, "ledger"))
	if err != nil {
		t.Fatalf("new backends: %v", err)
	}
	t.Cleanup(func() { _ = backends.Close() })

	manifest := skillmfr.Manifest{
		Name:            "deterministic-echo",
		Version:         "1.0.0",
		Description:     "deterministic echo",
		InputSchema:     json.RawMessage(`{"type":"object"}`),
		OutputSchema:    json.RawMessage(`{"type":"object"}`),
		WhenToUse:       []string{"echo"},
		WhenNotToUse:    []string{"not echo", "use markdown"},
		UseIR:           true,
		IRRef:           skillPath,
		CompileProofRef: proofPath,
	}
	if err := backends.ManifestRegistry.Register(manifest); err != nil {
		t.Fatalf("register manifest: %v", err)
	}

	resp, err := backends.Invoke(context.Background(), "m-test", "deterministic-echo", json.RawMessage(`{"message":"hi"}`), "")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if resp["deterministic"] != true {
		t.Fatalf("deterministic flag missing: %+v", resp)
	}
	output, ok := resp["output"].(map[string]any)
	if !ok {
		t.Fatalf("output type = %T", resp["output"])
	}
	if output["value"] != "hi" {
		t.Fatalf("output value = %#v", output["value"])
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
