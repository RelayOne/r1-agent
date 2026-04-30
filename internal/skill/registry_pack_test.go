package skill

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/skillmfr"
)

func TestDefaultRegistry_LoadsInstalledSignedPackSkills(t *testing.T) {
	proj := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	packRoot := createInstalledPackFixture(t, proj, "signed-pack", "pack-skill", false)

	reg := DefaultRegistry(proj)
	if err := reg.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := reg.Get("pack-skill")
	if got == nil {
		t.Fatalf("installed pack skill not discovered from %s", packRoot)
	}
	if got.Source != skillSourceProject {
		t.Fatalf("skill source = %q, want %q", got.Source, skillSourceProject)
	}
	if !strings.HasSuffix(got.Path, filepath.Join("pack-skill", "SKILL.md")) {
		t.Fatalf("skill path = %q, want pack skill path", got.Path)
	}
}

func TestDefaultRegistry_RejectsTamperedSignedPack(t *testing.T) {
	proj := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	packRoot := createInstalledPackFixture(t, proj, "tampered-pack", "pack-skill", true)

	reg := DefaultRegistry(proj)
	err := reg.Load()
	if err == nil || !strings.Contains(err.Error(), "pack signature invalid") {
		t.Fatalf("Load() error = %v, want pack signature invalid", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(".r1", "skills", "tampered-pack")) {
		t.Fatalf("Load() error = %v, want installed pack link path", err)
	}
	if _, statErr := os.Stat(packRoot); statErr != nil {
		t.Fatalf("pack root vanished: %v", statErr)
	}
}

func createInstalledPackFixture(t *testing.T, repoRoot, packName, skillName string, tamper bool) string {
	t.Helper()

	packRoot := filepath.Join(repoRoot, ".r1", "skills", "packs", packName)
	skillDir := filepath.Join(packRoot, skillName)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	packYAML := fmt.Sprintf("name: %s\nversion: 1.0.0\ndescription: fixture pack\nskill_count: 1\n", packName)
	if err := os.WriteFile(filepath.Join(packRoot, "pack.yaml"), []byte(packYAML), 0o644); err != nil {
		t.Fatalf("write pack.yaml: %v", err)
	}
	manifest := fmt.Sprintf("{\n  \"name\": %q,\n  \"version\": \"0.1.0\",\n  \"description\": \"fixture manifest\",\n  \"inputSchema\": {\"type\": \"object\"},\n  \"outputSchema\": {\"type\": \"object\"},\n  \"whenToUse\": [\"Need fixture coverage\"],\n  \"whenNotToUse\": [\"Need a different fixture\", \"Need a different service\"],\n  \"behaviorFlags\": {\"mutatesState\": false, \"requiresNetwork\": false}\n}\n", skillName)
	if err := os.WriteFile(filepath.Join(skillDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	skillBody := fmt.Sprintf("---\nname: %s\ndescription: fixture skill\nkeywords:\n  - fixture\n---\n# %s\n", skillName, skillName)
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillBody), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	privateKey := ed25519.NewKeyFromSeed([]byte("01234567890123456789012345678901"))
	signature, err := skillmfr.SignPack(packRoot, "", privateKey)
	if err != nil {
		t.Fatalf("SignPack: %v", err)
	}
	if err := skillmfr.WritePackSignature(packRoot, signature); err != nil {
		t.Fatalf("WritePackSignature: %v", err)
	}
	if tamper {
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillBody+"\nTampered.\n"), 0o644); err != nil {
			t.Fatalf("tamper skill body: %v", err)
		}
	}

	linkPath := filepath.Join(repoRoot, ".r1", "skills", packName)
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatalf("mkdir link parent: %v", err)
	}
	relTarget, err := filepath.Rel(filepath.Dir(linkPath), packRoot)
	if err != nil {
		t.Fatalf("filepath.Rel: %v", err)
	}
	if err := os.Symlink(relTarget, linkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	return packRoot
}
