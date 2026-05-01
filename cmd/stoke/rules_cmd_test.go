package main

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/rules"
)

func TestRunRulesCommandAddListPauseResumeDelete(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	var lines []string
	printLine := func(format string, args ...interface{}) {
		lines = append(lines, formatLine(format, args...))
	}

	result := runRulesCommand(repo, `add "no GH actions"`, rulesCommandOptions{printLine: printLine})
	if result != "rules add done" {
		t.Fatalf("add result = %q", result)
	}
	if !containsLine(lines, "Strategy: subagent_check on tool filter ^gh_run_.*$") {
		t.Fatalf("add output missing synthesized strategy: %v", lines)
	}
	if !containsLine(lines, "Estimated impact: higher cost; matching tool calls may incur extra latency and token usage for semantic checks") {
		t.Fatalf("add output missing estimated impact: %v", lines)
	}

	registry := rules.NewRepoRegistry(repo, nil)
	list, err := registry.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(List) = %d, want 1", len(list))
	}
	ruleID := list[0].ID

	lines = nil
	result = runRulesCommand(repo, "list", rulesCommandOptions{printLine: printLine})
	if result != "rules listed (1)" {
		t.Fatalf("list result = %q", result)
	}
	if !containsLine(lines, "| ID") {
		t.Fatalf("list output missing header row: %v", lines)
	}
	if !containsLine(lines, ruleID) {
		t.Fatalf("list output missing rule id %q: %v", ruleID, lines)
	}

	lines = nil
	result = runRulesCommand(repo, "pause "+ruleID, rulesCommandOptions{printLine: printLine})
	if result != "rules pause done" {
		t.Fatalf("pause result = %q", result)
	}
	paused, err := registry.Get(ruleID)
	if err != nil {
		t.Fatalf("Get paused: %v", err)
	}
	if paused.Status != rules.StatusPaused {
		t.Fatalf("paused status = %q, want %q", paused.Status, rules.StatusPaused)
	}

	lines = nil
	result = runRulesCommand(repo, "resume "+ruleID, rulesCommandOptions{printLine: printLine})
	if result != "rules resume done" {
		t.Fatalf("resume result = %q", result)
	}
	resumed, err := registry.Get(ruleID)
	if err != nil {
		t.Fatalf("Get resumed: %v", err)
	}
	if resumed.Status != rules.StatusActive {
		t.Fatalf("resumed status = %q, want %q", resumed.Status, rules.StatusActive)
	}

	lines = nil
	result = runRulesCommand(repo, "delete "+ruleID, rulesCommandOptions{
		printLine: printLine,
		confirmDelete: func(prompt string) bool {
			if !strings.Contains(prompt, ruleID) {
				t.Fatalf("delete prompt %q missing rule id %q", prompt, ruleID)
			}
			return true
		},
	})
	if result != "rules delete done" {
		t.Fatalf("delete result = %q", result)
	}
	finalList, err := registry.List()
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(finalList) != 0 {
		t.Fatalf("len(List after delete) = %d, want 0", len(finalList))
	}
}

func TestRunRulesCommandDeleteCancelled(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	registry := rules.NewRepoRegistry(repo, nil)
	rule, err := registry.AddWithOptions(context.Background(), rules.AddRequest{Text: "no GH actions"})
	if err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	var lines []string
	result := runRulesCommand(repo, "delete "+rule.ID, rulesCommandOptions{
		printLine: func(format string, args ...interface{}) {
			lines = append(lines, formatLine(format, args...))
		},
		confirmDelete: func(string) bool {
			return false
		},
	})
	if result != "rules delete cancelled" {
		t.Fatalf("delete result = %q", result)
	}
	if !containsLine(lines, "Delete cancelled.") {
		t.Fatalf("delete output missing cancellation line: %v", lines)
	}
	if _, err := registry.Get(rule.ID); err != nil {
		t.Fatalf("Get after cancelled delete: %v", err)
	}
}

func TestConfirmDeleteWithScannerDefaultsYes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "empty", text: "", want: true},
		{name: "yes", text: "yes", want: true},
		{name: "y", text: "y", want: true},
		{name: "no", text: "n", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scanner := bufio.NewScanner(strings.NewReader(tc.text + "\n"))
			if got := confirmDeleteWithScanner("Delete?", scanner); got != tc.want {
				t.Fatalf("confirmDeleteWithScanner(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func containsLine(lines []string, needle string) bool {
	for _, line := range lines {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

func formatLine(format string, args ...interface{}) string {
	return strings.TrimSpace(fmt.Sprintf(format, args...))
}
