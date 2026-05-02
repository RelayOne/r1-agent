// action_flag.go: --action CLI flag parser.
//
// Implements spec 17 §5/§32: parseActionFlag takes one CLI string
// like "click:#submit" or "type:#username:alice" and returns a
// browser.Action. Reserved for `stoke browse` subcommand wiring in
// cmd/r1/main.go. Lives in the browser package so the format
// rules are colocated with the types they produce.

package browser

import (
	"fmt"
	"strings"
	"time"
)

// ParseActionFlag parses one --action CLI argument into an Action.
//
// Formats (spec 17 §5):
//
//	navigate:URL
//	click:SELECTOR
//	type:SELECTOR:TEXT              (TEXT may contain ':' — split on first 2)
//	wait:SELECTOR[:TIMEOUT]         (TIMEOUT is a Go duration, default 10s)
//	wait_idle[:TIMEOUT]
//	screenshot[:OUT.png]
//	extract:SELECTOR
//	extract_attr:SELECTOR:ATTR
//
// Unknown prefixes return an error. Malformed payloads (missing
// required segments, bad duration) also error with a descriptive
// message that names the offending segment.
func ParseActionFlag(s string) (Action, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Action{}, fmt.Errorf("parse action: empty input")
	}
	// Prefix is everything up to the first ':'; use the rest as the
	// payload. For no-payload prefixes (wait_idle, screenshot) the
	// colon is optional.
	prefix, payload, hasPayload := splitFirst(s, ':')

	switch prefix {
	case "navigate":
		if !hasPayload || payload == "" {
			return Action{}, fmt.Errorf("parse action %q: navigate requires URL", s)
		}
		return Action{Kind: ActionNavigate, URL: payload}, nil

	case "click":
		if !hasPayload || payload == "" {
			return Action{}, fmt.Errorf("parse action %q: click requires SELECTOR", s)
		}
		return Action{Kind: ActionClick, Selector: payload}, nil

	case "type":
		if !hasPayload {
			return Action{}, fmt.Errorf("parse action %q: type requires SELECTOR:TEXT", s)
		}
		sel, text, ok := splitFirst(payload, ':')
		if !ok || sel == "" || text == "" {
			return Action{}, fmt.Errorf("parse action %q: type requires SELECTOR:TEXT", s)
		}
		return Action{Kind: ActionType, Selector: sel, Text: text}, nil

	case "wait":
		if !hasPayload || payload == "" {
			return Action{}, fmt.Errorf("parse action %q: wait requires SELECTOR[:TIMEOUT]", s)
		}
		sel, tail, hasTail := splitFirst(payload, ':')
		if sel == "" {
			return Action{}, fmt.Errorf("parse action %q: wait requires SELECTOR[:TIMEOUT]", s)
		}
		a := Action{Kind: ActionWaitForSelector, Selector: sel}
		if hasTail {
			d, err := time.ParseDuration(tail)
			if err != nil {
				return Action{}, fmt.Errorf("parse action %q: bad timeout %q: %w", s, tail, err)
			}
			a.Timeout = d
		}
		return a, nil

	case "wait_idle":
		a := Action{Kind: ActionWaitForNetworkIdle}
		if hasPayload && payload != "" {
			d, err := time.ParseDuration(payload)
			if err != nil {
				return Action{}, fmt.Errorf("parse action %q: bad timeout %q: %w", s, payload, err)
			}
			a.Timeout = d
		}
		return a, nil

	case "screenshot":
		a := Action{Kind: ActionScreenshot}
		if hasPayload && payload != "" {
			a.OutputPath = payload
		}
		return a, nil

	case "extract":
		if !hasPayload || payload == "" {
			return Action{}, fmt.Errorf("parse action %q: extract requires SELECTOR", s)
		}
		return Action{Kind: ActionExtractText, Selector: payload}, nil

	case "extract_attr":
		if !hasPayload {
			return Action{}, fmt.Errorf("parse action %q: extract_attr requires SELECTOR:ATTR", s)
		}
		sel, attr, ok := splitFirst(payload, ':')
		if !ok || sel == "" || attr == "" {
			return Action{}, fmt.Errorf("parse action %q: extract_attr requires SELECTOR:ATTR", s)
		}
		return Action{Kind: ActionExtractAttribute, Selector: sel, Attribute: attr}, nil

	default:
		return Action{}, fmt.Errorf("parse action %q: unknown prefix %q "+
			"(want one of: navigate, click, type, wait, wait_idle, screenshot, extract, extract_attr)",
			s, prefix)
	}
}

// ParseActionFlags parses a slice of --action arguments, preserving
// argv order. Returns the first parse error encountered. Empty
// input returns (nil, nil).
func ParseActionFlags(ss []string) ([]Action, error) {
	if len(ss) == 0 {
		return nil, nil
	}
	out := make([]Action, 0, len(ss))
	for i, s := range ss {
		a, err := ParseActionFlag(s)
		if err != nil {
			return nil, fmt.Errorf("--action[%d]: %w", i, err)
		}
		out = append(out, a)
	}
	return out, nil
}

// splitFirst splits s at the first occurrence of sep. Returns
// (head, tail, true) when sep is present, else (s, "", false). Used
// for prefix parsing — doesn't allocate for the no-sep case.
func splitFirst(s string, sep byte) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
