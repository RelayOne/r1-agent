package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Policy struct {
	Phases       map[string]PhasePolicy `json:"phases"`
	Files        FilesPolicy            `json:"files"`
	Verification VerificationPolicy     `json:"verification"`
}

type PhasePolicy struct {
	BuiltinTools []string `json:"builtin_tools"`
	DeniedRules  []string `json:"denied_rules"`
	AllowedRules []string `json:"allowed_rules"`
	MCPEnabled   bool     `json:"mcp_enabled"`
}

type FilesPolicy struct {
	Protected []string `json:"protected"`
}

type VerificationPolicy struct {
	Build            bool `json:"build"`
	Tests            bool `json:"tests"`
	Lint             bool `json:"lint"`
	CrossModelReview bool `json:"cross_model_review"`
	ScopeCheck       bool `json:"scope_check"`
}

func DefaultPolicy() Policy {
	return Policy{
		Phases: map[string]PhasePolicy{
			"plan": {
				BuiltinTools: []string{"Read", "Glob", "Grep"},
				AllowedRules: []string{"Read", "Glob", "Grep"},
				DeniedRules:  []string{},
				MCPEnabled:   false,
			},
			"execute": {
				BuiltinTools: []string{"Read", "Edit", "Write", "Bash", "Glob", "Grep"},
				AllowedRules: []string{"Read", "Edit", "Bash(npm test:*)", "Bash(npm run lint:*)", "Bash(npm run build:*)", "Bash(cat *)", "Bash(grep *)", "Bash(git status)", "Bash(git diff *)"},
				DeniedRules:  []string{"Bash(rm -rf *)", "Bash(git push *)", "Bash(git reset --hard *)", "Bash(git rebase *)", "Bash(sudo *)", "Bash(curl *)", "Bash(wget *)"},
				MCPEnabled:   true,
			},
			"verify": {
				BuiltinTools: []string{"Read", "Glob", "Grep", "Bash"},
				AllowedRules: []string{"Read", "Bash(npm test:*)", "Bash(npm run lint:*)"},
				DeniedRules:  []string{"Edit", "Write", "Bash(rm *)", "Bash(git *)"},
				MCPEnabled:   false,
			},
		},
		Files:        FilesPolicy{Protected: []string{".claude/", ".stoke/", "CLAUDE.md", ".env*", "stoke.policy.yaml"}},
		Verification: VerificationPolicy{Build: true, Tests: true, Lint: true, CrossModelReview: true, ScopeCheck: true},
	}
}

func DefaultPolicyYAML() string {
	return `phases:
  plan:
    builtin_tools: [Read, Glob, Grep]
    denied_rules: []
    allowed_rules: [Read, Glob, Grep]
    mcp_enabled: false

  execute:
    builtin_tools: [Read, Edit, Write, Bash, Glob, Grep]
    denied_rules:
      - "Bash(rm -rf *)"
      - "Bash(git push *)"
      - "Bash(git reset --hard *)"
      - "Bash(git rebase *)"
      - "Bash(sudo *)"
      - "Bash(curl *)"
      - "Bash(wget *)"
    allowed_rules:
      - "Read"
      - "Edit"
      - "Bash(npm test:*)"
      - "Bash(npm run lint:*)"
      - "Bash(npm run build:*)"
      - "Bash(cat *)"
      - "Bash(grep *)"
      - "Bash(git status)"
      - "Bash(git diff *)"
    mcp_enabled: true

  verify:
    builtin_tools: [Read, Glob, Grep, Bash]
    denied_rules:
      - "Edit"
      - "Write"
      - "Bash(rm *)"
      - "Bash(git *)"
    allowed_rules:
      - "Read"
      - "Bash(npm test:*)"
      - "Bash(npm run lint:*)"
    mcp_enabled: false

files:
  protected: [".claude/", ".stoke/", "CLAUDE.md", ".env*", "stoke.policy.yaml"]

verification:
  build: required
  tests: required
  lint: required
  cross_model_review: required
  scope_check: required
`
}

func LoadPolicy(path string) (Policy, error) {
	if strings.TrimSpace(path) == "" {
		return DefaultPolicy(), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, err
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "{") {
		var p Policy
		if err := json.Unmarshal(raw, &p); err != nil {
			return Policy{}, err
		}
		return normalizePolicy(p), nil
	}
	p, err := parsePolicyYAML(trimmed)
	if err != nil {
		return Policy{}, err
	}
	return normalizePolicy(p), nil
}

func normalizePolicy(p Policy) Policy {
	d := DefaultPolicy()
	if p.Phases == nil {
		p.Phases = d.Phases
	}
	for name, phase := range d.Phases {
		current, ok := p.Phases[name]
		if !ok {
			p.Phases[name] = phase
			continue
		}
		if current.BuiltinTools == nil {
			current.BuiltinTools = phase.BuiltinTools
		}
		if current.DeniedRules == nil {
			current.DeniedRules = phase.DeniedRules
		}
		if current.AllowedRules == nil {
			current.AllowedRules = phase.AllowedRules
		}
		p.Phases[name] = current
	}
	if p.Files.Protected == nil {
		p.Files = d.Files
	}
	if !p.Verification.Build && !p.Verification.Tests && !p.Verification.Lint && !p.Verification.CrossModelReview && !p.Verification.ScopeCheck {
		p.Verification = d.Verification
	}
	return p
}

