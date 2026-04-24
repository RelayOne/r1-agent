// Package memory — tiers.go
//
// STOKE-009: 4-tier memory architecture interface + Router. Each
// tier has a distinct storage interface so a write to
// Procedural (skill) doesn't accidentally land in Semantic
// (wisdom), and reads are scored with tier-appropriate
// algorithms (recency × importance × relevance for Episodic;
// exact-match for Procedural).
//
// This file ships the tier abstractions. Concrete backend
// wiring (checkpoint for Working, vecindex for Episodic,
// wisdom for Semantic, skill for Procedural) lives behind
// adapter types the caller constructs — keeping this package
// free of direct imports on those other packages keeps the
// dependency graph clean + prevents import cycles.
//
// Scope of this file:
//   - Tier enum (4 values)
//   - Storage interfaces per tier
//   - Router that dispatches reads + writes to the right tier
//     based on query class
//   - Composite scoring for Episodic queries
package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

// Tier identifies one of the 4 memory tiers per the SOW.
type Tier string

const (
	// TierWorking: context window + session scratch. Volatile;
	// cleared on session end. Backed by internal/checkpoint/
	// in production.
	TierWorking Tier = "working"

	// TierEpisodic: time-stamped experience log. Persistent.
	// Backed by internal/vecindex/ with composite retrieval
	// (recency × importance × relevance).
	TierEpisodic Tier = "episodic"

	// TierSemantic: structured knowledge / facts / patterns.
	// Persistent. Backed by internal/wisdom/ with ADD / EDIT /
	// UPVOTE / DOWNVOTE operations.
	TierSemantic Tier = "semantic"

	// TierProcedural: reusable skills + workflow patterns.
	// Persistent. Backed by internal/skill/ in agentskills.io
	// format.
	TierProcedural Tier = "procedural"
)

// AllTiers returns the 4 declared tiers in canonical order.
func AllTiers() []Tier {
	return []Tier{TierWorking, TierEpisodic, TierSemantic, TierProcedural}
}

// Item is a generic memory entry. Different tiers use subsets
// of the fields — Importance + Confidence are most useful for
// Episodic; Semantic uses Votes; Procedural uses Name +
// Description; Working uses ExpiresAt.
type Item struct {
	ID          string
	Tier        Tier
	Content     string
	Tags        []string
	CreatedAt   time.Time

	// Episodic scoring inputs.
	Importance  float64 // [0, 1]
	Confidence  float64 // [0, 1]

	// Semantic voting (net of upvotes - downvotes).
	Votes int

	// Procedural descriptors.
	Name        string
	Description string

	// Working-memory expiry (zero = no expiry).
	ExpiresAt time.Time

	// Scope hierarchy (spec/memory-full-stack.md §5).
	// HierScope is one of HierGlobal / HierRepo / HierTask.
	// Zero-value HierScope ("") means "unscoped" and yields
	// Specificity 0; callers that need hierarchy filtering
	// should set this explicitly on Put. HierScopeID holds
	// the repo hash for HierRepo and the task ID for
	// HierTask (empty for HierGlobal). Distinct from the
	// bus/visibility Scope field because a memory row has
	// two orthogonal labels: which project/task bucket it
	// belongs to (HierScope) and which workers can see it
	// (bus Scope, wired by higher layers).
	HierScope   HierScope
	HierScopeID string
}

// Query parameterizes a retrieval.
type Query struct {
	Tier   Tier
	Text   string // keyword / semantic query
	Tags   []string
	Limit  int
	// For Episodic: clamp the result set to items no older
	// than MaxAge (zero = unbounded).
	MaxAge time.Duration
}

// Storage is the per-tier backend contract. Each tier's
// implementation honors the subset of operations that make
// sense for it — an operation unsupported by a tier returns
// ErrUnsupported so callers can fall through deterministically.
type Storage interface {
	Put(ctx context.Context, item Item) error
	Get(ctx context.Context, id string) (Item, error)
	Query(ctx context.Context, q Query) ([]Item, error)
	// Vote adjusts the net vote count on an item
	// (delta=+1 for upvote, -1 for downvote). Semantic-tier-
	// specific; other tiers return ErrUnsupported.
	Vote(ctx context.Context, id string, delta int) error
	// Delete removes an item. Idempotent.
	Delete(ctx context.Context, id string) error
}

// ErrUnsupported is returned when an operation isn't
// meaningful for the tier.
var ErrUnsupported = errors.New("memory: operation not supported for this tier")

// ErrNotFound is returned when an item isn't in the tier's
// store.
var ErrNotFound = errors.New("memory: item not found")

// Router dispatches memory operations to the right tier's
// Storage. Callers construct it with one Storage per tier —
// tiers not registered return ErrUnsupported on access.
type Router struct {
	mu       sync.RWMutex
	backends map[Tier]Storage
}

// NewRouter returns a Router with no backends registered.
func NewRouter() *Router {
	return &Router{backends: map[Tier]Storage{}}
}

// Register installs (or replaces) the Storage for a tier.
func (r *Router) Register(t Tier, s Storage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backends[t] = s
}

