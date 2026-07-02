package memory

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStore_PutGetPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory", "tiers.json")
	ctx := context.Background()

	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	item := Item{ID: "s1", Tier: TierSemantic, Content: "fact", Tags: []string{"go"}, CreatedAt: time.Now().UTC()}
	if err := fs.Put(ctx, item); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Reopen from disk — the write must have persisted.
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := fs2.Get(ctx, "s1")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if got.Content != "fact" || got.Tier != TierSemantic {
		t.Errorf("got %+v want content=fact tier=semantic", got)
	}
}

func TestFileStore_QueryFiltersByTier(t *testing.T) {
	ctx := context.Background()
	fs, err := NewFileStore(filepath.Join(t.TempDir(), "tiers.json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = fs.Put(ctx, Item{ID: "e1", Tier: TierEpisodic, Content: "x"})
	_ = fs.Put(ctx, Item{ID: "s1", Tier: TierSemantic, Content: "y"})

	sem, err := fs.Query(ctx, Query{Tier: TierSemantic})
	if err != nil {
		t.Fatal(err)
	}
	if len(sem) != 1 || sem[0].ID != "s1" {
		t.Errorf("semantic query=%+v want [s1]", sem)
	}
}

func TestFileStore_MissingFileIsEmpty(t *testing.T) {
	fs, err := NewFileStore(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("NewFileStore on missing file should succeed: %v", err)
	}
	got, err := fs.Query(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("fresh store should be empty, got %d", len(got))
	}
}

func TestFileStore_DeleteAndVote(t *testing.T) {
	ctx := context.Background()
	fs, err := NewFileStore(filepath.Join(t.TempDir(), "tiers.json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = fs.Put(ctx, Item{ID: "s1", Tier: TierSemantic, Content: "y"})
	if err := fs.Vote(ctx, "s1", 3); err != nil {
		t.Fatalf("Vote: %v", err)
	}
	got, _ := fs.Get(ctx, "s1")
	if got.Votes != 3 {
		t.Errorf("votes=%d want 3", got.Votes)
	}
	if err := fs.Delete(ctx, "s1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := fs.Get(ctx, "s1"); err != ErrNotFound {
		t.Errorf("Get after delete err=%v want ErrNotFound", err)
	}
	// Idempotent delete.
	if err := fs.Delete(ctx, "s1"); err != nil {
		t.Errorf("second Delete should be no-op, got %v", err)
	}
}

// TestFileStore_AsRouterBackend proves the Router can dispatch to a
// persistent FileStore across tiers — the wiring the tiered Router
// lacked before A101.
func TestFileStore_AsRouterBackend(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	epi, err := NewFileStore(filepath.Join(dir, "epi.json"))
	if err != nil {
		t.Fatal(err)
	}
	sem, err := NewFileStore(filepath.Join(dir, "sem.json"))
	if err != nil {
		t.Fatal(err)
	}
	r := NewRouter()
	r.Register(TierEpisodic, epi)
	r.Register(TierSemantic, sem)

	if err := r.Put(ctx, Item{ID: "e1", Tier: TierEpisodic, Content: "seen"}); err != nil {
		t.Fatal(err)
	}
	items, err := r.Query(ctx, Query{Tier: TierEpisodic, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "e1" {
		t.Errorf("router episodic query=%+v want [e1]", items)
	}
}
