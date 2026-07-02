// Package memory — filestore.go
//
// A101 / S-U-030: FileStore is the persistent analog of
// InMemoryStorage — a JSON-file-backed implementation of the tiered
// Storage interface (tiers.go). It exists because the tiered Router
// had no durable backend: the only Storage implementation was the
// in-memory reference type, so no on-demand or offline job could read
// what a previous run wrote. The STOKE-010 consolidation pass
// (internal/consolidation) needs exactly that — it writes consolidated
// Semantic insights that must survive between process invocations.
//
// Design:
//   - One file holds every tier's items keyed by ID; Query filters by
//     Tier so a single FileStore can back several tiers if wanted.
//   - Each mutating op (Put/Vote/Delete) rewrites the whole file
//     atomically (temp + rename) under the same lock, matching
//     Store.Save's durability contract. On-demand consolidation is a
//     low-frequency writer, so full-file rewrites are acceptable; a
//     high-write tier would want an append log instead.
//   - Load happens once at construction; a missing file yields an empty
//     store (first run), mirroring memory.NewStore.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// FileStore is a durable, JSON-file-backed Storage. Safe for
// concurrent use; every mutation is flushed to disk before returning.
type FileStore struct {
	mu    sync.Mutex
	path  string
	items map[string]Item
}

// NewFileStore opens (or lazily creates) a file-backed tier store at
// path. A non-existent file is treated as an empty store; a malformed
// file is an error so callers don't silently start from scratch on
// corruption.
func NewFileStore(path string) (*FileStore, error) {
	fs := &FileStore{path: path, items: map[string]Item{}}
	if err := fs.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return fs, nil
}

func (f *FileStore) load() error {
	data, err := os.ReadFile(f.path) // #nosec G304 -- path is caller-supplied config, not user input.
	if err != nil {
		return err
	}
	var items []Item
	if err := json.Unmarshal(data, &items); err != nil {
		return fmt.Errorf("memory: parse file store %s: %w", f.path, err)
	}
	for _, it := range items {
		f.items[it.ID] = it
	}
	return nil
}

// saveLocked writes the current item set to disk atomically. Callers
// must hold f.mu. Items are emitted in ID order so the on-disk form is
// deterministic (stable diffs, reproducible tests).
func (f *FileStore) saveLocked() error {
	if f.path == "" {
		return nil
	}
	items := make([]Item, 0, len(f.items))
	for _, it := range f.items {
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

// Put stores (or replaces) an item and flushes to disk.
func (f *FileStore) Put(_ context.Context, item Item) error {
	if item.ID == "" {
		return fmt.Errorf("memory: item id is required")
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[item.ID] = item
	return f.saveLocked()
}

// Get returns the item with id, honoring Working-tier expiry.
func (f *FileStore) Get(_ context.Context, id string) (Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[id]
	if !ok {
		return Item{}, ErrNotFound
	}
	if !it.ExpiresAt.IsZero() && time.Now().After(it.ExpiresAt) {
		delete(f.items, id)
		_ = f.saveLocked()
		return Item{}, ErrNotFound
	}
	return it, nil
}

// Query returns items matching q, using the same tier/age/text
// semantics as InMemoryStorage. Router applies Episodic re-ranking and
// the Limit clamp on top of this.
func (f *FileStore) Query(_ context.Context, q Query) ([]Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Item, 0, len(f.items))
	now := time.Now()
	for _, it := range f.items {
		if q.Tier != "" && it.Tier != q.Tier {
			continue
		}
		if q.MaxAge > 0 && now.Sub(it.CreatedAt) > q.MaxAge {
			continue
		}
		if !it.ExpiresAt.IsZero() && now.After(it.ExpiresAt) {
			continue
		}
		if q.Text != "" && !itemMatchesText(it, q.Text) {
			continue
		}
		out = append(out, it)
	}
	// Deterministic order for callers that don't re-rank.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Vote adjusts the net vote count on an item (Semantic-tier semantics)
// and flushes. Other stores route Vote here via the Router.
func (f *FileStore) Vote(_ context.Context, id string, delta int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[id]
	if !ok {
		return ErrNotFound
	}
	it.Votes += delta
	f.items[id] = it
	return f.saveLocked()
}

// Delete removes an item and flushes. Idempotent.
func (f *FileStore) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.items[id]; !ok {
		return nil
	}
	delete(f.items, id)
	return f.saveLocked()
}

// itemMatchesText reports whether the item's content or any tag matches
// the query text. Shared shape with InMemoryStorage.Query's inline
// matcher.
func itemMatchesText(it Item, text string) bool {
	if it.Content != "" && containsIgnoreCase(it.Content, text) {
		return true
	}
	for _, tag := range it.Tags {
		if tag == text {
			return true
		}
	}
	return false
}
