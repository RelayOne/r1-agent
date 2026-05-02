package main

import (
	"bufio"
	"context"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/conversation"
	"github.com/RelayOne/r1/internal/workflow"
)

func TestChatInteractiveApprovalLoop(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantPlans []string
		wantExecs []string
	}{
		{
			name:      "approve first plan",
			input:     "fix auth bug\ny\nquit\n",
			wantPlans: []string{"fix auth bug"},
			wantExecs: []string{"fix auth bug"},
		},
		{
			name:      "edit replans before execute",
			input:     "fix auth bug\nedit\nadd regression test\ny\nquit\n",
			wantPlans: []string{"fix auth bug", "add regression test"},
			wantExecs: []string{"add regression test"},
		},
		{
			name:      "reject clears without execute",
			input:     "fix auth bug\nn\nquit\n",
			wantPlans: []string{"fix auth bug"},
			wantExecs: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var planCalls []string
			var execCalls []string
			var out strings.Builder

			session := &chatInteractiveSession{
				in:        bufio.NewScanner(strings.NewReader(tc.input)),
				out:       &out,
				storePath: t.TempDir() + "/chat.json",
				conv:      conversation.NewRuntime("test", 200000),
				planFn: func(_ context.Context, task string) (workflow.Result, error) {
					planCalls = append(planCalls, task)
					return workflow.Result{PlanOutput: "plan for " + task}, nil
				},
				execFn: func(_ context.Context, task string) (workflow.Result, error) {
					execCalls = append(execCalls, task)
					return workflow.Result{TaskType: "refactor", WorktreePath: "/tmp/worktree"}, nil
				},
			}

			if err := session.run(context.Background()); err != nil {
				t.Fatalf("run: %v", err)
			}
			if got := strings.Join(planCalls, "|"); got != strings.Join(tc.wantPlans, "|") {
				t.Fatalf("plan calls = %q want %q", got, strings.Join(tc.wantPlans, "|"))
			}
			if got := strings.Join(execCalls, "|"); got != strings.Join(tc.wantExecs, "|") {
				t.Fatalf("exec calls = %q want %q", got, strings.Join(tc.wantExecs, "|"))
			}
			if !strings.Contains(out.String(), "execute? [y/n/edit]") {
				t.Fatalf("output missing approval prompt: %s", out.String())
			}
		})
	}
}
