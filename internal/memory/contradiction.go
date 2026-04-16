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
	// Same-tag different-content is NOT automatically a
	// contradiction — complementary updates ("JWT tokens
	// expire after 15m" + "JWT tokens store a kid claim")
	// aren't disagreements. LiteralChecker can only
	// positively identify contradictions when the text
	// shape explicitly disagrees (numeric values that
	// differ on the same attribute, boolean flips). Anything
	// weaker is an LLM-backed validator's job.
	//
	// Concrete signal we still flag here: the two facts
	// share a tag AND both contain a numeric token referring
	// to the same subject but with different values. E.g.
	// "cache TTL is 5 minutes" vs "cache TTL is 10 minutes"
	// share the "cache" + "ttl" tokens and both have
	// numerics — 5 vs 10 disagree.
	if tagsOverlap(existing.Tags, incoming.Tags) {
		eNums := extractNumbers(existing.Content)
		iNums := extractNumbers(incoming.Content)
		if len(eNums) > 0 && len(iNums) > 0 && !numbersAgree(eNums, iNums) {
			return &Contradiction{
				Existing:    existing,
				New:         incoming,
				Kind:        KindFactualDelta,
				Explanation: fmt.Sprintf("same tags [%s]; numeric values differ (%v vs %v)",
					strings.Join(commonTags(existing.Tags, incoming.Tags), ", "), eNums, iNums),
			}, nil
		}
	}
	return nil, nil
}

// extractNumbers pulls numeric tokens (integers + decimals)
// from s. Used by the contradiction validator's tightened
// factual-delta path so same-tag complementary facts
// ("cache is fast" + "cache stores bytes") don't false-flag
// but same-tag numeric-value facts ("TTL is 5" + "TTL is 10")
// do.
func extractNumbers(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		if (r >= '0' && r <= '9') || r == '.' {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// numbersAgree reports whether every number in a also
// appears in b (and vice versa, modulo formatting).
// Used by the contradiction detector to decide whether
// shared-tag + differing-content is a numeric disagreement.
func numbersAgree(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	asSet := map[string]bool{}
	for _, n := range a {
		asSet[n] = true
	}
	bsSet := map[string]bool{}
	for _, n := range b {
		bsSet[n] = true
	}
	// If at least one numeric appears in ONE set but not
	// the other, the facts disagree.
	for n := range asSet {
		if !bsSet[n] {
			return false
		}
	}
	for n := range bsSet {
		if !asSet[n] {
			return false
		}
	}
	return true
}

// DetectContradictions runs `validator.Validate` against
// every existing fact in the specified tier matching ANY
// of the incoming item's tags (not just Tags[0]). Returns
// all contradictions (caller decides whether to reject the
// write, warn the operator, or accept with a low-confidence
// provenance tag).
//
// Tag iteration: a contradiction whose only shared tag is
// the incoming item's 3rd or 7th tag would have been missed
// by prior versions that only pre-filtered on Tags[0].
// We now run one query per tag (deduplicating by candidate
// ID) so detection isn't dependent on tag ordering.
func DetectContradictions(ctx context.Context, router *Router, tier Tier, incoming Item, validator SemanticValidator) ([]Contradiction, error) {
	seen := map[string]Item{}
	queryOne := func(text string) error {
		candidates, err := router.Query(ctx, Query{
			Tier:  tier,
			Text:  text,
			Limit: 50,
		})
		if err != nil {
			return fmt.Errorf("memory: contradiction candidate query: %w", err)
		}
		for _, c := range candidates {
			if c.ID == incoming.ID {
				continue
			}
			seen[c.ID] = c
		}
		return nil
	}
	if len(incoming.Tags) == 0 {
		if err := queryOne(incoming.Content); err != nil {
			return nil, err
		}
	} else {
		for _, tag := range incoming.Tags {
			if err := queryOne(tag); err != nil {
				return nil, err
			}
		}
	}

	var out []Contradiction
	for _, cand := range seen {
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
