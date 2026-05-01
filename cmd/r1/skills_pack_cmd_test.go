package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/skillmfr"
	"golang.org/x/crypto/ssh"
)

func TestInstallSkillPackCreatesDualLinks(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	packDir := filepath.Join(repo, ".stoke", "skills", "packs", "actium-studio")
	manifestDir := filepath.Join(packDir, "studio.scaffold_site")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(pack): %v", err)
	}
	packYAML := "name: actium-studio\nversion: 0.1.0\nskill_count: 1\n"
	if err := os.WriteFile(filepath.Join(packDir, "pack.yaml"), []byte(packYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(pack.yaml): %v", err)
	}
	manifest := `{
  "name": "studio.scaffold_site",
  "version": "0.1.0",
  "description": "Scaffold a site",
  "inputSchema": {"type":"object"},
  "outputSchema": {"type":"object"},
  "whenToUse": ["Need to scaffold a site"],
  "whenNotToUse": ["Need to delete a site", "Need a different service"],
  "behaviorFlags": {"mutatesState": true, "requiresNetwork": true}
}`
	if err := os.WriteFile(filepath.Join(manifestDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("WriteFile(manifest): %v", err)
	}

	result, err := installSkillPack(repo, "actium-studio")
	if err != nil {
		t.Fatalf("installSkillPack() error = %v", err)
	}
	if result.PackName != "actium-studio" {
		t.Fatalf("PackName = %q, want actium-studio", result.PackName)
	}
	if result.InstalledCount != 2 {
		t.Fatalf("InstalledCount = %d, want 2", result.InstalledCount)
	}
	if !reflect.DeepEqual(result.InstalledPacks, []string{"actium-studio"}) {
		t.Fatalf("InstalledPacks = %v, want [actium-studio]", result.InstalledPacks)
	}
	for _, linkPath := range []string{
		filepath.Join(repo, ".r1", "skills", "actium-studio"),
		filepath.Join(repo, ".stoke", "skills", "actium-studio"),
	} {
		info, err := os.Lstat(linkPath)
		if err != nil {
			t.Fatalf("Lstat(%q): %v", linkPath, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%q is not a symlink", linkPath)
		}
		resolved, err := filepath.EvalSymlinks(linkPath)
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", linkPath, err)
		}
		if resolved != packDir {
			t.Fatalf("EvalSymlinks(%q) = %q, want %q", linkPath, resolved, packDir)
		}
	}

	if _, err := installSkillPack(repo, "actium-studio"); err != nil {
		t.Fatalf("second installSkillPack() error = %v", err)
	}
}

func TestInstallSkillPackMissingPack(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	if _, err := installSkillPack(repo, "missing-pack"); err == nil {
		t.Fatal("installSkillPack() error = nil, want error")
	}
}

func TestInstallSkillPackResolvesFromUserLibrary(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	packDir := filepath.Join(home, ".r1", "skills", "packs", "shared-pack")
	writePackFixture(t, packDir, "shared-pack", nil)

	result, err := installSkillPack(repo, "shared-pack")
	if err != nil {
		t.Fatalf("installSkillPack() error = %v", err)
	}
	if result.SourcePath != packDir {
		t.Fatalf("SourcePath = %q, want %q", result.SourcePath, packDir)
	}
	for _, linkPath := range []string{
		filepath.Join(repo, ".r1", "skills", "shared-pack"),
		filepath.Join(repo, ".stoke", "skills", "shared-pack"),
	} {
		resolved, err := filepath.EvalSymlinks(linkPath)
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", linkPath, err)
		}
		if resolved != packDir {
			t.Fatalf("EvalSymlinks(%q) = %q, want %q", linkPath, resolved, packDir)
		}
	}
}

func TestInstallSkillPackInstallsDependenciesRecursively(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writePackFixture(t, filepath.Join(repo, ".r1", "skills", "packs", "base-pack"), "base-pack", nil)
	writePackFixture(t, filepath.Join(repo, ".r1", "skills", "packs", "shared-pack"), "shared-pack", []string{"base-pack"})
	writePackFixture(t, filepath.Join(repo, ".r1", "skills", "packs", "app-pack"), "app-pack", []string{"shared-pack", "base-pack"})

	result, err := installSkillPack(repo, "app-pack")
	if err != nil {
		t.Fatalf("installSkillPack() error = %v", err)
	}
	if result.InstalledCount != 6 {
		t.Fatalf("InstalledCount = %d, want 6", result.InstalledCount)
	}
	wantPacks := []string{"app-pack", "base-pack", "shared-pack"}
	if !reflect.DeepEqual(result.InstalledPacks, wantPacks) {
		t.Fatalf("InstalledPacks = %v, want %v", result.InstalledPacks, wantPacks)
	}
	for _, packName := range wantPacks {
		for _, linkPath := range []string{
			filepath.Join(repo, ".r1", "skills", packName),
			filepath.Join(repo, ".stoke", "skills", packName),
		} {
			resolved, err := filepath.EvalSymlinks(linkPath)
			if err != nil {
				t.Fatalf("EvalSymlinks(%q): %v", linkPath, err)
			}
			want := filepath.Join(repo, ".r1", "skills", "packs", packName)
			if resolved != want {
				t.Fatalf("EvalSymlinks(%q) = %q, want %q", linkPath, resolved, want)
			}
		}
	}
}

