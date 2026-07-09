package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strconv"
	"strings"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/ctxcompress"
)

// policyCompactorEnabled reports whether the ContextCompressor policy seam is
// selected for compaction (opt-in). When unset the runner uses the existing LLM
// condenser / byte-truncation tiers unchanged.
func policyCompactorEnabled() bool {
	return strings.TrimSpace(os.Getenv("R1_CTX_COMPRESS")) == "1"
}

// policyBudgetBytes reads an optional byte budget for the policy compactor.
// 0 (default / unset / invalid) selects threshold mode: every over-floor middle
// tool_result is compressed (behavior-equivalent to the byte-truncation tier).
func policyBudgetBytes() int {
	if v := strings.TrimSpace(os.Getenv("R1_CTX_COMPRESS_BUDGET")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// buildPolicyCompactor returns an agentloop.CompactFunc driven by the
// ContextCompressor seam (ctxcompress.GetCompressor). It builds an Item per
// compressible content block — pinning the first user message (task brief) and
// the last keepRecent messages — lets the active compressor SELECT which items
// to compress (age/redundancy/budget/pinned), and rewrites exactly those blocks
// to pointer markers. Message count and tool_use/tool_result pairing are
// preserved (only content shrinks), so the seam never breaks the API contract.
//
// Deterministic: for a fixed history and a deterministic compressor, the output
// is byte-identical across calls and idempotent on an already-compacted history.
func buildPolicyCompactor(keepRecent, summaryChars int, exemptPrefix string) agentloop.CompactFunc {
	if keepRecent <= 0 {
		keepRecent = 6
	}
	if summaryChars <= 0 {
		summaryChars = 200
	}
	return func(messages []agentloop.Message, _ int) []agentloop.Message {
		n := len(messages)
		if n <= keepRecent+1 {
			return messages
		}
		compactStart := 1
		compactEnd := n - keepRecent
		if compactEnd <= compactStart {
			return messages
		}

		// The active compressor (registry seam) is the selection authority. The
		// native runner registers the configured policy default at wire time;
		// a swapped-in backend routes through the SAME call site below.
		compressor := ctxcompress.GetCompressor()

		// Build items for every compressible block in the middle window. A
		// stable Index encodes (message, block) position so selection maps back
		// exactly. Pinned messages (before compactStart / at-or-after compactEnd)
		// contribute their bytes to the budget but are never selected.
		type blockRef struct{ msg, block int }
		var items []ctxcompress.Item
		refs := map[int]blockRef{}
		idx := 0
		for m := range messages {
			pinnedMsg := m < compactStart || m >= compactEnd
			for b, blk := range messages[m].Content {
				compressible := false
				bytes := 0
				key := ""
				switch blk.Type {
				case "tool_result":
					compressible = !isCondensedPointer(blk.Content)
					bytes = len(blk.Content)
					key = contentKey(blk.Content)
				case "text":
					// Long model narration is compressible unless it is the
					// exempt (condenser summary) block.
					compressible = !strings.HasPrefix(blk.Text, exemptPrefix)
					bytes = len(blk.Text)
					key = contentKey(blk.Text)
				default:
					bytes = len(blk.Content) + len(blk.Text)
				}
				items = append(items, ctxcompress.Item{
					Index:        idx,
					Bytes:        bytes,
					Pinned:       pinnedMsg,
					Compressible: compressible && !pinnedMsg,
					ContentKey:   key,
				})
				refs[idx] = blockRef{msg: m, block: b}
				idx++
			}
		}

		selected := compressor.Compress(items)
		if len(selected) == 0 {
			return messages
		}

		out := make([]agentloop.Message, n)
		copy(out, messages)
		// Deep-copy only the messages we rewrite.
		touched := map[int]bool{}
		for _, si := range selected {
			ref := refs[si]
			if !touched[ref.msg] {
				nc := make([]agentloop.ContentBlock, len(messages[ref.msg].Content))
				copy(nc, messages[ref.msg].Content)
				out[ref.msg] = agentloop.Message{Role: messages[ref.msg].Role, Content: nc}
				touched[ref.msg] = true
			}
			blk := out[ref.msg].Content[ref.block]
			switch blk.Type {
			case "tool_result":
				blk.Content = "(tool result truncated: " + strconv.Itoa(len(messages[ref.msg].Content[ref.block].Content)) + " bytes)"
			case "text":
				orig := messages[ref.msg].Content[ref.block].Text
				if len(orig) > summaryChars {
					blk.Text = orig[:summaryChars] + "... (narration truncated)"
				}
			}
			out[ref.msg].Content[ref.block] = blk
		}
		return out
	}
}

// contentKey is the redundancy identity for a block's content: a sha256 prefix
// so identical outputs share a key without holding the full bytes.
func contentKey(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}
