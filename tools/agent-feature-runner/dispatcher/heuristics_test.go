package dispatcher

import "testing"

func TestMatch_WebHeuristics(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{`the web UI is loaded at "/"`, "r1.web.navigate"},
		{`I navigate to "/"`, "r1.web.navigate"},
		{`I fill the textbox with name "Message" with "ping"`, "r1.web.fill"},
		{`I click the button with name "Send"`, "r1.web.click"},
		{`the chat log contains an assistant message matching "pong"`, "r1.web.snapshot"},
	}
	for _, tc := range cases {
		got, ok := Match(tc.text, nil)
		if !ok {
			t.Errorf("no match for %q", tc.text)
			continue
		}
		if got != tc.want {
			t.Errorf("Match(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestMatch_LaneAndCortexHeuristics(t *testing.T) {
	cases := map[string]string{
		`no lane has status "errored"`:                "r1.lanes.list",
		`I click the button with name "Kill lane x"`: "r1.web.click", // web wins because click is more specific
		`the cortex Workspace contains a Note`:       "r1.cortex.notes",
		`pin lane memory-curator`:                    "r1.lanes.pin",
	}
	for text, want := range cases {
		got, ok := Match(text, nil)
		if !ok {
			t.Errorf("no match for %q", text)
			continue
		}
		if got != want {
			t.Errorf("Match(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestMatch_PerFileOverrideWins(t *testing.T) {
	overrides := map[string]string{
		"loaded at": "r1.web.navigate", // matches both override and built-in
	}
	got, ok := Match("the web UI is loaded at /", overrides)
	if !ok {
		t.Fatal("override should match")
	}
	if got != "r1.web.navigate" {
		t.Errorf("got %q, want r1.web.navigate", got)
	}
}

func TestMatch_PerFileOverrideAdoptsCustomTool(t *testing.T) {
	overrides := map[string]string{
		"super custom phrase": "r1.cli.invoke",
	}
	got, ok := Match("a super custom phrase here", overrides)
	if !ok {
		t.Fatal("override should match")
	}
	if got != "r1.cli.invoke" {
		t.Errorf("override should adopt the custom tool: got %q", got)
	}
}

func TestMatch_NoMatchReturnsFalse(t *testing.T) {
	_, ok := Match("a sentence completely unrelated to any tool", nil)
	if ok {
		t.Error("unrelated text should NOT match any rule")
	}
}

func TestExplain_ReportsReason(t *testing.T) {
	_, tool, ok := Explain("I fill the textbox with name X", nil)
	if !ok {
		t.Fatal("expected match")
	}
	if tool != "r1.web.fill" {
		t.Errorf("tool = %q, want r1.web.fill", tool)
	}
}

func TestExplain_OverridePathReason(t *testing.T) {
	overrides := map[string]string{"the magic phrase": "r1.cli.invoke"}
	rule, tool, ok := Explain("the magic phrase appears", overrides)
	if !ok {
		t.Fatal("override should match")
	}
	if tool != "r1.cli.invoke" {
		t.Errorf("tool = %q", tool)
	}
	if rule.Reason == "" {
		t.Error("override path should set Reason")
	}
}

func TestDefaultRules_AllToolsAreR1Prefixed(t *testing.T) {
	for _, r := range DefaultRules {
		if len(r.Tool) < 3 || r.Tool[:3] != "r1." {
			t.Errorf("rule %q maps to non-r1.* tool %q", r.Pattern.String(), r.Tool)
		}
	}
}

func TestDefaultRules_NoEmptyPatterns(t *testing.T) {
	for i, r := range DefaultRules {
		if r.Pattern == nil {
			t.Errorf("rule %d has nil Pattern", i)
		}
	}
}
