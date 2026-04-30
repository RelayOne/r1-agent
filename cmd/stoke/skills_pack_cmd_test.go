package main

import (
	"os"
	"path/filepath"
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
