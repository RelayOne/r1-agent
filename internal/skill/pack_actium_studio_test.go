package skill

import (
	"path/filepath"
	"testing"

	"github.com/RelayOne/r1-agent/internal/skillmfr"
)

// TestActiumStudioPackSeed asserts the Actium Studio skill pack at
// `.stoke/skills/packs/actium-studio/` loads, every manifest.json
// parses + validates, and the set matches the expected roster.
//
// Roster: 5 hand-authored hero seeds (PR #55) + 20 batch-2 thin
// wrappers (PR #64) + 31 batch-3 thin wrappers (this PR). Total 56
// manifests covering every bundled tool enumerated in
// work-r1-actium-studio-skills.md §1 that has a concrete Studio
// endpoint today.
//
// The three membership-admin tools (invite_member, update_member_role,
// remove_member) are absent by design per work order §1.1. A manifest
// for studio.list_templates is absent because the Studio templates
// endpoint itself is unshipped (work order §1.2 item 5).
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

	// Expected skills — byte-order (LoadPack sorts by Name using Go's
	// default string comparison, which is byte-wise: '_' 0x5F < 's'
	// 0x73, so e.g. list_form_submissions precedes list_forms).
	want := []string{
		"studio.add_keyword",
		"studio.create_blog_category",
		"studio.create_blog_tag",
		"studio.create_page",
		"studio.create_post",
		"studio.create_redirect",
		"studio.create_site",
		"studio.create_snapshot",
		"studio.delete_blog_category",
		"studio.delete_blog_tag",
		"studio.delete_redirect",
		"studio.diff_versions",
		"studio.get_analytics_overview",
		"studio.get_billing_overview",
		"studio.get_credit_balance",
		"studio.get_form",
		"studio.get_media",
		"studio.get_navigation",
		"studio.get_page",
		"studio.get_page_analytics",
		"studio.get_post",
		"studio.get_scaffold_status",
		"studio.get_seo_report",
		"studio.get_settings",
		"studio.get_site",
		"studio.get_staging_info",
		"studio.get_theme",
		"studio.list_blog_categories",
		"studio.list_blog_tags",
		"studio.list_form_submissions",
		"studio.list_forms",
		"studio.list_keywords",
		"studio.list_media",
		"studio.list_members",
		"studio.list_pages",
		"studio.list_posts",
		"studio.list_redirects",
		"studio.list_sites",
		"studio.list_snapshots",
		"studio.promote_staging",
		"studio.publish",
		"studio.publish_page",
		"studio.publish_post",
		"studio.restore_snapshot",
		"studio.scaffold_site",
		"studio.site_status",
		"studio.trigger_seo_audit",
		"studio.unpublish_page",
		"studio.update_content",
		"studio.update_navigation",
		"studio.update_page",
		"studio.update_post",
		"studio.update_redirect",
		"studio.update_settings",
		"studio.update_site",
		"studio.update_theme_tokens",
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
	// Spot-check at least one representative per category resolves
	// after register.
	for _, name := range []string{
		// Hero seeds (PR #55)
		"studio.scaffold_site",
		"studio.update_content",
		"studio.publish",
		"studio.diff_versions",
		"studio.site_status",
		// Batch-2 wrappers (PR #64)
		"studio.list_sites",
		"studio.get_page",
		"studio.create_post",
		"studio.publish_page",
		"studio.create_snapshot",
		// Batch-3 wrappers — one per new category
		"studio.get_scaffold_status",
		"studio.list_blog_categories",
		"studio.create_blog_tag",
		"studio.get_seo_report",
		"studio.add_keyword",
		"studio.get_media",
		"studio.update_settings",
		"studio.create_redirect",
		"studio.get_analytics_overview",
		"studio.get_page_analytics",
		"studio.list_forms",
		"studio.list_form_submissions",
		"studio.update_navigation",
		"studio.update_theme_tokens",
		"studio.get_staging_info",
		"studio.promote_staging",
		"studio.restore_snapshot",
		"studio.list_members",
		"studio.get_credit_balance",
		"studio.get_billing_overview",
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
