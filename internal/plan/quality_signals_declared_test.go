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

// H-24 regression tests — monorepo-prefix normalizer. H1-v2 + H2-v2
// each logged 200+ gate-hits on the same 8 findings over 6h+ because
// SOW said `app/api/foo/route.ts` but the worker wrote
// `apps/web/src/app/api/foo/route.ts`. fileExistsVariant now checks
// under each `apps/*` and `packages/*` workspace (with and without
// `src/`), so the gate correctly sees the file the worker produced.

func TestScanDeclaredFilesNotCreated_MonorepoAppsSrc(t *testing.T) {
	// Most common pnpm-monorepo Next.js layout: apps/web/src/app/...
	root := t.TempDir()
	mkfile(t, root, "apps/web/src/app/api/v1/users/route.ts")
	got := ScanDeclaredFilesNotCreated(root, []string{
		"app/api/v1/users/route.ts",
	})
	if len(got) != 0 {
		t.Errorf("expected 0 findings (apps/web/src prefix), got %d: %+v", len(got), got)
	}
}

func TestScanDeclaredFilesNotCreated_MonorepoAppsNoSrc(t *testing.T) {
	// Turborepo default: apps/web/app/... (no src/ layer).
	root := t.TempDir()
	mkfile(t, root, "apps/web/app/api/v1/users/route.ts")
	got := ScanDeclaredFilesNotCreated(root, []string{
		"app/api/v1/users/route.ts",
	})
	if len(got) != 0 {
		t.Errorf("expected 0 findings (apps/web no-src prefix), got %d: %+v", len(got), got)
	}
}

func TestScanDeclaredFilesNotCreated_MonorepoPackagesSrc(t *testing.T) {
	// Workspace package: packages/api-client/src/... . Declared must
	// be multi-segment (`src/index.ts`) or the narrowing fires — a
	// bare `index.ts` is correctly treated as a root-only declaration.
	root := t.TempDir()
	mkfile(t, root, "packages/api-client/src/client.ts")
	got := ScanDeclaredFilesNotCreated(root, []string{
		"src/client.ts", // multi-segment: narrowing lets it hit the workspace search
	})
	if len(got) != 0 {
		t.Errorf("expected 0 findings (packages/<name>/src), got %d: %+v", len(got), got)
	}
}

func TestScanDeclaredFilesNotCreated_MonorepoWithCurlyVariant(t *testing.T) {
	// The actual D-opus / H1 / H2 failure pattern: SOW uses `{id}`,
	// worker writes `[id]` under apps/web/src. Both normalizers must
	// chain (monorepo prefix + curly-to-square), so the file is found.
	root := t.TempDir()
	mkfile(t, root, "apps/web/src/app/api/v1/alarms/[id]/acknowledge/route.ts")
	got := ScanDeclaredFilesNotCreated(root, []string{
		"app/api/v1/alarms/{id}/acknowledge/route.ts",
	})
	if len(got) != 0 {
		t.Errorf("expected 0 findings (apps/web/src + {id}→[id]), got %d: %+v", len(got), got)
	}
}

// Codex P2-1 regression: narrowing the monorepo fallback so a
// workspace's package.json (or README, docker-compose, etc.) does
// not silently satisfy a missing root-level file of the same name.

func TestScanDeclaredFilesNotCreated_RootPackageJsonMissingFallbackStaysOff(t *testing.T) {
	// Declared: package.json at repo root. Worker populated workspace
	// package.jsons but never created the root one. This must still
	// flag — pre-narrowing, `apps/web/package.json` would falsely
	// satisfy the check.
	root := t.TempDir()
	mkfile(t, root, "apps/web/package.json")
	mkfile(t, root, "apps/api/package.json")
	mkfile(t, root, "packages/shared/package.json")
	got := ScanDeclaredFilesNotCreated(root, []string{"package.json"})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding (root package.json missing), got %d: %+v", len(got), got)
	}
}

func TestScanDeclaredFilesNotCreated_RootReadmeMissingStaysFlagged(t *testing.T) {
	// Same pattern for README.md — single-segment root doc.
	root := t.TempDir()
	mkfile(t, root, "apps/web/README.md")
	got := ScanDeclaredFilesNotCreated(root, []string{"README.md"})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding (root README missing), got %d: %+v", len(got), got)
	}
}

func TestScanDeclaredFilesNotCreated_AppsPrefixedDeclaredIsLiteral(t *testing.T) {
	// If the SOW already says `apps/web/...`, the path is literal.
	// Fallback must not re-prefix and produce `apps/web/apps/web/...`
	// spurious matches.
	root := t.TempDir()
	mkfile(t, root, "apps/web/page.tsx") // different filename than declared
	got := ScanDeclaredFilesNotCreated(root, []string{"apps/web/notpresent.tsx"})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding (apps/-prefixed literal, miss), got %d: %+v", len(got), got)
	}
}

