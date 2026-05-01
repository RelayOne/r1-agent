package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/rules"
)

type rulesCommandOptions struct {
	printLine     func(format string, args ...interface{})
	confirmDelete func(prompt string) bool
}

type rulesListRow struct {
	ID          string
	Text        string
	Strategy    string
	Status      string
	Invocations string
	Blocks      string
	AvgMS       string
	AvgTokens   string
}

func runRulesCommand(repoRoot, args string, opts rulesCommandOptions) string {
	printLine := opts.printLine
	if printLine == nil {
		printLine = func(string, ...interface{}) {}
	}
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
		renderRulesTable(list, printLine)
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
		printLine("Added rule %s", rule.ID)
		printLine("  Text: %s", rule.Text)
		printLine("  Strategy: %s", describeRuleStrategy(rule))
		printLine("  Estimated impact: %s", estimateRuleImpact(rule))
		return "rules add done"
	case "delete":
		if rest == "" {
			printLine("Usage: /rules delete <id>")
			return "rules delete missing id"
		}
		rule, err := registry.Get(rest)
		if err != nil {
			printLine("rules delete failed: %v", err)
			return "rules delete failed"
		}
		confirmed := true
		if opts.confirmDelete != nil {
			confirmed = opts.confirmDelete(fmt.Sprintf("Delete rule %s [%s]? [Y/n]", rule.ID, truncOne(rule.Text, 48)))
		}
		if !confirmed {
			printLine("Delete cancelled.")
			return "rules delete cancelled"
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

func renderRulesTable(list []rules.Rule, printLine func(format string, args ...interface{})) {
	rows := make([]rulesListRow, 0, len(list))
	for _, rule := range list {
		rows = append(rows, rulesListRow{
			ID:          rule.ID,
			Text:        truncOne(rule.Text, 44),
			Strategy:    rule.EnforcementStrategy,
			Status:      rule.Status,
			Invocations: fmt.Sprintf("%d", rule.ImpactMetrics.Invocations),
			Blocks:      fmt.Sprintf("%d", rule.ImpactMetrics.Blocked),
			AvgMS:       fmt.Sprintf("%.2f", rule.ImpactMetrics.AvgCheckMS),
			AvgTokens:   fmt.Sprintf("%.2f", rule.ImpactMetrics.AvgTokensUsed),
		})
	}

	headers := []string{"ID", "Text", "Strategy", "Status", "Invocations", "Blocks", "Avg ms", "Avg tokens"}
	widths := []int{
		len(headers[0]),
		len(headers[1]),
		len(headers[2]),
		len(headers[3]),
		len(headers[4]),
		len(headers[5]),
		len(headers[6]),
		len(headers[7]),
	}
	for _, row := range rows {
		widths[0] = maxInt(widths[0], len(row.ID))
		widths[1] = maxInt(widths[1], len(row.Text))
		widths[2] = maxInt(widths[2], len(row.Strategy))
		widths[3] = maxInt(widths[3], len(row.Status))
		widths[4] = maxInt(widths[4], len(row.Invocations))
		widths[5] = maxInt(widths[5], len(row.Blocks))
		widths[6] = maxInt(widths[6], len(row.AvgMS))
		widths[7] = maxInt(widths[7], len(row.AvgTokens))
	}

	printLine(formatRulesRow(widths, headers...))
	printLine(formatRulesDivider(widths))
	for _, row := range rows {
		printLine(formatRulesRow(widths,
			row.ID,
			row.Text,
			row.Strategy,
			row.Status,
			row.Invocations,
			row.Blocks,
			row.AvgMS,
			row.AvgTokens,
		))
	}
}

func formatRulesRow(widths []int, cols ...string) string {
	parts := make([]string, 0, len(cols))
	for idx, col := range cols {
		parts = append(parts, padRight(col, widths[idx]))
	}
	return "| " + strings.Join(parts, " | ") + " |"
}

func formatRulesDivider(widths []int) string {
	parts := make([]string, 0, len(widths))
	for _, width := range widths {
		parts = append(parts, strings.Repeat("-", width))
	}
	return "|-" + strings.Join(parts, "-|-") + "-|"
}

func describeRuleStrategy(rule rules.Rule) string {
	switch rule.EnforcementStrategy {
	case rules.StrategyRegexFilter:
		return fmt.Sprintf("%s on tool filter %s", rule.EnforcementStrategy, rule.ToolFilter)
	case rules.StrategyArgumentValidate:
		return fmt.Sprintf("%s on tool filter %s", rule.EnforcementStrategy, rule.ToolFilter)
	case rules.StrategySubagentCheck:
		return fmt.Sprintf("%s on tool filter %s", rule.EnforcementStrategy, rule.ToolFilter)
	default:
		return rule.EnforcementStrategy
	}
}

func estimateRuleImpact(rule rules.Rule) string {
	switch rule.EnforcementStrategy {
	case rules.StrategyRegexFilter:
		return "low cost; regex check on matching tool calls with near-zero token overhead"
	case rules.StrategyArgumentValidate:
		return "low cost; structured argument validation on matching tool calls with near-zero token overhead"
	case rules.StrategySubagentCheck:
		return "higher cost; matching tool calls may incur extra latency and token usage for semantic checks"
	default:
		return "unknown cost; strategy is not recognized"
	}
}

func confirmDeleteWithScanner(prompt string, scanner interface {
	Scan() bool
	Text() string
}) bool {
	fmt.Printf("  %s ", prompt)
	if scanner == nil {
		return true
	}
	if !scanner.Scan() {
		return true
	}
	answer := strings.TrimSpace(scanner.Text())
	return answer == "" || strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes")
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

func padRight(value string, width int) string {
	if len(value) >= width {
		return value
	}
	return value + strings.Repeat(" ", width-len(value))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
