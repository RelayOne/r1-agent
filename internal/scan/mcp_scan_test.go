package scan

import (
	"os"
	"path/filepath"
	"testing"
)

// TestScanEnvMcpUngated_CIConfig_Fires verifies the env_mcp_ungated rule fires
// when STOKE_MCP_UNGATED=1 appears in a GitHub Actions workflow file.
func TestScanEnvMcpUngated_CIConfig_Fires(t *testing.T) {
	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ci := filepath.Join(wfDir, "ci.yml")
	content := "name: ci\njobs:\n  test:\n    runs-on: ubuntu-latest\n    env:\n      STOKE_MCP_UNGATED=1\n    steps:\n      - run: echo hi\n"
	if err := os.WriteFile(ci, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := ScanFiles(dir, DefaultRules(), []string{filepath.Join(".github", "workflows", "ci.yml")})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range result.Findings {
		if f.Rule == "env_mcp_ungated" {
			found = true
			if f.Severity != "critical" {
				t.Errorf("severity=%q want critical", f.Severity)
			}
			if f.Message != "STOKE_MCP_UNGATED=1 in CI config disables MCP gating — do not use in shared infrastructure" {
				t.Errorf("unexpected message: %q", f.Message)
			}
			if f.Fix != "Remove STOKE_MCP_UNGATED=1 or confine it to ephemeral developer envs" {
				t.Errorf("unexpected fix: %q", f.Fix)
			}
		}
	}
	if !found {
		t.Fatalf("expected env_mcp_ungated finding, got %+v", result.Findings)
	}
}

// TestScanEnvMcpUngated_ScriptsCI_Fires verifies the rule also fires for scripts/ci/**.
func TestScanEnvMcpUngated_ScriptsCI_Fires(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "scripts", "ci")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(sub, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexport STOKE_MCP_UNGATED=1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	result, _ := ScanFiles(dir, DefaultRules(), []string{filepath.Join("scripts", "ci", "run.sh")})
	found := false
	for _, f := range result.Findings {
		if f.Rule == "env_mcp_ungated" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected env_mcp_ungated finding on scripts/ci path, got %+v", result.Findings)
	}
}

// TestScanEnvMcpUngated_ReadmeDoesNotFire verifies the path gate prevents the
// rule from firing on files outside .github/** or scripts/ci/**.
func TestScanEnvMcpUngated_ReadmeDoesNotFire(t *testing.T) {
	dir := t.TempDir()
	readme := filepath.Join(dir, "README.md")
	content := "# Dev tips\n\nTo bypass in local dev only: STOKE_MCP_UNGATED=1\n"
	if err := os.WriteFile(readme, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := ScanFiles(dir, DefaultRules(), []string{"README.md"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range result.Findings {
		if f.Rule == "env_mcp_ungated" {
			t.Fatalf("env_mcp_ungated must not fire outside .github/** or scripts/ci/**, got finding on %s", f.File)
		}
	}
}

// TestScanEnvMcpUngated_FullWalkPicksUpHiddenDir verifies that a full directory
// walk (no modifiedOnly) still scans .github/** workflow files for this rule,
// even though .github starts with a dot (normally skipped).
func TestScanEnvMcpUngated_FullWalkPicksUpHiddenDir(t *testing.T) {
	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ci := filepath.Join(wfDir, "ci.yml")
	if err := os.WriteFile(ci, []byte("env:\n  STOKE_MCP_UNGATED=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := ScanFiles(dir, DefaultRules(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range result.Findings {
		if f.Rule == "env_mcp_ungated" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected env_mcp_ungated finding on full walk, got %+v", result.Findings)
	}
}