func TestMonorepoFallbackEligible(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"package.json", false},             // root config, single segment
		{"README.md", false},                // root doc
		{"tsconfig.json", false},            // root config
		{"apps/web/page.tsx", false},        // already workspace-prefixed
		{"packages/shared/x.ts", false},     // already workspace-prefixed
		{".github/workflows/ci.yml", false}, // unambiguously root-only
		{".vscode/settings.json", false},    // editor config
		{"tooling/eslint/index.cjs", false}, // monorepo tooling, root
		{"infra/terraform/main.tf", false},  // infra
		// Codex P2-6: docs/ and scripts/ go back on the deny-list.
		// Masking a missing repo-root docs/scripts file with an
		// accidentally-same-named workspace copy is a worse failure
		// than missing a workspace-local docs false-negative.
		{"docs/architecture.md", false}, // root docs
		{"scripts/build.sh", false},     // root scripts
		// Codex P2-4: tests/ e2e/ examples/ stay workspace-accessible.
		// Real monorepos keep these per-package as much as per-repo.
		{"tests/login.test.ts", true},       // workspace-local tests
		{"e2e/checkout.spec.ts", true},      // workspace-local e2e
		{"examples/basic.ts", true},         // workspace-local examples
		{"app/api/v1/users/route.ts", true}, // Next.js workspace-internal
		{"src/index.ts", true},              // library-style workspace entry
		{"components/Button.tsx", true},     // React component, workspace-internal
		{"lib/client.ts", true},             // common workspace helper dir
		{"types/index.d.ts", true},          // shared types, typically per-workspace
	}
	for _, tc := range cases {
		if got := monorepoFallbackEligible(tc.in); got != tc.want {
			t.Errorf("monorepoFallbackEligible(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// Codex P2-4 / P2-6 combined: tests/e2e/examples stay workspace-searchable;
// docs/scripts do NOT (keeping root-level declarations safe from silent
// workspace-copy masking).
func TestScanDeclaredFilesNotCreated_WorkspaceLocalTestsFound(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, "apps/web/tests/login.test.ts")
	mkfile(t, root, "packages/ui/e2e/button.spec.ts")
	mkfile(t, root, "apps/web/examples/basic.ts")
	got := ScanDeclaredFilesNotCreated(root, []string{
		"tests/login.test.ts",
		"e2e/button.spec.ts",
		"examples/basic.ts",
	})
	if len(got) != 0 {
		t.Errorf("expected 0 findings (workspace-local tests/e2e/examples), got %d: %+v", len(got), got)
	}
}

func TestScanDeclaredFilesNotCreated_RootDocsNotMaskedByWorkspaceCopy(t *testing.T) {
	// Codex P2-6 regression: declared `docs/architecture.md` means ROOT
	// architecture doc. A workspace's copy must NOT satisfy the check.
	root := t.TempDir()
	mkfile(t, root, "apps/web/docs/architecture.md") // workspace copy
	got := ScanDeclaredFilesNotCreated(root, []string{"docs/architecture.md"})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding (root docs missing), got %d: %+v", len(got), got)
	}
}

func TestScanDeclaredFilesNotCreated_RootScriptsNotMaskedByWorkspaceCopy(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, "apps/web/scripts/build.sh") // workspace copy
	got := ScanDeclaredFilesNotCreated(root, []string{"scripts/build.sh"})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding (root scripts missing), got %d: %+v", len(got), got)
	}
}

func TestScanDeclaredFilesNotCreated_NotAMonorepoStillMisses(t *testing.T) {
	// Ensure the fix does not turn into a false-negative factory: a
	// genuinely missing file in a polyrepo must still flag.
	root := t.TempDir()
	mkfile(t, root, "app/api/v1/users/route.ts")
	got := ScanDeclaredFilesNotCreated(root, []string{
		"app/api/v1/alarms/[id]/acknowledge/route.ts", // genuinely missing
	})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for genuinely-missing file, got %d: %+v", len(got), got)
	}
	if got[0].Kind != "declared-file-not-created" {
		t.Errorf("kind = %s", got[0].Kind)
	}
}

func TestScanDeclaredFilesNotCreated_MonorepoMultipleApps(t *testing.T) {
	// Real pnpm workspace: file could be under apps/web, apps/installer,
	// apps/caregiver, packages/api-client, etc. Scanner must find the
	// file regardless of which workspace it lives in. Both paths here
	// are multi-segment so P2-1 narrowing lets them hit the fallback.
	root := t.TempDir()
	mkfile(t, root, "apps/web/src/app/page.tsx")
	mkfile(t, root, "apps/installer/app/page.tsx")
	mkfile(t, root, "apps/caregiver/src/app/page.tsx")
	mkfile(t, root, "packages/shared/src/helpers.ts")
	got := ScanDeclaredFilesNotCreated(root, []string{
		"app/page.tsx",   // matches apps/web/src (and/or others)
		"src/helpers.ts", // multi-segment: matches packages/shared/src via workspace search
	})
	if len(got) != 0 {
		t.Errorf("expected 0 findings (multiple-workspace lookup), got %d: %+v", len(got), got)
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
