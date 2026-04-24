// Package vecindex provides vector/embedding-based semantic search.
// Inspired by Cursor's embedding-based code retrieval:
//
// TF-IDF finds lexical matches, but misses semantic similarity.
// Vector search finds code by meaning:
// - "error handling" finds try/catch, if err != nil, raise
// - "HTTP server" finds net/http, gin, fiber, express
//
// This is a lightweight in-process vector index using cosine similarity.
// It works with any embedding provider (OpenAI, Voyage, local).
// The index stores pre-computed vectors and supports incremental updates.
package vecindex

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
)

// Vector is a float64 embedding vector.
type Vector []float64

// Document is an indexed item with its embedding.
type Document struct {
	ID       string            `json:"id"`
	Content  string            `json:"content"`
	Path     string            `json:"path,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Vector   Vector            `json:"vector"`
}

// SearchResult is a ranked search result.
type SearchResult struct {
	Document   Document `json:"document"`
	Score      float64  `json:"score"` // cosine similarity, 0-1
	Rank       int      `json:"rank"`
}

// EmbedFunc generates an embedding vector for text.
type EmbedFunc func(text string) (Vector, error)

// Index is an in-memory vector index.
type Index struct {
	mu        sync.RWMutex
	docs      []Document
	dim       int       // vector dimension
	embedFn   EmbedFunc // embedding function
}

// Config for the index.
type Config struct {
	Dimension int       // vector dimension
	EmbedFunc EmbedFunc // optional embedding function
}

// New creates an empty vector index.
func New(cfg Config) *Index {
	return &Index{
		dim:     cfg.Dimension,
		embedFn: cfg.EmbedFunc,
	}
}

// Add inserts a document with a pre-computed vector.
func (idx *Index) Add(doc Document) error {
	if len(doc.Vector) == 0 {
		return fmt.Errorf("document %q has no vector", doc.ID)
	}
	if idx.dim > 0 && len(doc.Vector) != idx.dim {
		return fmt.Errorf("vector dimension mismatch: expected %d, got %d", idx.dim, len(doc.Vector))
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Set dimension from first doc
	if idx.dim == 0 {
		idx.dim = len(doc.Vector)
	}

	// Replace if ID exists
	for i, d := range idx.docs {
		if d.ID == doc.ID {
			idx.docs[i] = doc
			return nil
		}
	}

	idx.docs = append(idx.docs, doc)
	return nil
}

// AddText adds a document, computing its embedding via the EmbedFunc.
func (idx *Index) AddText(id, content, path string) error {
	if idx.embedFn == nil {
		return fmt.Errorf("no embedding function configured")
	}
	vec, err := idx.embedFn(content)
	if err != nil {
		return fmt.Errorf("embed %q: %w", id, err)
	}
	return idx.Add(Document{
		ID:      id,
		Content: content,
		Path:    path,
		Vector:  vec,
	})
}

// Search finds the top-K most similar documents to the query vector.
func (idx *Index) Search(query Vector, k int) []SearchResult {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if k <= 0 {
		k = 10
	}

	type scored struct {
		idx   int
		score float64
	}

	results := make([]scored, 0, len(idx.docs))
	for i, doc := range idx.docs {
		score := CosineSimilarity(query, doc.Vector)
		results = append(results, scored{i, score})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if len(results) > k {
		results = results[:k]
	}

	out := make([]SearchResult, len(results))
	for i, r := range results {
		out[i] = SearchResult{
			Document: idx.docs[r.idx],
			Score:    r.score,
			Rank:     i + 1,
		}
	}
	return out
}

// SearchText searches using the embedding function.
func (idx *Index) SearchText(query string, k int) ([]SearchResult, error) {
	if idx.embedFn == nil {
		return nil, fmt.Errorf("no embedding function configured")
	}
	vec, err := idx.embedFn(query)
	if err != nil {
		return nil, err
	}
	return idx.Search(vec, k), nil
}

// Remove deletes a document by ID.
func (idx *Index) Remove(id string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	for i, d := range idx.docs {
		if d.ID == id {
			idx.docs = append(idx.docs[:i], idx.docs[i+1:]...)
			return
		}
	}
}

// Get retrieves a document by ID.
func (idx *Index) Get(id string) *Document {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	for _, d := range idx.docs {
		if d.ID == id {
			doc := d
			return &doc
		}
	}
	return nil
}

// Count returns the number of documents.
func (idx *Index) Count() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.docs)
}

// Save persists the index to a JSON file.
func (idx *Index) Save(path string) error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	data, err := json.Marshal(idx.docs)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644) // #nosec G306 -- vector index cache; user-readable.
}

// Load reads the index from a JSON file.
func (idx *Index) Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var docs []Document
	if err := json.Unmarshal(data, &docs); err != nil {
		return err
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.docs = docs
	if len(docs) > 0 {
		idx.dim = len(docs[0].Vector)
	}
	return nil
}

// CosineSimilarity computes cosine similarity between two vectors.
func CosineSimilarity(a, b Vector) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// EuclideanDistance computes L2 distance between two vectors.
func EuclideanDistance(a, b Vector) float64 {
	if len(a) != len(b) {
		return math.MaxFloat64
	}
	var sum float64
	for i := range a {
		d := a[i] - b[i]
		sum += d * d
	}
	return math.Sqrt(sum)
}

// Normalize returns a unit-length copy of the vector.
func Normalize(v Vector) Vector {
	var norm float64
	for _, x := range v {
		norm += x * x
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return v
	}
	result := make(Vector, len(v))
	for i, x := range v {
		result[i] = x / norm
	}
	return result
}

// BagOfWordsEmbed creates a simple bag-of-words embedding for testing
// and fallback when no external embedding service is available.
func BagOfWordsEmbed(vocab []string) EmbedFunc {
	wordIdx := make(map[string]int, len(vocab))
	for i, w := range vocab {
		wordIdx[strings.ToLower(w)] = i
	}
	dim := len(vocab)

	return func(text string) (Vector, error) {
		vec := make(Vector, dim)
		words := strings.Fields(strings.ToLower(text))
		for _, w := range words {
			if idx, ok := wordIdx[w]; ok {
				vec[idx]++
			}
		}
		return Normalize(vec), nil
	}
}
