// cicd_test.go — tests for T-R1P-020/021/022 CI/CD integration templates.
package cicd

import (
	"strings"
	"testing"
)

// TestGenerateGitHub_Review verifies the GitHub Actions review template.
func TestGenerateGitHub_Review(t *testing.T) {
	yaml, path, err := GenerateConfig(ProviderGitHub, Options{Mode: ModeReview})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if !strings.HasPrefix(path, ".github/workflows/") {
		t.Errorf("path should start with .github/workflows/, got: %q", path)
	}
	for _, want := range []string{
		"ANTHROPIC_API_KEY",
		"pull_request",
		"actions/checkout",
		"r1 review",
		"jobs:",
		"steps:",
		"on:",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("review template missing %q", want)
		}
	}
	if warns := ValidateConfig(ProviderGitHub, yaml); len(warns) > 0 {
		t.Errorf("validate warnings: %v", warns)
	}
}

// TestGenerateGitHub_AutoFix verifies the GitHub Actions auto-fix template.
func TestGenerateGitHub_AutoFix(t *testing.T) {
	yaml, _, err := GenerateConfig(ProviderGitHub, Options{Mode: ModeAutoFix})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if !strings.Contains(yaml, "scan-repair") {
		t.Error("autofix template should contain scan-repair command")
	}
	if warns := ValidateConfig(ProviderGitHub, yaml); len(warns) > 0 {
		t.Errorf("validate warnings: %v", warns)
	}
}

// TestGenerateGitHub_Mission verifies the GitHub Actions mission template.
func TestGenerateGitHub_Mission(t *testing.T) {
	yaml, _, err := GenerateConfig(ProviderGitHub, Options{
		Mode:     ModeMission,
		PlanFile: "plans/my-plan.json",
		Workers:  4,
	})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if !strings.Contains(yaml, "plans/my-plan.json") {
		t.Error("mission template should contain plan file path")
	}
	if !strings.Contains(yaml, "workers 4") || !strings.Contains(yaml, "--workers") {
		t.Error("mission template should include --workers flag")
	}
	if warns := ValidateConfig(ProviderGitHub, yaml); len(warns) > 0 {
		t.Errorf("validate warnings: %v", warns)
	}
}

// TestGenerateGitLab_Review verifies the GitLab CI review template.
func TestGenerateGitLab_Review(t *testing.T) {
	yaml, path, err := GenerateConfig(ProviderGitLab, Options{Mode: ModeReview})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if path != ".gitlab-ci.yml" {
		t.Errorf("path = %q, want .gitlab-ci.yml", path)
	}
	for _, want := range []string{
		"ANTHROPIC_API_KEY",
		"stages:",
		"script:",
		"r1 review",
		"merge_request_event",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("gitlab review template missing %q", want)
		}
	}
	if warns := ValidateConfig(ProviderGitLab, yaml); len(warns) > 0 {
		t.Errorf("validate warnings: %v", warns)
	}
}

// TestGenerateGitLab_Mission verifies the GitLab CI mission template.
func TestGenerateGitLab_Mission(t *testing.T) {
	yaml, _, err := GenerateConfig(ProviderGitLab, Options{Mode: ModeMission, Workers: 2})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if !strings.Contains(yaml, "r1 build") {
		t.Error("mission template should contain r1 build command")
	}
	if warns := ValidateConfig(ProviderGitLab, yaml); len(warns) > 0 {
		t.Errorf("validate warnings: %v", warns)
	}
}

// TestGenerateCircleCI_Review verifies the CircleCI review template.
func TestGenerateCircleCI_Review(t *testing.T) {
	yaml, path, err := GenerateConfig(ProviderCircleCI, Options{Mode: ModeReview})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if path != ".circleci/config.yml" {
		t.Errorf("path = %q, want .circleci/config.yml", path)
	}
	for _, want := range []string{
		"version:",
		"jobs:",
		"steps:",
		"ANTHROPIC_API_KEY",
		"r1 review",
		"workflows:",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("circleci review template missing %q", want)
		}
	}
	if warns := ValidateConfig(ProviderCircleCI, yaml); len(warns) > 0 {
		t.Errorf("validate warnings: %v", warns)
	}
}

// TestGenerateCircleCI_AutoFix verifies the CircleCI auto-fix template.
func TestGenerateCircleCI_AutoFix(t *testing.T) {
	yaml, _, err := GenerateConfig(ProviderCircleCI, Options{Mode: ModeAutoFix})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if !strings.Contains(yaml, "scan-repair") {
		t.Error("autofix template should contain scan-repair")
	}
	if warns := ValidateConfig(ProviderCircleCI, yaml); len(warns) > 0 {
		t.Errorf("validate warnings: %v", warns)
	}
}

// TestGenerateCircleCI_Mission verifies the CircleCI mission template.
func TestGenerateCircleCI_Mission(t *testing.T) {
	yaml, _, err := GenerateConfig(ProviderCircleCI, Options{Mode: ModeMission})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if !strings.Contains(yaml, "r1 build") {
		t.Error("circleci mission template should contain r1 build")
	}
	if warns := ValidateConfig(ProviderCircleCI, yaml); len(warns) > 0 {
		t.Errorf("validate warnings: %v", warns)
	}
}

// TestValidateConfig_InvalidYAML confirms a bad YAML string surfaces warnings.
func TestValidateConfig_InvalidYAML(t *testing.T) {
	warns := ValidateConfig(ProviderGitHub, "# empty yaml with nothing useful")
	if len(warns) == 0 {
		t.Error("expected validation warnings for a stripped YAML")
	}
}

// TestUnsupportedProvider returns an error gracefully.
func TestUnsupportedProvider(t *testing.T) {
	_, _, err := GenerateConfig("bitbucket", Options{})
	if err == nil {
		t.Fatal("expected error for unsupported provider")
	}
}

// TestAllModesAllProviders ensures all 9 combinations render without error.
func TestAllModesAllProviders(t *testing.T) {
	for _, prov := range AllProviders() {
		for _, mode := range AllModes() {
			yaml, path, err := GenerateConfig(prov, Options{Mode: mode})
			if err != nil {
				t.Errorf("%s/%s: GenerateConfig error: %v", prov, mode, err)
				continue
			}
			if yaml == "" {
				t.Errorf("%s/%s: empty YAML output", prov, mode)
			}
			if path == "" {
				t.Errorf("%s/%s: empty output path", prov, mode)
			}
			warns := ValidateConfig(prov, yaml)
			if len(warns) > 0 {
				t.Errorf("%s/%s: validate warnings: %v", prov, mode, warns)
			}
		}
	}
}

// TestDefaultOptions verifies that zero-value Options fills sensible defaults.
func TestDefaultOptions(t *testing.T) {
	yaml, _, err := GenerateConfig(ProviderGitHub, Options{}) // all defaults
	if err != nil {
		t.Fatalf("GenerateConfig with defaults: %v", err)
	}
	// Should have defaulted to review mode.
	if !strings.Contains(yaml, "r1 review") {
		t.Error("default mode should be review")
	}
}
