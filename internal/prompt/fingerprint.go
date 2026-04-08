// Package prompt provides prompt engineering utilities for AI-assisted coding.
// fingerprint.go implements FNV-1a prompt fingerprinting for cache break detection.
//
// Inspired by claw-code-parity's prompt fingerprinting: hash the static portions
// of system prompts to detect when cache-stable content has changed. When the
// fingerprint changes, all prompt caches are invalidated — so we want to minimize
// unnecessary fingerprint changes while detecting real content shifts.
//
// Uses FNV-1a (not SHA-256) because it's fast, non-cryptographic, and the use case
// is change detection, not security.
package prompt

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
	"sync"
)

// Fingerprint identifies a specific version of a prompt template.
// Two prompts with the same fingerprint will hit the same API cache slot.
type Fingerprint struct {
	Hash       uint64 `json:"hash"`
	HexHash    string `json:"hex_hash"`
	StaticLen  int    `json:"static_len"`   // length of static content that was hashed
	DynamicLen int    `json:"dynamic_len"`  // length of dynamic content that was stripped
	Version    int    `json:"version"`      // incremented when hash changes
}

// String returns a human-readable fingerprint.
func (f Fingerprint) String() string {
	return fmt.Sprintf("fp:%s(v%d,s%d,d%d)", f.HexHash, f.Version, f.StaticLen, f.DynamicLen)
}

// Same returns true if two fingerprints have the same hash.
func (f Fingerprint) Same(other Fingerprint) bool {
	return f.Hash == other.Hash
}

// CacheBreak returns true if the fingerprint changed from a previous version.
func (f Fingerprint) CacheBreak(previous Fingerprint) bool {
	return f.Hash != previous.Hash && previous.Hash != 0
}

// DynamicBoundary is the marker that separates cache-stable content from dynamic content.
// Everything before this marker is hashed; everything after is excluded.
// Inspired by claw-code-parity's __SYSTEM_PROMPT_DYNAMIC_BOUNDARY__.
const DynamicBoundary = "__DYNAMIC_BOUNDARY__"

// Patterns stripped before hashing to normalize away noisy details.
var (
	timestampRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}`)
	dateRe      = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	uuidRe      = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	sessionIDRe = regexp.MustCompile(`session[_-]id[:\s]*\S+`)
)

// ComputeFingerprint generates an FNV-1a fingerprint from a prompt string.
// Strips dynamic content after DynamicBoundary and normalizes timestamps/UUIDs.
func ComputeFingerprint(prompt string) Fingerprint {
	static, dynamic := splitAtBoundary(prompt)
	normalized := normalize(static)

	h := fnv.New64a()
	h.Write([]byte(normalized))

	hash := h.Sum64()
	return Fingerprint{
		Hash:       hash,
		HexHash:    fmt.Sprintf("%016x", hash),
		StaticLen:  len(static),
		DynamicLen: len(dynamic),
	}
}

// Tracker tracks fingerprint changes across prompt versions.
// Thread-safe. Detects cache breaks when static prompt content changes.
type Tracker struct {
	mu       sync.Mutex
	current  Fingerprint
	previous Fingerprint
	breaks   int
	history  []Fingerprint
}

// NewTracker creates a fingerprint tracker.
func NewTracker() *Tracker {
	return &Tracker{}
}

// Update computes a new fingerprint and checks for cache breaks.
// Returns true if the cache was broken (fingerprint changed).
func (t *Tracker) Update(prompt string) bool {
	fp := ComputeFingerprint(prompt)

	t.mu.Lock()
	defer t.mu.Unlock()

	broke := fp.CacheBreak(t.current)
	if broke {
		t.breaks++
		fp.Version = t.current.Version + 1
	} else if t.current.Hash == 0 {
		fp.Version = 1
	} else {
		fp.Version = t.current.Version
	}

	t.previous = t.current
	t.current = fp
	t.history = append(t.history, fp)

	return broke
}

// Current returns the current fingerprint.
func (t *Tracker) Current() Fingerprint {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.current
}

// Breaks returns the number of cache breaks detected.
func (t *Tracker) Breaks() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.breaks
}

// History returns all recorded fingerprints.
func (t *Tracker) History() []Fingerprint {
	t.mu.Lock()
	defer t.mu.Unlock()
	cp := make([]Fingerprint, len(t.history))
	copy(cp, t.history)
	return cp
}

// splitAtBoundary splits a prompt at the dynamic boundary marker.
func splitAtBoundary(prompt string) (static, dynamic string) {
	idx := strings.Index(prompt, DynamicBoundary)
	if idx < 0 {
		return prompt, ""
	}
	return prompt[:idx], prompt[idx+len(DynamicBoundary):]
}

// normalize strips noisy dynamic content that shouldn't affect the fingerprint.
func normalize(s string) string {
	s = timestampRe.ReplaceAllString(s, "")
	s = dateRe.ReplaceAllString(s, "")
	s = uuidRe.ReplaceAllString(s, "")
	s = sessionIDRe.ReplaceAllString(s, "")
	// Normalize whitespace
	s = strings.Join(strings.Fields(s), " ")
	return s
}

// BuildCacheStablePrompt constructs a prompt with clear static/dynamic separation.
// Static parts are hashed for cache stability; dynamic parts change per-request.
func BuildCacheStablePrompt(staticParts []string, dynamicParts []string) string {
	var sb strings.Builder
	for _, p := range staticParts {
		sb.WriteString(p)
		sb.WriteString("\n")
	}
	if len(dynamicParts) > 0 {
		sb.WriteString(DynamicBoundary)
		sb.WriteString("\n")
		for _, p := range dynamicParts {
			sb.WriteString(p)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}
