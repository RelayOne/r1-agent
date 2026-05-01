package rules

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

type SynthesisRequest struct {
	Text             string
	Scope            string
	ToolFilter       string
	StrategyOverride string
}

type SynthesisResult struct {
	Scope      string
	ToolFilter string
	Strategy   string
	Config     EnforcementConfig
}

type HeuristicSynthesizer struct{}

func (HeuristicSynthesizer) Synthesize(_ context.Context, req SynthesisRequest) (SynthesisResult, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return SynthesisResult{}, fmt.Errorf("rule text required")
	}
	scope := normalizeScope(req.Scope)
	if scope == "" {
		scope = ScopeRepo
	}
	toolFilter := strings.TrimSpace(req.ToolFilter)
	if toolFilter == "" {
		toolFilter = inferToolFilter(text)
	}
	if toolFilter == "" {
		toolFilter = ".*"
	}

	strategy := strings.TrimSpace(req.StrategyOverride)
	if strategy == "" {
		strategy = inferStrategy(text)
	}

	result := SynthesisResult{
		Scope:      scope,
		ToolFilter: toolFilter,
		Strategy:   strategy,
	}
	switch strategy {
	case StrategyRegexFilter:
		result.Config.RegexFilter = &RegexFilterSpec{
			Target:  "raw_args",
			Pattern: inferRegexPattern(text),
			Verdict: string(inferVerdict(text)),
			Reason:  text,
		}
	case StrategyArgumentValidate:
		result.Config.ArgumentValidator = &ArgumentValidatorSpec{
			MatchAll:    true,
			Constraints: inferArgumentConstraints(text),
			Verdict:     string(inferVerdict(text)),
			Reason:      text,
		}
		if len(result.Config.ArgumentValidator.Constraints) == 0 {
			result.Config.ArgumentValidator.Constraints = []ArgumentConstraint{{
				Field:    "name",
				Operator: "matches",
				Value:    ".*",
			}}
		}
	case StrategySubagentCheck:
		result.Config.SubagentCheck = &SubagentCheckSpec{
			SummaryTemplate: "Evaluate whether the proposed tool call violates the user-defined rule text.",
			DefaultVerdict:  string(VerdictWarn),
			Reason:          text,
		}
	default:
		return SynthesisResult{}, fmt.Errorf("unsupported strategy %q", strategy)
	}
	return result, nil
}

func inferStrategy(text string) string {
	if extractMatchingRegex(text) != "" {
		return StrategyArgumentValidate
	}
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "matching"), strings.Contains(lower, "regex"), strings.Contains(lower, "starts with"), strings.Contains(lower, "ends with"):
		return StrategyArgumentValidate
	case strings.Contains(lower, "tool "), strings.Contains(lower, "call tool "), strings.Contains(lower, "raw args"):
		return StrategyRegexFilter
	default:
		return StrategySubagentCheck
	}
}

func inferToolFilter(text string) string {
	quotedTool := regexp.MustCompile(`(?i)\btool\s+([a-zA-Z0-9_:-]+)`)
	if match := quotedTool.FindStringSubmatch(text); len(match) == 2 {
		return "^" + regexp.QuoteMeta(match[1]) + "$"
	}
	switch {
	case strings.Contains(strings.ToLower(text), "github actions"), strings.Contains(strings.ToLower(text), "gh actions"):
		return `^gh_run_.*$`
	case strings.Contains(strings.ToLower(text), "delete branch"):
		return `^(delete_branch|bash)$`
	default:
		return ".*"
	}
}

func inferRegexPattern(text string) string {
	if rx := extractMatchingRegex(text); rx != "" {
		return rx
	}
	if strings.Contains(strings.ToLower(text), "squash") {
		return `(?i)squash`
	}
	if strings.Contains(strings.ToLower(text), "github actions") {
		return `(?i)actions`
	}
	return `.*`
}

func inferArgumentConstraints(text string) []ArgumentConstraint {
	regex := extractMatchingRegex(text)
	field := inferFieldName(text)
	if regex == "" {
		return nil
	}
	return []ArgumentConstraint{{
		Field:    field,
		Operator: "matches",
		Value:    regex,
	}}
}

func inferFieldName(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, " cmd "), strings.Contains(lower, "cmd "), strings.Contains(lower, " cmd"):
		return "cmd"
	case strings.Contains(lower, " branch "), strings.Contains(lower, "branch name"), strings.Contains(lower, " name "):
		return "name"
	case strings.Contains(lower, " path "):
		return "path"
	case strings.Contains(lower, " command "):
		return "command"
	default:
		return "name"
	}
}

func inferVerdict(text string) Verdict {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "warn"):
		return VerdictWarn
	case strings.Contains(lower, "never"), strings.Contains(lower, "don't"), strings.Contains(lower, "do not"):
		return VerdictBlock
	default:
		return VerdictWarn
	}
}

func extractMatchingRegex(text string) string {
	if value := extractSlashRegex(text); value != "" {
		return value
	}
	re := regexp.MustCompile(`(?i)\bmatching\s+(.+)$`)
	match := re.FindStringSubmatch(strings.TrimSpace(text))
	if len(match) != 2 {
		return ""
	}
	value := strings.TrimSpace(match[1])
	value = strings.Trim(value, `"`)
	value = strings.Trim(value, `'`)
	return value
}

func extractSlashRegex(text string) string {
	re := regexp.MustCompile(`/((?:\\.|[^/])*)/`)
	match := re.FindStringSubmatch(text)
	if len(match) != 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}
