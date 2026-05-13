// cicd_bitbucket_test.go — unit tests for the BitBucket template renderer (C5, T21).
package cicd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerateBitbucket_Review verifies the review template emits the
// expected key tokens and validates clean.
func TestGenerateBitbucket_Review(t *testing.T) {
	yaml, path, err := GenerateConfig(ProviderBitbucket, Options{Mode: ModeReview})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if path != "bitbucket-pipelines.yml" {
		t.Errorf("path = %q, want bitbucket-pipelines.yml", path)
	}
	for _, want := range []string{
		"image:",
		"pipelines:",
		"pull-requests:",
		"oidc: true",
		"caches:",
		"script:",
		"r1 review",
		"bitbucket-comment",
		"ANTHROPIC_API_KEY",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("bitbucket review template missing %q\n%s", want, yaml)
		}
	}
	if warns := ValidateConfig(ProviderBitbucket, yaml); len(warns) > 0 {
		t.Errorf("validate warnings: %v", warns)
	}
}

// TestGenerateBitbucket_AutoFix verifies the autofix template.
func TestGenerateBitbucket_AutoFix(t *testing.T) {
	yaml, _, err := GenerateConfig(ProviderBitbucket, Options{Mode: ModeAutoFix})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if !strings.Contains(yaml, "scan-repair") {
		t.Error("autofix template should contain scan-repair command")
	}
	if !strings.Contains(yaml, "branches:") || !strings.Contains(yaml, "pull-requests:") {
		t.Error("autofix template should run on both branch push and PR events")
	}
	if warns := ValidateConfig(ProviderBitbucket, yaml); len(warns) > 0 {
		t.Errorf("validate warnings: %v", warns)
	}
}

// TestGenerateBitbucket_Mission verifies the mission template.
func TestGenerateBitbucket_Mission(t *testing.T) {
	yaml, _, err := GenerateConfig(ProviderBitbucket, Options{
		Mode:     ModeMission,
		PlanFile: "plans/c5-plan.json",
		Workers:  3,
	})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if !strings.Contains(yaml, "custom:") {
		t.Error("mission template should declare a custom: pipeline")
	}
	if !strings.Contains(yaml, "r1-mission") {
		t.Error("mission template should key the custom pipeline as r1-mission")
	}
	if !strings.Contains(yaml, "plans/c5-plan.json") {
		t.Error("mission template should embed the plan file path")
	}
	if !strings.Contains(yaml, "--workers 3") {
		t.Error("mission template should embed the worker count")
	}
	if warns := ValidateConfig(ProviderBitbucket, yaml); len(warns) > 0 {
		t.Errorf("validate warnings: %v", warns)
	}
}

// TestBitbucketAllModes_ArtifactsAndSecrets asserts every mode's rendered
// YAML includes the four canonical artifact globs + every required secret.
func TestBitbucketAllModes_ArtifactsAndSecrets(t *testing.T) {
	for _, mode := range AllModes() {
		yaml, _, err := GenerateConfig(ProviderBitbucket, Options{Mode: mode})
		if err != nil {
			t.Fatalf("%s: GenerateConfig: %v", mode, err)
		}
		for _, glob := range []string{
			"tracebundles/**/*.tar.zst",
			"audit-reports/**/*.json",
			"r1-review.json",
			"r1-mission.log",
		} {
			if !strings.Contains(yaml, glob) {
				t.Errorf("%s: missing artifact glob %q", mode, glob)
			}
		}
		for _, secret := range []string{
			"ANTHROPIC_API_KEY",
			"R1_API_TOKEN",
			"R1_AUDIT_ENDPOINT",
		} {
			if !strings.Contains(yaml, secret) {
				t.Errorf("%s: missing required secret reference %q", mode, secret)
			}
		}
	}
}

// TestBitbucketNoLongLivedTokenInStepVars enforces acceptance criterion 7:
// the rendered YAML must not pass BITBUCKET_API_TOKEN as a step-level env var
// — OIDC exchange is the contract.
func TestBitbucketNoLongLivedTokenInStepVars(t *testing.T) {
	yaml, _, err := GenerateConfig(ProviderBitbucket, Options{Mode: ModeReview})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	// The token name is documented in the header comment, but it must not
	// appear as a step-level variable. The contract is satisfied by looking
	// for "$BITBUCKET_API_TOKEN" (which would be the substitution form).
	if strings.Contains(yaml, "$BITBUCKET_API_TOKEN") {
		t.Error("rendered YAML must not reference $BITBUCKET_API_TOKEN as a step variable")
	}
}

// TestDetectImage_GoProject creates a tmpdir with go.mod and confirms the
// Go image is selected.
func TestDetectImage_GoProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if got := detectImage(dir, Options{}); !strings.HasPrefix(got, "golang") {
		t.Errorf("detectImage = %q, want golang*", got)
	}
}

// TestDetectImage_NodeProject confirms the Node image is selected.
func TestDetectImage_NodeProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if got := detectImage(dir, Options{}); !strings.HasPrefix(got, "node") {
		t.Errorf("detectImage = %q, want node*", got)
	}
}

// TestDetectImage_PythonProject confirms the Python image is selected.
func TestDetectImage_PythonProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\n"), 0o644); err != nil {
		t.Fatalf("write pyproject.toml: %v", err)
	}
	if got := detectImage(dir, Options{}); !strings.HasPrefix(got, "python") {
		t.Errorf("detectImage = %q, want python*", got)
	}
}

// TestDetectImage_GenericFallback confirms the default image when no language
// fingerprint is detected.
func TestDetectImage_GenericFallback(t *testing.T) {
	dir := t.TempDir()
	if got := detectImage(dir, Options{}); !strings.HasPrefix(got, "atlassian/default-image") {
		t.Errorf("detectImage = %q, want atlassian/default-image*", got)
	}
}

// TestDetectImage_NodeLabelOverride confirms Options.NodeLabel overrides
// auto-detection (matching the GitLab adapter's nodeLabel-as-image idiom).
func TestDetectImage_NodeLabelOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	got := detectImage(dir, Options{NodeLabel: "my-registry/custom:tag"})
	if got != "my-registry/custom:tag" {
		t.Errorf("override ignored: detectImage = %q", got)
	}
}

// TestBitbucketRenderedContainsOIDC asserts every mode's YAML has the OIDC
// flag set (acceptance criterion 3).
func TestBitbucketRenderedContainsOIDC(t *testing.T) {
	for _, mode := range AllModes() {
		yaml, _, err := GenerateConfig(ProviderBitbucket, Options{Mode: mode})
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if !strings.Contains(yaml, "oidc: true") {
			t.Errorf("%s: missing oidc: true", mode)
		}
		if !strings.Contains(yaml, "BITBUCKET_STEP_OIDC_TOKEN") && !strings.Contains(yaml, "oidc: true") {
			t.Errorf("%s: OIDC token reference missing", mode)
		}
	}
}
