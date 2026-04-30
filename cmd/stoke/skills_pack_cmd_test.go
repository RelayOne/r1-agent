package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
