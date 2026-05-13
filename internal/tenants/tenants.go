// Package tenants — A5 admin panel tenant store.
//
// Phase 1 ships a file-backed StaticStore that loads ~/.r1/tenants.json
// at startup. Phase 2 migrates to the A4 SSO directory once it ships a
// tenant API; the Store interface here is the migration seam.
//
// Spec: specs/admin-panel.md §4.2 and §10 task 1.
package tenants

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// Tenant is the read-only projection rendered in the admin tenants
// list + detail views. Mirrors specs/admin-panel.md §4.2.
type Tenant struct {
	Slug        string    `json:"slug"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
	AdminEmails []string  `json:"admin_emails"`
}

// Store is the interface every tenant backend implements. List
// returns tenants in slug-sorted order so callers get deterministic
// output. Get returns (Tenant, false) for unknown slugs.
type Store interface {
	List() []Tenant
	Get(slug string) (Tenant, bool)
}

// StaticStore is a JSON-file-backed Store. The file lives at
// ~/.r1/tenants.json by default and is loaded at construction time —
// no automatic reload, the operator restarts r1-server to pick up
// changes (admin panel changes are infrequent in Phase 1).
type StaticStore struct {
	mu      sync.RWMutex
	byslug  map[string]Tenant
	ordered []Tenant
	path    string
}

// NewStaticStore loads tenants from the given path. A missing file is
// treated as an empty tenant set (operator hasn't onboarded anyone
// yet); malformed JSON returns a wrapped error so the operator can
// diagnose the problem at startup.
func NewStaticStore(path string) (*StaticStore, error) {
	s := &StaticStore{
		byslug:  map[string]Tenant{},
		ordered: nil,
		path:    path,
	}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// NewStaticStoreFromMemory is the test seam — pass tenants directly.
// Production code always uses NewStaticStore.
func NewStaticStoreFromMemory(tenants []Tenant) *StaticStore {
	s := &StaticStore{byslug: map[string]Tenant{}}
	s.replace(tenants)
	return s
}

// reload reads s.path and replaces the in-memory state.
func (s *StaticStore) reload() error {
	if s.path == "" {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.replace(nil)
			return nil
		}
		return fmt.Errorf("tenants: read %s: %w", s.path, err)
	}
	if len(data) == 0 {
		s.replace(nil)
		return nil
	}
	var raw []Tenant
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("tenants: parse %s: %w", s.path, err)
	}
	s.replace(raw)
	return nil
}

func (s *StaticStore) replace(tenants []Tenant) {
	byslug := make(map[string]Tenant, len(tenants))
	ordered := make([]Tenant, 0, len(tenants))
	seen := map[string]bool{}
	for _, t := range tenants {
		if t.Slug == "" || seen[t.Slug] {
			continue
		}
		seen[t.Slug] = true
		byslug[t.Slug] = t
		ordered = append(ordered, t)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Slug < ordered[j].Slug
	})
	s.mu.Lock()
	s.byslug = byslug
	s.ordered = ordered
	s.mu.Unlock()
}

// List returns every tenant in slug-sorted order. The returned slice
// is a defensive copy — callers may sort or filter without affecting
// the store.
func (s *StaticStore) List() []Tenant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Tenant, len(s.ordered))
	copy(out, s.ordered)
	return out
}

// Get returns the tenant matching slug or (zero, false) when unknown.
func (s *StaticStore) Get(slug string) (Tenant, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.byslug[slug]
	return t, ok
}

// Len returns the number of tenants currently loaded.
func (s *StaticStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.ordered)
}

// Reload re-reads the underlying file. Useful for operators who edit
// tenants.json without restarting the daemon.
func (s *StaticStore) Reload() error { return s.reload() }
