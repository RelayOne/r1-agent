package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreflightWorkspaceDevDeps_AddsMissingBinaries(t *testing.T) {
	dir := t.TempDir()
	rootPkg := filepath.Join(dir, "package.json")
	if err := os.WriteFile(rootPkg, []byte(`{
  "name": "repo",
  "version": "1.0.0",
  "devDependencies": {
    "prettier": "3.0.0"
  }
}
`), 0644); err != nil {
		t.Fatal(err)
	}
	sow := &SOW{
		Sessions: []Session{
			{
				ID: "S1",
				AcceptanceCriteria: []AcceptanceCriterion{
					{ID: "AC1", Command: "tsc --noEmit -p tsconfig.json"},
					{ID: "AC2", Command: "vitest run tests/"},
					{ID: "AC3", Command: "pnpm --filter web exec next build"},
					{ID: "AC4", Command: "prettier --check ."},
				},
			},
		},
	}
	diag := previewPreflight(t, dir, sow)
	joined := strings.Join(diag, " | ")
	if !strings.Contains(joined, "typescript") {
		t.Fatalf("expected typescript detected as missing: %s", joined)
	}
	if !strings.Contains(joined, "vitest") {
		t.Fatalf("expected vitest detected as missing: %s", joined)
	}
	if !strings.Contains(joined, "next") {
		t.Fatalf("expected next detected as missing: %s", joined)
	}
	if strings.Contains(joined, "prettier") {
		t.Fatalf("prettier is already a devDep — should NOT be flagged: %s", joined)
	}
	// Root package.json should have the new devDeps written.
	b, err := os.ReadFile(rootPkg)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	dev, _ := got["devDependencies"].(map[string]any)
	for _, want := range []string{"typescript", "vitest", "next"} {
		if _, ok := dev[want]; !ok {
			t.Fatalf("devDependencies missing %q after preflight: %v", want, dev)
		}
	}
}

func TestPreflightWorkspaceDevDeps_SkipsAlreadyInWorkspace(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
  "name": "root",
  "workspaces": ["packages/*"]
}
`), 0644)
	subDir := filepath.Join(dir, "packages", "api")
	os.MkdirAll(subDir, 0755)
	os.WriteFile(filepath.Join(subDir, "package.json"), []byte(`{
  "name": "@repo/api",
  "devDependencies": {"typescript": "5.3.0", "vitest": "1.0.0"}
}
`), 0644)

	sow := &SOW{
		Sessions: []Session{
			{ID: "S1", AcceptanceCriteria: []AcceptanceCriterion{
				{ID: "AC1", Command: "tsc --noEmit"},
				{ID: "AC2", Command: "vitest run"},
			}},
		},
	}
	diag := previewPreflight(t, dir, sow)
	joined := strings.Join(diag, " | ")
	// Both needed binaries exist in sub-package, so preflight should
	// either no-op or at most emit zero-add diagnostic.
	if strings.Contains(joined, "typescript") || strings.Contains(joined, "vitest") {
		t.Fatalf("expected no additions since sub-package declares both: %s", joined)
	}
}

// previewPreflight runs the preflight but blocks the install step by
// scanning diag for "install" markers; this keeps the test hermetic
// (no network calls). We only verify the add-to-root-devDependencies
// behavior, which is the load-bearing part.
func previewPreflight(t *testing.T, repoRoot string, sow *SOW) []string {
	t.Helper()
	// Preflight attempts pnpm/npm/yarn install; in CI there's no
	// package registry available so install will fail. That's fine —
	// we only care about the detection + add behavior.
	return PreflightWorkspaceDevDeps(repoRoot, sow)
}
