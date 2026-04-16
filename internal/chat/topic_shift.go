// Package chat — topic_shift.go
//
// STOKE-022 primitive #7: topic-shift detection for multi-task
// sessions. When the conversation wanders from one topic to
// another, the detector fires so the caller can either (a)
// spawn a sub-agent with fresh context, or (b) apply
// hierarchical summarization to compress the old context.
//
// The detector is keyword-based for the initial cut: each
// user turn is tokenized and compared against a rolling
// keyword-vector of the recent window. Cosine similarity below
// a threshold for K consecutive turns indicates a shift.
//
// Scope:
//
//   - TokenVector + helpers
//   - ShiftDetector with configurable window + threshold +
//     streak
//   - Observe + Shifted + Reset lifecycle
//
// A future pass will swap the keyword-vector for an embedding-
// backed similarity check, but the interface stays the same.
package chat

import (
	"strings"
	"sync"
)

// ShiftDetector tracks a rolling window of recent turns and
// fires when similarity to the rolling window drops below a
// threshold for a consecutive streak of turns.
type ShiftDetector struct {
	mu        sync.Mutex
	windowSize int
	threshold  float64 // cosine similarity below this = shift signal
	streakNeeded int    // how many consecutive below-threshold turns fire Shifted
	window    []tokenVector
	belowStreak int
	fired     bool
}

// NewShiftDetector returns a detector with sensible defaults:
// 6-turn window, 0.2 similarity threshold, 2-turn consecutive
// shift streak required to fire. Operators can tune via the
// constructor.
func NewShiftDetector() *ShiftDetector {
	return &ShiftDetector{
		windowSize:   6,
		threshold:    0.2,
		streakNeeded: 2,
	}
}

// SetWindowSize / SetThreshold / SetStreak allow operators
// to tune without reconstructing.
//
// Window size is clamped to >=2. A window of 1 would make
// the rolling aggregate always equal the most-recent turn,
// which in turn would always produce similarity=1 in
// Observe's compare step — the detector could never fire.
// Clamping prevents that silent disable.
func (d *ShiftDetector) SetWindowSize(n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if n < 2 {
		n = 2
	}
	d.windowSize = n
}

func (d *ShiftDetector) SetThreshold(t float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.threshold = t
}

func (d *ShiftDetector) SetStreak(s int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s < 1 {
		s = 1
	}
	d.streakNeeded = s
}

// tokenVector is a sparse term-frequency vector. Cosine
// similarity against the rolling window is computed via
// ad-hoc dot-product.
type tokenVector map[string]float64

// tokenize splits s into lowercase word tokens, dropping
// punctuation. Conservative — the point is coarse topic
// detection, not NLP perfection.
func tokenize(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, strings.ToLower(cur.String()))
			cur.Reset()
		}
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
	// Drop English stop-words so "the" + "of" + "a" don't
	// dominate the vector.
	return filterStopWords(out)
}

var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "of": true, "to": true,
	"is": true, "it": true, "and": true, "or": true, "but": true,
	"in": true, "on": true, "for": true, "with": true, "as": true,
	"by": true, "at": true, "i": true, "you": true, "that": true,
	"this": true, "be": true, "are": true, "was": true, "were": true,
	"do": true, "does": true, "did": true, "what": true, "how": true,
}

func filterStopWords(tokens []string) []string {
	out := tokens[:0]
	for _, t := range tokens {
		if stopWords[t] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// vectorize returns a term-frequency vector for text.
func vectorize(text string) tokenVector {
	v := tokenVector{}
	for _, t := range tokenize(text) {
		v[t]++
	}
	return v
}

// cosineSimilarity between two tokenVectors. Zero when either
// is empty; in [0,1] otherwise (term-frequency vectors have
// non-negative components so similarity is always >= 0).
func cosineSimilarity(a, b tokenVector) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	var dot, na, nb float64
	for t, w := range a {
		na += w * w
		if w2, ok := b[t]; ok {
			dot += w * w2
		}
	}
	for _, w := range b {
		nb += w * w
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrt(na) * sqrt(nb))
}

// sqrt is a local helper to avoid importing math for one call.
// Uses Newton's method, good to ~1e-10 for non-negative
// inputs.
func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 10; i++ {
		z = (z + x/z) / 2
	}
	return z
}

// Observe records a new user turn. Returns true when the
// detector has fired this turn (the consecutive-streak
// threshold was crossed). Subsequent calls without a Reset
// return false even if the signal persists — the detector
// fires once per shift event.
func (d *ShiftDetector) Observe(userTurn string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	v := vectorize(userTurn)

	// Compute similarity against the rolling window's
	// aggregate vector.
	agg := tokenVector{}
	for _, wv := range d.window {
		for t, w := range wv {
			agg[t] += w
		}
	}
	similarity := cosineSimilarity(v, agg)

	// Window update: append and truncate to size.
	d.window = append(d.window, v)
	if len(d.window) > d.windowSize {
		d.window = d.window[len(d.window)-d.windowSize:]
	}

	if len(d.window) <= 1 || len(agg) == 0 {
		// First turn or empty aggregate — can't detect a
		// shift yet.
		return false
	}

	if similarity < d.threshold {
		d.belowStreak++
	} else {
		d.belowStreak = 0
		// NOTE: do NOT clear d.fired here. Once fired, the
		// detector stays latched until the caller calls
		// Reset — per the documented contract. Clearing on
		// similarity rebound would re-fire the same shift
		// and violate the one-signal-per-event invariant.
	}

	if d.belowStreak >= d.streakNeeded && !d.fired {
		d.fired = true
		return true
	}
	return false
}

// Shifted reports whether a shift has been detected in the
// current window (persists until Reset).
func (d *ShiftDetector) Shifted() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fired
}

// Reset clears the rolling window + streak + fired flag.
// Call after the caller has acted on a Shifted signal (e.g.
// spawned a sub-agent or applied summarization).
func (d *ShiftDetector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.window = d.window[:0]
	d.belowStreak = 0
	d.fired = false
}