func TestInstallSkillPackRejectsDependencyCycles(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writePackFixture(t, filepath.Join(repo, ".r1", "skills", "packs", "pack-a"), "pack-a", []string{"pack-b"})
	writePackFixture(t, filepath.Join(repo, ".r1", "skills", "packs", "pack-b"), "pack-b", []string{"pack-a"})

	_, err := installSkillPack(repo, "pack-a")
	if err == nil {
		t.Fatal("installSkillPack() error = nil, want cycle error")
	}
	if !strings.Contains(err.Error(), "skill pack dependency cycle") {
		t.Fatalf("installSkillPack() error = %v, want cycle error", err)
	}
}

func TestInstallSkillPackRejectsTamperedSignedPack(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	packDir := filepath.Join(repo, ".r1", "skills", "packs", "signed-pack")
	writePackFixture(t, packDir, "signed-pack", nil)
	privateKeyPath := writePackSigningKey(t)
	if _, err := signSkillPack(repo, "signed-pack", privateKeyPath, "fixture-key"); err != nil {
		t.Fatalf("signSkillPack() error = %v", err)
	}
	manifestPath := filepath.Join(packDir, "signed-pack.skill", "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(`{"name":"signed-pack.skill","version":"0.2.0","description":"tampered","inputSchema":{"type":"object"},"outputSchema":{"type":"object"},"whenToUse":["tamper"],"whenNotToUse":["other","different"],"behaviorFlags":{"mutatesState":false,"requiresNetwork":false}}`), 0o644); err != nil {
		t.Fatalf("WriteFile(manifest): %v", err)
	}
	if _, err := installSkillPack(repo, "signed-pack"); err == nil || !strings.Contains(err.Error(), "pack signature invalid") {
		t.Fatalf("installSkillPack() error = %v, want pack signature invalid", err)
	}
}

