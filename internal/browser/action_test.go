package browser

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestActionValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		a       Action
		wantErr string // empty → expect OK; non-empty → expect err whose Error() contains substr
	}{
		// happy
		{"navigate_ok", Action{Kind: ActionNavigate, URL: "https://example.com"}, ""},
		{"click_ok", Action{Kind: ActionClick, Selector: "#submit"}, ""},
		{"type_ok", Action{Kind: ActionType, Selector: "#q", Text: "hello"}, ""},
		{"wait_sel_ok", Action{Kind: ActionWaitForSelector, Selector: ".x"}, ""},
		{"wait_idle_ok", Action{Kind: ActionWaitForNetworkIdle}, ""},
		{"screenshot_ok", Action{Kind: ActionScreenshot}, ""},
		{"extract_text_ok", Action{Kind: ActionExtractText, Selector: "h1"}, ""},
		{"extract_attr_ok", Action{Kind: ActionExtractAttribute, Selector: "a", Attribute: "href"}, ""},

		// missing required
		{"navigate_no_url", Action{Kind: ActionNavigate}, "requires URL"},
		{"click_no_sel", Action{Kind: ActionClick}, "requires Selector"},
		{"type_no_sel", Action{Kind: ActionType, Text: "x"}, "requires Selector"},
		{"type_no_text", Action{Kind: ActionType, Selector: "#q"}, "requires Text"},
		{"wait_sel_no_sel", Action{Kind: ActionWaitForSelector}, "requires Selector"},
		{"extract_text_no_sel", Action{Kind: ActionExtractText}, "requires Selector"},
		{"extract_attr_no_sel", Action{Kind: ActionExtractAttribute, Attribute: "href"}, "requires Selector"},
		{"extract_attr_no_attr", Action{Kind: ActionExtractAttribute, Selector: "a"}, "requires Attribute"},

		// unknown
		{"unknown", Action{Kind: "bogus"}, "unknown kind"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.a.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want ok, got err: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want err containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want err containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestActionKindConstants(t *testing.T) {
	t.Parallel()
	// Ensures the string form matches the on-wire name used by
	// ParseActionFlag and BuildCriteria. Any rename must be a
	// conscious breaking change.
	want := map[ActionKind]string{
		ActionNavigate:           "navigate",
		ActionClick:              "click",
		ActionType:               "type",
		ActionWaitForSelector:    "wait_for_selector",
		ActionWaitForNetworkIdle: "wait_for_network_idle",
		ActionScreenshot:         "screenshot",
		ActionExtractText:        "extract_text",
		ActionExtractAttribute:   "extract_attribute",
	}
	for k, s := range want {
		if string(k) != s {
			t.Errorf("ActionKind(%q).String() = %q, want %q", k, string(k), s)
		}
	}
	if got := len(AllActionKinds()); got != 8 {
		t.Errorf("AllActionKinds() len = %d, want 8", got)
	}
}

func TestActionDefaultTimeout(t *testing.T) {
	t.Parallel()
	// explicit timeout wins
	if got := (Action{Kind: ActionClick, Timeout: 2 * time.Second}).DefaultTimeout(); got != 2*time.Second {
		t.Errorf("explicit timeout not honored: %v", got)
	}
	// navigate + wait_idle default to 30s
	if got := (Action{Kind: ActionNavigate}).DefaultTimeout(); got != 30*time.Second {
		t.Errorf("navigate default = %v, want 30s", got)
	}
	if got := (Action{Kind: ActionWaitForNetworkIdle}).DefaultTimeout(); got != 30*time.Second {
		t.Errorf("wait_idle default = %v, want 30s", got)
	}
	// others default to 10s
	if got := (Action{Kind: ActionClick}).DefaultTimeout(); got != 10*time.Second {
		t.Errorf("click default = %v, want 10s", got)
	}
	if got := (Action{Kind: ActionExtractText}).DefaultTimeout(); got != 10*time.Second {
		t.Errorf("extract default = %v, want 10s", got)
	}
}

func TestActionResultSummary(t *testing.T) {
	t.Parallel()
	ok := ActionResult{Kind: ActionClick, OK: true, DurationMs: 42}
	if s := ok.Summary(); !strings.Contains(s, "click") || !strings.Contains(s, "ok") || !strings.Contains(s, "42ms") {
		t.Errorf("ok summary bad: %q", s)
	}
	fail := ActionResult{Kind: ActionClick, OK: false, Err: errors.New("boom")}
	if s := fail.Summary(); !strings.Contains(s, "fail") || !strings.Contains(s, "boom") {
		t.Errorf("fail summary bad: %q", s)
	}
}
