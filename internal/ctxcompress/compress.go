// Package ctxcompress is R1's context-compaction policy seam.
//
// A ContextCompressor decides WHICH conversation items to compress when history
// grows too large. The default PolicyCompressor is deterministic and policy-
// driven — it keeps pinned items verbatim and compresses the rest by
// redundancy, age, and a byte budget. A different compressor swaps in by config
// with zero caller changes (see the swap-proof test). The compaction seam wires
// this to rewrite over-budget content in place; message count and tool-call
// pairing are preserved by the caller, not by dropping messages.
package ctxcompress

import (
	"sort"
	"sync"
)

// Item is one compressible unit of history (a message content block) plus the
// signals the policy uses. Items are supplied in ascending Index order (oldest
// first).
type Item struct {
	// Index is the item's position in the original sequence (0 = oldest).
	Index int
	// Bytes is the size of the item's content.
	Bytes int
	// Pinned items are never compressed (task brief, recent tail, exempt
	// summary block).
	Pinned bool
	// Compressible marks items whose content the caller can safely rewrite to a
	// pointer (e.g. a tool_result body). Non-compressible items are left alone.
	Compressible bool
	// ContentKey identifies an item for redundancy detection: two items with the
	// same non-empty key carry duplicate content, and older duplicates are
	// compressed first. Empty means "no redundancy signal".
	ContentKey string
}

// Policy configures the default compressor.
type Policy struct {
	// MaxBytes is the byte budget. When > 0, items are compressed oldest-first
	// until total live bytes fall to or below it. When 0, the policy runs in
	// threshold mode: every compressible non-pinned item at or above MinBytes is
	// compressed (the classic "truncate all oversized middle results" behavior).
	MaxBytes int
	// MinBytes is a floor: an item smaller than this is never compressed.
	MinBytes int
	// DropRedundant compresses older duplicates (same ContentKey) first,
	// regardless of budget.
	DropRedundant bool
}

// markerBytes approximates the size a compressed item shrinks to (a short
// pointer marker), used for budget accounting.
const markerBytes = 48

// ContextCompressor selects which item indices to compress. Implementations must
// be deterministic: the same items must always yield the same selection.
type ContextCompressor interface {
	// Compress returns the sorted, de-duplicated set of item indices to compress.
	Compress(items []Item) []int
	// Backend is the stable backend name.
	Backend() string
}

// BackendPolicy is the default ContextCompressor backend name.
const BackendPolicy = "policy"

// PolicyCompressor is the unencumbered default: age / redundancy / budget /
// pinned, fully deterministic.
type PolicyCompressor struct {
	policy Policy
}

// NewPolicyCompressor builds a policy compressor. A zero Policy runs in threshold
// mode with no floor (compress every compressible non-pinned item).
func NewPolicyCompressor(p Policy) *PolicyCompressor {
	return &PolicyCompressor{policy: p}
}

// Backend returns the stable backend name.
func (c *PolicyCompressor) Backend() string { return BackendPolicy }

// Compress applies the policy deterministically. Order of operations:
//  1. redundancy: older duplicates (same ContentKey) are selected first;
//  2. budget: if MaxBytes > 0, compress oldest-first (by Index) until total live
//     bytes are within budget; otherwise threshold mode selects every eligible
//     item. Pinned items and items below MinBytes are never selected.
func (c *PolicyCompressor) Compress(items []Item) []int {
	ordered := append([]Item(nil), items...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Index < ordered[j].Index })

	selected := map[int]bool{}
	eligible := func(it Item) bool {
		return it.Compressible && !it.Pinned && it.Bytes >= c.policy.MinBytes
	}

	if c.policy.DropRedundant {
		lastIdx := map[string]int{}
		for _, it := range ordered {
			if !eligible(it) || it.ContentKey == "" {
				continue
			}
			if prev, ok := lastIdx[it.ContentKey]; ok {
				selected[prev] = true // older duplicate compressed; keep the most recent
			}
			lastIdx[it.ContentKey] = it.Index
		}
	}

	if c.policy.MaxBytes > 0 {
		total := 0
		for _, it := range ordered {
			total += it.Bytes
		}
		for _, it := range ordered {
			if selected[it.Index] {
				total -= saved(it)
			}
		}
		for _, it := range ordered {
			if total <= c.policy.MaxBytes {
				break
			}
			if !eligible(it) || selected[it.Index] {
				continue
			}
			selected[it.Index] = true
			total -= saved(it)
		}
	} else {
		for _, it := range ordered {
			if eligible(it) {
				selected[it.Index] = true
			}
		}
	}

	out := make([]int, 0, len(selected))
	for idx := range selected {
		out = append(out, idx)
	}
	sort.Ints(out)
	return out
}

// saved is the byte reduction from compressing one item to a marker.
func saved(it Item) int {
	if it.Bytes <= markerBytes {
		return 0
	}
	return it.Bytes - markerBytes
}

// ---- Registry: swap a different compressor in by config, zero caller changes ----

var (
	mu     sync.RWMutex
	active ContextCompressor = NewPolicyCompressor(Policy{})
)

// GetCompressor is the canonical accessor every caller uses.
func GetCompressor() ContextCompressor {
	mu.RLock()
	defer mu.RUnlock()
	return active
}

// SetCompressor swaps the compressor backend by config.
func SetCompressor(next ContextCompressor) {
	mu.Lock()
	defer mu.Unlock()
	active = next
}

// ResetCompressor restores the unencumbered default (test hygiene).
func ResetCompressor() {
	mu.Lock()
	defer mu.Unlock()
	active = NewPolicyCompressor(Policy{})
}