// RegisteredTiers returns the sorted list of tiers with
// registered backends. Useful for reports + debugging.
func (r *Router) RegisteredTiers() []Tier {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tier, 0, len(r.backends))
	for t := range r.backends {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// backendFor returns the Storage for tier t, or ErrUnsupported.
func (r *Router) backendFor(t Tier) (Storage, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.backends[t]
	if !ok {
		return nil, fmt.Errorf("%w: no backend for tier %q", ErrUnsupported, t)
	}
	return s, nil
}

// Put dispatches to the tier's Storage.
func (r *Router) Put(ctx context.Context, item Item) error {
	s, err := r.backendFor(item.Tier)
	if err != nil {
		return err
	}
	return s.Put(ctx, item)
}

// Get looks up across tiers in priority order (Working first,
// then Semantic, Procedural, Episodic). Returns the first hit
// or ErrNotFound.
func (r *Router) Get(ctx context.Context, id string) (Item, error) {
	for _, t := range []Tier{TierWorking, TierSemantic, TierProcedural, TierEpisodic} {
		s, err := r.backendFor(t)
		if err != nil {
			continue
		}
		item, err := s.Get(ctx, id)
		if err == nil {
			return item, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Item{}, err
		}
	}
	return Item{}, ErrNotFound
}

// Query runs the query against the tier's Storage. For
// Episodic tier, results are re-ranked by the composite score
// (recency × importance × relevance) before being truncated to
// Limit.
func (r *Router) Query(ctx context.Context, q Query) ([]Item, error) {
	s, err := r.backendFor(q.Tier)
	if err != nil {
		return nil, err
	}
	items, err := s.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	if q.Tier == TierEpisodic {
		items = rankEpisodic(items, q.Text)
	}
	if q.Limit > 0 && len(items) > q.Limit {
		items = items[:q.Limit]
	}
	return items, nil
}

// Vote forwards a Vote call to the Semantic tier's backend.
// Other tiers don't support voting.
func (r *Router) Vote(ctx context.Context, id string, delta int) error {
	s, err := r.backendFor(TierSemantic)
	if err != nil {
		return err
	}
	return s.Vote(ctx, id, delta)
}

// Delete removes an item from a named tier.
func (r *Router) Delete(ctx context.Context, tier Tier, id string) error {
	s, err := r.backendFor(tier)
	if err != nil {
		return err
	}
	return s.Delete(ctx, id)
}

// rankEpisodic sorts items by the composite score:
//
//   score = recency × importance × relevance
//
// where:
//   recency    = exp(-age_days / 30)              (decay halflife ~30d)
//   importance = item.Importance                   (caller-supplied [0,1])
//   relevance  = 1.0 if query text matches a tag; 0.5 otherwise
//                (keyword matching is intentionally coarse — the
//                real relevance comes from the vecindex backend's
//                semantic search; this package is the outer
//                ranker that post-mixes the backend's results).
func rankEpisodic(items []Item, query string) []Item {
	scored := make([]struct {
		item  Item
		score float64
	}, len(items))
	now := time.Now()
	for i, it := range items {
		ageDays := now.Sub(it.CreatedAt).Hours() / 24
		if ageDays < 0 {
			ageDays = 0
		}
		recency := math.Exp(-ageDays / 30)
		importance := it.Importance
		if importance == 0 {
			importance = 0.5 // neutral default
		}
		relevance := 0.5
		if query != "" {
			for _, tag := range it.Tags {
				if tag == query {
					relevance = 1.0
					break
				}
			}
		}
		scored[i] = struct {
			item  Item
			score float64
		}{it, recency * importance * relevance}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})
	out := make([]Item, len(scored))
	for i := range scored {
		out[i] = scored[i].item
	}
	return out
}

// InMemoryStorage is a reference Storage implementation used
// by tests + local dev. Every tier can use this backend for
// scaffolding; production deployments inject their real
// backend (checkpoint / vecindex / wisdom / skill).
type InMemoryStorage struct {
	mu    sync.Mutex
	items map[string]Item
}

// NewInMemoryStorage returns an empty in-memory storage.
func NewInMemoryStorage() *InMemoryStorage {
	return &InMemoryStorage{items: map[string]Item{}}
}

func (m *InMemoryStorage) Put(_ context.Context, item Item) error {
	if item.ID == "" {
		return fmt.Errorf("memory: item id is required")
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[item.ID] = item
	return nil
}

func (m *InMemoryStorage) Get(_ context.Context, id string) (Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.items[id]
	if !ok {
		return Item{}, ErrNotFound
	}
	// Working-tier expiry.
	if !it.ExpiresAt.IsZero() && time.Now().After(it.ExpiresAt) {
		delete(m.items, id)
		return Item{}, ErrNotFound
	}
	return it, nil
}

func (m *InMemoryStorage) Query(_ context.Context, q Query) ([]Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Item, 0, len(m.items))
	now := time.Now()
	for _, it := range m.items {
		if q.Tier != "" && it.Tier != q.Tier {
			continue
		}
		if q.MaxAge > 0 && now.Sub(it.CreatedAt) > q.MaxAge {
			continue
		}
		if !it.ExpiresAt.IsZero() && now.After(it.ExpiresAt) {
			continue
		}
		if q.Text != "" {
			match := false
			if it.Content != "" && containsIgnoreCase(it.Content, q.Text) {
				match = true
			} else {
				for _, tag := range it.Tags {
					if tag == q.Text {
						match = true
						break
					}
				}
			}
			if !match {
				continue
			}
		}
		out = append(out, it)
	}
	return out, nil
}

func (m *InMemoryStorage) Vote(_ context.Context, id string, delta int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.items[id]
	if !ok {
		return ErrNotFound
	}
	it.Votes += delta
	m.items[id] = it
	return nil
}

func (m *InMemoryStorage) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func containsIgnoreCase(haystack, needle string) bool {
	h := toLower(haystack)
	n := toLower(needle)
	return len(n) == 0 || indexOf(h, n) >= 0
}

func toLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
