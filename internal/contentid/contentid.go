// Package contentid provides content-addressed IDs used throughout Stoke.
// Each ID is a prefix + SHA256 hash of the content. IDs are immutable and
// deterministic: same content always produces the same ID.
package contentid

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// ID prefixes for different node types.
const (
	PrefixDecisionInternal = "dec-i-"
	PrefixDecisionRepo     = "dec-r-"
	PrefixTask             = "task-"
	PrefixDraft            = "draft-"
	PrefixLoop             = "loop-"
	PrefixSkill            = "skill-"
	PrefixSnapshot         = "snap-"
	PrefixEvent            = "evt-"
	PrefixEscalation       = "esc-"
	PrefixResearch         = "res-"
	PrefixAdvisory         = "adv-"
	PrefixAgree            = "agr-"
	PrefixDissent          = "dis-"
	PrefixJudge            = "jdg-"
	PrefixStakeholder      = "stk-"
	PrefixSupervisor       = "sup-"
)

// knownPrefixes is the set of all valid prefixes for fast lookup.
var knownPrefixes = []string{
	PrefixDecisionInternal,
	PrefixDecisionRepo,
	PrefixTask,
	PrefixDraft,
	PrefixLoop,
	PrefixSkill,
	PrefixSnapshot,
	PrefixEvent,
	PrefixEscalation,
	PrefixResearch,
	PrefixAdvisory,
	PrefixAgree,
	PrefixDissent,
	PrefixJudge,
	PrefixStakeholder,
	PrefixSupervisor,
}

const hashLen = 12 // number of hex characters kept from the SHA256

// New creates a content-addressed ID from a prefix and raw content bytes.
// The ID is prefix + first 12 hex chars of SHA256(content).
func New(prefix string, content []byte) string {
	h := sha256.Sum256(content)
	return prefix + hex.EncodeToString(h[:])[:hashLen]
}

// NewFromString is a convenience wrapper around New for string content.
func NewFromString(prefix, s string) string {
	return New(prefix, []byte(s))
}

// Valid checks whether id has a known prefix followed by exactly hashLen hex characters.
func Valid(id string) bool {
	p := Prefix(id)
	if p == "" {
		return false
	}
	hash := id[len(p):]
	if len(hash) != hashLen {
		return false
	}
	for _, c := range hash {
		if !isHex(c) {
			return false
		}
	}
	return true
}

// Prefix extracts the prefix portion of an ID, or "" if not recognised.
func Prefix(id string) string {
	for _, p := range knownPrefixes {
		if strings.HasPrefix(id, p) {
			return p
		}
	}
	return ""
}

// Hash extracts the hash portion of an ID, or "" if the prefix is not recognised.
func Hash(id string) string {
	p := Prefix(id)
	if p == "" {
		return ""
	}
	return id[len(p):]
}

func isHex(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
}
