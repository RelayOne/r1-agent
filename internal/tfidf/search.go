// Package tfidf implements TF-IDF semantic search over codebase files.
// Inspired by Aider's semantic search and claw-code's context retrieval:
//
// Keyword-based search (grep) misses semantic matches:
// - "authentication" won't find files about "login" or "JWT"
// - Relevant files may use different terminology
//
// TF-IDF provides a lightweight alternative to embedding-based search:
// - No external API calls needed
// - Fast indexing and querying
// - Weights terms by importance (rare terms score higher)
// - Works well for code search where variable names matter
//
// This is the 80/20 solution: much better than grep, much cheaper than embeddings.
package tfidf

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ericmacdougall/stoke/internal/chunker"
)

// Document is an indexed file.
type Document struct {
	Path  string             `json:"path"`
	Terms map[string]float64 `json:"-"` // term -> TF score
}

// Index holds the TF-IDF inverted index.
type Index struct {
	docs     []Document
	idf      map[string]float64 // term -> IDF score
	docCount int
}

// Result is a search match.
type Result struct {
	Path  string  `json:"path"`
	Score float64 `json:"score"`
}

// NewIndex creates an empty index.
func NewIndex() *Index {
	return &Index{
		idf: make(map[string]float64),
	}
}

// Build indexes all files in a directory matching the given extensions.
func Build(root string, extensions []string) (*Index, error) {
	idx := NewIndex()

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}

		rel, _ := filepath.Rel(root, path)
		if shouldSkip(rel) {
			return nil
		}

		ext := filepath.Ext(path)
		if !matchExt(ext, extensions) {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			return nil
		}

		idx.AddDocument(rel, string(data))
		return nil
	})

	if err != nil {
		return nil, err
	}

	idx.computeIDF()
	return idx, nil
}

// AddDocument adds a single document to the index.
func (idx *Index) AddDocument(path, content string) {
	terms := tokenize(content)
	tf := computeTF(terms)

	idx.docs = append(idx.docs, Document{
		Path:  path,
		Terms: tf,
	})
	idx.docCount++
}

// AddDocumentChunked uses semantic chunking to split a file into meaningful
// chunks (functions, types, methods) and indexes each chunk separately.
// This improves search precision by allowing queries to match individual
// functions rather than whole files.
func (idx *Index) AddDocumentChunked(path, content string) {
	chunks := chunker.ChunkFile(path, content)
	if len(chunks) == 0 {
		// Fallback to whole-file indexing if chunker finds no boundaries.
		idx.AddDocument(path, content)
		return
	}
	for _, ch := range chunks {
		chunkPath := path + "#" + ch.Name
		terms := tokenize(ch.Content)
		tf := computeTF(terms)
		idx.docs = append(idx.docs, Document{
			Path:  chunkPath,
			Terms: tf,
		})
		idx.docCount++
	}
}

// Finalize computes IDF scores. Call after all documents are added.
func (idx *Index) Finalize() {
	idx.computeIDF()
}

// Search finds documents most relevant to the query.
func (idx *Index) Search(query string, topK int) []Result {
	queryTerms := tokenize(query)
	queryTF := computeTF(queryTerms)

	var results []Result
	for _, doc := range idx.docs {
		score := idx.cosineSimilarity(queryTF, doc.Terms)
		if score > 0 {
			results = append(results, Result{Path: doc.Path, Score: score})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}
	return results
}

// SearchWithContext searches and returns results with context about why they matched.
func (idx *Index) SearchWithContext(query string, topK int) []ContextResult {
	queryTerms := tokenize(query)
	queryTF := computeTF(queryTerms)

	var results []ContextResult
	for _, doc := range idx.docs {
		score := idx.cosineSimilarity(queryTF, doc.Terms)
		if score > 0 {
			// Find matching terms
			var matching []string
			for term := range queryTF {
				if doc.Terms[term] > 0 {
					matching = append(matching, term)
				}
			}
			sort.Strings(matching)
			results = append(results, ContextResult{
				Path:          doc.Path,
				Score:         score,
				MatchingTerms: matching,
			})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}
	return results
}

// ContextResult includes matching term information.
type ContextResult struct {
	Path          string   `json:"path"`
	Score         float64  `json:"score"`
	MatchingTerms []string `json:"matching_terms"`
}

// DocCount returns the number of indexed documents.
func (idx *Index) DocCount() int {
	return idx.docCount
}

// TermCount returns the number of unique terms.
func (idx *Index) TermCount() int {
	return len(idx.idf)
}

// --- internals ---

func (idx *Index) computeIDF() {
	// Count document frequency for each term
	df := make(map[string]int)
	for _, doc := range idx.docs {
		for term := range doc.Terms {
			df[term]++
		}
	}

	// IDF = log(N / df)
	n := float64(idx.docCount)
	if n == 0 {
		return
	}
	for term, freq := range df {
		idx.idf[term] = math.Log(n / float64(freq))
	}
}

func (idx *Index) cosineSimilarity(queryTF, docTF map[string]float64) float64 {
	var dotProduct, queryMag, docMag float64

	for term, qTF := range queryTF {
		idf := idx.idf[term]
		qWeight := qTF * idf

		dTF := docTF[term]
		dWeight := dTF * idf

		dotProduct += qWeight * dWeight
		queryMag += qWeight * qWeight
	}

	for term, dTF := range docTF {
		idf := idx.idf[term]
		dWeight := dTF * idf
		docMag += dWeight * dWeight
	}

	if queryMag == 0 || docMag == 0 {
		return 0
	}

	return dotProduct / (math.Sqrt(queryMag) * math.Sqrt(docMag))
}

var wordRegex = regexp.MustCompile(`[a-zA-Z_][a-zA-Z0-9_]*`)

func tokenize(text string) []string {
	// Split camelCase and snake_case
	expanded := expandIdentifiers(text)
	words := wordRegex.FindAllString(expanded, -1)

	var tokens []string
	for _, w := range words {
		lower := strings.ToLower(w)
		if len(lower) >= 2 && !isStopWord(lower) {
			tokens = append(tokens, lower)
		}
	}
	return tokens
}

func expandIdentifiers(text string) string {
	// Split camelCase: "camelCase" -> "camel Case"
	re := regexp.MustCompile(`([a-z])([A-Z])`)
	text = re.ReplaceAllString(text, "${1} ${2}")
	// Split snake_case: "snake_case" -> "snake case"
	text = strings.ReplaceAll(text, "_", " ")
	return text
}

func computeTF(terms []string) map[string]float64 {
	counts := make(map[string]int)
	for _, t := range terms {
		counts[t]++
	}

	total := float64(len(terms))
	if total == 0 {
		return nil
	}

	tf := make(map[string]float64)
	for term, count := range counts {
		tf[term] = float64(count) / total
	}
	return tf
}

var stopWords = map[string]bool{
	"the": true, "is": true, "at": true, "of": true, "on": true,
	"in": true, "to": true, "for": true, "and": true, "or": true,
	"if": true, "it": true, "as": true, "be": true, "by": true,
	"an": true, "do": true, "no": true, "so": true, "up": true,
	"var": true, "err": true, "nil": true, "int": true,
}

func isStopWord(w string) bool {
	return stopWords[w]
}

func shouldSkip(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, p := range parts {
		if p == "vendor" || p == "node_modules" || p == ".git" || p == "target" || p == "__pycache__" {
			return true
		}
	}
	return false
}

func matchExt(ext string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if ext == a {
			return true
		}
	}
	return false
}
