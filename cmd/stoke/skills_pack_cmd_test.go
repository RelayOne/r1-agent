package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/skillmfr"
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
