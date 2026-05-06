package mcp

import (
	"sort"
	"testing"
)

func TestTUIToolNames_FourCanonicalNames(t *testing.T) {
	got := TUIToolNames()
	if len(got) != 4 {
		t.Fatalf("TUIToolNames returned %d names, want 4", len(got))
	}
	want := []string{
		"r1.tui.focus_lane",
		"r1.tui.get_model",
		"r1.tui.press_key",
		"r1.tui.snapshot",
	}
	sortedGot := append([]string(nil), got...)
	sort.Strings(sortedGot)
	for i, name := range want {
		if sortedGot[i] != name {
			t.Errorf("TUIToolNames[%d] = %q, want %q", i, sortedGot[i], name)
		}
	}
}

func TestTUIToolNames_AllInCatalog(t *testing.T) {
	catalog := map[string]bool{}
	for _, td := range R1ToolCatalog() {
		catalog[td.Name] = true
	}
	for _, name := range TUIToolNames() {
		if !catalog[name] {
			t.Errorf("TUI name %q advertised by TUIToolNames but missing from R1ToolCatalog", name)
		}
	}
}
