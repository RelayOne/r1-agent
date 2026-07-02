package wizard

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/RelayOne/r1/internal/config"
)

// TestRunWizardEmitsLoadablePolicy is the audit A076 activation proof:
// the modern RunWizard flow must emit r1.policy.yaml at the project root
// — the artifact downstream config loading actually reads — and that
// artifact must survive config.LoadPolicy, alongside the wizard-native
// config.yaml + rationale outputs.
func TestRunWizardEmitsLoadablePolicy(t *testing.T) {
	dir := t.TempDir()
	writeFileT(t, filepath.Join(dir, "go.mod"), "module testproject\ngo 1.22\n")
	writeFileT(t, filepath.Join(dir, "main.go"), "package main\nfunc main() {}\n")
	writeFileT(t, filepath.Join(dir, "Makefile"), "build:\n\tgo build ./...\ntest:\n\tgo test ./...\n")

	res, err := RunWizard(context.Background(), Opts{
		ProjectRoot: dir,
		Mode:        ModeYes,
	})
	if err != nil {
		t.Fatal(err)
	}

	policyPath := filepath.Join(dir, "r1.policy.yaml")
	if _, err := os.Stat(policyPath); err != nil {
		t.Fatalf("RunWizard did not emit r1.policy.yaml: %v", err)
	}

	pol, err := config.LoadPolicy(policyPath)
	if err != nil {
		t.Fatalf("RunWizard-emitted policy does not load through config.LoadPolicy: %v", err)
	}
	protected := false
	for _, p := range pol.Files.Protected {
		if p == "r1.policy.yaml" {
			protected = true
		}
	}
	if !protected {
		t.Errorf("emitted policy does not protect r1.policy.yaml; protected=%v", pol.Files.Protected)
	}

	// The wizard-native artifacts must still be written.
	for _, rel := range []string{"config.yaml", "wizard-rationale.md"} {
		found := false
		for _, base := range []string{".r1", ".stoke"} {
			if _, err := os.Stat(filepath.Join(dir, base, rel)); err == nil {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %s under the r1 dir, not found", rel)
		}
	}

	if res.Config.Project.Name == "" {
		t.Error("expected non-empty project name in result")
	}
}

// TestPolicyPreferencesMapping pins the WizardConfig → legacy Preferences
// translation that feeds GenerateYAML (audit A076).
func TestPolicyPreferencesMapping(t *testing.T) {
	dir := t.TempDir()
	r := &WizardResult{
		Config: WizardConfig{
			Project:  ProjectConfig{Name: "proj", Stage: "growth"},
			Models:   ModelsConfig{Strategy: "balanced", Architect: "claude", Reviewer: "codex"},
			Quality:  QualityConfig{Verification: "thorough", CodeQuality: "strict", ReviewMode: "cross_model"},
			Security: SecurityConfig{Posture: "high", DataSensitivity: "confidential", Compliance: []string{"pci_dss"}},
			Team:     TeamConfig{Size: "2-5", OpenSource: true},
			Domains:  []string{"web"},
		},
	}

	p := policyPreferences(dir, r)

	if p.ScaleTier != ScaleGrowth {
		t.Errorf("stage growth should map to ScaleGrowth, got %s", p.ScaleTier)
	}
	if p.AdversarialDepth != DepthMaximum {
		t.Errorf("cross_model review should map to DepthMaximum (drives cross_model_review: required), got %s", p.AdversarialDepth)
	}
	if p.PolishLevel != PolishPerfectionist {
		t.Errorf("strict code quality should map to PolishPerfectionist, got %s", p.PolishLevel)
	}
	if p.PrimaryModel != "claude" || p.ReviewModel != "codex" {
		t.Errorf("model mapping wrong: primary=%s review=%s", p.PrimaryModel, p.ReviewModel)
	}

	// The mapped preferences must render a policy that both loads and
	// carries the cross-model gate.
	w := &Wizard{ProjectDir: dir, Prefs: p}
	out := filepath.Join(dir, "r1.policy.yaml")
	if err := os.WriteFile(out, []byte(w.GenerateYAML()), 0o644); err != nil {
		t.Fatal(err)
	}
	pol, err := config.LoadPolicy(out)
	if err != nil {
		t.Fatalf("mapped policy does not load: %v", err)
	}
	if !pol.Verification.CrossModelReview {
		t.Error("DepthMaximum mapping should emit cross_model_review: required")
	}
}

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
