package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeV2PackFixture(t *testing.T, packDir, name string, compat []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(packDir, name+".skill"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	packYAML := "name: " + name + "\nversion: 0.1.0\ndescription: V2 fixture\n"
	if err := os.WriteFile(filepath.Join(packDir, "pack.yaml"), []byte(packYAML), 0o644); err != nil {
		t.Fatalf("WriteFile pack.yaml: %v", err)
	}
	manifest := `{
"name": "` + name + `.skill",
"version": "0.1.0",
"description": "fixture",
"inputSchema": {"type":"object"},
"outputSchema": {"type":"object"},
"whenToUse": ["use it"],
"whenNotToUse": ["not in tests", "not for prod"],
"behaviorFlags": {"mutatesState": false, "requiresNetwork": false}
}`
	if err := os.WriteFile(filepath.Join(packDir, name+".skill", "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("WriteFile manifest: %v", err)
	}
	compatJSON, _ := json.Marshal(compat)
	v2 := `{"manifest_schema_version":"2.0.0","name":"` + name + `","version":"0.1.0","compat":` + string(compatJSON) + `,"signature_authority":"r1"}`
	if err := os.WriteFile(filepath.Join(packDir, "manifest.v2.json"), []byte(v2), 0o644); err != nil {
		t.Fatalf("WriteFile manifest.v2.json: %v", err)
	}
}

func TestAdoptSkillPack_HappyCloudSwarm(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	packDir := filepath.Join(repo, ".r1", "skills", "packs", "alpha")
	writeV2PackFixture(t, packDir, "alpha", []string{"r1", "cloudswarm"})

	now := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	result, err := adoptSkillPack(repo, "alpha", "cloudswarm", now)
	if err != nil {
		t.Fatalf("adoptSkillPack: %v", err)
	}
	if result.TargetProduct != "cloudswarm" {
		t.Fatalf("TargetProduct = %q", result.TargetProduct)
	}
	if _, err := os.Stat(result.WrapperPath); err != nil {
		t.Fatalf("wrapper file missing: %v", err)
	}
	wrapperBytes, err := os.ReadFile(result.WrapperPath)
	if err != nil {
		t.Fatalf("ReadFile wrapper: %v", err)
	}
	if !strings.Contains(string(wrapperBytes), `"params.context"`) {
		t.Fatalf("wrapper missing cloudswarm contract: %s", wrapperBytes)
	}
	if result.LedgerNodeID == "" {
		t.Fatalf("LedgerNodeID empty")
	}
	// Ledger node should exist on disk
	ledgerNodePath := filepath.Join(repo, ".r1", "ledger", "chain", result.LedgerNodeID+".json")
	if _, err := os.Stat(ledgerNodePath); err != nil {
		t.Fatalf("ledger node missing: %v", err)
	}
}

func TestAdoptSkillPack_RefusesIncompatibleTarget(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	packDir := filepath.Join(repo, ".r1", "skills", "packs", "r1only")
	writeV2PackFixture(t, packDir, "r1only", []string{"r1"})

	_, err := adoptSkillPack(repo, "r1only", "heroa", time.Now().UTC())
	if err == nil {
		t.Fatalf("want err, got nil")
	}
	if !strings.Contains(err.Error(), "not compatible") {
		t.Fatalf("err = %v", err)
	}
}

func TestAdoptSkillPack_RefusesUnknownTarget(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	packDir := filepath.Join(repo, ".r1", "skills", "packs", "x")
	writeV2PackFixture(t, packDir, "x", []string{"r1"})
	_, err := adoptSkillPack(repo, "x", "mars", time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), "unsupported adoption target") {
		t.Fatalf("err = %v", err)
	}
}

func TestAdoptSkillPack_PackNotFound(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	_, err := adoptSkillPack(repo, "ghost", "cloudswarm", time.Now().UTC())
	if err == nil {
		t.Fatalf("want err, got nil")
	}
}

func TestDeriveAdoptNodeID_Stable(t *testing.T) {
	t.Parallel()
	a := deriveAdoptNodeID("alpha", "cloudswarm", "2026-05-12T00:00:00Z")
	b := deriveAdoptNodeID("alpha", "cloudswarm", "2026-05-12T00:00:00Z")
	if a != b {
		t.Fatalf("deriveAdoptNodeID not stable: %q vs %q", a, b)
	}
	c := deriveAdoptNodeID("alpha", "heroa", "2026-05-12T00:00:00Z")
	if a == c {
		t.Fatalf("deriveAdoptNodeID collision across target")
	}
}
