package vecindex

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestAddAndSearch(t *testing.T) {
	idx := New(Config{Dimension: 3})
	idx.Add(Document{ID: "a", Content: "hello", Vector: Vector{1, 0, 0}})
	idx.Add(Document{ID: "b", Content: "world", Vector: Vector{0, 1, 0}})
	idx.Add(Document{ID: "c", Content: "hello world", Vector: Vector{0.7, 0.7, 0}})

	results := idx.Search(Vector{1, 0, 0}, 2)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Document.ID != "a" {
		t.Errorf("expected 'a' as best match, got %q", results[0].Document.ID)
	}
	if results[0].Score < 0.9 {
		t.Errorf("exact match should score high, got %f", results[0].Score)
	}
}

func TestDimensionMismatch(t *testing.T) {
	idx := New(Config{Dimension: 3})
	err := idx.Add(Document{ID: "bad", Vector: Vector{1, 2}})
	if err == nil {
		t.Error("should error on dimension mismatch")
	}
}

func TestNoVector(t *testing.T) {
	idx := New(Config{})
	err := idx.Add(Document{ID: "empty"})
	if err == nil {
		t.Error("should error on missing vector")
	}
}

func TestAutoDimension(t *testing.T) {
	idx := New(Config{}) // no dimension set
	err := idx.Add(Document{ID: "first", Vector: Vector{1, 2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	if idx.dim != 3 {
		t.Errorf("should auto-detect dimension as 3, got %d", idx.dim)
	}
}

func TestReplace(t *testing.T) {
	idx := New(Config{})
	idx.Add(Document{ID: "a", Content: "v1", Vector: Vector{1, 0}})
	idx.Add(Document{ID: "a", Content: "v2", Vector: Vector{0, 1}})

	if idx.Count() != 1 {
		t.Error("should replace, not duplicate")
	}
	doc := idx.Get("a")
	if doc.Content != "v2" {
		t.Error("should have updated content")
	}
}

func TestRemove(t *testing.T) {
	idx := New(Config{})
	idx.Add(Document{ID: "a", Vector: Vector{1, 0}})
	idx.Remove("a")
	if idx.Count() != 0 {
		t.Error("should remove document")
	}
}

func TestGet(t *testing.T) {
	idx := New(Config{})
	idx.Add(Document{ID: "a", Content: "hello", Vector: Vector{1}})

	doc := idx.Get("a")
	if doc == nil || doc.Content != "hello" {
		t.Error("should find document")
	}

	if idx.Get("nonexistent") != nil {
		t.Error("should return nil for missing doc")
	}
}

func TestCosineSimilarity(t *testing.T) {
	if CosineSimilarity(Vector{1, 0}, Vector{1, 0}) != 1.0 {
		t.Error("identical vectors should be 1.0")
	}

	score := CosineSimilarity(Vector{1, 0}, Vector{0, 1})
	if math.Abs(score) > 0.001 {
		t.Errorf("orthogonal vectors should be ~0, got %f", score)
	}

	if CosineSimilarity(Vector{}, Vector{}) != 0 {
		t.Error("empty vectors should return 0")
	}

	if CosineSimilarity(Vector{1, 0}, Vector{1}) != 0 {
		t.Error("different dimensions should return 0")
	}
}

func TestEuclideanDistance(t *testing.T) {
	d := EuclideanDistance(Vector{0, 0}, Vector{3, 4})
	if math.Abs(d-5.0) > 0.001 {
		t.Errorf("expected 5.0, got %f", d)
	}
}

func TestNormalize(t *testing.T) {
	n := Normalize(Vector{3, 4})
	magnitude := math.Sqrt(n[0]*n[0] + n[1]*n[1])
	if math.Abs(magnitude-1.0) > 0.001 {
		t.Errorf("normalized vector should have magnitude 1, got %f", magnitude)
	}
}

func TestNormalizeZero(t *testing.T) {
	n := Normalize(Vector{0, 0})
	if n[0] != 0 || n[1] != 0 {
		t.Error("zero vector should stay zero")
	}
}

func TestBagOfWordsEmbed(t *testing.T) {
	embed := BagOfWordsEmbed([]string{"hello", "world", "go", "test"})

	v1, _ := embed("hello world")
	v2, _ := embed("hello go")
	v3, _ := embed("test")

	// hello world should be more similar to hello go than to test
	sim12 := CosineSimilarity(v1, v2)
	sim13 := CosineSimilarity(v1, v3)
	if sim12 <= sim13 {
		t.Error("'hello world' should be more similar to 'hello go' than to 'test'")
	}
}

func TestAddText(t *testing.T) {
	embed := BagOfWordsEmbed([]string{"hello", "world"})
	idx := New(Config{EmbedFunc: embed})

	err := idx.AddText("doc1", "hello world", "test.go")
	if err != nil {
		t.Fatal(err)
	}
	if idx.Count() != 1 {
		t.Error("should add document")
	}
}

func TestAddTextNoEmbed(t *testing.T) {
	idx := New(Config{})
	err := idx.AddText("doc1", "hello", "")
	if err == nil {
		t.Error("should error without embed function")
	}
}

func TestSearchText(t *testing.T) {
	embed := BagOfWordsEmbed([]string{"func", "error", "test", "http", "server"})
	idx := New(Config{EmbedFunc: embed})

	idx.AddText("a", "func error handling", "errors.go")
	idx.AddText("b", "http server func", "server.go")
	idx.AddText("c", "test func error", "test.go")

	results, err := idx.SearchText("error handling", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestSearchTextNoEmbed(t *testing.T) {
	idx := New(Config{})
	_, err := idx.SearchText("query", 5)
	if err == nil {
		t.Error("should error without embed function")
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")

	idx := New(Config{})
	idx.Add(Document{ID: "a", Content: "hello", Vector: Vector{1, 0, 0}})
	idx.Add(Document{ID: "b", Content: "world", Vector: Vector{0, 1, 0}})

	if err := idx.Save(path); err != nil {
		t.Fatal(err)
	}

	idx2 := New(Config{})
	if err := idx2.Load(path); err != nil {
		t.Fatal(err)
	}
	if idx2.Count() != 2 {
		t.Errorf("expected 2 docs, got %d", idx2.Count())
	}
}

func TestLoadMissing(t *testing.T) {
	idx := New(Config{})
	err := idx.Load("/nonexistent/path.json")
	if err == nil {
		t.Error("should error on missing file")
	}
}

func TestEmptySearch(t *testing.T) {
	idx := New(Config{Dimension: 3})
	results := idx.Search(Vector{1, 0, 0}, 5)
	if len(results) != 0 {
		t.Error("empty index should return no results")
	}
}

func TestSearchRanking(t *testing.T) {
	idx := New(Config{})
	idx.Add(Document{ID: "far", Vector: Vector{0, 0, 1}})
	idx.Add(Document{ID: "close", Vector: Vector{0.9, 0.1, 0}})
	idx.Add(Document{ID: "exact", Vector: Vector{1, 0, 0}})

	results := idx.Search(Vector{1, 0, 0}, 3)
	if results[0].Document.ID != "exact" {
		t.Error("exact match should rank first")
	}
	if results[0].Rank != 1 {
		t.Errorf("first result should have rank 1, got %d", results[0].Rank)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idx.json")

	idx := New(Config{})
	idx.Add(Document{ID: "x", Content: "test", Path: "test.go", Vector: Vector{0.5, 0.5},
		Metadata: map[string]string{"lang": "go"}})

	idx.Save(path)

	idx2 := New(Config{})
	idx2.Load(path)

	doc := idx2.Get("x")
	if doc == nil {
		t.Fatal("should find doc after load")
	}
	if doc.Path != "test.go" {
		t.Error("should preserve path")
	}
	if doc.Metadata["lang"] != "go" {
		t.Error("should preserve metadata")
	}

	// Clean up
	os.Remove(path)
}
