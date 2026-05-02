package rules

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryCRUDAndCheck(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	reg := NewRepoRegistry(repo, nil)

	rule, err := reg.AddWithOptions(context.Background(), AddRequest{
		Text: "never call tool delete_branch with name matching ^(staging|dev|prod)$",
	})
	if err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}
	if rule.EnforcementStrategy != StrategyArgumentValidate {
		t.Fatalf("strategy = %q, want %q", rule.EnforcementStrategy, StrategyArgumentValidate)
	}

	list, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(List) = %d, want 1", len(list))
	}
	if list[0].Name == "" {
		t.Fatal("rule name should be populated")
	}

	blocked, err := reg.Check(context.Background(), "delete_branch", json.RawMessage(`{"name":"staging"}`), CheckContext{RepoRoot: repo, TaskID: "t1"})
	if err != nil {
		t.Fatalf("Check blocked: %v", err)
	}
	if blocked.Verdict != VerdictBlock {
		t.Fatalf("blocked verdict = %q, want %q", blocked.Verdict, VerdictBlock)
	}

	allowed, err := reg.Check(context.Background(), "delete_branch", json.RawMessage(`{"name":"feature/foo"}`), CheckContext{RepoRoot: repo, TaskID: "t2"})
	if err != nil {
		t.Fatalf("Check allowed: %v", err)
	}
	if allowed.Verdict != VerdictPass {
		t.Fatalf("allowed verdict = %q, want %q", allowed.Verdict, VerdictPass)
	}

	got, err := reg.Get(rule.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ImpactMetrics.Blocked != 1 {
		t.Fatalf("blocked metrics = %d, want 1", got.ImpactMetrics.Blocked)
	}
	if got.ImpactMetrics.Allowed != 1 {
		t.Fatalf("allowed metrics = %d, want 1", got.ImpactMetrics.Allowed)
	}

	if err := reg.Pause(rule.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	paused, err := reg.Get(rule.ID)
	if err != nil {
		t.Fatalf("Get paused: %v", err)
	}
	if paused.Status != StatusPaused {
		t.Fatalf("paused status = %q, want %q", paused.Status, StatusPaused)
	}

	if err := reg.Resume(rule.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	resumed, err := reg.Get(rule.ID)
	if err != nil {
		t.Fatalf("Get resumed: %v", err)
	}
	if resumed.Status != StatusActive {
		t.Fatalf("resumed status = %q, want %q", resumed.Status, StatusActive)
	}

	if err := reg.Delete(rule.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, err = reg.List()
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("len(List after delete) = %d, want 0", len(list))
	}

	for _, rel := range []string{filepath.Join(".r1", "rules.json"), filepath.Join(".stoke", "rules.json")} {
		if _, err := os.Stat(filepath.Join(repo, rel)); err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
	}
}

func TestRegistryResolveAndDisableByName(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	reg := NewRepoRegistry(repo, nil)

	rule, err := reg.AddWithOptions(context.Background(), AddRequest{
		Name: "block-prod-branch",
		Text: "never call tool delete_branch with name matching ^prod$",
	})
	if err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	resolved, err := reg.Resolve("block-prod-branch")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.ID != rule.ID {
		t.Fatalf("Resolve ID = %q, want %q", resolved.ID, rule.ID)
	}

	if err := reg.Disable("block-prod-branch"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	disabled, err := reg.Resolve(rule.ID)
	if err != nil {
		t.Fatalf("Resolve disabled: %v", err)
	}
	if disabled.Status != StatusPaused {
		t.Fatalf("status = %q, want %q", disabled.Status, StatusPaused)
	}
}

func TestRegistryCommandRegexRuleBlocksExecBash(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	reg := NewRepoRegistry(repo, nil)

	rule, err := reg.AddWithOptions(context.Background(), AddRequest{
		Text:       "never call exec_bash with cmd /^rm -rf/",
		ToolFilter: "^exec_bash$",
	})
	if err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}
	if rule.EnforcementStrategy != StrategyArgumentValidate {
		t.Fatalf("strategy = %q, want %q", rule.EnforcementStrategy, StrategyArgumentValidate)
	}

	blocked, err := reg.Check(context.Background(), "exec_bash", json.RawMessage(`{"cmd":"rm -rf /tmp/foo"}`), CheckContext{RepoRoot: repo, TaskID: "t-block"})
	if err != nil {
		t.Fatalf("Check blocked: %v", err)
	}
	if blocked.Verdict != VerdictBlock {
		t.Fatalf("blocked verdict = %q, want %q", blocked.Verdict, VerdictBlock)
	}

	allowed, err := reg.Check(context.Background(), "exec_bash", json.RawMessage(`{"cmd":"echo safe"}`), CheckContext{RepoRoot: repo, TaskID: "t-allow"})
	if err != nil {
		t.Fatalf("Check allowed: %v", err)
	}
	if allowed.Verdict != VerdictPass {
		t.Fatalf("allowed verdict = %q, want %q", allowed.Verdict, VerdictPass)
	}
}
