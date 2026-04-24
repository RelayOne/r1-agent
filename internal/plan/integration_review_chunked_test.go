package plan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEnumerateBuckets_EmptyRepo(t *testing.T) {
	dir := t.TempDir()
	buckets := enumerateBuckets(dir)
	if len(buckets) != 0 {
		t.Fatalf("expected no buckets in empty repo, got %v", buckets)
	}
}

func TestEnumerateBuckets_FindsManifests(t *testing.T) {
	dir := t.TempDir()

	// Create apps/web/package.json, packages/types/package.json,
	// packages/utils/package.json, services/api/go.mod,
	// crates/core/Cargo.toml, tools/linter/pyproject.toml.
	writes := []struct {
		path    string
		content string
	}{
		{"apps/web/package.json", `{"name":"web"}`},
		{"packages/types/package.json", `{"name":"types"}`},
		{"packages/utils/package.json", `{"name":"utils"}`},
		{"services/api/go.mod", "module api\ngo 1.22\n"},
		{"crates/core/Cargo.toml", "[package]\nname = \"core\"\n"},
		{"tools/linter/pyproject.toml", "[project]\nname = \"linter\"\n"},
		// Files that should NOT become buckets:
		{"apps/web/src/index.ts", "export {}"}, // nested source file
		{"README.md", "# repo"},                // no manifest
	}
	for _, w := range writes {
		full := filepath.Join(dir, w.path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(w.content), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	got := enumerateBuckets(dir)

	wantSet := map[string]bool{
		filepath.Join("apps", "web"):           true,
		filepath.Join("packages", "types"):     true,
		filepath.Join("packages", "utils"):     true,
		filepath.Join("services", "api"):       true,
		filepath.Join("crates", "core"):        true,
		filepath.Join("tools", "linter"):       true,
	}
	if len(got) != len(wantSet) {
		t.Fatalf("expected %d buckets, got %d: %v", len(wantSet), len(got), got)
	}
	for _, b := range got {
		if !wantSet[b] {
			t.Errorf("unexpected bucket: %s", b)
		}
	}

	// Verify deterministic sort order.
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Errorf("buckets not sorted: %v", got)
		}
	}
}

func TestEnumerateBuckets_SkipsNodeModulesAndDotGit(t *testing.T) {
	dir := t.TempDir()
	// A manifest inside a skipped subtree must NOT become a bucket.
	writes := []string{
		"apps/web/node_modules/foo/package.json",
		"apps/web/.git/package.json",
		"apps/web/package.json", // this one SHOULD be a bucket
	}
	for _, p := range writes {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte("{}"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	got := enumerateBuckets(dir)
	want := filepath.Join("apps", "web")
	if len(got) != 1 || got[0] != want {
		t.Errorf("expected exactly [%q], got %v", want, got)
	}
}

func TestEnumerateBuckets_RespectsMaxDepth(t *testing.T) {
	dir := t.TempDir()
	// Depth 1 (apps/foo/package.json) — valid bucket.
	// Depth 3 (apps/foo/nested/deep/package.json) — beyond cap.
	deep := filepath.Join(dir, "apps", "foo", "nested", "deep")
	if err := os.MkdirAll(deep, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "apps", "foo", "package.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deep, "package.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := enumerateBuckets(dir)
	// Only the depth-1 bucket should appear.
	if len(got) != 1 {
		t.Fatalf("expected 1 bucket, got %v", got)
	}
	if got[0] != filepath.Join("apps", "foo") {
		t.Errorf("expected apps/foo, got %s", got[0])
	}
}

func TestIsTimeoutLikely_DeadlineExceeded(t *testing.T) {
	if !isTimeoutLikely(nil, context.DeadlineExceeded) {
		t.Error("expected timeout=true for DeadlineExceeded")
	}
}

func TestIsTimeoutLikely_NilReportNoCtxErr(t *testing.T) {
	if isTimeoutLikely(nil, nil) {
		t.Error("expected timeout=false for nil report + nil ctx err")
	}
}

func TestIsTimeoutLikely_HaltedSentinel(t *testing.T) {
	cases := []string{
		"turn cap 40 reached without verdict",
		"halted — out of budget",
		"reviewer timed out mid-scan",
	}
	for _, s := range cases {
		r := &IntegrationReport{Summary: s}
		if !isTimeoutLikely(r, nil) {
			t.Errorf("expected timeout=true for summary %q", s)
		}
	}
}

func TestIsTimeoutLikely_NormalSummary(t *testing.T) {
	r := &IntegrationReport{Summary: "scanned 3 packages, verified 12 imports, found 0 gaps"}
	if isTimeoutLikely(r, nil) {
		t.Error("expected timeout=false for clean summary")
	}
}

func TestRunIntegrationReviewChunked_NilProviderNoop(t *testing.T) {
	// Spec: returns nil+nil when prov is nil.
	got, err := RunIntegrationReviewChunked(context.Background(), nil, "", IntegrationReviewInput{
		RepoRoot: t.TempDir(),
	}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil report for nil provider, got %+v", got)
	}
}
