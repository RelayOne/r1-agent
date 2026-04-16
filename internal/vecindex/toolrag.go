// Package vecindex — toolrag.go
//
// STOKE-022 primitive #2: tool retrieval-augmented-generation
// (RAG). When a toolset exceeds ~50 tools, presenting every
// tool description to the LLM bloats the prompt and degrades
// selection accuracy. This file implements the retrieval
// step: index tool + skill descriptions, score against a
// query, return top-K.
//
// Scope of this file:
//
//   - ToolDescriptor: the indexable unit (name + description
//     + tags + manifest hash)
//   - ToolIndex: keyword+tag term-frequency index
//   - Retrieve(query, k) returns top-K descriptors
//
// Uses plain TF-IDF-flavored scoring (not embeddings). A
// follow-up can swap in embedding-backed similarity when the
// operator's environment has an embedding provider wired up —
// the Retriever interface at the bottom exists for that
// plug.
package vecindex

import (
	"math"
	"sort"
	"strings"
	"sync"
)

// ToolDescriptor is one indexable capability. Callers
// populate the fields from their capability manifest
// (STOKE-003) before adding to the index.
type ToolDescriptor struct {
	Name         string
	Description  string
	Tags         []string
	ManifestHash string // for drift detection
}

// ToolIndex is the searchable collection of descriptors.
// Thread-safe — index reads + writes are rare compared to
// retrievals so a single RWMutex is enough.
type ToolIndex struct {
	mu    sync.RWMutex
	tools []indexedTool
	// docFreq maps term → number of tool-entries containing
	// that term (for IDF weighting).
	docFreq map[string]int
}

type indexedTool struct {
	desc   ToolDescriptor
	tf     map[string]float64 // term → count within this descriptor
	length float64            // precomputed l2 for cosine fast path
}

// NewToolIndex returns an empty index.
func NewToolIndex() *ToolIndex {
	return &ToolIndex{docFreq: map[string]int{}}
}

// Add inserts (or replaces) a descriptor. Replacement is by
// Name (Name is the unique key; manifest hash is carried for
// drift auditing, not identity).
func (i *ToolIndex) Add(d ToolDescriptor) {
	i.mu.Lock()
	defer i.mu.Unlock()
	// If an entry with this name exists, decrement its
	// contribution to docFreq before replacing.
	for idx, t := range i.tools {
		if t.desc.Name == d.Name {
			for term := range t.tf {
				if i.docFreq[term] > 0 {
					i.docFreq[term]--
					if i.docFreq[term] == 0 {
						delete(i.docFreq, term)
					}
				}
			}
			i.tools = append(i.tools[:idx], i.tools[idx+1:]...)
			break
		}
	}
	tf := tokenCounts(i.terms(d))
	it := indexedTool{desc: d, tf: tf, length: l2Norm(tf)}
	i.tools = append(i.tools, it)
	for term := range tf {
		i.docFreq[term]++
	}
}

// Remove drops a descriptor by name. Idempotent.
func (i *ToolIndex) Remove(name string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	for idx, t := range i.tools {
		if t.desc.Name == name {
			for term := range t.tf {
				if i.docFreq[term] > 0 {
					i.docFreq[term]--
					if i.docFreq[term] == 0 {
						delete(i.docFreq, term)
					}
				}
			}
			i.tools = append(i.tools[:idx], i.tools[idx+1:]...)
			return
		}
	}
}

// Len reports the number of indexed tools.
func (i *ToolIndex) Len() int {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return len(i.tools)
}

// terms extracts the indexable terms from a descriptor: name
// + description + tags concatenated. Tags are emphasized
// (duplicated 2x) so tag matches score higher than generic
// description matches.
func (i *ToolIndex) terms(d ToolDescriptor) []string {
	var out []string
	out = append(out, tokenize(d.Name)...)
	out = append(out, tokenize(d.Description)...)
	for _, tag := range d.Tags {
		tags := tokenize(tag)
		out = append(out, tags...)
		out = append(out, tags...) // emphasis
	}
	return out
}

// tokenize splits text into lowercase word tokens, dropping
// punctuation + stopwords. Keeps the index from ballooning
// with common English glue words.
func tokenize(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		t := strings.ToLower(cur.String())
		cur.Reset()
		if toolRAGStopwords[t] {
			return
		}
		if len(t) < 2 {
			return
		}
		out = append(out, t)
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}

var toolRAGStopwords = map[string]bool{
	"the": true, "a": true, "an": true, "of": true, "to": true,
	"is": true, "it": true, "and": true, "or": true, "but": true,
	"in": true, "on": true, "for": true, "with": true, "as": true,
	"by": true, "at": true, "that": true, "this": true, "be": true,
	"are": true, "was": true, "were": true, "do": true, "does": true,
	"did": true, "what": true, "how": true, "when": true, "where": true,
	"which": true, "who": true, "why": true,
}

func tokenCounts(tokens []string) map[string]float64 {
	m := map[string]float64{}
	for _, t := range tokens {
		m[t]++
	}
	return m
}

// l2Norm is the Euclidean length of the tf vector. Used by
// cosine similarity.
func l2Norm(v map[string]float64) float64 {
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	return math.Sqrt(sum)
}

// Hit is one retrieval result.
type Hit struct {
	Descriptor ToolDescriptor
	Score      float64
}

// Retrieve returns the top-K descriptors for a query. Score
// is cosine(query-tf, tool-tf) × IDF. Ties broken by Name
// (lexicographic) for determinism.
//
// k <= 0 returns the entire ranked list. Empty query
// returns nothing (no signal to rank on) rather than the
// full index — that prevents an accidental empty query from
// flooding the LLM with every tool.
func (i *ToolIndex) Retrieve(query string, k int) []Hit {
	tokens := tokenize(query)
	if len(tokens) == 0 {
		return nil
	}
	qtf := tokenCounts(tokens)
	qNorm := l2Norm(qtf)
	if qNorm == 0 {
		return nil
	}

	i.mu.RLock()
	total := len(i.tools)
	hits := make([]Hit, 0, total)
	for _, t := range i.tools {
		var dot float64
		for term, w := range qtf {
			if tw, ok := t.tf[term]; ok {
				idf := 1.0
				df := i.docFreq[term]
				if df > 0 && total > 0 {
					idf = math.Log(1.0 + float64(total)/float64(df))
				}
				dot += w * tw * idf
			}
		}
		if dot == 0 {
			continue
		}
		sim := dot / (qNorm * t.length)
		hits = append(hits, Hit{Descriptor: t.desc, Score: sim})
	}
	i.mu.RUnlock()

	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].Score != hits[b].Score {
			return hits[a].Score > hits[b].Score
		}
		return hits[a].Descriptor.Name < hits[b].Descriptor.Name
	})

	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

// Retriever is the abstract interface the tool-RAG layer
// exposes to downstream prompt builders. ToolIndex
// implements it directly; embedding-backed alternatives drop
// in by implementing the same shape.
type Retriever interface {
	Retrieve(query string, k int) []Hit
}

// Compile-time assertion.
var _ Retriever = (*ToolIndex)(nil)
