package rules_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/daemon"
	"github.com/RelayOne/r1/internal/rules"
)

type udrelTestSynthesizer struct{}

func (udrelTestSynthesizer) Synthesize(_ context.Context, req rules.SynthesisRequest) (rules.SynthesisResult, error) {
	return rules.SynthesisResult{
		Scope:      rules.ScopeRepo,
		ToolFilter: "^exec_bash$",
		Strategy:   rules.StrategyRegexFilter,
		Config: rules.EnforcementConfig{
			RegexFilter: &rules.RegexFilterSpec{
				Target:  "raw_args",
				Pattern: "rm -rf",
				Verdict: string(rules.VerdictBlock),
				Reason:  "no-rm: destructive command denied",
			},
		},
	}, nil
}

type udrelStubExecutor struct {
	calls int
}

func (s *udrelStubExecutor) Type() string { return "stub" }

func (s *udrelStubExecutor) Capabilities() []string { return []string{"*"} }

func (s *udrelStubExecutor) Execute(_ context.Context, _ *daemon.Task) daemon.ExecutionResult {
	s.calls++
	return daemon.ExecutionResult{MissionID: "udrel-stub"}
}

func TestUDRELBlockAllowLiveExecution(t *testing.T) {
	t.Parallel()

	registry := rules.NewFSRegistry(t.TempDir(), udrelTestSynthesizer{})
	rule, err := registry.AddWithOptions(context.Background(), rules.AddRequest{
		Text:       "deny rm -rf tool invocations",
		ToolFilter: "^exec_bash$",
	})
	if err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	base := &udrelStubExecutor{}
	exec := daemon.WrapExecutorWithRules(base, registry)

	blocked := exec.Execute(context.Background(), &daemon.Task{
		ID:   "task-block",
		Repo: t.TempDir(),
		Meta: map[string]string{
			"tool_name": "exec_bash",
			"tool_args": `{"cmd":"rm -rf /tmp/foo"}`,
		},
	})
	if blocked.Err == nil {
		t.Fatal("blocked.Err = nil, want rule block")
	}
	if base.calls != 0 {
		t.Fatalf("base.calls after blocked execution = %d, want 0", base.calls)
	}
	if !strings.Contains(blocked.Err.Error(), "exec_bash") {
		t.Fatalf("blocked.Err = %q, want tool name", blocked.Err)
	}
	if !strings.Contains(blocked.Err.Error(), "no-rm") {
		t.Fatalf("blocked.Err = %q, want rule marker", blocked.Err)
	}
	if !strings.Contains(blocked.Err.Error(), "destructive command denied") {
		t.Fatalf("blocked.Err = %q, want rule reason", blocked.Err)
	}

	allowed := exec.Execute(context.Background(), &daemon.Task{
		ID:   "task-allow",
		Repo: t.TempDir(),
		Meta: map[string]string{
			"tool_name": "exec_bash",
			"tool_args": `{"cmd":"ls /tmp"}`,
		},
	})
	if allowed.Err != nil {
		t.Fatalf("allowed.Err = %v, want nil", allowed.Err)
	}
	if base.calls != 1 {
		t.Fatalf("base.calls after allowed execution = %d, want 1", base.calls)
	}

	check, err := registry.Check(context.Background(), "exec_bash", json.RawMessage(`{"cmd":"rm -rf /tmp/foo"}`), rules.CheckContext{
		RepoRoot: t.TempDir(),
		TaskID:   "task-audit",
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if check.Verdict != rules.VerdictBlock {
		t.Fatalf("check.Verdict = %q, want %q", check.Verdict, rules.VerdictBlock)
	}
	if len(check.Evaluations) != 1 {
		t.Fatalf("len(check.Evaluations) = %d, want 1", len(check.Evaluations))
	}
	if check.Evaluations[0].RuleID != rule.ID {
		t.Fatalf("check.Evaluations[0].RuleID = %q, want %q", check.Evaluations[0].RuleID, rule.ID)
	}
	if check.Evaluations[0].Reason != "no-rm: destructive command denied" {
		t.Fatalf("check.Evaluations[0].Reason = %q, want %q", check.Evaluations[0].Reason, "no-rm: destructive command denied")
	}
}
