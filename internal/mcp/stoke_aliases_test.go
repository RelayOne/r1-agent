package mcp

import (
	"strings"
	"testing"
)

// TestStokeR1Aliases_BothNamesAdvertised checks that ToolDefinitions emits
// every legacy stoke_* tool under its canonical r1_* name AND retains the
// legacy name. Per spec item 3 (tests asserting both names dispatch).
func TestStokeR1Aliases_BothNamesAdvertised(t *testing.T) {
	s := NewStokeServer("")
	defs := s.ToolDefinitions()

	stokeNames := map[string]bool{}
	r1Names := map[string]bool{}
	for _, d := range defs {
		switch {
		case strings.HasPrefix(d.Name, "stoke_"):
			stokeNames[d.Name] = true
		case strings.HasPrefix(d.Name, "r1_"):
			r1Names[d.Name] = true
		}
	}

	if len(stokeNames) == 0 {
		t.Fatal("no stoke_* tools advertised — legacy alias missing")
	}
	if len(r1Names) == 0 {
		t.Fatal("no r1_* tools advertised — canonical alias missing")
	}

	// For every stoke_X there MUST be an r1_X.
	for name := range stokeNames {
		want := canonicalStokeServerToolName(name)
		if !r1Names[want] {
			t.Errorf("stoke alias %q missing canonical r1 form %q", name, want)
		}
	}
	// For every r1_X (in this server) there MUST be a stoke_X.
	for name := range r1Names {
		want := legacyStokeServerToolName(name)
		if !stokeNames[want] {
			t.Errorf("r1 alias %q missing legacy stoke form %q", name, want)
		}
	}
}

// TestStokeR1Aliases_DispatchSameHandler verifies that calling a tool by its
// stoke_* name and by its r1_* name reaches the same handler. The handler
// observation is "produces the same kind of error / output for an empty
// payload" — both should reject the missing repo_root the same way.
func TestStokeR1Aliases_DispatchSameHandler(t *testing.T) {
	s := NewStokeServer("")

	pairs := []struct{ stokeName, r1Name string }{
		{"stoke_build_from_sow", "r1_build_from_sow"},
		{"stoke_get_mission_status", "r1_get_mission_status"},
		{"stoke_get_mission_logs", "r1_get_mission_logs"},
		{"stoke_cancel_mission", "r1_cancel_mission"},
		{"stoke_list_missions", "r1_list_missions"},
	}
	for _, p := range pairs {
		_, err1 := s.HandleToolCall(p.stokeName, map[string]interface{}{})
		_, err2 := s.HandleToolCall(p.r1Name, map[string]interface{}{})
		// Both should produce identical error strings (handler is shared).
		errStr1 := ""
		errStr2 := ""
		if err1 != nil {
			errStr1 = err1.Error()
		}
		if err2 != nil {
			errStr2 = err2.Error()
		}
		if errStr1 != errStr2 {
			t.Errorf("dispatch divergence for %s vs %s:\n  stoke: %q\n  r1:    %q",
				p.stokeName, p.r1Name, errStr1, errStr2)
		}
	}
}

// TestCanonicalAndLegacyRoundTrip ensures the prefix translators are inverse.
func TestCanonicalAndLegacyRoundTrip(t *testing.T) {
	cases := []string{
		"stoke_build_from_sow",
		"stoke_get_mission_status",
		"stoke_anything",
	}
	for _, legacy := range cases {
		r1 := canonicalStokeServerToolName(legacy)
		if !strings.HasPrefix(r1, "r1_") {
			t.Errorf("canonical(%q) = %q, want r1_* prefix", legacy, r1)
		}
		back := legacyStokeServerToolName(r1)
		if back != legacy {
			t.Errorf("round-trip mismatch: %q → %q → %q", legacy, r1, back)
		}
	}
	// Pass-through for non-prefixed names.
	if got := canonicalStokeServerToolName("foo"); got != "foo" {
		t.Errorf("canonical(\"foo\") = %q, want \"foo\"", got)
	}
	if got := legacyStokeServerToolName("foo"); got != "foo" {
		t.Errorf("legacy(\"foo\") = %q, want \"foo\"", got)
	}
}
