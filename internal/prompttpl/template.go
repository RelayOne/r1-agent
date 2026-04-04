// Package prompttpl implements a prompt template engine with variable substitution.
// Inspired by claw-code's system prompt construction and OmX's dynamic prompts:
//
// System prompts need to be dynamic but cache-friendly. This engine:
// - Separates static template from dynamic variables
// - Supports conditionals ({{#if var}}...{{/if}})
// - Supports iteration ({{#each items}}...{{/each}})
// - Supports defaults ({{var|default}})
// - Preserves prompt fingerprint stability for cache routing
//
// Templates are loaded once and rendered many times with different variables.
package prompttpl

import (
	"fmt"
	"regexp"
	"strings"
)

// Template is a parsed prompt template.
type Template struct {
	Name   string
	Source string
	nodes  []node
}

type nodeKind int

const (
	nodeText nodeKind = iota
	nodeVar
	nodeIf
	nodeEach
)

type node struct {
	kind     nodeKind
	text     string   // for nodeText
	varName  string   // for nodeVar
	defVal   string   // default value for nodeVar
	condVar  string   // condition variable for nodeIf
	body     []node   // children for nodeIf/nodeEach
	elseBody []node   // else branch for nodeIf
	itemVar  string   // iterator variable for nodeEach
	listVar  string   // list variable for nodeEach
}

// Vars is the variable map for rendering.
type Vars map[string]any

// Parse compiles a template string.
func Parse(name, source string) (*Template, error) {
	nodes, err := parse(source)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	return &Template{Name: name, Source: source, nodes: nodes}, nil
}

// MustParse parses and panics on error.
func MustParse(name, source string) *Template {
	t, err := Parse(name, source)
	if err != nil {
		panic(err)
	}
	return t
}

// Render executes the template with the given variables.
func (t *Template) Render(vars Vars) string {
	var b strings.Builder
	renderNodes(&b, t.nodes, vars)
	return b.String()
}

// Variables returns the variable names used in the template.
func (t *Template) Variables() []string {
	seen := make(map[string]bool)
	var names []string
	collectVars(t.nodes, seen, &names)
	return names
}

func collectVars(nodes []node, seen map[string]bool, names *[]string) {
	for _, n := range nodes {
		switch n.kind {
		case nodeVar:
			if !seen[n.varName] {
				seen[n.varName] = true
				*names = append(*names, n.varName)
			}
		case nodeIf:
			if !seen[n.condVar] {
				seen[n.condVar] = true
				*names = append(*names, n.condVar)
			}
			collectVars(n.body, seen, names)
			collectVars(n.elseBody, seen, names)
		case nodeEach:
			if !seen[n.listVar] {
				seen[n.listVar] = true
				*names = append(*names, n.listVar)
			}
			collectVars(n.body, seen, names)
		}
	}
}

// --- Parser ---

var (
	reVar     = regexp.MustCompile(`\{\{(\w+)(?:\|([^}]*))?\}\}`)
	reIfOpen  = regexp.MustCompile(`\{\{#if\s+(\w+)\}\}`)
	reElse    = regexp.MustCompile(`\{\{else\}\}`)
	reIfClose = regexp.MustCompile(`\{\{/if\}\}`)
	reEachOpen  = regexp.MustCompile(`\{\{#each\s+(\w+)\s+as\s+(\w+)\}\}`)
	reEachClose = regexp.MustCompile(`\{\{/each\}\}`)
)

