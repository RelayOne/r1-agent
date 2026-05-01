package rules

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

func evaluateRule(_ context.Context, rule Rule, toolName string, toolArgs json.RawMessage, _ CheckContext) (Verdict, string, int64) {
	switch rule.EnforcementStrategy {
	case StrategyRegexFilter:
		return evaluateRegexFilter(rule, toolName, toolArgs), reasonFor(rule), 0
	case StrategyArgumentValidate:
		return evaluateArgumentValidator(rule, toolArgs), reasonFor(rule), 0
	case StrategySubagentCheck:
		return evaluateSubagentFallback(rule, toolName, toolArgs)
	default:
		return VerdictPass, "", 0
	}
}

func evaluateRegexFilter(rule Rule, toolName string, toolArgs json.RawMessage) Verdict {
	spec := rule.EnforcementConfig.RegexFilter
	if spec == nil {
		return VerdictPass
	}
	target := spec.Target
	if target == "" {
		target = "raw_args"
	}
	var value string
	switch target {
	case "tool_name":
		value = toolName
	default:
		value = string(toolArgs)
	}
	if matchesPattern(spec.Pattern, value) {
		return parseVerdict(spec.Verdict, VerdictBlock)
	}
	return VerdictPass
}

func evaluateArgumentValidator(rule Rule, toolArgs json.RawMessage) Verdict {
	spec := rule.EnforcementConfig.ArgumentValidator
	if spec == nil {
		return VerdictPass
	}
	var decoded map[string]any
	if err := json.Unmarshal(toolArgs, &decoded); err != nil {
		return VerdictPass
	}
	if len(spec.Constraints) == 0 {
		return VerdictPass
	}
	matched := 0
	for _, constraint := range spec.Constraints {
		value, ok := lookupField(decoded, constraint.Field)
		if !ok {
			if spec.MatchAll {
				return VerdictPass
			}
			continue
		}
		if compareConstraint(value, constraint) {
			matched++
			continue
		}
		if spec.MatchAll {
			return VerdictPass
		}
	}
	if spec.MatchAll && matched == len(spec.Constraints) {
		return parseVerdict(spec.Verdict, VerdictBlock)
	}
	if !spec.MatchAll && matched > 0 {
		return parseVerdict(spec.Verdict, VerdictBlock)
	}
	return VerdictPass
}

func evaluateSubagentFallback(rule Rule, toolName string, toolArgs json.RawMessage) (Verdict, string, int64) {
	spec := rule.EnforcementConfig.SubagentCheck
	if spec == nil {
		return VerdictWarn, rule.Text, 0
	}
	summary := strings.ToLower(toolName + " " + string(toolArgs))
	text := strings.ToLower(rule.Text)
	verdict := parseVerdict(spec.DefaultVerdict, VerdictWarn)
	if containsSemanticConflict(text, summary) {
		return verdict, reasonFor(rule), int64(len(summary) / 4)
	}
	return VerdictPass, "subagent heuristic allowed tool call", int64(len(summary) / 4)
}

func containsSemanticConflict(ruleText, summary string) bool {
	switch {
	case strings.Contains(ruleText, "always verify"):
		return !strings.Contains(summary, "verify")
	case strings.Contains(ruleText, "don't squash"), strings.Contains(ruleText, "do not squash"):
		return strings.Contains(summary, "squash")
	case strings.Contains(ruleText, "never use github actions"):
		return strings.Contains(summary, "gh_run_")
	default:
		keywords := []string{"delete", "branch", "prod", "staging", "dev"}
		for _, keyword := range keywords {
			if strings.Contains(ruleText, keyword) && strings.Contains(summary, keyword) {
				return true
			}
		}
		return false
	}
}

func reasonFor(rule Rule) string {
	switch rule.EnforcementStrategy {
	case StrategyRegexFilter:
		if rule.EnforcementConfig.RegexFilter != nil && rule.EnforcementConfig.RegexFilter.Reason != "" {
			return rule.EnforcementConfig.RegexFilter.Reason
		}
	case StrategyArgumentValidate:
		if rule.EnforcementConfig.ArgumentValidator != nil && rule.EnforcementConfig.ArgumentValidator.Reason != "" {
			return rule.EnforcementConfig.ArgumentValidator.Reason
		}
	case StrategySubagentCheck:
		if rule.EnforcementConfig.SubagentCheck != nil && rule.EnforcementConfig.SubagentCheck.Reason != "" {
			return rule.EnforcementConfig.SubagentCheck.Reason
		}
	}
	return rule.Text
}

func parseVerdict(raw string, fallback Verdict) Verdict {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case string(VerdictBlock):
		return VerdictBlock
	case string(VerdictWarn):
		return VerdictWarn
	case string(VerdictPass):
		return VerdictPass
	default:
		return fallback
	}
}

func matchesPattern(pattern, value string) bool {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return strings.Contains(value, pattern)
	}
	return re.MatchString(value)
}

func lookupField(input map[string]any, field string) (string, bool) {
	field = strings.TrimSpace(field)
	if field == "" {
		return "", false
	}
	current := any(input)
	for _, part := range strings.Split(field, ".") {
		m, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		current, ok = m[part]
		if !ok {
			return "", false
		}
	}
	switch typed := current.(type) {
	case string:
		return typed, true
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case bool:
		if typed {
			return "true", true
		}
		return "false", true
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return "", false
		}
		return string(data), true
	}
}

func compareConstraint(value string, constraint ArgumentConstraint) bool {
	switch strings.ToLower(strings.TrimSpace(constraint.Operator)) {
	case "equals":
		return value == constraint.Value
	case "contains":
		return strings.Contains(value, constraint.Value)
	case "starts_with":
		return strings.HasPrefix(value, constraint.Value)
	case "ends_with":
		return strings.HasSuffix(value, constraint.Value)
	case "matches":
		return matchesPattern(constraint.Value, value)
	default:
		return false
	}
}
