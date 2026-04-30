package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
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

func TestSeedBundledSkillPacks_RegistersInvoiceProcessorRuntime(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))

	backends, err := NewBackends(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatalf("new backends: %v", err)
	}
	t.Cleanup(func() { _ = backends.Close() })

	registered, skipped, err := backends.SeedBundledSkillPacks(filepath.Join(repoRoot, ".stoke", "skills", "packs"))
	if err != nil {
		t.Fatalf("SeedBundledSkillPacks: %v", err)
	}
	if registered < 1 {
		t.Fatalf("registered=%d skipped=%d, want at least one bundled manifest", registered, skipped)
	}

	resp, err := backends.Invoke(
		context.Background(),
		"m-flagship",
		"invoice_processor_runtime",
		json.RawMessage(`{"accounts":["billing","ops"],"destination":"quickbooks","alert_unpaid_over_days":45}`),
		"",
	)
	if err != nil {
		t.Fatalf("invoke bundled skill: %v", err)
	}
	if resp["deterministic"] != true {
		t.Fatalf("deterministic flag missing: %+v", resp)
	}

	output, ok := resp["output"].(map[string]any)
	if !ok {
		t.Fatalf("output type = %T", resp["output"])
	}
	if output["flow_slug"] != "invoice-processor" {
		t.Fatalf("flow_slug = %#v, want invoice-processor", output["flow_slug"])
	}
	if output["destination"] != "quickbooks" {
		t.Fatalf("destination = %#v, want quickbooks", output["destination"])
	}
	required, ok := output["required_credentials"].([]any)
	if !ok {
		t.Fatalf("required_credentials type = %T", output["required_credentials"])
	}
	if len(required) != 2 || required[0] != "gmail_oauth" || required[1] != "quickbooks_oauth" {
		t.Fatalf("required_credentials = %#v, want [gmail_oauth quickbooks_oauth]", required)
	}
}

func TestSeedBundledSkillPacks_RegistersDentistOutreachRuntime(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))

	backends, err := NewBackends(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatalf("new backends: %v", err)
	}
	t.Cleanup(func() { _ = backends.Close() })

	registered, skipped, err := backends.SeedBundledSkillPacks(filepath.Join(repoRoot, ".stoke", "skills", "packs"))
	if err != nil {
		t.Fatalf("SeedBundledSkillPacks: %v", err)
	}
	if registered < 2 {
		t.Fatalf("registered=%d skipped=%d, want both flagship manifests", registered, skipped)
	}

	resp, err := backends.Invoke(
		context.Background(),
		"m-flagship",
		"dentist_outreach_runtime",
		json.RawMessage(`{"markets":["implants","invisalign"],"location":"Toronto, ON","crm":"hubspot","daily_new_leads":18,"sequence_days":10}`),
		"",
	)
	if err != nil {
		t.Fatalf("invoke bundled skill: %v", err)
	}
	if resp["deterministic"] != true {
		t.Fatalf("deterministic flag missing: %+v", resp)
	}

	output, ok := resp["output"].(map[string]any)
	if !ok {
		t.Fatalf("output type = %T", resp["output"])
	}
	if output["flow_slug"] != "dentist-outreach" {
		t.Fatalf("flow_slug = %#v, want dentist-outreach", output["flow_slug"])
	}
	if output["crm"] != "hubspot" {
		t.Fatalf("crm = %#v, want hubspot", output["crm"])
	}
	required, ok := output["required_credentials"].([]any)
	if !ok {
		t.Fatalf("required_credentials type = %T", output["required_credentials"])
	}
	if len(required) != 3 || required[0] != "hunter_oauth" || required[1] != "google_oauth" || required[2] != "hubspot_oauth" {
		t.Fatalf("required_credentials = %#v, want [hunter_oauth google_oauth hubspot_oauth]", required)
	}
}