func TestInfoSkillPackReportsMetadataAndInstallState(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writePackFixture(t, filepath.Join(repo, ".r1", "skills", "packs", "shared-pack"), "shared-pack", nil)
	writePackFixture(t, filepath.Join(repo, ".r1", "skills", "packs", "base-pack"), "base-pack", nil)
	packDir := filepath.Join(repo, ".r1", "skills", "packs", "actium-studio")
	writePackFixture(t, packDir, "actium-studio", []string{"shared-pack", "base-pack"})
	packYAML := strings.Join([]string{
		"name: actium-studio",
		"version: 1.2.3",
		"description: Actium Studio operator pack",
		"min_r1_version: 0.9.0",
		"upstream_api_version: 2026-04-30",
		"skill_count: 4",
		"dependencies:",
		"  - shared-pack",
		"  - base-pack",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(packDir, "pack.yaml"), []byte(packYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(pack.yaml): %v", err)
	}

	if _, err := installSkillPack(repo, "actium-studio"); err != nil {
		t.Fatalf("installSkillPack() error = %v", err)
	}

	result, err := infoSkillPack(repo, "actium-studio")
	if err != nil {
		t.Fatalf("infoSkillPack() error = %v", err)
	}
	if result.PackName != "actium-studio" {
		t.Fatalf("PackName = %q, want actium-studio", result.PackName)
	}
	if result.Version != "1.2.3" {
		t.Fatalf("Version = %q, want 1.2.3", result.Version)
	}
	if result.Description != "Actium Studio operator pack" {
		t.Fatalf("Description = %q, want Actium Studio operator pack", result.Description)
	}
	if result.MinR1Version != "0.9.0" {
		t.Fatalf("MinR1Version = %q, want 0.9.0", result.MinR1Version)
	}
	if result.UpstreamAPIVersion != "2026-04-30" {
		t.Fatalf("UpstreamAPIVersion = %q, want 2026-04-30", result.UpstreamAPIVersion)
	}
	if result.DeclaredSkillCount != 4 {
		t.Fatalf("DeclaredSkillCount = %d, want 4", result.DeclaredSkillCount)
	}
	if result.ManifestCount != 1 {
		t.Fatalf("ManifestCount = %d, want 1", result.ManifestCount)
	}
	if !reflect.DeepEqual(result.Dependencies, []string{"shared-pack", "base-pack"}) {
		t.Fatalf("Dependencies = %v, want [shared-pack base-pack]", result.Dependencies)
	}
	if !result.CanonicalInstalled || !result.LegacyInstalled {
		t.Fatalf("install flags = canonical:%t legacy:%t, want both true", result.CanonicalInstalled, result.LegacyInstalled)
	}
	if result.InstalledSourcePath != packDir {
		t.Fatalf("InstalledSourcePath = %q, want %q", result.InstalledSourcePath, packDir)
	}
}

func TestInfoSkillPackResolvesUserLibraryWithoutInstall(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	packDir := filepath.Join(home, ".r1", "skills", "packs", "shared-pack")
	writePackFixture(t, packDir, "shared-pack", nil)

	result, err := infoSkillPack(repo, "shared-pack")
	if err != nil {
		t.Fatalf("infoSkillPack() error = %v", err)
	}
	if result.SourcePath != packDir {
		t.Fatalf("SourcePath = %q, want %q", result.SourcePath, packDir)
	}
	if result.CanonicalInstalled || result.LegacyInstalled {
		t.Fatalf("install flags = canonical:%t legacy:%t, want both false", result.CanonicalInstalled, result.LegacyInstalled)
	}
	if result.InstalledSourcePath != "" {
		t.Fatalf("InstalledSourcePath = %q, want empty", result.InstalledSourcePath)
	}
}

func TestInitSkillPackScaffoldsCanonicalPack(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	result, err := initSkillPack(repo, "invoice-ingestion", "", "", "")
	if err != nil {
		t.Fatalf("initSkillPack() error = %v", err)
	}
	packPath := filepath.Join(repo, ".r1", "skills", "packs", "invoice-ingestion")
	if result.PackPath != packPath {
		t.Fatalf("PackPath = %q, want %q", result.PackPath, packPath)
	}
	if result.SkillName != "invoice-ingestion_sample" {
		t.Fatalf("SkillName = %q, want invoice-ingestion_sample", result.SkillName)
	}
	loaded, err := skillmfr.LoadPack(packPath)
	if err != nil {
		t.Fatalf("LoadPack(%q): %v", packPath, err)
	}
	if loaded.Meta.Name != "invoice-ingestion" {
		t.Fatalf("Meta.Name = %q, want invoice-ingestion", loaded.Meta.Name)
	}
	if loaded.Meta.Version != "0.1.0" {
		t.Fatalf("Meta.Version = %q, want 0.1.0", loaded.Meta.Version)
	}
	if len(loaded.Manifests) != 1 || loaded.Manifests[0].Name != "invoice-ingestion_sample" {
		t.Fatalf("Manifests = %#v, want single starter manifest", loaded.Manifests)
	}
	if _, err := os.Stat(filepath.Join(packPath, "README.md")); err != nil {
		t.Fatalf("Stat(README.md): %v", err)
	}
	if _, err := os.Stat(filepath.Join(packPath, "invoice-ingestion_sample", "SKILL.md")); err != nil {
		t.Fatalf("Stat(SKILL.md): %v", err)
	}
}

func TestInitSkillPackHonorsExplicitMetadata(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	result, err := initSkillPack(repo, "billing-pack", "1.2.3", "Billing operator pack", "billing.lookup_invoice")
	if err != nil {
		t.Fatalf("initSkillPack() error = %v", err)
	}
	if result.SkillName != "billing.lookup_invoice" {
		t.Fatalf("SkillName = %q, want billing.lookup_invoice", result.SkillName)
	}
	loaded, err := skillmfr.LoadPack(result.PackPath)
	if err != nil {
		t.Fatalf("LoadPack(%q): %v", result.PackPath, err)
	}
	if loaded.Meta.Version != "1.2.3" {
		t.Fatalf("Meta.Version = %q, want 1.2.3", loaded.Meta.Version)
	}
	if loaded.Meta.Description != "Billing operator pack" {
		t.Fatalf("Meta.Description = %q, want Billing operator pack", loaded.Meta.Description)
	}
	if len(loaded.Manifests) != 1 || loaded.Manifests[0].Name != "billing.lookup_invoice" {
		t.Fatalf("Manifests = %#v, want billing.lookup_invoice", loaded.Manifests)
	}
}

func TestInitSkillPackRejectsExistingTarget(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	packPath := filepath.Join(repo, ".r1", "skills", "packs", "duplicate-pack")
	if err := os.MkdirAll(packPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(packPath): %v", err)
	}
	if _, err := initSkillPack(repo, "duplicate-pack", "", "", ""); err == nil {
		t.Fatal("initSkillPack() error = nil, want existing-target error")
	}
}

func TestListInstalledSkillPacksReportsInstalledPacks(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writePackFixture(t, filepath.Join(repo, ".r1", "skills", "packs", "base-pack"), "base-pack", nil)
	writePackFixture(t, filepath.Join(repo, ".r1", "skills", "packs", "app-pack"), "app-pack", []string{"base-pack"})

	if _, err := installSkillPack(repo, "app-pack"); err != nil {
		t.Fatalf("installSkillPack() error = %v", err)
	}

	result, err := listInstalledSkillPacks(repo)
	if err != nil {
		t.Fatalf("listInstalledSkillPacks() error = %v", err)
	}
	if result.PackCount != 2 {
		t.Fatalf("PackCount = %d, want 2", result.PackCount)
	}
	want := []skillPackListEntry{
		{
			PackName:           "app-pack",
			SourcePath:         filepath.Join(repo, ".r1", "skills", "packs", "app-pack"),
			CanonicalLinkPath:  filepath.Join(repo, ".r1", "skills", "app-pack"),
			LegacyLinkPath:     filepath.Join(repo, ".stoke", "skills", "app-pack"),
			CanonicalInstalled: true,
			LegacyInstalled:    true,
		},
		{
			PackName:           "base-pack",
			SourcePath:         filepath.Join(repo, ".r1", "skills", "packs", "base-pack"),
			CanonicalLinkPath:  filepath.Join(repo, ".r1", "skills", "base-pack"),
			LegacyLinkPath:     filepath.Join(repo, ".stoke", "skills", "base-pack"),
			CanonicalInstalled: true,
			LegacyInstalled:    true,
		},
	}
	if !reflect.DeepEqual(result.Packs, want) {
		t.Fatalf("Packs = %#v, want %#v", result.Packs, want)
	}
}

func TestListInstalledSkillPacksIgnoresNonPackSkills(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writePackFixture(t, filepath.Join(repo, ".r1", "skills", "packs", "listed-pack"), "listed-pack", nil)
	if _, err := installSkillPack(repo, "listed-pack"); err != nil {
		t.Fatalf("installSkillPack() error = %v", err)
	}
	manualSkill := filepath.Join(repo, ".r1", "skills", "manual-skill")
	if err := os.MkdirAll(manualSkill, 0o755); err != nil {
		t.Fatalf("MkdirAll(manualSkill): %v", err)
	}

	result, err := listInstalledSkillPacks(repo)
	if err != nil {
		t.Fatalf("listInstalledSkillPacks() error = %v", err)
	}
	if result.PackCount != 1 {
		t.Fatalf("PackCount = %d, want 1", result.PackCount)
	}
	if len(result.Packs) != 1 || result.Packs[0].PackName != "listed-pack" {
		t.Fatalf("Packs = %#v, want listed-pack only", result.Packs)
	}
}

func TestListInstalledSkillPacksReportsSingleSideInstall(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	packDir := filepath.Join(repo, ".r1", "skills", "packs", "shared-pack")
	writePackFixture(t, packDir, "shared-pack", nil)
	canonicalLink := filepath.Join(repo, ".r1", "skills", "shared-pack")
	if err := os.MkdirAll(filepath.Dir(canonicalLink), 0o755); err != nil {
		t.Fatalf("MkdirAll(canonicalLink): %v", err)
	}
	relTarget, err := filepath.Rel(filepath.Dir(canonicalLink), packDir)
	if err != nil {
		t.Fatalf("filepath.Rel(): %v", err)
	}
	if err := os.Symlink(relTarget, canonicalLink); err != nil {
		t.Fatalf("Symlink(): %v", err)
	}

	result, err := listInstalledSkillPacks(repo)
	if err != nil {
		t.Fatalf("listInstalledSkillPacks() error = %v", err)
	}
	if result.PackCount != 1 {
		t.Fatalf("PackCount = %d, want 1", result.PackCount)
	}
	got := result.Packs[0]
	if !got.CanonicalInstalled || got.LegacyInstalled {
		t.Fatalf("installed flags = %#v, want canonical only", got)
	}
	if got.LegacyLinkPath != filepath.Join(repo, ".stoke", "skills", "shared-pack") {
		t.Fatalf("LegacyLinkPath = %q, want %q", got.LegacyLinkPath, filepath.Join(repo, ".stoke", "skills", "shared-pack"))
	}
}

func TestListInstalledSkillPacksEmpty(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	result, err := listInstalledSkillPacks(repo)
	if err != nil {
		t.Fatalf("listInstalledSkillPacks() error = %v", err)
	}
	if result.PackCount != 0 {
		t.Fatalf("PackCount = %d, want 0", result.PackCount)
	}
	if len(result.Packs) != 0 {
		t.Fatalf("Packs = %#v, want empty", result.Packs)
	}
}

func TestSearchSkillPacksMatchesMetadataAndManifestNames(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	writePackFixture(t, filepath.Join(repo, ".r1", "skills", "packs", "shared-ledger"), "shared-ledger", nil)
	writePackFixture(t, filepath.Join(repo, ".r1", "skills", "packs", "invoice-ingestion"), "invoice-ingestion", []string{"shared-ledger"})
	writePackFixture(t, filepath.Join(home, ".r1", "skills", "packs", "billing-ops"), "billing-ops", nil)

	invoicePack := filepath.Join(repo, ".r1", "skills", "packs", "invoice-ingestion")
	invoiceYAML := strings.Join([]string{
		"name: invoice-ingestion",
		"version: 1.4.0",
		"description: OCR intake and invoice normalization",
		"skill_count: 1",
		"dependencies:",
		"  - shared-ledger",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(invoicePack, "pack.yaml"), []byte(invoiceYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(invoice pack.yaml): %v", err)
	}

	billingPack := filepath.Join(home, ".r1", "skills", "packs", "billing-ops")
	billingYAML := strings.Join([]string{
		"name: billing-ops",
		"version: 0.8.0",
		"description: Searchable AP controls",
		"skill_count: 1",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(billingPack, "pack.yaml"), []byte(billingYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(billing pack.yaml): %v", err)
	}

	if _, err := installSkillPack(repo, "invoice-ingestion"); err != nil {
		t.Fatalf("installSkillPack() error = %v", err)
	}

	result, err := searchSkillPacks(repo, "invoice")
	if err != nil {
		t.Fatalf("searchSkillPacks() error = %v", err)
	}
	if result.MatchCount != 1 {
		t.Fatalf("MatchCount = %d, want 1", result.MatchCount)
	}
	got := result.Matches[0]
	if got.PackName != "invoice-ingestion" {
		t.Fatalf("PackName = %q, want invoice-ingestion", got.PackName)
	}
	if got.SourceScope != "repo_canonical" {
		t.Fatalf("SourceScope = %q, want repo_canonical", got.SourceScope)
	}
	if !reflect.DeepEqual(got.MatchFields, []string{"name", "description", "manifests"}) {
		t.Fatalf("MatchFields = %v, want [name description manifests]", got.MatchFields)
	}
	if !reflect.DeepEqual(got.ManifestNames, []string{"invoice-ingestion.skill"}) {
		t.Fatalf("ManifestNames = %v, want [invoice-ingestion.skill]", got.ManifestNames)
	}
	if !got.CanonicalInstalled || !got.LegacyInstalled {
		t.Fatalf("install flags = canonical:%t legacy:%t, want both true", got.CanonicalInstalled, got.LegacyInstalled)
	}
}

func TestSearchSkillPacksDedupesByResolutionOrder(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoPack := filepath.Join(repo, ".r1", "skills", "packs", "shared-pack")
	userPack := filepath.Join(home, ".r1", "skills", "packs", "shared-pack")
	writePackFixture(t, repoPack, "shared-pack", nil)
	writePackFixture(t, userPack, "shared-pack", nil)

	repoYAML := strings.Join([]string{
		"name: shared-pack",
		"version: 2.0.0",
		"description: Repo-local override",
		"skill_count: 1",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(repoPack, "pack.yaml"), []byte(repoYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(repo pack.yaml): %v", err)
	}
	userYAML := strings.Join([]string{
		"name: shared-pack",
		"version: 1.0.0",
		"description: User library copy",
		"skill_count: 1",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(userPack, "pack.yaml"), []byte(userYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(user pack.yaml): %v", err)
	}

	result, err := searchSkillPacks(repo, "shared")
	if err != nil {
		t.Fatalf("searchSkillPacks() error = %v", err)
	}
	if result.MatchCount != 1 {
		t.Fatalf("MatchCount = %d, want 1", result.MatchCount)
	}
	got := result.Matches[0]
	if got.SourcePath != repoPack {
		t.Fatalf("SourcePath = %q, want %q", got.SourcePath, repoPack)
	}
	if got.Version != "2.0.0" {
		t.Fatalf("Version = %q, want 2.0.0", got.Version)
	}
}

func TestSearchSkillPacksMatchesDescriptionAndDependencies(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	packDir := filepath.Join(repo, ".r1", "skills", "packs", "billing-ops")
	writePackFixture(t, packDir, "billing-ops", []string{"shared-ledger"})

	packYAML := strings.Join([]string{
		"name: billing-ops",
		"version: 0.8.0",
		"description: Controls for AP approval queues",
		"skill_count: 1",
		"dependencies:",
		"  - shared-ledger",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(packDir, "pack.yaml"), []byte(packYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(pack.yaml): %v", err)
	}

	result, err := searchSkillPacks(repo, "ledger")
	if err != nil {
		t.Fatalf("searchSkillPacks() error = %v", err)
	}
	if result.MatchCount != 1 {
		t.Fatalf("MatchCount = %d, want 1", result.MatchCount)
	}
	if !reflect.DeepEqual(result.Matches[0].MatchFields, []string{"dependencies"}) {
		t.Fatalf("MatchFields = %v, want [dependencies]", result.Matches[0].MatchFields)
	}

	result, err = searchSkillPacks(repo, "approval")
	if err != nil {
		t.Fatalf("searchSkillPacks() error = %v", err)
	}
	if result.MatchCount != 1 {
		t.Fatalf("MatchCount = %d, want 1", result.MatchCount)
	}
	if !reflect.DeepEqual(result.Matches[0].MatchFields, []string{"description"}) {
		t.Fatalf("MatchFields = %v, want [description]", result.Matches[0].MatchFields)
	}
}

func TestPublishSkillPackCopiesPackToUserLibrary(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	packDir := filepath.Join(repo, ".r1", "skills", "packs", "shared-pack")
	writePackFixture(t, packDir, "shared-pack", []string{"base-pack"})

	result, err := publishSkillPack(repo, "shared-pack", "", false)
	if err != nil {
		t.Fatalf("publishSkillPack() error = %v", err)
	}
	if result.PackName != "shared-pack" {
		t.Fatalf("PackName = %q, want shared-pack", result.PackName)
	}
	if result.Version != "0.1.0" {
		t.Fatalf("Version = %q, want 0.1.0", result.Version)
	}
	if result.SourcePath != packDir {
		t.Fatalf("SourcePath = %q, want %q", result.SourcePath, packDir)
	}
	if result.ManifestCount != 1 {
		t.Fatalf("ManifestCount = %d, want 1", result.ManifestCount)
	}
	if result.DeclaredSkillCount != 1 {
		t.Fatalf("DeclaredSkillCount = %d, want 1", result.DeclaredSkillCount)
	}
	if !reflect.DeepEqual(result.Dependencies, []string{"base-pack"}) {
		t.Fatalf("Dependencies = %v, want [base-pack]", result.Dependencies)
	}
	for _, publishPath := range []string{
		filepath.Join(home, ".r1", "skills", "packs", "shared-pack"),
		filepath.Join(home, ".stoke", "skills", "packs", "shared-pack"),
	} {
		publishedPack, err := skillmfr.LoadPack(publishPath)
		if err != nil {
			t.Fatalf("LoadPack(%q): %v", publishPath, err)
		}
		if publishedPack.Meta.Name != "shared-pack" {
			t.Fatalf("published pack name at %q = %q, want shared-pack", publishPath, publishedPack.Meta.Name)
		}
	}
}

func TestPublishSkillPackRejectsExistingTargetWithoutForce(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	packDir := filepath.Join(repo, ".r1", "skills", "packs", "shared-pack")
	writePackFixture(t, packDir, "shared-pack", nil)
	writePackFixture(t, filepath.Join(home, ".r1", "skills", "packs", "shared-pack"), "shared-pack", nil)

	_, err := publishSkillPack(repo, "shared-pack", "", false)
	if err == nil {
		t.Fatal("publishSkillPack() error = nil, want existing target error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("publishSkillPack() error = %v, want existing target error", err)
	}
}

func TestPublishSkillPackForceReplacesPublishedCopy(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	destRoot := t.TempDir()

	packDir := filepath.Join(repo, ".r1", "skills", "packs", "shared-pack")
	writePackFixture(t, packDir, "shared-pack", nil)
	packYAML := strings.Join([]string{
		"name: shared-pack",
		"version: 0.2.0",
		"description: updated pack",
		"skill_count: 1",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(packDir, "pack.yaml"), []byte(packYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(pack.yaml): %v", err)
	}

	publishedDir := filepath.Join(destRoot, ".r1", "skills", "packs", "shared-pack")
	writePackFixture(t, publishedDir, "shared-pack", nil)

	result, err := publishSkillPack(repo, "shared-pack", destRoot, true)
	if err != nil {
		t.Fatalf("publishSkillPack() error = %v", err)
	}
	if result.Version != "0.2.0" {
		t.Fatalf("Version = %q, want 0.2.0", result.Version)
	}

	for _, publishPath := range []string{
		filepath.Join(destRoot, ".r1", "skills", "packs", "shared-pack"),
		filepath.Join(destRoot, ".stoke", "skills", "packs", "shared-pack"),
	} {
		publishedPack, err := skillmfr.LoadPack(publishPath)
		if err != nil {
			t.Fatalf("LoadPack(%q): %v", publishPath, err)
		}
		if publishedPack.Meta.Version != "0.2.0" {
			t.Fatalf("published version at %q = %q, want 0.2.0", publishPath, publishedPack.Meta.Version)
		}
		if publishedPack.Meta.Description != "updated pack" {
			t.Fatalf("published description at %q = %q, want updated pack", publishPath, publishedPack.Meta.Description)
		}
	}
}

func TestSignAndVerifySkillPack(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	packDir := filepath.Join(repo, ".r1", "skills", "packs", "signed-pack")
	writePackFixture(t, packDir, "signed-pack", nil)
	privateKeyPath := writePackSigningKey(t)

	signResult, err := signSkillPack(repo, "signed-pack", privateKeyPath, "")
	if err != nil {
		t.Fatalf("signSkillPack() error = %v", err)
	}
	if signResult.KeyID == "" {
		t.Fatal("signSkillPack() KeyID empty, want derived key id")
	}
	if _, err := os.Stat(signResult.SignaturePath); err != nil {
		t.Fatalf("Stat(signature): %v", err)
	}

	verifyResult, err := verifySkillPack(repo, "signed-pack")
	if err != nil {
		t.Fatalf("verifySkillPack() error = %v", err)
	}
	if verifyResult.KeyID != signResult.KeyID {
		t.Fatalf("verifySkillPack() KeyID = %q, want %q", verifyResult.KeyID, signResult.KeyID)
	}
	if verifyResult.PackDigest != signResult.PackDigest {
		t.Fatalf("verifySkillPack() PackDigest = %q, want %q", verifyResult.PackDigest, signResult.PackDigest)
	}
}

func TestUninstallSkillPackRemovesRequestedPackOnly(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writePackFixture(t, filepath.Join(repo, ".r1", "skills", "packs", "base-pack"), "base-pack", nil)
	writePackFixture(t, filepath.Join(repo, ".r1", "skills", "packs", "shared-pack"), "shared-pack", []string{"base-pack"})
	writePackFixture(t, filepath.Join(repo, ".r1", "skills", "packs", "app-pack"), "app-pack", []string{"shared-pack", "base-pack"})

	if _, err := installSkillPack(repo, "app-pack"); err != nil {
		t.Fatalf("installSkillPack() error = %v", err)
	}

	result, err := uninstallSkillPack(repo, "app-pack")
	if err != nil {
		t.Fatalf("uninstallSkillPack() error = %v", err)
	}
	if result.RemovedCount != 2 {
		t.Fatalf("RemovedCount = %d, want 2", result.RemovedCount)
	}
	for _, removed := range []string{
		filepath.Join(repo, ".r1", "skills", "app-pack"),
		filepath.Join(repo, ".stoke", "skills", "app-pack"),
	} {
		if _, err := os.Lstat(removed); !os.IsNotExist(err) {
			t.Fatalf("Lstat(%q) err = %v, want not exist", removed, err)
		}
	}
	for _, remaining := range []string{
		filepath.Join(repo, ".r1", "skills", "base-pack"),
		filepath.Join(repo, ".stoke", "skills", "base-pack"),
		filepath.Join(repo, ".r1", "skills", "shared-pack"),
		filepath.Join(repo, ".stoke", "skills", "shared-pack"),
	} {
		info, err := os.Lstat(remaining)
		if err != nil {
			t.Fatalf("Lstat(%q): %v", remaining, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%q is not a symlink", remaining)
		}
	}
}

func TestUninstallSkillPackMissingLinksIsNoOp(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	result, err := uninstallSkillPack(repo, "missing-pack")
	if err != nil {
		t.Fatalf("uninstallSkillPack() error = %v", err)
	}
	if result.RemovedCount != 0 {
		t.Fatalf("RemovedCount = %d, want 0", result.RemovedCount)
	}
	if len(result.RemovedPaths) != 0 {
		t.Fatalf("RemovedPaths = %v, want empty", result.RemovedPaths)
	}
}

func TestUninstallSkillPackRejectsNonSymlinkTargets(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	blocked := filepath.Join(repo, ".r1", "skills", "actium-studio")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatalf("MkdirAll(blocked): %v", err)
	}

	_, err := uninstallSkillPack(repo, "actium-studio")
	if err == nil {
		t.Fatal("uninstallSkillPack() error = nil, want non-symlink error")
	}
	if !strings.Contains(err.Error(), "is not a symlink") {
		t.Fatalf("uninstallSkillPack() error = %v, want non-symlink error", err)
	}

	if _, statErr := os.Stat(blocked); statErr != nil {
		t.Fatalf("Stat(%q): %v", blocked, statErr)
	}
}

func TestUpdateSkillPackSkipsRepoLocalGitPullAndRelinksDependencies(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writePackFixture(t, filepath.Join(repo, ".r1", "skills", "packs", "base-pack"), "base-pack", nil)
	writePackFixture(t, filepath.Join(repo, ".r1", "skills", "packs", "app-pack"), "app-pack", []string{"base-pack"})

	result, err := updateSkillPack(repo, "app-pack")
	if err != nil {
		t.Fatalf("updateSkillPack() error = %v", err)
	}
	if result.UpdatedCount != 2 {
		t.Fatalf("UpdatedCount = %d, want 2", result.UpdatedCount)
	}
	if len(result.PulledGitDirs) != 0 {
		t.Fatalf("PulledGitDirs = %v, want empty", result.PulledGitDirs)
	}
	for _, pack := range result.UpdatedPacks {
		if pack.PullStatus != skillPackPullStatusSkippedRepoLocal {
			t.Fatalf("PullStatus(%s) = %q, want %q", pack.PackName, pack.PullStatus, skillPackPullStatusSkippedRepoLocal)
		}
		for _, linkPath := range []string{pack.CanonicalLinkPath, pack.LegacyLinkPath} {
			resolved, err := filepath.EvalSymlinks(linkPath)
			if err != nil {
				t.Fatalf("EvalSymlinks(%q): %v", linkPath, err)
			}
			want := filepath.Join(repo, ".r1", "skills", "packs", pack.PackName)
			if resolved != want {
				t.Fatalf("EvalSymlinks(%q) = %q, want %q", linkPath, resolved, want)
			}
		}
	}
}

func TestUpdateSkillPackPullsExternalGitSourceAndInstallsNewDependency(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	remote := filepath.Join(t.TempDir(), "shared-pack.git")
	runGit(t, filepath.Dir(remote), "init", "--bare", remote)

	author := filepath.Join(t.TempDir(), "author")
	runGit(t, filepath.Dir(author), "clone", remote, author)
	runGit(t, author, "config", "user.name", "Test User")
	runGit(t, author, "config", "user.email", "test@example.com")
	writePackFixture(t, author, "shared-pack", nil)
	runGit(t, author, "add", ".")
	runGit(t, author, "commit", "-m", "seed pack")
	runGit(t, author, "push", "-u", "origin", "HEAD")

	basePackDir := filepath.Join(home, ".r1", "skills", "packs", "base-pack")
	writePackFixture(t, basePackDir, "base-pack", nil)

	localPackDir := filepath.Join(home, ".r1", "skills", "packs", "shared-pack")
	runGit(t, filepath.Dir(localPackDir), "clone", remote, localPackDir)

	if _, err := installSkillPack(repo, "shared-pack"); err != nil {
		t.Fatalf("installSkillPack() error = %v", err)
	}

	writePackFixture(t, author, "shared-pack", []string{"base-pack"})
	runGit(t, author, "add", ".")
	runGit(t, author, "commit", "-m", "add dependency")
	runGit(t, author, "push")

	result, err := updateSkillPack(repo, "shared-pack")
	if err != nil {
		t.Fatalf("updateSkillPack() error = %v", err)
	}
	if result.UpdatedCount != 2 {
		t.Fatalf("UpdatedCount = %d, want 2", result.UpdatedCount)
	}
	if !reflect.DeepEqual(result.PulledGitDirs, []string{localPackDir}) {
		t.Fatalf("PulledGitDirs = %v, want [%s]", result.PulledGitDirs, localPackDir)
	}

	shared := updatedPackEntry(t, result, "shared-pack")
	if shared.PullStatus != skillPackPullStatusPulled {
		t.Fatalf("shared-pack PullStatus = %q, want %q", shared.PullStatus, skillPackPullStatusPulled)
	}
	if shared.GitRoot != localPackDir {
		t.Fatalf("shared-pack GitRoot = %q, want %q", shared.GitRoot, localPackDir)
	}
	base := updatedPackEntry(t, result, "base-pack")
	if base.PullStatus != skillPackPullStatusSkippedRepoLocal && base.PullStatus != skillPackPullStatusSkippedNoGit {
		t.Fatalf("base-pack PullStatus = %q, want repo-local or no-git skip", base.PullStatus)
	}

	updatedPack, err := skillmfr.LoadPack(localPackDir)
	if err != nil {
		t.Fatalf("skillPackMetadata(localPackDir): %v", err)
	}
	if !reflect.DeepEqual(updatedPack.Meta.Dependencies, []string{"base-pack"}) {
		t.Fatalf("Dependencies = %v, want [base-pack]", updatedPack.Meta.Dependencies)
	}

	for _, linkPath := range []string{
		filepath.Join(repo, ".r1", "skills", "base-pack"),
		filepath.Join(repo, ".stoke", "skills", "base-pack"),
	} {
		resolved, err := filepath.EvalSymlinks(linkPath)
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", linkPath, err)
		}
		if resolved != basePackDir {
			t.Fatalf("EvalSymlinks(%q) = %q, want %q", linkPath, resolved, basePackDir)
		}
	}
}

func writePackFixture(t *testing.T, packDir, name string, dependencies []string) {
	t.Helper()

	manifestDir := filepath.Join(packDir, name+".skill")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(pack): %v", err)
	}
	packYAML := "name: " + name + "\nversion: 0.1.0\nskill_count: 1\n"
	if len(dependencies) > 0 {
		packYAML += "dependencies:\n"
		for _, dependency := range dependencies {
			packYAML += "  - " + dependency + "\n"
		}
	}
	if err := os.WriteFile(filepath.Join(packDir, "pack.yaml"), []byte(packYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(pack.yaml): %v", err)
	}
	manifest := `{
  "name": "` + name + `.skill",
  "version": "0.1.0",
  "description": "Fixture manifest",
  "inputSchema": {"type":"object"},
  "outputSchema": {"type":"object"},
  "whenToUse": ["Need fixture coverage"],
  "whenNotToUse": ["Need a different fixture", "Need a different service"],
  "behaviorFlags": {"mutatesState": false, "requiresNetwork": false}
}`
	if err := os.WriteFile(filepath.Join(manifestDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("WriteFile(manifest): %v", err)
	}
}

func writePackSigningKey(t *testing.T) string {
	t.Helper()

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey(): %v", err)
	}
	sshKey, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		t.Fatalf("MarshalPrivateKey(): %v", err)
	}
	path := filepath.Join(t.TempDir(), "pack-signing-key")
	if err := os.WriteFile(path, pem.EncodeToMemory(sshKey), 0o600); err != nil {
		t.Fatalf("WriteFile(signing key): %v", err)
	}
	return path
}

func updatedPackEntry(t *testing.T, result *skillPackUpdateResult, packName string) skillPackUpdateEntry {
	t.Helper()

	for _, entry := range result.UpdatedPacks {
		if entry.PackName == packName {
			return entry
		}
	}
	t.Fatalf("updated pack %q not found in %#v", packName, result.UpdatedPacks)
	return skillPackUpdateEntry{}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (dir=%q): %v\n%s", strings.Join(args, " "), dir, err, string(out))
	}
}
