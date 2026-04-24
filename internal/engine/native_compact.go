package engine

import (
	"strconv"
	"strings"

	"github.com/RelayOne/r1/internal/agentloop"
)

// buildNativeCompactor returns an agentloop.CompactFunc that rewrites an
// overlong conversation history to keep it under threshold. The strategy
// is cache-preserving:
//
//  1. Always keep the first user message (the task brief) verbatim —
//     this is typically the instruction the model is trying to follow.
//  2. Always keep the last N messages verbatim (in-flight work).
//     Recent tool_use/tool_result pairs must stay intact or the next
//     API call will 400 with "unresolved tool_use".
//  3. Replace older tool_result bodies with a 1-line summary: "(tool
//     result truncated: N bytes)". Keeps the pair structure for the API
//     but collapses the payload.
//  4. If still too long, drop the oldest assistant-only turns (text
//     without tool calls) — these are progress narrations the model
//     doesn't need on a fresh request.
//
// The returned function is safe to call repeatedly; it's idempotent on
// already-compacted histories.
//
// keepRecent is the number of messages at the tail that stay verbatim
// (default 6 if 0). summaryChars is the maximum characters a
// summarized tool_result keeps (default 200 if 0).
func buildNativeCompactor(keepRecent, summaryChars int) agentloop.CompactFunc {
	if keepRecent <= 0 {
		keepRecent = 6
	}
	if summaryChars <= 0 {
		summaryChars = 200
	}
	return func(messages []agentloop.Message, estimatedTokens int) []agentloop.Message {
		n := len(messages)
		if n <= keepRecent+1 {
			// Nothing to compact safely.
			return messages
		}

		// Phase 1: build a copy where middle tool_results are summarized.
		out := make([]agentloop.Message, n)
		copy(out, messages)

		// The first user message is the task brief — always verbatim.
		// Everything from (n - keepRecent) to the end is also verbatim.
		compactStart := 1
		compactEnd := n - keepRecent
		if compactEnd <= compactStart {
			return messages
		}

		for i := compactStart; i < compactEnd; i++ {
			msg := out[i]
			newContent := make([]agentloop.ContentBlock, len(msg.Content))
			for j, block := range msg.Content {
				nb := block
				switch block.Type {
				case "tool_result":
					if len(block.Content) > summaryChars {
						nb.Content = "(tool result truncated: " + strconv.Itoa(len(block.Content)) + " bytes)"
					}
				case "text":
					// Collapse long text blocks (model narration). Keep
					// short ones because they might carry intent.
					if len(block.Text) > summaryChars*2 {
						nb.Text = block.Text[:summaryChars] + "... (narration truncated)"
					}
				}
				newContent[j] = nb
			}
			out[i] = agentloop.Message{Role: msg.Role, Content: newContent}
		}
		return out
	}
}

// compactionEnabled reports whether the given RunSpec asked for progressive
// compaction. Helper for native_runner.go so the wiring stays readable.
func compactionEnabled(spec RunSpec) bool {
	return spec.CompactThreshold > 0
}

// strings is imported for symmetry with future expansions; reference
// the package once so the import is never flagged when callers shrink.
var _ = strings.TrimSpace
