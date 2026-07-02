package memory

import (
	"context"
	"testing"
)

func TestEpisodicView_ExposesStoreEntries(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(Config{}) // in-memory (no path)
	if err != nil {
		t.Fatal(err)
	}
	store.RememberWithContext(CatGotcha, "vet before commit", "ci", "", "ci")
	store.RememberWithContext(CatPattern, "table-driven tests", "testing", "", "testing")

	view := NewEpisodicView(store)

	// Query with empty text must return the FULL episodic log — Recall
	// would drop these (zero score), which is why All() exists.
	items, err := view.Query(ctx, Query{Tier: TierEpisodic})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("episodic items=%d want 2", len(items))
	}
	for _, it := range items {
		if it.Tier != TierEpisodic {
			t.Errorf("item %s tier=%q want episodic", it.ID, it.Tier)
		}
	}

	// Get maps by entry ID.
	got, err := view.Get(ctx, items[0].ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != items[0].ID {
		t.Errorf("Get id=%q want %q", got.ID, items[0].ID)
	}

	// Text filter narrows to matching content.
	filtered, err := view.Query(ctx, Query{Tier: TierEpisodic, Text: "table-driven"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].Content != "table-driven tests" {
		t.Errorf("text query=%+v want single table-driven item", filtered)
	}
}

func TestEpisodicView_PutVoteUnsupported(t *testing.T) {
	ctx := context.Background()
	store, _ := NewStore(Config{})
	view := NewEpisodicView(store)
	if err := view.Put(ctx, Item{ID: "x", Tier: TierEpisodic}); err != ErrUnsupported {
		t.Errorf("Put err=%v want ErrUnsupported", err)
	}
	if err := view.Vote(ctx, "x", 1); err != ErrUnsupported {
		t.Errorf("Vote err=%v want ErrUnsupported", err)
	}
}

func TestStoreAll_ReturnsCopy(t *testing.T) {
	store, _ := NewStore(Config{})
	store.Remember(CatFact, "a")
	all := store.All()
	if len(all) != 1 {
		t.Fatalf("All()=%d want 1", len(all))
	}
	// Mutating the returned slice header must not affect the store.
	all = append(all, Entry{ID: "fake"})
	if store.Count() != 1 {
		t.Errorf("store mutated via All() copy: count=%d", store.Count())
	}
}
