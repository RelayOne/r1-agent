// Package dispatcher maps parsed feature steps onto r1.* MCP tool calls
// per spec 8 §6 (verbatim "Tool mapping" examples) and §12 item 17.
//
// The mapping has two layers:
//
//   1. Per-file Tool mapping block (from parser.Feature.ToolMapping):
//      explicit substring -> tool overrides supplied by the feature
//      author. These take priority.
//
//   2. Built-in heuristics (DefaultRules below): a regex/substring
//      table that recognizes common Gherkin phrasings and emits the
//      canonical r1.* tool name.
//
// Lookup is deterministic: the first matching rule (in declaration
// order) wins. Authors who hit a false positive supply an override in
// the feature's Tool mapping block.
package dispatcher

import (
	"regexp"
	"strings"
)

// Rule is one entry in the heuristic table. Pattern is a regexp; the
// first capture group (if any) is preserved as a hint for the
// dispatcher (e.g. selector text). Tool is the r1.* canonical name.
type Rule struct {
	Pattern *regexp.Regexp
	Tool    string
	Reason  string // human-readable doc shown in --explain mode
}

// DefaultRules is the built-in heuristic table per spec §6 examples.
// Order matters: the first match wins.
var DefaultRules = []Rule{
	// Web (Playwright MCP wrappers).
	{regexp.MustCompile(`(?i)\b(loaded at|navigate to|web UI is loaded at)\b`), "r1.web.navigate",
		"web URL navigation"},
	{regexp.MustCompile(`(?i)\bfill the textbox\b`), "r1.web.fill",
		"web form fill"},
	{regexp.MustCompile(`(?i)\bclick the (button|link|checkbox)\b`), "r1.web.click",
		"web click"},
	{regexp.MustCompile(`(?i)\b(web snapshot|web a11y tree|chat log contains|the UI snapshot)\b`), "r1.web.snapshot",
		"web a11y snapshot"},

	// Cortex (Workspace pane, Notes, Lobes).
	{regexp.MustCompile(`(?i)\bcortex Workspace\b`), "r1.cortex.notes",
		"cortex workspace inspection"},
	{regexp.MustCompile(`(?i)\bcortex.publish\b`), "r1.cortex.publish",
		"cortex note publish"},
	{regexp.MustCompile(`(?i)\bLobes? (status|list)\b`), "r1.cortex.lobes_list",
		"cortex lobe inventory"},

	// Lanes.
	{regexp.MustCompile(`(?i)\b(lane(s)? (status|list)|no lane has status)\b`), "r1.lanes.list",
		"lane status snapshot"},
	{regexp.MustCompile(`(?i)\bkill lane\b`), "r1.lanes.kill",
		"lane termination"},
	{regexp.MustCompile(`(?i)\bpin (lane|note)\b`), "r1.lanes.pin",
		"lane spotlight pin"},

	// Sessions.
	{regexp.MustCompile(`(?i)\bsession (is started|with id)\b`), "r1.session.start",
		"session lifecycle"},
	{regexp.MustCompile(`(?i)\bsend (the )?message\b`), "r1.session.send",
		"session user input"},
	{regexp.MustCompile(`(?i)\bcancel the (turn|session)\b`), "r1.session.cancel",
		"session in-flight cancel"},

	// TUI.
	{regexp.MustCompile(`(?i)\b(press the )?key "[^"]+"\b`), "r1.tui.press_key",
		"TUI keypress"},
	{regexp.MustCompile(`(?i)\bTUI snapshot\b`), "r1.tui.snapshot",
		"TUI a11y snapshot"},
	{regexp.MustCompile(`(?i)\bTUI model field\b`), "r1.tui.get_model",
		"TUI model introspection"},
	{regexp.MustCompile(`(?i)\bfocus (the )?lane\b`), "r1.tui.focus_lane",
		"TUI lane focus"},

	// Mission, worktree, bus, verify, cli.
	{regexp.MustCompile(`(?i)\bmission (is created|create|exists)\b`), "r1.mission.create",
		"mission lifecycle"},
	{regexp.MustCompile(`(?i)\bcancel (the )?mission\b`), "r1.mission.cancel",
		"mission cancel"},
	{regexp.MustCompile(`(?i)\bworktree diff\b`), "r1.worktree.diff",
		"worktree diff inspection"},
	{regexp.MustCompile(`(?i)\bworktree merge\b`), "r1.worktree.merge",
		"worktree merge"},
	{regexp.MustCompile(`(?i)\bbus tail\b`), "r1.bus.tail",
		"bus event stream"},
	{regexp.MustCompile(`(?i)\bbus replay\b`), "r1.bus.replay",
		"bus deterministic replay"},
	{regexp.MustCompile(`(?i)\bverify (the )?(build|compile)\b`), "r1.verify.build",
		"build verification"},
	{regexp.MustCompile(`(?i)\bverify (the )?tests?\b`), "r1.verify.test",
		"test verification"},
	{regexp.MustCompile(`(?i)\bverify (the )?lint\b`), "r1.verify.lint",
		"lint verification"},
	{regexp.MustCompile(`(?i)\br1 cli (invoke|run|exec)\b`), "r1.cli.invoke",
		"CLI invocation"},
}

// Match returns the canonical r1.* tool name for stepText, taking into
// account the feature's per-file overrides. Returns ("", false) when
// no rule matches.
func Match(stepText string, perFileOverrides map[string]string) (tool string, ok bool) {
	// 1. Per-file overrides win. The override pattern is a substring;
	// the first match (in map iteration order — undefined but stable
	// per Go's map randomization) is returned. Authors who need
	// determinism should make their patterns mutually exclusive.
	for pattern, t := range perFileOverrides {
		if pattern == "" {
			continue
		}
		if strings.Contains(stepText, pattern) {
			return t, true
		}
	}
	// 2. Built-in heuristics.
	for _, rule := range DefaultRules {
		if rule.Pattern.MatchString(stepText) {
			return rule.Tool, true
		}
	}
	return "", false
}

// Explain returns the rule that matched (for diagnostics) plus the
// resolved tool name. When no rule matches, the returned Rule is the
// zero value and ok=false.
func Explain(stepText string, perFileOverrides map[string]string) (rule Rule, tool string, ok bool) {
	for pattern, t := range perFileOverrides {
		if pattern == "" {
			continue
		}
		if strings.Contains(stepText, pattern) {
			return Rule{Reason: "per-file override (" + pattern + ")"}, t, true
		}
	}
	for _, r := range DefaultRules {
		if r.Pattern.MatchString(stepText) {
			return r, r.Tool, true
		}
	}
	return Rule{}, "", false
}