func TestSeedBundledSkillPacks_RegistersBetBuddiesRuntime(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))

	backends, err := NewBackends(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatalf("new backends: %v", err)
	}
	t.Cleanup(func() { _ = backends.Close() })

	registered, skipped, err := backends.SeedBundledSkillPacks(filepath.Join(repoRoot, ".stoke", "skills", "packs"))
	if err != nil {
		t.Fatalf("SeedBundledSkillPacks: %v", err)
	}
	if registered < 3 {
		t.Fatalf("registered=%d skipped=%d, want all flagship manifests", registered, skipped)
	}

	resp, err := backends.Invoke(
		context.Background(),
		"m-flagship",
		"betbuddies_group_runtime",
		json.RawMessage(`{"event_title":"Stanley Cup Final Pool","invitees":["a@example.com","b@example.com"],"stake_amount_cents":2500,"currency":"cad","ledger_backend":"google_sheets","house_rules_summary":"Most points wins; total goals tie-break."}`),
		"",
	)
	if err != nil {
		t.Fatalf("invoke bundled skill: %v", err)
	}
	if resp["deterministic"] != true {
		t.Fatalf("deterministic flag missing: %+v", resp)
	}

	output, ok := resp["output"].(map[string]any)
	if !ok {
		t.Fatalf("output type = %T", resp["output"])
	}
	if output["flow_slug"] != "betbuddies-group" {
		t.Fatalf("flow_slug = %#v, want betbuddies-group", output["flow_slug"])
	}
	if output["ledger_backend"] != "google_sheets" {
		t.Fatalf("ledger_backend = %#v, want google_sheets", output["ledger_backend"])
	}
	required, ok := output["required_credentials"].([]any)
	if !ok {
		t.Fatalf("required_credentials type = %T", output["required_credentials"])
	}
	if len(required) != 2 || required[0] != "google_oauth" || required[1] != "stripe_secret_key" {
		t.Fatalf("required_credentials = %#v, want [google_oauth stripe_secret_key]", required)
	}
}

func TestSeedBundledSkillPacks_RegistersLedgerAuditQueryRuntime(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	ledgerRoot := filepath.Join(t.TempDir(), "ledger")

	lg, err := ledger.New(ledgerRoot)
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	if _, err := lg.AddNode(context.Background(), ledger.Node{
		Type:          "honesty_decision",
		SchemaVersion: 1,
		CreatedAt:     time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
		CreatedBy:     "stoke honesty",
		MissionID:     "mission-audit",
		Content:       json.RawMessage(`{"kind":"refused","reason":"missing deploy probe"}`),
	}); err != nil {
		t.Fatalf("add honesty node: %v", err)
	}
	if _, err := lg.AddNode(context.Background(), ledger.Node{
		Type:          "verification_evidence",
		SchemaVersion: 1,
		CreatedAt:     time.Date(2026, 4, 30, 12, 1, 0, 0, time.UTC),
		CreatedBy:     "stoke verify",
		MissionID:     "mission-audit",
		Content:       json.RawMessage(`{"evidence":"curl https://example.test/health"}`),
	}); err != nil {
		t.Fatalf("add verification node: %v", err)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("close ledger: %v", err)
	}

	backends, err := NewBackends(filepath.Join(t.TempDir(), "stoke-mcp-ledger"))
	if err != nil {
		t.Fatalf("new backends: %v", err)
	}
	t.Cleanup(func() { _ = backends.Close() })

	registered, skipped, err := backends.SeedBundledSkillPacks(filepath.Join(repoRoot, ".stoke", "skills", "packs"))
	if err != nil {
		t.Fatalf("SeedBundledSkillPacks: %v", err)
	}
	if registered < 4 {
		t.Fatalf("registered=%d skipped=%d, want at least four bundled manifests", registered, skipped)
	}

	resp, err := backends.Invoke(
		context.Background(),
		"m-audit",
		"ledger_audit_query_runtime",
		json.RawMessage(fmt.Sprintf(`{"ledger_dir":%q,"mission_id":"mission-audit","node_types":["honesty_decision","verification_evidence"],"created_by":"stoke honesty","limit":10,"include_content":true}`, ledgerRoot)),
		"",
	)
	if err != nil {
		t.Fatalf("invoke bundled skill: %v", err)
	}
	if resp["deterministic"] != true {
		t.Fatalf("deterministic flag missing: %+v", resp)
	}

	output, ok := resp["output"].(map[string]any)
	if !ok {
		t.Fatalf("output type = %T", resp["output"])
	}
	if output["query_slug"] != "ledger-audit-query" {
		t.Fatalf("query_slug = %#v, want ledger-audit-query", output["query_slug"])
	}
	if output["matched_count"] != float64(1) {
		t.Fatalf("matched_count = %#v, want 1", output["matched_count"])
	}
	if output["ledger_dir"] != ledgerRoot {
		t.Fatalf("ledger_dir = %#v, want %q", output["ledger_dir"], ledgerRoot)
	}
	summary, ok := output["summary"].(string)
	if !ok || !strings.Contains(summary, "honesty_decision=1") {
		t.Fatalf("summary = %#v, want honesty_decision=1", output["summary"])
	}
	nodes, ok := output["nodes"].([]any)
	if !ok || len(nodes) != 1 {
		t.Fatalf("nodes = %#v, want one node", output["nodes"])
	}
	firstNode, ok := nodes[0].(map[string]any)
	if !ok {
		t.Fatalf("first node type = %T", nodes[0])
	}
	if firstNode["type"] != "honesty_decision" {
		t.Fatalf("first node type = %#v, want honesty_decision", firstNode["type"])
	}
	content, ok := firstNode["content"].(map[string]any)
	if !ok || content["reason"] != "missing deploy probe" {
		t.Fatalf("content = %#v, want decoded raw content", firstNode["content"])
	}
}

