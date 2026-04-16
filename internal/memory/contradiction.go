// Package memory — contradiction.go
//
// STOKE-022 primitive #4: memory provenance strengthening.
// When an agent writes a new semantic fact to memory, a
// validator checks it against existing facts for
// contradictions — if the new fact disagrees with an
// older high-confidence fact, the write is flagged (and
// optionally rejected) rather than silently overwriting.
//
// Scope of this file:
//
//   - Contradiction struct (existing vs new, disagreement kind)
//   - SemanticValidator interface (pluggable, LLM-backed in
//     production; keyword-based fallback here)
//   - DetectContradictions walks candidate facts from the
//     store and returns matches the validator flags
//
// The default validator uses a conservative keyword /
// negation heuristic: if two facts share ≥2 content tokens
// AND one contains an explicit negation (not / never / no /
// false / incorrect) while the other doesn't, that's a
// contradiction. Anything more subtle requires an LLM-
// backed validator, which operators plug in via the
// interface.
package memory

import (
	"context"
	"fmt"
	"strings"
)

// ContradictionKind classifies how two facts disagree.
type ContradictionKind string

const (
	// KindNegationFlip: one fact asserts, the other
	// explicitly negates (shares tokens + one has not/never).
	KindNegationFlip ContradictionKind = "negation_flip"

	// KindFactualDelta: different factual content on the
	// same tags (e.g. "capital of France is Paris" vs
	// "capital of France is Lyon"). Detected via exact-tag
	// match with distinct content that's NOT a negation.
	KindFactualDelta ContradictionKind = "factual_delta"

	// KindUnknown: the validator flagged a disagreement but
	// couldn't classify the kind.
	KindUnknown ContradictionKind = "unknown"
)

// Contradiction pairs a new fact with an existing one.
type Contradiction struct {
	Existing    Item
	New         Item
	Kind        ContradictionKind
	Explanation string
}

// SemanticValidator compares two facts and returns a
// Contradiction descriptor when they disagree, or nil when
// they're consistent / unrelated.
type SemanticValidator interface {
	Validate(ctx context.Context, existing, incoming Item) (*Contradiction, error)
}

// KeywordValidator is the deterministic fallback validator
// shipped with this package. Uses token-overlap + negation
// presence to detect negation-flip + factual-delta without
// an LLM call.
type KeywordValidator struct {
	// MinSharedTokens is the minimum number of overlapping
	// content tokens for the validator to even consider the
	// two facts related. Default 2.
	MinSharedTokens int
}

// Validate implements SemanticValidator.
func (k KeywordValidator) Validate(_ context.Context, existing, incoming Item) (*Contradiction, error) {
	minShared := k.MinSharedTokens
	if minShared == 0 {
		minShared = 2
	}
	ea := extractContentTokens(existing.Content)
	ib := extractContentTokens(incoming.Content)
	if len(ea) == 0 || len(ib) == 0 {
		return nil, nil
	}
	overlap := intersectTokens(ea, ib)
	if len(overlap) < minShared {
		return nil, nil
	}
	eNeg := hasNegation(existing.Content)
	iNeg := hasNegation(incoming.Content)
	// Negation flip: shared tokens + exactly one of the two
	// has a negation marker.
	if eNeg != iNeg {
		return &Contradiction{
			Existing:    existing,
			New:         incoming,
			Kind:        KindNegationFlip,
			Explanation: fmt.Sprintf("shared tokens [%s] but negation-present in exactly one", joinTop(overlap, 3)),
		}, nil
	}
	// Shared tags + different content that's NOT a negation
	// flip — likely factual delta. Only flag when tags align
	// (otherwise coincidental token overlap is too noisy).
	if tagsOverlap(existing.Tags, incoming.Tags) && existing.Content != incoming.Content {
		return &Contradiction{
			Existing:    existing,
			New:         incoming,
			Kind:        KindFactualDelta,
			Explanation: fmt.Sprintf("same tags [%s]; content differs", strings.Join(commonTags(existing.Tags, incoming.Tags), ", ")),
		}, nil
	}
	return nil, nil
}

// DetectContradictions runs `validator.Validate` against
// every existing fact in the specified tier matching a
// coarse query. Returns all contradictions (caller decides
// whether to reject the write, warn the operator, or accept
// with a low-confidence provenance tag).
func DetectContradictions(ctx context.Context, router *Router, tier Tier, incoming Item, validator SemanticValidator) ([]Contradiction, error) {
	// Query the tier for candidates. We narrow by tag where
	// possible so the validator isn't run against every
	// stored fact — O(n) over the tier would be too much
	// at any reasonable scale.
	var query Query
	query.Tier = tier
	if len(incoming.Tags) > 0 {
		query.Text = incoming.Tags[0] // coarse pre-filter
	} else {
		query.Text = incoming.Content
	}
	query.Limit = 50 // bounded scan to keep this cheap
	candidates, err := router.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("memory: contradiction candidate query: %w", err)
	}
	var out []Contradiction
	for _, cand := range candidates {
		if cand.ID == incoming.ID {
			continue
		}
		c, err := validator.Validate(ctx, cand, incoming)
		if err != nil {
			return nil, fmt.Errorf("memory: validator: %w", err)
		}
		if c != nil {
			out = append(out, *c)
		}
	}
	return out, nil
}

// --- Helpers ---

func extractContentTokens(s string) map[string]bool {
	out := map[string]bool{}
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		t := strings.ToLower(cur.String())
		cur.Reset()
		if contradictionStopwords[t] || len(t) < 3 {
			return
		}
		out[t] = true
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

func intersectTokens(a, b map[string]bool) []string {
	var out []string
	for t := range a {
		if b[t] {
			out = append(out, t)
		}
	}
	return out
}

func hasNegation(s string) bool {
	lower := strings.ToLower(s)
	for _, n := range []string{" not ", " never ", " no ", " false", " incorrect", " wrong", "n't ", " isn't", " aren't", " wasn't", " weren't", " don't", " doesn't", " didn't"} {
		if strings.Contains(" "+lower+" ", n) {
			return true
		}
	}
	return false
}

func tagsOverlap(a, b []string) bool {
	m := map[string]bool{}
	for _, t := range a {
		m[t] = true
	}
	for _, t := range b {
		if m[t] {
			return true
		}
	}
	return false
}

func commonTags(a, b []string) []string {
	m := map[string]bool{}
	for _, t := range a {
		m[t] = true
	}
	var out []string
	for _, t := range b {
		if m[t] {
			out = append(out, t)
		}
	}
	return out
}

func joinTop(items []string, n int) string {
	if n > len(items) {
		n = len(items)
	}
	return strings.Join(items[:n], ",")
}

var contradictionStopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true,
	"this": true, "are": true, "was": true, "were": true, "have": true,
	"has": true, "but": true, "not": true, "never": true, // keep negation tokens as content
}

// Re-enable "not" / "never" so they're NOT treated as
// negation markers when computing overlap — hasNegation
// handles those separately. Without this override the
// stopword list would eat them.
func init() {
	delete(contradictionStopwords, "not")
	delete(contradictionStopwords, "never")
}
