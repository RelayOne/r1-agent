// Privacy filter for MemoryCuratorLobe (spec item 30).
//
// The spec privacy contract says: "drop tool calls whose source-message-window
// includes any message with tags:[\"private\"]". Two operational details
// the spec leaves to the implementation:
//
//  1. agentloop.Message does not carry a Tags field (the cortex pipeline
//     uses ContentBlock.Type discriminators, not message-level metadata).
//     Callers that want to mark a message private therefore prefix the
//     visible text with a [private] sentinel — this is what the
//     `r1 chat --private` flag emits, and what the privacy-skip tests
//     pin against.
//
//  2. The "source-message-window" is the entire curatorRecentN tail
//     (default 20). Any single message in that tail carrying the
//     sentinel skips the haikuCall pipeline entirely (the model is
//     never shown the private text and never decides about it).
//
// Both decisions are recorded here as documented constants/predicates so
// future work that adds a structured tags field to agentloop.Message can
// extend isPrivate without touching the call sites.
package memorycurator

import (
	"strings"

	"github.com/RelayOne/r1/internal/agentloop"
)

// privateTagSentinel is the text marker that opts a message into the
// privacy filter. Production callers (e.g. r1 chat --private) prefix the
// user-visible text with this sentinel; the curator scans for it and
// skips the entire haikuCall when any message in the tail carries it.
//
// The exact byte sequence MUST match between the producer and consumer;
// drift here silently leaks private content into Haiku. The constant
// lives in this dedicated file so a single text-search reveals every
// integration point.
const privateTagSentinel = "[private]"

// isPrivateMessage reports whether m's joined text contains the
// privateTagSentinel. Matching is case-sensitive — the sentinel is a
// machine-emitted marker, not a free-form user phrase, so the byte
// sequence is stable.
//
// Tool-use, tool-result, and thinking blocks are skipped (private
// content only travels in user-visible text blocks). Future work that
// adds a structured Tags field can extend this predicate.
func isPrivateMessage(m agentloop.Message) bool {
	for _, blk := range m.Content {
		if blk.Type != "" && blk.Type != "text" {
			continue
		}
		if strings.Contains(blk.Text, privateTagSentinel) {
			return true
		}
	}
	return false
}

// windowContainsPrivate reports whether ANY message in the supplied
// window carries the privacy sentinel. Used by the curator's haikuCall
// to decide whether to skip the call entirely (privacy.SkipPrivateMessages
// gate). Returns false on a nil/empty window.
func windowContainsPrivate(window []agentloop.Message) bool {
	for _, m := range window {
		if isPrivateMessage(m) {
			return true
		}
	}
	return false
}
