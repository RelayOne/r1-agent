package verify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDocDriftCheckerCheck(t *testing.T) {
	origNow := docDriftNow
	origCommitExists := docDriftCommitExists
	t.Cleanup(func() {
		docDriftNow = origNow
		docDriftCommitExists = origCommitExists
	})

	docDriftNow = func() time.Time {
		return time.Date(2026, time.April, 30, 0, 0, 0, 0, time.UTC)
	}

	tests := []struct {
		name              string
		commitExists      func(context.Context, string, string) (bool, error)
		readmeBody        string
		wantKindsContains []string
	}{
		{
			name: "finds stale doc drift",
			commitExists: func(_ context.Context, _ string, ref string) (bool, error) {
				return ref == "abc1234", nil
			},
			readmeBody: strings.Join([]string{
				"# README",
				"Last updated: 2024-01-01",
				"TODO: rewrite the rollout notes.",
				"See `docs/missing.md` for the old plan.",
				"Reference commit deadbee for the prior release.",
				"",
			}, "\n"),
			wantKindsContains: []string{"stale_date", "todo_marker", "missing_file_ref", "missing_commit_ref"},
		},
		{
			name: "passes clean canonical docs",
			commitExists: func(_ context.Context, _ string, ref string) (bool, error) {
				return ref == "abc1234", nil
			},
			readmeBody: strings.Join([]string{
				"# README",
				"Last updated: 2026-04-01",
				"See `docs/existing.md` for the current plan.",
				"Reference commit abc1234 for the prior release.",
				"",
			}, "\n"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeCanonicalDocs(t, root, tc.readmeBody)
			docDriftCommitExists = tc.commitExists

			checker := DocDriftChecker{Repo: root}
			findings, err := checker.Check(context.Background())
			if err != nil {
				t.Fatalf("Check: %v", err)
			}

			gotKinds := make([]string, 0, len(findings))
			for _, finding := range findings {
				gotKinds = append(gotKinds, finding.Kind)
			}

			for _, wantKind := range tc.wantKindsContains {
				if !containsString(gotKinds, wantKind) {
					t.Fatalf("expected finding kind %q, got %v", wantKind, gotKinds)
				}
			}
			if len(tc.wantKindsContains) == 0 && len(findings) != 0 {
				t.Fatalf("expected no findings, got %#v", findings)
			}
		})
	}
}

func writeCanonicalDocs(t *testing.T, root, readmeBody string) {
	t.Helper()

	docsDir := filepath.Join(root, "docs")
	if err := writeDocFile(filepath.Join(docsDir, "existing.md"), "# existing\n"); err != nil {
		t.Fatalf("seed existing doc: %v", err)
	}
	if err := writeDocFile(filepath.Join(root, "README.md"), readmeBody); err != nil {
		t.Fatalf("write README: %v", err)
	}

	for _, rel := range canonicalDocPaths[1:] {
		full := filepath.Join(root, rel)
		if err := writeDocFile(full, "# ok\nLast updated: 2026-04-01\n"); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

func writeDocFile(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
