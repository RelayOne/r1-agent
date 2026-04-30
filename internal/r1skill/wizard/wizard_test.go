package wizard

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWizardHeadlessOnRealSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "skill.toml")
	if err := os.WriteFile(source, []byte("description = \"Checks coverage\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), RunOptions{
		Mode:         "headless",
		SourcePath:   source,
		SourceFormat: "codex-toml",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Skill.SkillID == "" || result.Decisions.SessionID == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestWizardInteractivePromptsAndSkipsDependentQuestions(t *testing.T) {
	input := strings.Join([]string{
		"scratch",
		"Summarize CI logs",
		"api.example.com",
		"grep,jq",
		"0.25",
		"yes",
	}, "\n") + "\n"
	var output bytes.Buffer

	result, err := Run(context.Background(), RunOptions{
		Mode:    "interactive",
		SkillID: "ci-log-summary",
		Stdin:   strings.NewReader(input),
		Stdout:  &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Skill.Description != "Summarize CI logs" {
		t.Fatalf("description = %q", result.Skill.Description)
	}
	if got := result.Skill.Capabilities.Shell.AllowCommands; strings.Join(got, ",") != "grep,jq" {
		t.Fatalf("shell commands = %v", got)
	}
	if result.Skill.Capabilities.LLM.BudgetUSD != 0.25 {
		t.Fatalf("budget = %f", result.Skill.Capabilities.LLM.BudgetUSD)
	}
	if strings.Contains(output.String(), "[source.format]") {
		t.Fatalf("unexpected source.format prompt in output:\n%s", output.String())
	}
	for _, decision := range result.Decisions.Decisions {
		if decision.QuestionID == "source.format" {
			t.Fatalf("unexpected decision for skipped dependent question: %+v", decision)
		}
		if decision.Mode != "operator" {
			t.Fatalf("interactive decision mode = %q for %s", decision.Mode, decision.QuestionID)
		}
	}
}

func TestWizardHybridUsesInferencesUntilInteractiveCaps(t *testing.T) {
	input := strings.Join([]string{
		"",
		"git,status",
		"0.15",
		"yes",
	}, "\n") + "\n"
	var output bytes.Buffer

	result, err := resolveAnswers(context.Background(), DefaultPack(), map[string]inferredAnswer{
		"intent.purpose": {
			QuestionID: "intent.purpose",
			Answer:     "Checks coverage",
			Confidence: 0.7,
			Source:     "fixture",
		},
	}, RunOptions{
		Mode:   "hybrid",
		Stdin:  strings.NewReader(input),
		Stdout: &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "[intent.purpose]") {
		t.Fatalf("hybrid mode should not prompt for inferred purpose:\n%s", output.String())
	}
	got, ok := result["intent.purpose"]
	if !ok {
		t.Fatal("expected inferred intent.purpose answer")
	}
	if got.Answer != "Checks coverage" {
		t.Fatalf("intent.purpose answer = %q", got.Answer)
	}
	if got.DecisionMode != "llm-best-judgment" {
		t.Fatalf("intent.purpose mode = %q", got.DecisionMode)
	}
	if got.OperatorConfirmed {
		t.Fatal("intent.purpose should not be operator-confirmed")
	}
	if got.Confidence <= 0 {
		t.Fatalf("intent.purpose confidence = %f", got.Confidence)
	}
}
