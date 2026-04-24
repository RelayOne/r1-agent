package skill

import (
	"path/filepath"
	"testing"

	"github.com/RelayOne/r1/internal/skillmfr"
)

// TestActiumStudioPackSeed asserts the Actium Studio skill pack at
// `.stoke/skills/packs/actium-studio/` loads, every manifest.json
// parses + validates, and the set matches the expected roster.
//
// Batch 2 (this file): 5 hand-authored heroes (PR #55 seed) + 20
// high-frequency thin wrappers (sites, pages, blog, media, navigation,
// snapshots, theme). Remaining ~28 thin wrappers land in later batches
// per work-r1-actium-studio-skills.md §1.2.
//
// The test is path-relative to the package source (go test runs with
// cwd == internal/skill), so it finds the pack regardless of where the
// caller invokes `go test` from.
func TestActiumStudioPackSeed(t *testing.T) {
	// internal/skill → repo root is two levels up.
	packRoot := filepath.Join("..", "..", ".stoke", "skills", "packs", "actium-studio")

	loaded, err := skillmfr.LoadPack(packRoot)
	if err != nil {
		t.Fatalf("LoadPack(%q): %v", packRoot, err)
	}

	if loaded.Meta.Name != "actium-studio" {
		t.Errorf("pack name = %q, want %q", loaded.Meta.Name, "actium-studio")
	}
	if loaded.Meta.Version == "" {
		t.Errorf("pack version empty")
	}

	// Expected skills — alphabetical (LoadPack sorts by Name). Update
	// this list and pack.yaml's skill_count together when the next batch
	// lands.
	want := []string{
		// Batch 2 — thin wrappers
		"studio.create_page",
		"studio.create_post",
		"studio.create_site",
		"studio.create_snapshot",
		// PR #55 seed — hero
		"studio.diff_versions",
		"studio.get_navigation",
		"studio.get_page",
		"studio.get_post",
		"studio.get_site",
		"studio.get_theme",
		"studio.list_media",
		"studio.list_pages",
		"studio.list_posts",
		"studio.list_sites",
		"studio.list_snapshots",
		// PR #55 seed — hero
		"studio.publish",
		"studio.publish_page",
		"studio.publish_post",
		// PR #55 seed — hero
		"studio.scaffold_site",
		"studio.site_status",
		"studio.unpublish_page",
		// PR #55 seed — hero
		"studio.update_content",
		"studio.update_page",
		"studio.update_post",
		"studio.update_site",
	}
	if got, wantN := len(loaded.Manifests), len(want); got != wantN {
		t.Fatalf("manifest count = %d, want %d (names loaded: %v)",
			got, wantN, manifestNames(loaded.Manifests))
	}
	for i, m := range loaded.Manifests {
		if m.Name != want[i] {
			t.Errorf("manifests[%d].Name = %q, want %q", i, m.Name, want[i])
		}
		// Belt-and-suspenders: LoadPack already validates, but re-
		// validating here makes the failure message point at this
		// test when someone edits a manifest into an invalid state.
		if err := m.Validate(); err != nil {
			t.Errorf("manifest %q failed Validate(): %v", m.Name, err)
		}
	}

	// Pack metadata should match actual skill count (caught drift
	// between pack.yaml claim and filesystem reality).
	if loaded.Meta.SkillCount != 0 && loaded.Meta.SkillCount != len(loaded.Manifests) {
		t.Errorf("pack.yaml skill_count=%d but loader found %d skills",
			loaded.Meta.SkillCount, len(loaded.Manifests))
	}
}

// TestActiumStudioPackRegisters exercises RegisterPack end-to-end:
// loads the pack and registers every manifest with a fresh
// skillmfr.Registry. Guards the integration path the production
// `r1 skills pack install` command (phase R1S-1.4) will use.
func TestActiumStudioPackRegisters(t *testing.T) {
	packRoot := filepath.Join("..", "..", ".stoke", "skills", "packs", "actium-studio")

	mr := skillmfr.NewRegistry()
	n, err := skillmfr.RegisterPack(mr, packRoot)
	if err != nil {
		t.Fatalf("RegisterPack: %v", err)
	}
	if n == 0 {
		t.Fatal("RegisterPack registered 0 manifests")
	}
	// Spot-check a mix of hero and batch-2 names resolve after register.
	for _, name := range []string{
		// PR #55 heroes
		"studio.scaffold_site",
		"studio.update_content",
		"studio.publish",
		"studio.diff_versions",
		"studio.site_status",
		// Batch 2 representatives
		"studio.list_sites",
		"studio.get_page",
		"studio.create_post",
		"studio.publish_page",
		"studio.create_snapshot",
	} {
		if _, ok := mr.Get(name); !ok {
			t.Errorf("manifest %q not in registry after RegisterPack", name)
		}
	}
}

func manifestNames(ms []skillmfr.Manifest) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Name
	}
	return out
}
