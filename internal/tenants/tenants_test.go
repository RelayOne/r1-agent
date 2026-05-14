package tenants

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Spec: specs/admin-panel.md §10 task 1 — unit tests for empty file,
// malformed file, and happy path.

func TestNewStaticStore_MissingFileTreatedAsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tenants.json")
	s, err := NewStaticStore(path)
	if err != nil {
		t.Fatalf("NewStaticStore: %v", err)
	}
	if got := s.Len(); got != 0 {
		t.Errorf("Len = %d, want 0 (file doesn't exist)", got)
	}
	if list := s.List(); len(list) != 0 {
		t.Errorf("List() = %v, want empty", list)
	}
}

func TestNewStaticStore_EmptyFileTreatedAsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tenants.json")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := NewStaticStore(path)
	if err != nil {
		t.Fatalf("NewStaticStore: %v", err)
	}
	if got := s.Len(); got != 0 {
		t.Errorf("Len = %d, want 0 (empty file)", got)
	}
}

func TestNewStaticStore_MalformedJSONErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tenants.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := NewStaticStore(path)
	if err == nil {
		t.Fatalf("NewStaticStore should error on malformed JSON")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should mention parse failure; got %q", err)
	}
}

func TestNewStaticStore_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tenants.json")
	body := []byte(`[
		{"slug":"acme","display_name":"Acme Inc","created_at":"2026-01-01T00:00:00Z","admin_emails":["a@acme"]},
		{"slug":"beta","display_name":"Beta Co","created_at":"2026-02-01T00:00:00Z","admin_emails":["b@beta"]}
	]`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := NewStaticStore(path)
	if err != nil {
		t.Fatalf("NewStaticStore: %v", err)
	}
	if got := s.Len(); got != 2 {
		t.Errorf("Len = %d, want 2", got)
	}
	list := s.List()
	if len(list) != 2 {
		t.Fatalf("List length = %d, want 2", len(list))
	}
	// Slug-sorted order.
	if list[0].Slug != "acme" || list[1].Slug != "beta" {
		t.Errorf("List not slug-sorted: %v", list)
	}
	if list[0].DisplayName != "Acme Inc" {
		t.Errorf("DisplayName = %q, want Acme Inc", list[0].DisplayName)
	}
	if !list[0].CreatedAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("CreatedAt = %v, want 2026-01-01 UTC", list[0].CreatedAt)
	}
	tenant, ok := s.Get("acme")
	if !ok {
		t.Fatalf("Get(acme) returned !ok")
	}
	if tenant.Slug != "acme" {
		t.Errorf("Get(acme).Slug = %q", tenant.Slug)
	}
	if _, ok := s.Get("unknown"); ok {
		t.Errorf("Get(unknown) returned ok, want false")
	}
}

func TestNewStaticStoreFromMemory(t *testing.T) {
	s := NewStaticStoreFromMemory([]Tenant{
		{Slug: "z-late", DisplayName: "Z"},
		{Slug: "a-early", DisplayName: "A"},
		{Slug: "", DisplayName: "skipped"},          // empty slug ignored
		{Slug: "a-early", DisplayName: "duplicate"}, // duplicate ignored
	})
	if got := s.Len(); got != 2 {
		t.Errorf("Len = %d, want 2 (empty + duplicate slugs filtered)", got)
	}
	list := s.List()
	if list[0].Slug != "a-early" || list[1].Slug != "z-late" {
		t.Errorf("List not slug-sorted: %v", list)
	}
}

func TestStaticStore_ListIsDefensiveCopy(t *testing.T) {
	s := NewStaticStoreFromMemory([]Tenant{{Slug: "a"}, {Slug: "b"}})
	list := s.List()
	list[0].Slug = "MUTATED"
	again := s.List()
	if again[0].Slug != "a" {
		t.Errorf("List() returned shared backing array; got %q after caller mutation", again[0].Slug)
	}
}

func TestStaticStore_ReloadPicksUpFileChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tenants.json")
	if err := os.WriteFile(path, []byte(`[{"slug":"v1"}]`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := NewStaticStore(path)
	if err != nil {
		t.Fatalf("NewStaticStore: %v", err)
	}
	if got := s.Len(); got != 1 {
		t.Errorf("initial Len = %d, want 1", got)
	}
	if err := os.WriteFile(path, []byte(`[{"slug":"v1"},{"slug":"v2"}]`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := s.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := s.Len(); got != 2 {
		t.Errorf("post-reload Len = %d, want 2", got)
	}
}
