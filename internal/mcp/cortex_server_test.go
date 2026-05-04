package mcp

import (
	"sort"
	"testing"
)

func TestCortexToolNames_FiveCanonicalNames(t *testing.T) {
	got := CortexToolNames()
	if len(got) != 5 {
		t.Fatalf("CortexToolNames returned %d names, want 5", len(got))
	}
	want := []string{
		"r1.cortex.lobe_pause",
		"r1.cortex.lobe_resume",
		"r1.cortex.lobes_list",
		"r1.cortex.notes",
		"r1.cortex.publish",
	}
	sortedGot := append([]string(nil), got...)
	sort.Strings(sortedGot)
	for i, name := range want {
		if sortedGot[i] != name {
			t.Errorf("CortexToolNames[%d] = %q, want %q", i, sortedGot[i], name)
		}
	}
}

// TestCortexToolNames_AllInCatalog verifies every cortex name returned by
// CortexToolNames is present in the wire catalog (R1ToolCatalog). Catches
// drift between the lint helper and the on-the-wire surface.
func TestCortexToolNames_AllInCatalog(t *testing.T) {
	catalog := map[string]bool{}
	for _, td := range R1ToolCatalog() {
		catalog[td.Name] = true
	}
	for _, name := range CortexToolNames() {
		if !catalog[name] {
			t.Errorf("cortex name %q advertised by CortexToolNames but missing from R1ToolCatalog", name)
		}
	}
}
