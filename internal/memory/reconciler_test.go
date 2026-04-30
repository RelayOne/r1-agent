package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReconcilerReconcile(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "docs", "existing.md")
	if err := writeTestFile(existing, "ok\n"); err != nil {
		t.Fatalf("ensure existing file: %v", err)
	}

	origRoot := reconcilerRepoRoot
	origFile := reconcilerFileExists
	origBranch := reconcilerBranchExists
	origService := reconcilerServiceExists
	t.Cleanup(func() {
		reconcilerRepoRoot = origRoot
		reconcilerFileExists = origFile
		reconcilerBranchExists = origBranch
		reconcilerServiceExists = origService
	})

	reconcilerRepoRoot = func() (string, error) { return root, nil }
	reconcilerFileExists = func(path string) (bool, error) {
		return filepath.Clean(path) == filepath.Clean(existing), nil
	}
	reconcilerBranchExists = func(_ context.Context, branch string) (bool, error) {
		return branch == "main", nil
	}
	reconcilerServiceExists = func(_ context.Context, service string) (bool, error) {
		return service == "api-service", nil
	}

	tests := []struct {
		name        string
		memories    []Memory
		wantDrifts  int
		wantTarget  string
		wantReason  string
		wantSuggest string
	}{
		{
			name: "file exists",
			memories: []Memory{{
				ID:     "m-file-ok",
				Kind:   MemoryKindFile,
				Target: "docs/existing.md",
			}},
			wantDrifts: 0,
		},
		{
			name: "file missing",
			memories: []Memory{{
				ID:     "m-file-missing",
				Kind:   MemoryKindFile,
				Target: "docs/missing.md",
			}},
			wantDrifts:  1,
			wantTarget:  "docs/missing.md",
			wantReason:  "referenced file no longer exists",
			wantSuggest: "update the file path or remove the memory",
		},
		{
			name: "branch exists",
			memories: []Memory{{
				ID:     "m-branch-ok",
				Kind:   MemoryKindBranch,
				Target: "main",
			}},
			wantDrifts: 0,
		},
		{
			name: "branch deleted",
			memories: []Memory{{
				ID:     "m-branch-missing",
				Kind:   MemoryKindBranch,
				Target: "feature/deleted",
			}},
			wantDrifts:  1,
			wantTarget:  "feature/deleted",
			wantReason:  "referenced branch no longer exists",
			wantSuggest: "update the branch name or remove the memory",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reconciler := Reconciler{Memories: tc.memories}
			drifts, err := reconciler.Reconcile(context.Background())
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if len(drifts) != tc.wantDrifts {
				t.Fatalf("len(drifts) = %d, want %d", len(drifts), tc.wantDrifts)
			}
			if tc.wantDrifts == 0 {
				return
			}
			if drifts[0].Target != tc.wantTarget {
				t.Fatalf("Target = %q, want %q", drifts[0].Target, tc.wantTarget)
			}
			if drifts[0].Reason != tc.wantReason {
				t.Fatalf("Reason = %q, want %q", drifts[0].Reason, tc.wantReason)
			}
			if drifts[0].Suggestion != tc.wantSuggest {
				t.Fatalf("Suggestion = %q, want %q", drifts[0].Suggestion, tc.wantSuggest)
			}
		})
	}
}

func writeTestFile(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}
