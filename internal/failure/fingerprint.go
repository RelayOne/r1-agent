package failure

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Fingerprint uniquely identifies a failure pattern for deduplication and tracking.
// Two failures with the same fingerprint are considered "the same bug."
// This enables: same-error detection across retries, cross-task learning,
// and aggregated failure analytics.
type Fingerprint struct {
	Hash       string `json:"hash"`        // SHA-256 of normalized components
	Class      Class  `json:"class"`       // failure classification
	Pattern    string `json:"pattern"`     // human-readable signature
	Components []string `json:"components"` // what went into the hash
}

// Compute generates a fingerprint from a failure analysis.
// Normalizes away noisy details (line numbers, timestamps, full paths)
// to match failures that have the same root cause.
func Compute(a *Analysis) Fingerprint {
	if a == nil {
		return Fingerprint{Hash: "nil", Class: Incomplete, Pattern: "no analysis"}
	}

	var components []string
	components = append(components, string(a.Class))

	// Normalize specifics: extract error patterns without noisy details
	for _, d := range a.Specifics {
		normalized := normalizeMessage(d.Message)
		if normalized != "" {
			components = append(components, normalized)
		}
	}

	// If no specifics, use the summary
	if len(a.Specifics) == 0 && a.Summary != "" {
		components = append(components, normalizeSummary(a.Summary))
	}

	// Sort for deterministic hashing
	sort.Strings(components[1:]) // keep class first

	// Hash
	h := sha256.New()
	for _, c := range components {
		h.Write([]byte(c))
		h.Write([]byte{0})
	}
	hash := fmt.Sprintf("%x", h.Sum(nil))[:16] // first 16 hex chars

	return Fingerprint{
		Hash:       hash,
		Class:      a.Class,
		Pattern:    buildPattern(a),
		Components: components,
	}
}

// Same returns true if two fingerprints represent the same failure.
func (f Fingerprint) Same(other Fingerprint) bool {
	return f.Hash == other.Hash
}

// Similar returns true if two fingerprints are in the same class
// and share at least one component (partial match).
func (f Fingerprint) Similar(other Fingerprint) bool {
	if f.Class != other.Class {
		return false
	}
	for _, a := range f.Components {
		for _, b := range other.Components {
			if a == b && a != string(f.Class) {
				return true
			}
		}
	}
	return false
}

// --- Normalization ---

// Strips noisy details to get at the core error pattern.
var (
	lineNumRe  = regexp.MustCompile(`:\d+:\d+:?`)
	lineNumRe2 = regexp.MustCompile(`\(\d+,\d+\)`)
	pathRe     = regexp.MustCompile(`(?:^|[\s'"])(/[^\s'"]+)`)
	hexRe      = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	tsErrorNum = regexp.MustCompile(`TS\d+`)
	goFileRe   = regexp.MustCompile(`[a-zA-Z0-9_/]+\.go`)
)

func normalizeMessage(msg string) string {
	s := msg
	// Remove line:col numbers
	s = lineNumRe.ReplaceAllString(s, "")
	s = lineNumRe2.ReplaceAllString(s, "")
	// Remove absolute paths but keep relative ones
	s = pathRe.ReplaceAllString(s, " <path>")
	// Remove hex addresses
	s = hexRe.ReplaceAllString(s, "<addr>")
	// Normalize whitespace
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}

func normalizeSummary(summary string) string {
	// Remove counts ("3 error(s)" → "error(s)")
	countRe := regexp.MustCompile(`\d+\s+(error|test|warning|violation)`)
	s := countRe.ReplaceAllString(summary, "$1")
	return strings.TrimSpace(s)
}

func buildPattern(a *Analysis) string {
	if len(a.Specifics) == 0 {
		return a.Summary
	}

	// Group by message type
	types := map[string]int{}
	for _, d := range a.Specifics {
		key := normalizeMessage(d.Message)
		if key != "" {
			types[key]++
		}
	}

	var parts []string
	for k, v := range types {
		if v > 1 {
			parts = append(parts, fmt.Sprintf("%s (x%d)", k, v))
		} else {
			parts = append(parts, k)
		}
	}
	sort.Strings(parts)

	if len(parts) > 3 {
		parts = parts[:3]
		parts = append(parts, "...")
	}

	return strings.Join(parts, "; ")
}

// MatchHistory checks if a fingerprint matches any previous failure in the history.
// Returns the matching fingerprint and how many times it occurred.
func MatchHistory(fp Fingerprint, history []Fingerprint) (matched *Fingerprint, count int) {
	for i := range history {
		if fp.Same(history[i]) {
			count++
			if matched == nil {
				matched = &history[i]
			}
		}
	}
	return
}
