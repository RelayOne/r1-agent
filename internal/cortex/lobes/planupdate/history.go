// Read-only helpers over LobeInput.History for the PlanUpdate trigger
// logic (TASK-17). Lifted from internal/cortex/lobes/memoryrecall/lobe.go
// to keep the two Lobes' parsing of agentloop.Message identical without
// importing across packages.
package planupdate

import (
	"strings"

	"github.com/RelayOne/r1/internal/agentloop"
)

// lastUserText returns the concatenated text of the most recent
// user-role agentloop.Message, or "" if there is no user message in
// history. Tool-result blocks are skipped — they never carry a
// user-typed message in the cortex pipeline.
func lastUserText(history []agentloop.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if m.Role != "user" {
			continue
		}
		text := joinTextBlocks(m.Content)
		if text == "" {
			continue
		}
		return text
	}
	return ""
}

// joinTextBlocks collapses a slice of agentloop.ContentBlock into a
// single concatenated string of every "text"-typed block. Other block
// types (tool_use, tool_result, thinking, ...) are skipped.
func joinTextBlocks(blocks []agentloop.ContentBlock) string {
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type != "" && blk.Type != "text" {
			continue
		}
		if blk.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(blk.Text)
	}
	return b.String()
}
