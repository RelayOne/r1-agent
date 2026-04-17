package plan

import (
	"os"
	"path/filepath"
	"testing"
)

func mkfile(t *testing.T, root, rel string) {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte("export const x = 1\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func TestScanDeclaredFilesNotCreated_EmptyInputs(t *testing.T) {
	if got := ScanDeclaredFilesNotCreated("", nil); got != nil {
		t.Errorf("empty root+declared should return nil, got %v", got)
	}
	if got := ScanDeclaredFilesNotCreated(t.TempDir(), nil); got != nil {
		t.Errorf("nil declared should return nil, got %v", got)
	}
	if got := ScanDeclaredFilesNotCreated(t.TempDir(), []string{"", "   "}); got != nil {
		t.Errorf("whitespace-only declared should return nil, got %v", got)
	}
}

func TestScanDeclaredFilesNotCreated_AllPresent(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, "app/api/v1/users/route.ts")
	mkfile(t, root, "packages/ui/src/Button.tsx")
	got := ScanDeclaredFilesNotCreated(root, []string{
		"app/api/v1/users/route.ts",
		"packages/ui/src/Button.tsx",
	})
	if len(got) != 0 {
		t.Errorf("expected 0 findings for all-present, got %d: %+v", len(got), got)
	}
}

func TestScanDeclaredFilesNotCreated_Missing(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, "app/api/v1/users/route.ts")
	got := ScanDeclaredFilesNotCreated(root, []string{
		"app/api/v1/users/route.ts",
		"app/api/v1/alarms/{id}/acknowledge/route.ts", // missing, no variant present
	})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(got), got)
	}
	f := got[0]
	if f.Severity != SevBlocking {
		t.Errorf("severity = %v, want blocking", f.Severity)
	}
	if f.Kind != "declared-file-not-created" {
		t.Errorf("kind = %s", f.Kind)
	}
}

func TestScanDeclaredFilesNotCreated_CurlyVsSquareVariant(t *testing.T) {
	// The D-opus pattern: SOW declares `{id}` but worker creates `[id]`.
	// We treat that as satisfied — the route handler IS there, just
	// under the Next.js-convention path.
	root := t.TempDir()
	mkfile(t, root, "app/api/v1/alarms/[id]/acknowledge/route.ts")
	got := ScanDeclaredFilesNotCreated(root, []string{
		"app/api/v1/alarms/{id}/acknowledge/route.ts",
	})
	if len(got) != 0 {
		t.Errorf("expected 0 findings ({id}↔[id] variant), got %d: %+v", len(got), got)
	}
}

func TestScanDeclaredFilesNotCreated_SquareVsCurlyVariant(t *testing.T) {
	// Reverse direction: SOW uses `[id]`, worker files under `{id}`.
	// Same class of mismatch; scanner should also tolerate it.
	root := t.TempDir()
	mkfile(t, root, "app/api/v1/buildings/{id}/route.ts")
	got := ScanDeclaredFilesNotCreated(root, []string{
		"app/api/v1/buildings/[id]/route.ts",
	})
	if len(got) != 0 {
		t.Errorf("expected 0 findings ([id]↔{id} variant), got %d: %+v", len(got), got)
	}
}

func TestScanDeclaredFilesNotCreated_TsTsxVariant(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, "app/api/v1/users/route.tsx") // worker wrote tsx
	got := ScanDeclaredFilesNotCreated(root, []string{
		"app/api/v1/users/route.ts", // declared ts
	})
	if len(got) != 0 {
		t.Errorf("expected 0 findings (.ts↔.tsx variant), got %d: %+v", len(got), got)
	}
}

func TestScanDeclaredFilesNotCreated_TrailingSlashAndLeadingSlash(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, "app/api/v1/users/route.ts")
	got := ScanDeclaredFilesNotCreated(root, []string{
		"/app/api/v1/users/route.ts",  // leading slash — should be tolerated
		"app/api/v1/users/route.ts/",  // trailing slash — should be tolerated
	})
	if len(got) != 0 {
		t.Errorf("expected 0 findings (slash-normalization), got %d: %+v", len(got), got)
	}
}

func TestScanDeclaredFilesNotCreated_MultipleMissing(t *testing.T) {
	// D-opus-full's real pattern: 5 sow-declared endpoints flagged for
	// 2+ hours because the worker keeps editing adjacent files. This
	// test mirrors that exact shape — 5 declared routes, none exist.
	root := t.TempDir()
	declared := []string{
		"app/api/v1/alarms/{id}/acknowledge/route.ts",
		"app/api/v1/alarms/{id}/resolve/route.ts",
		"app/api/v1/alert-rules/{id}/preview/route.ts",
		"app/api/v1/buildings/{id}/analytics/export/route.ts",
		"app/api/v1/buildings/{id}/route.ts",
	}
	got := ScanDeclaredFilesNotCreated(root, declared)
	if len(got) != 5 {
		t.Errorf("expected 5 findings, got %d", len(got))
	}
}

