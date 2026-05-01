package engine

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/rules"
)

func TestNativeRunner_UserRulesBlockAndThenAllowExtraTool(t *testing.T) {
	t.Parallel()

	spec := newMinimalRunSpec(t)
	if err := os.MkdirAll(spec.WorktreeDir+"/.r1", 0o755); err != nil {
		t.Fatalf("mkdir .r1: %v", err)
	}
	registry := rules.NewRepoRegistry(spec.WorktreeDir, nil)
	rule, err := registry.AddWithOptions(context.Background(), rules.AddRequest{
		Text: "never call tool delete_branch with name matching ^(staging|dev|prod)$",
	})
	if err != nil {
		t.Fatalf("add rule: %v", err)
	}

	handlerCalls := 0
	spec.ExtraTools = []ExtraTool{{
		Def: provider.ToolDef{
			Name:        "delete_branch",
			Description: "Delete a git branch by name",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
		},
		Handler: func(ctx context.Context, input json.RawMessage) (string, error) {
			handlerCalls++
			return "deleted", nil
		},
	}}

	firstProvider := &fakeMCPProvider{
		responses: []*provider.ChatResponse{
			{
				Content: []provider.ResponseContent{
					{Type: "tool_use", ID: "t1", Name: "delete_branch", Input: map[string]any{"name": "staging"}},
				},
				StopReason: "tool_use",
			},
			{
				Content:    []provider.ResponseContent{{Type: "text", Text: "blocked"}},
				StopReason: "end_turn",
			},
		},
	}
	runner := NewNativeRunner("", "claude-sonnet-4-5")
	if _, err := runWithProvider(t, runner, spec, firstProvider); err != nil {
		t.Fatalf("blocked run: %v", err)
	}
	if handlerCalls != 0 {
		t.Fatalf("handlerCalls after blocked run = %d, want 0", handlerCalls)
	}

	gotRule, err := registry.Get(rule.ID)
	if err != nil {
		t.Fatalf("Get blocked rule: %v", err)
	}
	if gotRule.ImpactMetrics.Blocked != 1 {
		t.Fatalf("blocked metrics = %d, want 1", gotRule.ImpactMetrics.Blocked)
	}

	if err := registry.Delete(rule.ID); err != nil {
		t.Fatalf("Delete rule: %v", err)
	}

	secondProvider := &fakeMCPProvider{
		responses: []*provider.ChatResponse{
			{
				Content: []provider.ResponseContent{
					{Type: "tool_use", ID: "t2", Name: "delete_branch", Input: map[string]any{"name": "feature/foo"}},
				},
				StopReason: "tool_use",
			},
			{
				Content:    []provider.ResponseContent{{Type: "text", Text: "allowed"}},
				StopReason: "end_turn",
			},
		},
	}
	if _, err := runWithProvider(t, runner, spec, secondProvider); err != nil {
		t.Fatalf("allowed run: %v", err)
	}
	if handlerCalls != 1 {
		t.Fatalf("handlerCalls after allowed run = %d, want 1", handlerCalls)
	}
}
