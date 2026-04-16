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
	"strconv"
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

// extractNumbers pulls numeric tokens from s and
// normalizes each to a canonical string so downstream
// comparison catches value-level equality AND preserves
// dotted-numeric distinctions.
//
// Normalization ladder:
//   - Multi-dot tokens (semver 1.2.3, IP 10.0.0.1, date
//     2026.4.16) → kept verbatim. ParseFloat rejects
//     these; dropping them would silently lose contradiction
//     signal ("API version is 1.2.3" vs "1.2.4" would go
//     unflagged).
//   - Single-dot or no-dot tokens → round-tripped through
//     ParseFloat + FormatFloat so "5" and "5.0" both
//     canonicalize to "5".
//   - Leading `-` only at a numeric boundary so hyphens
//     inside words ("front-end-123") don't flip the
//     trailing number negative.
func extractNumbers(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		raw := cur.String()
		cur.Reset()
		for len(raw) > 0 && raw[len(raw)-1] == '.' {
			raw = raw[:len(raw)-1]
		}
		if raw == "" || raw == "-" || raw == "." {
			return
		}
		// Multi-dot → keep verbatim.
		if strings.Count(raw, ".") >= 2 {
			out = append(out, raw)
			return
		}
		// Single-dot or no-dot → canonicalize.
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			// Filter should prevent this; preserve raw on
			// the off chance so signal isn't lost.
			out = append(out, raw)
			return
		}
		out = append(out, strconv.FormatFloat(f, 'f', -1, 64))
	}
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
			cur.WriteRune(r)
		case r == '.':
			cur.WriteRune(r)
		case r == '-':
			// Treat `-` as sign only at a numeric boundary
			// (start-of-string or preceded by whitespace /
			// punctuation). Otherwise a hyphen inside a
			// word like "front-end" shouldn't flip a
			// trailing number negative.
			if cur.Len() == 0 && isNumericBoundary(s, i) {
				cur.WriteRune(r)
			} else {
				flush()
			}
		default:
			flush()
		}
	}
	flush()
	return out
}

// isNumericBoundary reports whether byte position i in s is
// the start of a numeric literal — start-of-string or
// preceded by whitespace / punctuation.
func isNumericBoundary(s string, i int) bool {
	if i == 0 {
		return true
	}
	prev := s[i-1]
	return prev == ' ' || prev == '\t' || prev == '\n' ||
		prev == '(' || prev == '[' || prev == ',' ||
		prev == ':' || prev == ';' || prev == '='
}

// numbersAgree reports whether the two sets match after
// canonicalization. Since extractNumbers now returns
// canonical strings ("5" and "5.0" both land on "5",
// semver "1.2.3" stays "1.2.3"), a plain string compare
// gives the right value-level equality for floats AND the
// right string equality for multi-dot tokens.
func numbersAgree(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	has := func(set []string, v string) bool {
		for _, x := range set {
			if x == v {
				return true
			}
		}
		return false
	}
	for _, v := range a {
		if !has(b, v) {
			return false
		}
	}
	for _, v := range b {
		if !has(a, v) {
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
		// No tags → we can't pre-filter on Tags, so query on
		// the individual content tokens instead of the full
		// sentence. Backends treating Query.Text as a literal
		// match (InMemoryStorage, tag-based indexes) would miss
		// obvious contradictions like "JWT tokens expire after
		// 15 minutes" vs "JWT tokens do NOT expire after 15
		// minutes" because the full-sentence key never matches.
		// Union across top-k tokens by content-length so the
		// most-discriminative tokens are tried first.
		tokens := extractContentTokens(incoming.Content)
		if len(tokens) == 0 {
			// Fallback: the content had no indexable tokens
			// (all-punctuation or stopword-only). Use the raw
			// content as a last-resort literal key so callers
			// with text-search backends still see candidates.
			if err := queryOne(incoming.Content); err != nil {
				return nil, err
			}
		} else {
			for t := range tokens {
				if err := queryOne(t); err != nil {
					return nil, err
				}
			}
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