func parsePolicyYAML(input string) (Policy, error) {
	p := Policy{Phases: map[string]PhasePolicy{}}
	scanner := bufio.NewScanner(strings.NewReader(input))
	section := ""
	currentPhase := ""
	currentListField := ""

	for scanner.Scan() {
		raw := scanner.Text()
		line := stripComment(raw)
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := leadingSpaces(line)
		text := strings.TrimSpace(line)

		switch {
		case indent == 0 && strings.HasSuffix(text, ":"):
			section = strings.TrimSuffix(text, ":")
			currentPhase = ""
			currentListField = ""
		case section == "phases" && indent == 2 && strings.HasSuffix(text, ":"):
			currentPhase = strings.TrimSuffix(text, ":")
			currentListField = ""
			if _, ok := p.Phases[currentPhase]; !ok {
				p.Phases[currentPhase] = PhasePolicy{}
			}
		case section == "phases" && indent == 4:
			key, val, ok := splitKV(text)
			if !ok {
				return Policy{}, fmt.Errorf("invalid phases line: %q", raw)
			}
			currentListField = ""
			phase := p.Phases[currentPhase]
			switch key {
			case "builtin_tools":
				phase.BuiltinTools = parseListValue(val)
			case "denied_rules":
				if val == "" {
					currentListField = key
				} else {
					phase.DeniedRules = parseListValue(val)
				}
			case "allowed_rules":
				if val == "" {
					currentListField = key
				} else {
					phase.AllowedRules = parseListValue(val)
				}
			case "mcp_enabled":
				phase.MCPEnabled = parseBoolLike(val)
			default:
				return Policy{}, fmt.Errorf("unknown phase key %q", key)
			}
			p.Phases[currentPhase] = phase
		case section == "phases" && indent == 6 && strings.HasPrefix(text, "- "):
			phase := p.Phases[currentPhase]
			item := unquote(strings.TrimSpace(strings.TrimPrefix(text, "- ")))
			switch currentListField {
			case "denied_rules":
				phase.DeniedRules = append(phase.DeniedRules, item)
			case "allowed_rules":
				phase.AllowedRules = append(phase.AllowedRules, item)
			default:
				return Policy{}, fmt.Errorf("list item without list field: %q", raw)
			}
			p.Phases[currentPhase] = phase
		case section == "files" && indent == 2:
			key, val, ok := splitKV(text)
			if !ok {
				return Policy{}, fmt.Errorf("invalid files line: %q", raw)
			}
			if key != "protected" {
				return Policy{}, fmt.Errorf("unknown files key %q", key)
			}
			p.Files.Protected = parseListValue(val)
		case section == "verification" && indent == 2:
			key, val, ok := splitKV(text)
			if !ok {
				return Policy{}, fmt.Errorf("invalid verification line: %q", raw)
			}
			parsed := parseBoolLike(val)
			switch key {
			case "build":
				p.Verification.Build = parsed
			case "tests":
				p.Verification.Tests = parsed
			case "lint":
				p.Verification.Lint = parsed
			case "cross_model_review":
				p.Verification.CrossModelReview = parsed
			case "scope_check":
				p.Verification.ScopeCheck = parsed
			default:
				return Policy{}, fmt.Errorf("unknown verification key %q", key)
			}
		default:
			return Policy{}, fmt.Errorf("unsupported policy structure at line %q", raw)
		}
	}
	if err := scanner.Err(); err != nil {
		return Policy{}, err
	}
	return p, nil
}

func stripComment(line string) string {
	inQuote := false
	var quote rune
	var out strings.Builder
	escaped := false
	for _, r := range line {
		if escaped {
			out.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && inQuote {
			escaped = true
			out.WriteRune(r)
			continue
		}
		if (r == '\'' || r == '"') && (!inQuote || r == quote) {
			if inQuote && r == quote {
				inQuote = false
			} else if !inQuote {
				inQuote = true
				quote = r
			}
		}
		if r == '#' && !inQuote {
			break
		}
		out.WriteRune(r)
	}
	return strings.TrimRight(out.String(), " ")
}

func leadingSpaces(s string) int {
	count := 0
	for _, r := range s {
		if r != ' ' {
			break
		}
		count++
	}
	return count
}

func splitKV(s string) (string, string, bool) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
}

func parseListValue(v string) []string {
	v = strings.TrimSpace(v)
	if v == "[]" || v == "" {
		return []string{}
	}
	if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
		inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(v, "["), "]"))
		if inner == "" {
			return []string{}
		}
		parts := strings.Split(inner, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			out = append(out, unquote(strings.TrimSpace(part)))
		}
		return out
	}
	return []string{unquote(v)}
}

func parseBoolLike(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	switch v {
	case "true", "yes", "required", "on":
		return true
	case "false", "no", "off", "optional":
		return false
	default:
		b, _ := strconv.ParseBool(v)
		return b
	}
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
