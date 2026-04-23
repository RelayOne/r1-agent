package plan

import (
	"strings"
	"testing"
)

func TestDetectMCPGhostCalls_MatchedClaim(t *testing.T) {
	transcript := `I called mcp_linear_create_issue with the payload and got back a ticket.

<mcp_result server="linear" tool="create_issue" call_id="c-1">
{"id":"LIN-42","url":"..."}
</mcp_result>`
	findings := DetectMCPGhostCalls(transcript)
	if len(findings) != 0 {
		t.Errorf("matched claim+result should produce 0 findings, got %d: %+v", len(findings), findings)
	}
}

func TestDetectMCPGhostCalls_UnmatchedClaim(t *testing.T) {
	transcript := `I called mcp_linear_create_issue with the payload. The ticket was created successfully.`
	findings := DetectMCPGhostCalls(transcript)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	if findings[0].ClaimedTool != "mcp_linear_create_issue" {
		t.Errorf("ClaimedTool=%q", findings[0].ClaimedTool)
	}
	if !strings.Contains(findings[0].ClaimContext, "mcp_linear_create_issue") {
		t.Errorf("ClaimContext missing tool name: %q", findings[0].ClaimContext)
	}
}

func TestDetectMCPGhostCalls_MultipleClaimsOneMatched(t *testing.T) {
	transcript := `I invoked mcp_github_create_pr and then ran mcp_slack_post_message.

<mcp_result server="github" tool="create_pr" call_id="c-1">
{"url":"..."}
</mcp_result>`
	findings := DetectMCPGhostCalls(transcript)
	if len(findings) != 1 {
		t.Fatalf("expected 1 unmatched claim (slack), got %d", len(findings))
	}
	if findings[0].ClaimedTool != "mcp_slack_post_message" {
		t.Errorf("wrong tool flagged: %q", findings[0].ClaimedTool)
	}
}

func TestDetectMCPGhostCalls_DuplicateClaimsDeduped(t *testing.T) {
	transcript := `I called mcp_linear_create_issue three times; then I used mcp_linear_create_issue again to create a second ticket.`
	findings := DetectMCPGhostCalls(transcript)
	if len(findings) != 1 {
		t.Errorf("expected 1 finding (deduped), got %d: %+v", len(findings), findings)
	}
}

func TestDetectMCPGhostCalls_EmptyTranscript(t *testing.T) {
	if findings := DetectMCPGhostCalls(""); len(findings) != 0 {
		t.Errorf("empty transcript should produce no findings")
	}
}

func TestDetectMCPGhostCalls_NoMCPActivity(t *testing.T) {
	transcript := `I wrote the file, ran pnpm build, and it passed. All ACs green.`
	if findings := DetectMCPGhostCalls(transcript); len(findings) != 0 {
		t.Errorf("no MCP claims should produce no findings, got %d", len(findings))
	}
}

func TestDetectMCPGhostCalls_VariantVerbs(t *testing.T) {
	cases := []string{
		"I called mcp_foo_bar with args.",
		"I invoked mcp_foo_bar.",
		"Then I used the mcp_foo_bar tool.",
		"I executed mcp_foo_bar successfully.",
		"I issued mcp_foo_bar.",
	}
	for _, c := range cases {
		findings := DetectMCPGhostCalls(c)
		if len(findings) != 1 {
			t.Errorf("variant verb %q: expected 1 finding, got %d", c, len(findings))
		}
	}
}

func TestMCPStrictModeEnabled(t *testing.T) {
	t.Setenv("STOKE_MCP_STRICT", "1")
	if !MCPStrictModeEnabled() {
		t.Error("STOKE_MCP_STRICT=1 should enable strict mode")
	}
	t.Setenv("STOKE_MCP_STRICT", "")
	if MCPStrictModeEnabled() {
		t.Error("empty STOKE_MCP_STRICT should NOT enable strict mode")
	}
	t.Setenv("STOKE_MCP_STRICT", "0")
	if MCPStrictModeEnabled() {
		t.Error("STOKE_MCP_STRICT=0 should NOT enable strict mode")
	}
	t.Setenv("STOKE_MCP_STRICT", "true")
	if MCPStrictModeEnabled() {
		t.Error("only the literal \"1\" enables strict mode (matches spec wording)")
	}
}
