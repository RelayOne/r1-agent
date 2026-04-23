package browser

import (
	"strings"
	"testing"
	"time"
)

func TestParseActionFlag(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want Action
		err  string // substr; empty → want ok
	}{
		// happy
		{"navigate", "navigate:https://example.com",
			Action{Kind: ActionNavigate, URL: "https://example.com"}, ""},
		{"click", "click:#submit",
			Action{Kind: ActionClick, Selector: "#submit"}, ""},
		{"type_simple", "type:#q:hello",
			Action{Kind: ActionType, Selector: "#q", Text: "hello"}, ""},
		{"type_text_with_colons", "type:#url:https://x.com/a:b",
			Action{Kind: ActionType, Selector: "#url", Text: "https://x.com/a:b"}, ""},
		{"wait_no_timeout", "wait:.result",
			Action{Kind: ActionWaitForSelector, Selector: ".result"}, ""},
		{"wait_with_timeout", "wait:.result:5s",
			Action{Kind: ActionWaitForSelector, Selector: ".result", Timeout: 5 * time.Second}, ""},
		{"wait_idle_bare", "wait_idle",
			Action{Kind: ActionWaitForNetworkIdle}, ""},
		{"wait_idle_timeout", "wait_idle:3s",
			Action{Kind: ActionWaitForNetworkIdle, Timeout: 3 * time.Second}, ""},
		{"screenshot_bare", "screenshot",
			Action{Kind: ActionScreenshot}, ""},
		{"screenshot_out", "screenshot:out.png",
			Action{Kind: ActionScreenshot, OutputPath: "out.png"}, ""},
		{"extract", "extract:h1",
			Action{Kind: ActionExtractText, Selector: "h1"}, ""},
		{"extract_attr", "extract_attr:a:href",
			Action{Kind: ActionExtractAttribute, Selector: "a", Attribute: "href"}, ""},

		// malformed
		{"empty", "", Action{}, "empty input"},
		{"unknown_prefix", "boop:x", Action{}, "unknown prefix"},
		{"navigate_no_url", "navigate:", Action{}, "navigate requires URL"},
		{"navigate_bare", "navigate", Action{}, "navigate requires URL"},
		{"click_bare", "click", Action{}, "click requires SELECTOR"},
		{"click_empty", "click:", Action{}, "click requires SELECTOR"},
		{"type_missing_text", "type:#q", Action{}, "type requires SELECTOR:TEXT"},
		{"type_missing_all", "type", Action{}, "type requires SELECTOR:TEXT"},
		{"type_empty_text", "type:#q:", Action{}, "type requires SELECTOR:TEXT"},
		{"wait_bare", "wait", Action{}, "wait requires SELECTOR"},
		{"wait_empty", "wait:", Action{}, "wait requires SELECTOR"},
		{"wait_bad_timeout", "wait:.x:notaduration", Action{}, "bad timeout"},
		{"wait_idle_bad_timeout", "wait_idle:notaduration", Action{}, "bad timeout"},
		{"extract_bare", "extract", Action{}, "extract requires SELECTOR"},
		{"extract_empty", "extract:", Action{}, "extract requires SELECTOR"},
		{"extract_attr_missing", "extract_attr:a", Action{}, "extract_attr requires SELECTOR:ATTR"},
		{"extract_attr_bare", "extract_attr", Action{}, "extract_attr requires SELECTOR:ATTR"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseActionFlag(tc.in)
			if tc.err != "" {
				if err == nil {
					t.Fatalf("want err containing %q, got ok (%+v)", tc.err, got)
				}
				if !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want err containing %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got.Kind != tc.want.Kind ||
				got.URL != tc.want.URL ||
				got.Selector != tc.want.Selector ||
				got.Text != tc.want.Text ||
				got.Attribute != tc.want.Attribute ||
				got.OutputPath != tc.want.OutputPath ||
				got.Timeout != tc.want.Timeout {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
			// Every parsed action must also pass Validate() — the
			// parser should never produce invalid output for valid
			// input.
			if err := got.Validate(); err != nil {
				t.Errorf("parsed action fails Validate: %v", err)
			}
		})
	}
}

func TestParseActionFlags_OrderPreserved(t *testing.T) {
	t.Parallel()
	in := []string{
		"navigate:https://example.com",
		"click:#login",
		"type:#u:alice",
		"wait:.dash:5s",
		"screenshot:/tmp/x.png",
		"extract:h1",
	}
	got, err := ParseActionFlags(in)
	if err != nil {
		t.Fatalf("ParseActionFlags: %v", err)
	}
	if len(got) != len(in) {
		t.Fatalf("len=%d want %d", len(got), len(in))
	}
	wantKinds := []ActionKind{
		ActionNavigate, ActionClick, ActionType,
		ActionWaitForSelector, ActionScreenshot, ActionExtractText,
	}
	for i, k := range wantKinds {
		if got[i].Kind != k {
			t.Errorf("[%d] kind=%q want %q", i, got[i].Kind, k)
		}
	}
}

func TestParseActionFlags_EmptyAndError(t *testing.T) {
	t.Parallel()
	if r, err := ParseActionFlags(nil); r != nil || err != nil {
		t.Errorf("nil in → (%v, %v)", r, err)
	}
	_, err := ParseActionFlags([]string{"click:#ok", "bogus:x"})
	if err == nil || !strings.Contains(err.Error(), "--action[1]") {
		t.Errorf("want index-annotated error, got %v", err)
	}
}
