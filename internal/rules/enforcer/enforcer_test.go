package enforcer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/RelayOne/r1/internal/rules"
	"github.com/RelayOne/r1/internal/rules/monitor"
)

func TestEnforcerCheckRecordsDecision(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	reg := rules.NewRepoRegistry(repo, nil)
	if _, err := reg.AddWithOptions(context.Background(), rules.AddRequest{
		Name: "block-prod",
		Text: "never call tool delete_branch with name matching ^prod$",
	}); err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	enf := &Enforcer{
		Registry: reg,
		Monitor:  monitor.NewRepo(repo),
	}
	result, err := enf.Check(context.Background(), "delete_branch", json.RawMessage(`{"name":"prod"}`), rules.CheckContext{RepoRoot: repo})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.Verdict != rules.VerdictBlock {
		t.Fatalf("verdict = %q, want %q", result.Verdict, rules.VerdictBlock)
	}

	decisions, err := monitor.NewRepo(repo).List(10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("len(List) = %d, want 1", len(decisions))
	}
	if !decisions[0].Blocked {
		t.Fatal("decision should be marked blocked")
	}
}
