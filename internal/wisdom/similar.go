package wisdom

import (
	"time"

	"github.com/RelayOne/r1/internal/tfidf"
)

// FindSimilar ranks learnings by TF-IDF cosine similarity of their
// Description against query and returns the top-k (most similar first).
//
// This is the semantic half of the recall loop. FindByPattern only fires
// on an EXACT failure-fingerprint hash match, which rarely recurs
// verbatim across runs; FindSimilar surfaces prior learnings whose text
// is close to the current failure/task even when the fingerprint differs
// — the difference between "learning loop" as a slogan and later runs
// actually reusing what worked. Expired learnings (ValidUntil in the
// past, relative to now) are skipped so stale advice does not resurface.
//
// It is a pure function over a learnings snapshot so both Store and
// SQLiteStore share one implementation. k <= 0 or an empty query/corpus
// returns nil.
func FindSimilar(learnings []Learning, query string, k int) []Learning {
	if k <= 0 || query == "" || len(learnings) == 0 {
		return nil
	}
	now := time.Now()
	// Index descriptions as documents keyed by their slice position so the
	// tfidf Result path maps back to the originating learning.
	idx := tfidf.NewIndex()
	active := make([]Learning, 0, len(learnings))
	for _, l := range learnings {
		if l.ValidUntil != nil && l.ValidUntil.Before(now) {
			continue // expired
		}
		if l.Description == "" {
			continue
		}
		docID := intToPath(len(active))
		idx.AddDocument(docID, l.Description)
		active = append(active, l)
	}
	if len(active) == 0 {
		return nil
	}
	idx.Finalize()

	results := idx.Search(query, k)
	out := make([]Learning, 0, len(results))
	for _, r := range results {
		i := pathToInt(r.Path)
		if i >= 0 && i < len(active) {
			out = append(out, active[i])
		}
	}
	return out
}

// FindBySimilar returns the top-k learnings whose Description is most
// semantically similar to query (see FindSimilar).
func (s *Store) FindBySimilar(query string, k int) []Learning {
	return FindSimilar(s.Learnings(), query, k)
}

// FindBySimilar returns the top-k learnings whose Description is most
// semantically similar to query (see FindSimilar).
func (s *SQLiteStore) FindBySimilar(query string, k int) []Learning {
	return FindSimilar(s.Learnings(), query, k)
}

// intToPath / pathToInt encode a small non-negative index as a stable
// document path for the tfidf index and decode it back. Kept trivial and
// allocation-light; the index space is the number of learnings.
func intToPath(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = digits[i%10]
		i /= 10
	}
	return string(buf[pos:])
}

func pathToInt(p string) int {
	n := 0
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}
