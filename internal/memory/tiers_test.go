package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAllTiers_Four(t *testing.T) {
	if got := AllTiers(); len(got) != 4 {
		t.Fatalf("AllTiers()=%d want 4", len(got))
	}
}

func TestRouter_RegisterAndRoute(t *testing.T) {
	r := NewRouter()
	working := NewInMemoryStorage()
	episodic := NewInMemoryStorage()
	r.Register(TierWorking, working)
	r.Register(TierEpisodic, episodic)

	ctx := context.Background()
	_ = r.Put(ctx, Item{ID: "w1", Tier: TierWorking, Content: "scratch"})
	_ = r.Put(ctx, Item{ID: "e1", Tier: TierEpisodic, Content: "event", Importance: 0.8})

	// Working backend should have w1, not e1.
	if _, err := working.Get(ctx, "w1"); err != nil {
		t.Errorf("working should have w1: %v", err)
	}
	if _, err := working.Get(ctx, "e1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("working should NOT have e1, got err=%v", err)
	}
	if _, err := episodic.Get(ctx, "e1"); err != nil {
		t.Errorf("episodic should have e1: %v", err)
	}
}

func TestRouter_UnregisteredTierIsUnsupported(t *testing.T) {
	r := NewRouter()
	err := r.Put(context.Background(), Item{Tier: TierSemantic, ID: "x"})
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("want ErrUnsupported, got %v", err)
	}
}

func TestRouter_GetCrossTier(t *testing.T) {
	r := NewRouter()
	sem := NewInMemoryStorage()
	r.Register(TierSemantic, sem)
	_ = r.Put(context.Background(), Item{ID: "s1", Tier: TierSemantic, Content: "fact"})

	// Get by ID finds it via cross-tier walk.
	got, err := r.Get(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Content != "fact" {
		t.Errorf("content=%q want fact", got.Content)
	}
}

func TestRouter_GetNotFound(t *testing.T) {
	r := NewRouter()
	r.Register(TierWorking, NewInMemoryStorage())
	_, err := r.Get(context.Background(), "ghost")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestRouter_VoteRoutesToSemantic(t *testing.T) {
	r := NewRouter()
	sem := NewInMemoryStorage()
	r.Register(TierSemantic, sem)
	_ = r.Put(context.Background(), Item{ID: "s1", Tier: TierSemantic, Content: "fact"})
	if err := r.Vote(context.Background(), "s1", 1); err != nil {
		t.Fatalf("Vote: %v", err)
	}
	got, _ := sem.Get(context.Background(), "s1")
	if got.Votes != 1 {
		t.Errorf("Votes=%d want 1", got.Votes)
	}
}

func TestInMemoryStorage_QueryByTag(t *testing.T) {
	m := NewInMemoryStorage()
	ctx := context.Background()
	_ = m.Put(ctx, Item{ID: "a", Tier: TierEpisodic, Tags: []string{"go", "debug"}, Content: "x"})
	_ = m.Put(ctx, Item{ID: "b", Tier: TierEpisodic, Tags: []string{"python"}, Content: "y"})
	got, err := m.Query(ctx, Query{Tier: TierEpisodic, Text: "go"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("got %+v want [a]", got)
	}
}

func TestInMemoryStorage_WorkingExpiry(t *testing.T) {
	m := NewInMemoryStorage()
	ctx := context.Background()
	past := time.Now().Add(-time.Second)
	_ = m.Put(ctx, Item{ID: "a", Tier: TierWorking, ExpiresAt: past})
	_, err := m.Get(ctx, "a")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expired item should be ErrNotFound, got %v", err)
	}
}

func TestRankEpisodic_RecentImportantRanksFirst(t *testing.T) {
	now := time.Now()
	items := []Item{
		{ID: "old-important", Importance: 0.9, CreatedAt: now.Add(-60 * 24 * time.Hour)},
		{ID: "fresh-mundane", Importance: 0.3, CreatedAt: now.Add(-1 * time.Hour)},
		{ID: "fresh-important", Importance: 0.9, CreatedAt: now.Add(-1 * time.Hour)},
	}
	ranked := rankEpisodic(items, "")
	if ranked[0].ID != "fresh-important" {
		t.Errorf("top=%q want fresh-important", ranked[0].ID)
	}
}

func TestRankEpisodic_QueryTagMatchBoosts(t *testing.T) {
	now := time.Now()
	items := []Item{
		{ID: "fresh-no-tag", Importance: 0.5, CreatedAt: now.Add(-1 * time.Hour)},
		{ID: "fresh-tagged", Importance: 0.5, CreatedAt: now.Add(-1 * time.Hour), Tags: []string{"go"}},
	}
	ranked := rankEpisodic(items, "go")
	if ranked[0].ID != "fresh-tagged" {
		t.Errorf("query-matched tag should rank first, got %q", ranked[0].ID)
	}
}

func TestRouter_QueryLimit(t *testing.T) {
	r := NewRouter()
	epi := NewInMemoryStorage()
	r.Register(TierEpisodic, epi)
	for i := 0; i < 10; i++ {
		_ = r.Put(context.Background(), Item{
			ID:        "x" + string(rune('0'+i)),
			Tier:      TierEpisodic,
			CreatedAt: time.Now(),
			Importance: float64(i) / 10.0,
		})
	}
	got, err := r.Query(context.Background(), Query{Tier: TierEpisodic, Limit: 3})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("len=%d want 3", len(got))
	}
}

func TestRouter_RegisteredTiers(t *testing.T) {
	r := NewRouter()
	r.Register(TierWorking, NewInMemoryStorage())
	r.Register(TierSemantic, NewInMemoryStorage())
	got := r.RegisteredTiers()
	if len(got) != 2 {
		t.Fatalf("len=%d want 2", len(got))
	}
	// Alphabetically sorted.
	if got[0] != TierSemantic || got[1] != TierWorking {
		t.Errorf("got %v — expected alphabetical order", got)
	}
}
