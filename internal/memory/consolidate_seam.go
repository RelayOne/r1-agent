// Package memory — consolidate_seam.go
//
// A101 / S-U-030: bridges the flat cross-session Entry Store
// (memory.go) into the tiered Router (tiers.go) as the Episodic tier.
// The tiered Router was dormant not only because nothing constructed it
// but because it had no production source of Episodic items — the live
// memory surface agents write to is the Entry Store
// (.r1/agent-memory.json), which predates the tier abstraction. This
// file exposes that store, unmodified, as an Episodic-tier Storage so
// the STOKE-010 consolidation job can read the agent's accumulated
// learnings without a second persistence layer (filestore.go supplies
// the durable Semantic sink for the job's output).
package memory

import (
	"context"
	"time"
)

// All returns a snapshot of every entry in the store. The consolidation
// bridge needs the full episodic log; Recall is unsuitable because it
// scores against a query string and drops zero-score entries. The
// returned slice is a copy, so callers can range over it without
// holding the store lock; Tags slices are shared (read-only use).
func (s *Store) All() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

// episodicStoreView adapts *Store to the tiered Storage interface,
// presenting every Entry as an Episodic Item. It is read-through: Query
// and Get map live Store rows; Delete forwards to Forget. Put and Vote
// are unsupported because episodic writes have their own path (the
// agent memory tools call Store.Remember) and the Episodic tier does
// not vote — a caller reaching for either is using the wrong surface.
type episodicStoreView struct{ store *Store }

// NewEpisodicView wraps a cross-session Store as an Episodic-tier
// Storage backend for the Router.
func NewEpisodicView(s *Store) Storage { return &episodicStoreView{store: s} }

// entryToEpisodic maps an Entry onto the tiered Item shape. Confidence
// drives both the Item's Confidence and its Importance so the Router's
// recency×importance×relevance ranking has a signal to work with.
func entryToEpisodic(e Entry) Item {
	return Item{
		ID:         e.ID,
		Tier:       TierEpisodic,
		Content:    e.Content,
		Tags:       e.Tags,
		CreatedAt:  e.CreatedAt,
		Importance: e.Confidence,
		Confidence: e.Confidence,
	}
}

func (v *episodicStoreView) Query(_ context.Context, q Query) ([]Item, error) {
	entries := v.store.All()
	out := make([]Item, 0, len(entries))
	now := time.Now()
	for _, e := range entries {
		if q.MaxAge > 0 && now.Sub(e.CreatedAt) > q.MaxAge {
			continue
		}
		if q.Text != "" {
			match := containsIgnoreCase(e.Content, q.Text)
			if !match {
				for _, tag := range e.Tags {
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
		out = append(out, entryToEpisodic(e))
	}
	return out, nil
}

func (v *episodicStoreView) Get(_ context.Context, id string) (Item, error) {
	for _, e := range v.store.All() {
		if e.ID == id {
			return entryToEpisodic(e), nil
		}
	}
	return Item{}, ErrNotFound
}

func (v *episodicStoreView) Put(context.Context, Item) error { return ErrUnsupported }

func (v *episodicStoreView) Vote(context.Context, string, int) error { return ErrUnsupported }

func (v *episodicStoreView) Delete(_ context.Context, id string) error {
	v.store.Forget(id)
	return nil
}
