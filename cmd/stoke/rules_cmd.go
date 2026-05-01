package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/rules"
)

func runRulesCommand(repoRoot, args string, printLine func(format string, args ...interface{})) string {
	registry := rules.NewRepoRegistry(repoRoot, nil)
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		printRulesUsage(printLine)
		return "rules help shown"
	}
	command, rest := splitCommand(trimmed)
	switch command {
	case "list":
		list, err := registry.List()
		if err != nil {
			printLine("rules list failed: %v", err)
			return "rules list failed"
		}
		if len(list) == 0 {
			printLine("No user-defined rules.")
			return "rules list empty"
		}
		for _, rule := range list {
			metrics := rule.ImpactMetrics
			printLine("%s [%s] %s", rule.ID, rule.Status, rule.Text)
			printLine("  strategy=%s scope=%s filter=%s invocations=%d allowed=%d blocked=%d warnings=%d avg_check_ms=%.2f avg_tokens=%.2f",
				rule.EnforcementStrategy, rule.Scope, rule.ToolFilter, metrics.Invocations, metrics.Allowed, metrics.Blocked, metrics.Warnings, metrics.AvgCheckMS, metrics.AvgTokensUsed)
		}
		return fmt.Sprintf("rules listed (%d)", len(list))
	case "add":
		text := trimQuoted(rest)
		if text == "" {
			printLine("Usage: /rules add \"rule text\"")
			return "rules add missing text"
		}
		rule, err := registry.AddWithOptions(context.Background(), rules.AddRequest{Text: text})
		if err != nil {
			printLine("rules add failed: %v", err)
			return "rules add failed"
		}
		printLine("Added rule %s [%s] strategy=%s filter=%s", rule.ID, rule.Scope, rule.EnforcementStrategy, rule.ToolFilter)
		return "rules add done"
	case "delete":
		if rest == "" {
			printLine("Usage: /rules delete <id>")
			return "rules delete missing id"
		}
		if err := registry.Delete(rest); err != nil {
			printLine("rules delete failed: %v", err)
			return "rules delete failed"
		}
		printLine("Deleted rule %s", rest)
		return "rules delete done"
	case "pause":
		if rest == "" {
			printLine("Usage: /rules pause <id>")
			return "rules pause missing id"
		}
		if err := registry.Pause(rest); err != nil {
			printLine("rules pause failed: %v", err)
			return "rules pause failed"
		}
		printLine("Paused rule %s", rest)
		return "rules pause done"
	case "resume":
		if rest == "" {
			printLine("Usage: /rules resume <id>")
			return "rules resume missing id"
		}
		if err := registry.Resume(rest); err != nil {
			printLine("rules resume failed: %v", err)
			return "rules resume failed"
		}
		printLine("Resumed rule %s", rest)
		return "rules resume done"
	case "help":
		printRulesUsage(printLine)
		return "rules help shown"
	default:
		printLine("Unknown /rules subcommand %q", command)
		printRulesUsage(printLine)
		return "rules unknown subcommand"
	}
}

func printRulesUsage(printLine func(format string, args ...interface{})) {
	printLine("Rules commands:")
	printLine("  /rules list")
	printLine("  /rules add \"rule text\"")
	printLine("  /rules delete <id>")
	printLine("  /rules pause <id>")
	printLine("  /rules resume <id>")
}

func splitCommand(input string) (string, string) {
	parts := strings.SplitN(strings.TrimSpace(input), " ", 2)
	cmd := strings.ToLower(strings.TrimSpace(parts[0]))
	rest := ""
	if len(parts) > 1 {
		rest = strings.TrimSpace(parts[1])
	}
	return cmd, rest
}

func trimQuoted(input string) string {
	trimmed := strings.TrimSpace(input)
	if len(trimmed) >= 2 {
		if trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
			return strings.TrimSpace(trimmed[1 : len(trimmed)-1])
		}
		if trimmed[0] == '\'' && trimmed[len(trimmed)-1] == '\'' {
			return strings.TrimSpace(trimmed[1 : len(trimmed)-1])
		}
	}
	return trimmed
}