func TestSeedPackRegistries_LoadsUserCanonicalPacks(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeDeterministicPackFixture(
		t,
		filepath.Join(home, ".r1", "skills", "packs", "user-pack"),
		"user-pack",
		skillmfr.Manifest{
			Name:        "user.echo",
			Version:     "1.0.0",
			Description: "user canonical skill",
		},
	)

	backends, err := NewBackends(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatalf("new backends: %v", err)
	}
	t.Cleanup(func() { _ = backends.Close() })

	registered, skipped, err := backends.SeedPackRegistries(repo)
	if err != nil {
		t.Fatalf("SeedPackRegistries: %v", err)
	}
	if registered != 1 || skipped != 0 {
		t.Fatalf("registered=%d skipped=%d, want 1/0", registered, skipped)
	}
	manifest, ok := backends.ManifestRegistry.Get("user.echo")
	if !ok {
		t.Fatal("expected user.echo to be registered")
	}
	if manifest.Version != "1.0.0" {
		t.Fatalf("manifest version = %q, want 1.0.0", manifest.Version)
	}
}

func TestSeedPackRegistries_PrefersRepoCanonicalOverLegacyAndUser(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeDeterministicPackFixture(
		t,
		filepath.Join(home, ".r1", "skills", "packs", "shared-pack"),
		"shared-pack",
		skillmfr.Manifest{
			Name:        "shared.echo",
			Version:     "0.9.0",
			Description: "user canonical skill",
		},
	)
	writeDeterministicPackFixture(
		t,
		filepath.Join(repo, ".stoke", "skills", "packs", "shared-pack"),
		"shared-pack",
		skillmfr.Manifest{
			Name:        "shared.echo",
			Version:     "1.0.0",
			Description: "repo legacy skill",
		},
	)
	writeDeterministicPackFixture(
		t,
		filepath.Join(repo, ".r1", "skills", "packs", "shared-pack"),
		"shared-pack",
		skillmfr.Manifest{
			Name:        "shared.echo",
			Version:     "2.0.0",
			Description: "repo canonical skill",
		},
	)

	backends, err := NewBackends(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatalf("new backends: %v", err)
	}
	t.Cleanup(func() { _ = backends.Close() })

	registered, skipped, err := backends.SeedPackRegistries(repo)
	if err != nil {
		t.Fatalf("SeedPackRegistries: %v", err)
	}
	if registered != 1 || skipped != 2 {
		t.Fatalf("registered=%d skipped=%d, want 1/2", registered, skipped)
	}
	manifest, ok := backends.ManifestRegistry.Get("shared.echo")
	if !ok {
		t.Fatal("expected shared.echo to be registered")
	}
	if manifest.Version != "2.0.0" {
		t.Fatalf("manifest version = %q, want repo canonical 2.0.0", manifest.Version)
	}
	if !strings.HasPrefix(manifest.IRRef, filepath.Join(repo, ".r1", "skills", "packs", "shared-pack")) {
		t.Fatalf("manifest IRRef = %q, want repo canonical path prefix", manifest.IRRef)
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

func writeDeterministicPackFixture(t *testing.T, packDir, packName string, manifest skillmfr.Manifest) {
	t.Helper()
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(packDir): %v", err)
	}
	manifestDir := filepath.Join(packDir, manifest.Name)
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(manifestDir): %v", err)
	}
	if err := os.WriteFile(filepath.Join(packDir, "pack.yaml"), []byte(fmt.Sprintf("name: %s\nversion: 0.1.0\nskill_count: 1\n", packName)), 0o644); err != nil {
		t.Fatalf("WriteFile(pack.yaml): %v", err)
	}

	skill := &ir.Skill{
		SchemaVersion: ir.SchemaVersion,
		SkillID:       manifest.Name,
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
	writeJSON(t, filepath.Join(manifestDir, "skill.r1.json"), skill)
	writeJSON(t, filepath.Join(manifestDir, "skill.r1.proof.json"), proof)

	manifest.InputSchema = json.RawMessage(`{"type":"object"}`)
	manifest.OutputSchema = json.RawMessage(`{"type":"object"}`)
	manifest.WhenToUse = []string{"echo"}
	manifest.WhenNotToUse = []string{"not echo", "use markdown"}
	manifest.UseIR = true
	manifest.IRRef = "skill.r1.json"
	manifest.CompileProofRef = "skill.r1.proof.json"
	writeJSON(t, filepath.Join(manifestDir, "manifest.json"), manifest)
}
