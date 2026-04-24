package wizard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/skillselect"
)

func TestRunWizardYesMode(t *testing.T) {
	dir := t.TempDir()
	// Create a minimal Go project
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testproject\ngo 1.22\nrequire github.com/lib/pq v1.10.0\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "main_test.go"), []byte("package main\nimport \"testing\"\nfunc TestMain(t *testing.T) {}\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0o600)

	result, err := RunWizard(context.Background(), Opts{
		ProjectRoot: dir,
		Mode:        ModeYes,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify profile was detected
	if result.Profile == nil {
		t.Fatal("expected non-nil profile")
	}

	// Verify Go was detected
	found := false
	for _, lang := range result.Profile.Languages {
		if lang == "go" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected go in languages, got %v", result.Profile.Languages)
	}

	// Verify postgres was detected from go.mod
	foundDB := false
	for _, db := range result.Profile.Databases {
		if db == "postgres" {
			foundDB = true
			break
		}
	}
	if !foundDB {
		t.Errorf("expected postgres in databases, got %v", result.Profile.Databases)
	}

	// Verify config was generated
	if result.Config.Project.Name == "" {
		t.Error("expected non-empty project name")
	}
	if result.Config.Quality.HonestyEnforce == "" {
		t.Error("expected honesty enforcement set")
	}

	// Verify files were written
	cfgData, err := os.ReadFile(filepath.Join(dir, ".stoke", "config.yaml"))
	if err != nil {
		t.Fatal("expected .stoke/config.yaml:", err)
	}
	if !strings.Contains(string(cfgData), "quality:") {
		t.Error("config.yaml should contain quality section")
	}
	if !strings.Contains(string(cfgData), "honesty_enforcement: strict") {
		t.Error("config.yaml should contain honesty enforcement")
	}

	// Verify rationale was written
	ratData, err := os.ReadFile(filepath.Join(dir, ".stoke", "wizard-rationale.md"))
	if err != nil {
		t.Fatal("expected .stoke/wizard-rationale.md:", err)
	}
	if !strings.Contains(string(ratData), "Maturity Assessment") {
		t.Error("rationale should contain maturity assessment")
	}

	// Verify skills were selected
	if len(result.Skills) == 0 {
		t.Error("expected non-empty skills list")
	}
	foundSkill := false
	for _, s := range result.Skills {
		if s == "agent-discipline" {
			foundSkill = true
		}
	}
	if !foundSkill {
		t.Error("expected agent-discipline in skills")
	}
}

func TestInferMaturityPrototype(t *testing.T) {
	dir := t.TempDir()
	// Empty project = prototype
	profile := &skillselect.RepoProfile{
		Confidence: make(map[string]float64),
	}
	m := InferMaturity(dir, profile)
	if m.Stage != "prototype" {
		t.Errorf("expected prototype for empty dir, got %s (score %d)", m.Stage, m.Score)
	}
	if m.Score > 20 {
		t.Errorf("expected score <= 20 for empty dir, got %d", m.Score)
	}
}

func TestBuildDefaultConfig(t *testing.T) {
	dir := t.TempDir()
	profile := &skillselect.RepoProfile{
		Languages:      []string{"go"},
		Databases:      []string{"postgres"},
		CloudProviders: []string{"aws"},
		InfraTools:     []string{"terraform"},
		HasCI:          true,
		Confidence:     make(map[string]float64),
	}

	maturity := MaturityClassification{Stage: "growth", Score: 55, Breakdown: map[string]int{}}
	cfg := buildDefaultConfig(dir, profile, maturity)

	if cfg.Project.Stage != "growth" {
		t.Errorf("expected stage growth, got %s", cfg.Project.Stage)
	}
	if cfg.Infrastructure.IaC != "terraform" {
		t.Errorf("expected IaC terraform, got %s", cfg.Infrastructure.IaC)
	}
	if cfg.Quality.ReviewMode != "cross_model" {
		t.Errorf("expected cross_model review for growth, got %s", cfg.Quality.ReviewMode)
	}
	if cfg.Risk.Autonomy != "standard" {
		t.Errorf("expected standard autonomy for growth, got %s", cfg.Risk.Autonomy)
	}
	if cfg.Skills.TokenBudget != 3000 {
		t.Errorf("expected 3000 token budget, got %d", cfg.Skills.TokenBudget)
	}
}

func TestDefaultQualityStages(t *testing.T) {
	tests := []struct {
		stage            string
		wantVerification string
		wantHonesty      string
	}{
		{"prototype", "light", "strict"},
		{"mvp", "standard", "strict"},
		{"growth", "thorough", "strict"},
		{"mature", "maximum", "maximum"},
	}
	for _, tt := range tests {
		q := defaultQuality(tt.stage)
		if q.Verification != tt.wantVerification {
			t.Errorf("stage %s: verification=%s, want %s", tt.stage, q.Verification, tt.wantVerification)
		}
		if q.HonestyEnforce != tt.wantHonesty {
			t.Errorf("stage %s: honesty=%s, want %s", tt.stage, q.HonestyEnforce, tt.wantHonesty)
		}
	}
}

func TestDefaultRiskStages(t *testing.T) {
	tests := []struct {
		stage        string
		wantAutonomy string
	}{
		{"prototype", "yolo"},
		{"mvp", "permissive"},
		{"growth", "standard"},
		{"mature", "conservative"},
	}
	for _, tt := range tests {
		r := defaultRisk(tt.stage)
		if r.Autonomy != tt.wantAutonomy {
			t.Errorf("stage %s: autonomy=%s, want %s", tt.stage, r.Autonomy, tt.wantAutonomy)
		}
	}
}

func TestSelectSkillsFromConfig(t *testing.T) {
	profile := &skillselect.RepoProfile{
		Languages:      []string{"go"},
		Databases:      []string{"postgres", "redis"},
		Frameworks:     []string{"stripe"},
		CloudProviders: []string{"aws"},
		Protocols:      []string{"grpc"},
		Confidence:     make(map[string]float64),
	}
	cfg := WizardConfig{
		Domains:  []string{"payments"},
		Security: SecurityConfig{Compliance: []string{"pci_dss"}},
		Risk:     RiskConfig{BlastRadius: "limited_prod"},
	}

	skills := selectSkillsFromConfig(cfg, profile)

	expected := []string{"agent-discipline", "go", "postgres", "redis", "stripe", "cloud-aws", "grpc", "payments", "code-quality", "testing", "security", "error-handling", "compliance-pci_dss", "production-safety"}
	for _, want := range expected {
		found := false
		for _, s := range skills {
			if s == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected skill %q in list, got %v", want, skills)
		}
	}
}

func TestDetectDomainsPayments(t *testing.T) {
	profile := &skillselect.RepoProfile{
		Frameworks: []string{"stripe"},
		Protocols:  []string{"websocket", "graphql"},
		Confidence: make(map[string]float64),
	}
	domains := detectDomains(profile)
	expected := []string{"payments", "real-time", "graphql-api"}
	for _, want := range expected {
		found := false
		for _, d := range domains {
			if d == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected domain %q, got %v", want, domains)
		}
	}
}

func TestRenderProposal(t *testing.T) {
	r := &WizardResult{
		Profile: &skillselect.RepoProfile{
			Languages:  []string{"go", "typescript"},
			Frameworks: []string{"nextjs"},
			Databases:  []string{"postgres"},
			Confidence: make(map[string]float64),
		},
		Maturity: MaturityClassification{Stage: "growth", Score: 55},
		Config: WizardConfig{
			Project: ProjectConfig{Name: "test-project", Stage: "growth"},
			Quality: QualityConfig{Verification: "thorough", HonestyEnforce: "strict"},
			Security: SecurityConfig{Posture: "standard"},
			Models:  ModelsConfig{Strategy: "balanced"},
		},
		Skills: []string{"go", "nextjs"},
	}

	proposal := renderProposal(r)
	if !strings.Contains(proposal, "test-project") {
		t.Error("proposal should contain project name")
	}
	if !strings.Contains(proposal, "growth") {
		t.Error("proposal should contain stage")
	}
	if !strings.Contains(proposal, "55") {
		t.Error("proposal should contain maturity score")
	}
}

func TestScoreCapBounds(t *testing.T) {
	if capScore(150) != 100 {
		t.Error("capScore should cap at 100")
	}
	if capScore(50) != 50 {
		t.Error("capScore should pass through values under 100")
	}
}
