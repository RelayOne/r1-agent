// Package plan — content_judge_mcp.go
//
// MCP ghost-call detector. When a worker claims to have invoked an
// MCP tool (matching the regex `called\s+mcp_[a-z0-9_-]+` or similar
// prose shapes) but there is no matching `<mcp_result server=...>`
// XML block in the same turn transcript, the worker is either
// confused or fabricating. This is the MCP analogue of the
// ghost_write_detector — both catch tool-claim / tool-trace
// mismatches.
//
// Gating: advisory-by-default. Set STOKE_MCP_STRICT=1 in the env
// to upgrade mismatches to hard failures.

package plan

import (
	"regexp"
	"strings"

	"github.com/ericmacdougall/stoke/internal/r1env"
)

// mcpCallClaimPattern matches common worker prose shapes asserting an
// MCP tool invocation occurred. The regex is intentionally loose —
// we err toward recall, since the consequence of a mismatch is an
// advisory log unless STOKE_MCP_STRICT=1.
var mcpCallClaimPattern = regexp.MustCompile(
	`(?i)(called|invoked|ran|used|executed|issued)\s+(?:the\s+|tool\s+)?(mcp_[a-z0-9_-]+)`,
)

// mcpResultBlockPattern matches the structured XML block emitted by
// native_runner when an MCP CallTool succeeded. The server/tool
// attributes are required — bare <mcp_result/> is not a valid trace.
var mcpResultBlockPattern = regexp.MustCompile(
	`<mcp_result\s+[^>]*server\s*=\s*"([^"]+)"[^>]*tool\s*=\s*"([^"]+)"[^>]*>`,
)

// MCPGhostCallFinding captures one claim that had no matching result.
type MCPGhostCallFinding struct {
	// ClaimedTool is the `mcp_<server>_<tool>` form extracted from the
	// worker's prose.
	ClaimedTool string
	// ClaimContext is a short excerpt (up to 200 runes) around the claim
	// for operator diagnosis. Never the full transcript — logs need to
	// stay scannable.
	ClaimContext string
}

// DetectMCPGhostCalls scans a worker's final turn transcript for
// claimed MCP tool invocations without matching <mcp_result> blocks.
// Returns one finding per unmatched claim. Duplicate claims for the
// same tool name are de-duped.
//
// When STOKE_MCP_STRICT=1 is set, the caller should treat a
// non-empty finding slice as a hard blocker for task completion.
// Without the env flag, findings are advisory — log them so
// operators see the pattern, but do not fail the task.
func DetectMCPGhostCalls(transcript string) []MCPGhostCallFinding {
	if transcript == "" {
		return nil
	}
	claims := mcpCallClaimPattern.FindAllStringSubmatchIndex(transcript, -1)
	if len(claims) == 0 {
		return nil
	}
	// Collect every result block's fully-qualified tool name in a
	// set. Format: mcp_<server>_<tool>.
	seen := map[string]struct{}{}
	for _, m := range mcpResultBlockPattern.FindAllStringSubmatch(transcript, -1) {
		if len(m) < 3 {
			continue
		}
		name := "mcp_" + m[1] + "_" + m[2]
		seen[name] = struct{}{}
	}

	out := make([]MCPGhostCallFinding, 0, len(claims))
	emitted := map[string]struct{}{}
	for _, idx := range claims {
		// idx[4:6] = capture group 2 (the mcp_* tool name).
		if len(idx) < 6 {
			continue
		}
		tool := transcript[idx[4]:idx[5]]
		if _, already := emitted[tool]; already {
			continue
		}
		if _, present := seen[tool]; present {
			continue
		}
		emitted[tool] = struct{}{}
		out = append(out, MCPGhostCallFinding{
			ClaimedTool:  tool,
			ClaimContext: excerptAround(transcript, idx[0], 200),
		})
	}
	return out
}

// MCPStrictModeEnabled reports whether ghost-call findings should
// block task completion. Read from the environment on each call so
// operators can flip the policy mid-run without restarting.
func MCPStrictModeEnabled() bool {
	return r1env.Get("R1_MCP_STRICT", "STOKE_MCP_STRICT") == "1"
}

// excerptAround returns a window of `radius` runes centered on the
// match. Linebreaks inside the window are collapsed to spaces so log
// output stays single-line.
func excerptAround(s string, pos int, radius int) string {
	if pos < 0 || pos >= len(s) {
		return ""
	}
	lo := pos - radius
	if lo < 0 {
		lo = 0
	}
	hi := pos + radius
	if hi > len(s) {
		hi = len(s)
	}
	window := s[lo:hi]
	window = strings.ReplaceAll(window, "\n", " ")
	window = strings.ReplaceAll(window, "\r", " ")
	return strings.TrimSpace(window)
}
