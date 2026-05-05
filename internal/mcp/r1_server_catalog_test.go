package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestR1ToolCatalog_HasExactly38Tools(t *testing.T) {
	cat := R1ToolCatalog()
	if got := len(cat); got != 38 {
		t.Fatalf("R1ToolCatalog() length = %d, want 38", got)
	}
}

func TestR1ToolCatalog_AllToolsHaveR1DotPrefix(t *testing.T) {
	for _, td := range R1ToolCatalog() {
		if !strings.HasPrefix(td.Name, "r1.") {
			t.Errorf("tool %q missing r1.* prefix", td.Name)
		}
	}
}

func TestR1ToolCatalog_AllSchemasValidJSON(t *testing.T) {
	for _, td := range R1ToolCatalog() {
		if !json.Valid(td.InputSchema) {
			t.Errorf("tool %q has invalid JSON inputSchema", td.Name)
		}
		// Sanity: every schema must declare an object type.
		var m map[string]any
		if err := json.Unmarshal(td.InputSchema, &m); err != nil {
			t.Errorf("tool %q schema unmarshal: %v", td.Name, err)
			continue
		}
		if m["type"] != "object" {
			t.Errorf("tool %q schema type=%v, want \"object\"", td.Name, m["type"])
		}
	}
}

func TestR1ToolCatalog_CategoryCounts(t *testing.T) {
	want := map[string]int{
		"r1.session.":   6,
		"r1.lanes.":     5,
		"r1.cortex.":    5,
		"r1.mission.":   4,
		"r1.worktree.":  4,
		"r1.bus.":       2,
		"r1.verify.":    3,
		"r1.tui.":       4,
		"r1.web.":       4,
		"r1.cli.":       1,
	}
	got := map[string]int{}
	for _, td := range R1ToolCatalog() {
		for prefix := range want {
			if strings.HasPrefix(td.Name, prefix) {
				got[prefix]++
			}
		}
	}
	for prefix, n := range want {
		if got[prefix] != n {
			t.Errorf("category %q: got %d tools, want %d", prefix, got[prefix], n)
		}
	}
}

func TestR1ToolCatalog_NoDuplicateNames(t *testing.T) {
	seen := map[string]bool{}
	for _, td := range R1ToolCatalog() {
		if seen[td.Name] {
			t.Errorf("duplicate tool name: %s", td.Name)
		}
		seen[td.Name] = true
	}
}

func TestR1ToolNames_ReturnsAll38(t *testing.T) {
	names := R1ToolNames()
	if got := len(names); got != 38 {
		t.Fatalf("R1ToolNames() length = %d, want 38", got)
	}
}
