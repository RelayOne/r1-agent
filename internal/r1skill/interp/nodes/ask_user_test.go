package nodes

import (
	"context"
	"encoding/json"
	"testing"
)

type fakePrompter struct{}

func (fakePrompter) Prompt(context.Context, AskUserConfig) (string, bool, error) {
	return "answer", true, nil
}

type fakeReasoner struct{}

func (fakeReasoner) Reason(context.Context, AskUserConfig, json.RawMessage) (json.RawMessage, string, float64, error) {
	return json.RawMessage(`["go test ./..."]`), "inferred from source", 0.8, nil
}

func TestAskUserInteractiveBlocks(t *testing.T) {
	out, err := Execute(context.Background(), AskUserConfig{QuestionID: "intent.purpose"}, ExecuteOpts{
		Mode:     "interactive",
		Prompter: fakePrompter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Mode != "operator" || !out.OperatorConfirmed {
		t.Fatalf("unexpected output: %+v", out)
	}
}

func TestAskUserHeadlessInvokesReasoner(t *testing.T) {
	out, err := Execute(context.Background(), AskUserConfig{QuestionID: "caps.shell.commands"}, ExecuteOpts{
		Mode:     "headless",
		Reasoner: fakeReasoner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Mode != "llm-best-judgment" || out.LLMConfidence != 0.8 {
		t.Fatalf("unexpected output: %+v", out)
	}
}

func TestAskUserConstitutionForcesInteractive(t *testing.T) {
	out, err := Execute(context.Background(), AskUserConfig{QuestionID: "caps.shell.commands"}, ExecuteOpts{
		Mode:     "headless",
		Prompter: fakePrompter{},
		Reasoner: fakeReasoner{},
		ConstitutionPolicy: &ConstitutionPolicy{
			AlwaysInteractiveQuestions: []string{"caps.shell.*"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Mode != "operator" {
		t.Fatalf("expected operator mode, got %+v", out)
	}
}
