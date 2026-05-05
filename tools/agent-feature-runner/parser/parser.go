// Package parser parses *.agent.feature.md files into a Feature struct
// per spec 8 §6 (verbatim format) + §12 item 16 (parser/dispatcher
// split).
//
// File shape (Markdown subset):
//
//   # tests/agent/web/foo.agent.feature.md
//
//   <!-- TAGS: smoke, web, chat -->
//   <!-- DEPENDS: r1d-server, web-chat-ui -->
//
//   ## Scenario: User sends a message and sees a streamed response
//
//   - Given a fresh r1d daemon at "http://127.0.0.1:3948"
//   - When I fill the textbox with name "Message" with "ping"
//   - Then within 5 seconds the chat log contains an assistant message
//
//   ## Tool mapping (informative, runner derives automatically)
//   - "loaded at" -> r1.web.navigate
//   - "fill the textbox" -> r1.web.fill
//
// The parser is deliberately permissive: any markdown structure that
// is not a Scenario/Tool-mapping H2 is preserved as a comment in the
// Feature.Header. The runner does not require any specific text
// outside the recognized blocks.
package parser

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Feature is one parsed *.agent.feature.md file.
type Feature struct {
	Path        string
	Title       string // H1 line (file name typically)
	Tags        []string
	Depends     []string
	Header      string // raw text before the first ## Scenario:
	Scenarios   []Scenario
	ToolMapping map[string]string // pattern -> tool name (informative; runner derives heuristically)
}

// Scenario captures one ## Scenario block.
type Scenario struct {
	Name   string
	Tags   []string  // scenario-local tags (separate from file tags)
	Steps  []Step
	Source string    // raw block (for failure reporting)
}

// Step is one Given/When/Then/And entry.
type Step struct {
	Keyword string // "Given", "When", "Then", "And"
	Text    string // the step text minus the leading keyword
	Line    int    // 1-based line number in the source file
}

// ParseFile reads path and returns the parsed Feature.
func ParseFile(path string) (*Feature, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	feat, err := Parse(f, path)
	if err != nil {
		return nil, err
	}
	return feat, nil
}

// Parse reads the feature from r. The path is used only for the
// returned Feature.Path field (for failure messages).
func Parse(r interface {
	Read(p []byte) (int, error)
}, path string) (*Feature, error) {
	feat := &Feature{Path: path, ToolMapping: map[string]string{}}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		curScenario *Scenario
		section     = "header" // "header" | "scenario" | "tool_mapping"
		lineNo      int
	)
	flushScenario := func() {
		if curScenario != nil {
			feat.Scenarios = append(feat.Scenarios, *curScenario)
			curScenario = nil
		}
	}

	for scanner.Scan() {
		lineNo++
		raw := scanner.Text()
		line := strings.TrimSpace(raw)

		// H1 title (#).
		if strings.HasPrefix(line, "# ") {
			if feat.Title == "" {
				feat.Title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			}
			feat.Header += raw + "\n"
			continue
		}

		// Tag and Depends comments.
		if strings.HasPrefix(line, "<!-- TAGS:") {
			feat.Tags = appendTags(feat.Tags, extractCommentList(line, "TAGS:"))
			continue
		}
		if strings.HasPrefix(line, "<!-- DEPENDS:") {
			feat.Depends = appendTags(feat.Depends, extractCommentList(line, "DEPENDS:"))
			continue
		}

		// Section transitions.
		if strings.HasPrefix(line, "## Scenario:") {
			flushScenario()
			name := strings.TrimSpace(strings.TrimPrefix(line, "## Scenario:"))
			curScenario = &Scenario{Name: name}
			section = "scenario"
			continue
		}
		if strings.HasPrefix(line, "## Tool mapping") || strings.HasPrefix(line, "## Negative case") {
			flushScenario()
			section = "tool_mapping"
			continue
		}
		if strings.HasPrefix(line, "## ") {
			// Any other H2 ends the current scenario.
			flushScenario()
			section = "other"
			continue
		}

		switch section {
		case "scenario":
			if step, ok := parseStep(line, lineNo); ok {
				curScenario.Steps = append(curScenario.Steps, step)
				curScenario.Source += raw + "\n"
			}
		case "tool_mapping":
			if pattern, tool, ok := parseToolMapping(line); ok {
				feat.ToolMapping[pattern] = tool
			}
		case "header":
			feat.Header += raw + "\n"
		}
	}
	flushScenario()
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	return feat, nil
}

// parseStep recognizes "- Given ...", "- When ...", "- Then ...", "- And ...".
// Returns ok=false for any other line shape.
func parseStep(line string, lineNo int) (Step, bool) {
	if !strings.HasPrefix(line, "- ") {
		return Step{}, false
	}
	rest := strings.TrimPrefix(line, "- ")
	for _, kw := range []string{"Given", "When", "Then", "And"} {
		if strings.HasPrefix(rest, kw+" ") {
			return Step{
				Keyword: kw,
				Text:    strings.TrimSpace(strings.TrimPrefix(rest, kw+" ")),
				Line:    lineNo,
			}, true
		}
	}
	return Step{}, false
}

// parseToolMapping recognizes lines like:
//
//   - "loaded at" -> r1.web.navigate
//   - "fill the textbox" -> r1.web.fill
//   - "loaded at" → r1.web.navigate     (also accepts the unicode arrow)
func parseToolMapping(line string) (pattern, tool string, ok bool) {
	if !strings.HasPrefix(line, "- ") {
		return "", "", false
	}
	rest := strings.TrimPrefix(line, "- ")
	// Both → and -> are accepted.
	for _, sep := range []string{" → ", " -> "} {
		if i := strings.Index(rest, sep); i > 0 {
			pattern = strings.Trim(rest[:i], "\" ")
			tool = strings.TrimSpace(rest[i+len(sep):])
			if pattern != "" && tool != "" {
				return pattern, tool, true
			}
		}
	}
	return "", "", false
}

// extractCommentList parses "<!-- TAGS: a, b, c -->" -> ["a","b","c"].
func extractCommentList(line, prefix string) []string {
	idx := strings.Index(line, prefix)
	if idx < 0 {
		return nil
	}
	body := line[idx+len(prefix):]
	body = strings.TrimSpace(strings.TrimSuffix(body, "-->"))
	parts := strings.Split(body, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func appendTags(existing, more []string) []string {
	seen := map[string]bool{}
	out := append([]string(nil), existing...)
	for _, e := range existing {
		seen[e] = true
	}
	for _, m := range more {
		if !seen[m] {
			out = append(out, m)
			seen[m] = true
		}
	}
	return out
}
