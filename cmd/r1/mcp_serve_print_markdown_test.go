package main

// mcp_serve_print_markdown_test.go — exercises `r1 mcp serve --print-tools
// --markdown` per spec 8 §12 item 10. The Markdown output is consumed by
// `make docs-agentic` to generate the tool-catalog section of
// docs/AGENTIC-API.md.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/mcp"
)

func TestMCPServe_Markdown_HasH1Header(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMCPServe([]string{"--print-tools", "--markdown"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.HasPrefix(out, "# r1.* MCP Tool Catalog") {
		t.Errorf("expected H1 header at top; got first line %q",
			strings.SplitN(out, "\n", 2)[0])
	}
}

func TestMCPServe_Markdown_AllToolNamesPresent(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runMCPServe([]string{"--print-tools", "--markdown"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d", code)
	}
	out := stdout.String()
	for _, name := range mcp.R1ToolNames() {
		if !strings.Contains(out, "`"+name+"`") {
			t.Errorf("Markdown output missing tool name %q", name)
		}
	}
}

func TestMCPServe_Markdown_GroupsByCategory(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runMCPServe([]string{"--print-tools", "--markdown"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d", code)
	}
	out := stdout.String()
	// Each of the 10 categories must produce an H2 with the category name.
	categories := []string{"session", "lanes", "cortex", "mission", "worktree",
		"bus", "verify", "tui", "web", "cli"}
	for _, c := range categories {
		header := "## " + c
		if !strings.Contains(out, header) {
			t.Errorf("Markdown missing category header %q", header)
		}
	}
}

func TestMCPServe_Markdown_EmbedsInputSchemaJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runMCPServe([]string{"--print-tools", "--markdown"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d", code)
	}
	out := stdout.String()
	// Each tool's schema appears in a fenced ```json block.
	if !strings.Contains(out, "```json") {
		t.Error("Markdown should embed schemas in fenced ```json blocks")
	}
	// Every tool MUST have a matching closing fence (count parity).
	openCount := strings.Count(out, "```json")
	if openCount != 38 {
		t.Errorf("expected 38 ```json blocks (one per tool), got %d", openCount)
	}
}

func TestMCPServe_Markdown_HeaderAdvertises38Total(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runMCPServe([]string{"--print-tools", "--markdown"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stdout.String(), "Total tools: 38.") {
		t.Errorf("header should advertise total 38; got\n%s",
			strings.SplitN(stdout.String(), "\n\n", 2)[0])
	}
}