func TestPathVariants_CurlyToSquare(t *testing.T) {
	got := pathVariants("app/api/v1/users/{id}/route.ts")
	want := map[string]bool{
		"app/api/v1/users/{id}/route.ts":  true,
		"app/api/v1/users/[id]/route.ts":  true,
		"app/api/v1/users/{id}/route.tsx": true,
		"app/api/v1/users/[id]/route.tsx": true,
	}
	for _, v := range got {
		delete(want, v)
	}
	if len(want) != 0 {
		t.Errorf("missing variants: %v; got: %v", want, got)
	}
}

func TestExtractDeclaredFiles_ExplicitPaths(t *testing.T) {
	// SOW prose with 3 distinct explicit paths inline. All three
	// should surface.
	prose := "We need to write app/api/v1/users/route.ts and " +
		"packages/ui/src/Button.tsx plus cmd/stoke/main.go to " +
		"complete this."
	got := ExtractDeclaredFiles(prose)
	want := []string{
		"app/api/v1/users/route.ts",
		"packages/ui/src/Button.tsx",
		"cmd/stoke/main.go",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d paths, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestExtractDeclaredFiles_BulletList(t *testing.T) {
	prose := `The deliverables are:
- app/api/v1/alarms/{id}/acknowledge/route.ts
- app/api/v1/alarms/{id}/resolve/route.ts
- packages/api-client/src/index.ts
Done.`
	got := ExtractDeclaredFiles(prose)
	want := map[string]bool{
		"app/api/v1/alarms/{id}/acknowledge/route.ts": true,
		"app/api/v1/alarms/{id}/resolve/route.ts":     true,
		"packages/api-client/src/index.ts":            true,
	}
	if len(got) != 3 {
		t.Fatalf("got %d paths, want 3: %v", len(got), got)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected path: %q", p)
		}
	}
}

func TestExtractDeclaredFiles_InsideCodeFence(t *testing.T) {
	prose := "Create these files:\n" +
		"```\n" +
		"app/api/v1/health/route.ts\n" +
		"packages/ui/src/Card.tsx\n" +
		"```\n" +
		"And wire them in."
	got := ExtractDeclaredFiles(prose)
	if len(got) != 2 {
		t.Fatalf("got %d paths, want 2: %v", len(got), got)
	}
	want := map[string]bool{
		"app/api/v1/health/route.ts": true,
		"packages/ui/src/Card.tsx":   true,
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected path: %q", p)
		}
	}
}

func TestExtractDeclaredFiles_NoPaths(t *testing.T) {
	prose := "We should refactor the authentication flow and improve " +
		"the UX for the settings page. Ship by Friday."
	got := ExtractDeclaredFiles(prose)
	if len(got) != 0 {
		t.Errorf("expected 0 paths, got %d: %v", len(got), got)
	}
}

func TestExtractDeclaredFiles_FiltersURLsAndNonPaths(t *testing.T) {
	prose := "See https://example.com/api/v1/foo.json for schema. " +
		"Also http://docs.site.io/guide.md has more info. " +
		"Absolute path /etc/hosts.yaml is irrelevant. " +
		"A bare file README.md should NOT match either. " +
		"But src/app/config.yaml SHOULD match."
	got := ExtractDeclaredFiles(prose)
	if len(got) != 1 {
		t.Fatalf("got %d paths, want 1 (src/app/config.yaml): %v", len(got), got)
	}
	if got[0] != "src/app/config.yaml" {
		t.Errorf("got %q, want src/app/config.yaml", got[0])
	}
}

func TestExtractDeclaredFiles_Dedups(t *testing.T) {
	prose := "Edit app/api/v1/users/route.ts. Again edit " +
		"app/api/v1/users/route.ts for the POST handler."
	got := ExtractDeclaredFiles(prose)
	if len(got) != 1 {
		t.Errorf("expected 1 deduplicated path, got %d: %v", len(got), got)
	}
}

func TestExtractDeclaredFiles_EmptyInput(t *testing.T) {
	if got := ExtractDeclaredFiles(""); got != nil {
		t.Errorf("empty prose should return nil, got %v", got)
	}
	if got := ExtractDeclaredFiles("   \n\t  "); got != nil {
		t.Errorf("whitespace-only prose should return nil, got %v", got)
	}
}

func TestPathVariants_MultipleBrackets(t *testing.T) {
	// nested dynamic segments (e.g. buildings/[buildingId]/devices/[deviceId])
	got := pathVariants("app/api/v1/buildings/{buildingId}/devices/{deviceId}/route.ts")
	found := false
	for _, v := range got {
		if v == "app/api/v1/buildings/[buildingId]/devices/[deviceId]/route.ts" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected both curly pairs converted to square, got: %v", got)
	}
}