func parse(source string) ([]node, error) {
	var nodes []node
	remaining := source

	for len(remaining) > 0 {
		// Find the next template tag
		idx := strings.Index(remaining, "{{")
		if idx == -1 {
			nodes = append(nodes, node{kind: nodeText, text: remaining})
			break
		}

		// Text before the tag
		if idx > 0 {
			nodes = append(nodes, node{kind: nodeText, text: remaining[:idx]})
			remaining = remaining[idx:]
		}

		// Try each pattern
		if m := reIfOpen.FindStringIndex(remaining); m != nil && m[0] == 0 {
			sm := reIfOpen.FindStringSubmatch(remaining)
			condVar := sm[1]
			remaining = remaining[m[1]:]

			// Find matching {{/if}}
			body, elseBody, rest, err := parseIfBlock(remaining)
			if err != nil {
				return nil, err
			}

			bodyNodes, err := parse(body)
			if err != nil {
				return nil, err
			}
			var elseNodes []node
			if elseBody != "" {
				elseNodes, err = parse(elseBody)
				if err != nil {
					return nil, err
				}
			}

			nodes = append(nodes, node{
				kind:     nodeIf,
				condVar:  condVar,
				body:     bodyNodes,
				elseBody: elseNodes,
			})
			remaining = rest

		} else if m := reEachOpen.FindStringIndex(remaining); m != nil && m[0] == 0 {
			sm := reEachOpen.FindStringSubmatch(remaining)
			listVar := sm[1]
			itemVar := sm[2]
			remaining = remaining[m[1]:]

			body, rest, err := parseBlock(remaining, "each")
			if err != nil {
				return nil, err
			}

			bodyNodes, err := parse(body)
			if err != nil {
				return nil, err
			}

			nodes = append(nodes, node{
				kind:    nodeEach,
				listVar: listVar,
				itemVar: itemVar,
				body:    bodyNodes,
			})
			remaining = rest

		} else if m := reVar.FindStringIndex(remaining); m != nil && m[0] == 0 {
			sm := reVar.FindStringSubmatch(remaining)
			n := node{kind: nodeVar, varName: sm[1]}
			if len(sm) > 2 {
				n.defVal = sm[2]
			}
			nodes = append(nodes, n)
			remaining = remaining[m[1]:]

		} else {
			// Unrecognized {{ - treat as text
			nodes = append(nodes, node{kind: nodeText, text: "{{"})
			remaining = remaining[2:]
		}
	}

	return nodes, nil
}

func parseIfBlock(s string) (body, elseBody, rest string, err error) {
	depth := 1
	i := 0
	elseIdx := -1

	for i < len(s) {
		tagStart := strings.Index(s[i:], "{{")
		if tagStart < 0 {
			break
		}
		pos := i + tagStart

		if strings.HasPrefix(s[pos:], "{{#if ") {
			depth++
			i = pos + 6
		} else if strings.HasPrefix(s[pos:], "{{/if}}") {
			depth--
			if depth == 0 {
				if elseIdx >= 0 {
					body = s[:elseIdx]
					elseChunk := s[elseIdx:pos]
					if em := reElse.FindStringIndex(elseChunk); em != nil {
						elseBody = elseChunk[em[1]:]
					}
				} else {
					body = s[:pos]
				}
				rest = s[pos+7:] // len("{{/if}}")
				return
			}
			i = pos + 7
		} else if depth == 1 && elseIdx < 0 && strings.HasPrefix(s[pos:], "{{else}}") {
			elseIdx = pos
			i = pos + 8
		} else {
			i = pos + 2
		}
	}

	return "", "", "", fmt.Errorf("unclosed {{#if}} block")
}

func parseBlock(s, tag string) (body, rest string, err error) {
	closeTag := fmt.Sprintf("{{/%s}}", tag)
	idx := strings.Index(s, closeTag)
	if idx < 0 {
		return "", "", fmt.Errorf("unclosed {{#%s}} block", tag)
	}
	return s[:idx], s[idx+len(closeTag):], nil
}

// --- Renderer ---

func renderNodes(b *strings.Builder, nodes []node, vars Vars) {
	for _, n := range nodes {
		switch n.kind {
		case nodeText:
			b.WriteString(n.text)

		case nodeVar:
			val, ok := vars[n.varName]
			if ok {
				b.WriteString(fmt.Sprint(val))
			} else if n.defVal != "" {
				b.WriteString(n.defVal)
			}

		case nodeIf:
			if truthy(vars[n.condVar]) {
				renderNodes(b, n.body, vars)
			} else {
				renderNodes(b, n.elseBody, vars)
			}

		case nodeEach:
			list, ok := vars[n.listVar]
			if !ok {
				continue
			}
			switch items := list.(type) {
			case []string:
				for _, item := range items {
					childVars := copyVars(vars)
					childVars[n.itemVar] = item
					renderNodes(b, n.body, childVars)
				}
			case []any:
				for _, item := range items {
					childVars := copyVars(vars)
					childVars[n.itemVar] = item
					renderNodes(b, n.body, childVars)
				}
			}
		}
	}
}

func truthy(val any) bool {
	if val == nil {
		return false
	}
	switch v := val.(type) {
	case bool:
		return v
	case string:
		return v != ""
	case int:
		return v != 0
	case float64:
		return v != 0
	case []string:
		return len(v) > 0
	case []any:
		return len(v) > 0
	default:
		return true
	}
}

func copyVars(vars Vars) Vars {
	cp := make(Vars, len(vars))
	for k, v := range vars {
		cp[k] = v
	}
	return cp
}
