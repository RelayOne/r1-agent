package mcp

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/RelayOne/r1/internal/antitrunc"
)

func mkRepoForAntiTrunc(t *testing.T, subjects []string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("commit", "--allow-empty", "-m", "initial")
	for _, s := range subjects {
		c := exec.Command("git", "commit", "--allow-empty", "-m", s)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git commit: %v\n%s", err, out)
		}
	}
	return dir
}

func TestStokeAntiTruncVerify_NoLying(t *testing.T) {
	repo := mkRepoForAntiTrunc(t, []string{"refactor: rename helper"})

	srv := &StokeServer{}
	res, err := srv.HandleToolCall("stoke_antitrunc_verify", map[string]interface{}{
		"repo_root": repo,
		"n":         5,
	})
	if err != nil {
		t.Fatalf("HandleToolCall: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(res), &payload); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, res)
	}
	if payload["lying_count"].(float64) != 0 {
		t.Errorf("lying_count = %v, want 0", payload["lying_count"])
	}
}

func TestStokeAntiTruncVerify_Lying(t *testing.T) {
	repo := mkRepoForAntiTrunc(t, []string{"feat(x): all tasks done; spec 9 done"})
	plansDir := filepath.Join(repo, "plans")
	os.MkdirAll(plansDir, 0o755)
	os.WriteFile(filepath.Join(plansDir, "build-plan.md"),
		[]byte("<!-- STATUS: in-progress -->\n- [ ] open\n"), 0o644)

	srv := &StokeServer{}
	res, err := srv.HandleToolCall("stoke_antitrunc_verify", map[string]interface{}{
		"repo_root": repo,
		"n":         5,
	})
	if err != nil {
		t.Fatalf("HandleToolCall: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(res), &payload); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, res)
	}
	if payload["lying_count"].(float64) == 0 {
		t.Errorf("expected lying_count > 0, got %v\n%s", payload["lying_count"], res)
	}
}

func TestStokeAntiTruncVerify_ToolDef(t *testing.T) {
	srv := &StokeServer{}
	defs := srv.ToolDefinitions()
	found := false
	for _, d := range defs {
		if d.Name == "stoke_antitrunc_verify" || d.Name == "r1_antitrunc_verify" {
			found = true
		}
	}
	if !found {
		t.Error("antitrunc tool not in ToolDefinitions")
	}
}

// classifyChange-level tests.

func TestClassifyAntiTruncChange_Verified(t *testing.T) {
	specRep := antitrunc.ScopeReport{
		Path:  "/x/specs/spec-7.md",
		Total: 3,
		Done:  3,
	}
	v := classifyAntiTruncChange(
		antitruncChange{SHA: "ab", Subject: "feat: spec 7 complete"},
		antitrunc.ScopeReport{},
		[]antitrunc.ScopeReport{specRep},
	)
	if v.Verdict != "verified" {
		t.Errorf("verdict = %q, want verified", v.Verdict)
	}
}

func TestClassifyAntiTruncChange_Lying(t *testing.T) {
	specRep := antitrunc.ScopeReport{
		Path:  "/x/specs/spec-7.md",
		Total: 3,
		Done:  1,
	}
	v := classifyAntiTruncChange(
		antitruncChange{SHA: "ab", Subject: "feat: spec 7 complete"},
		antitrunc.ScopeReport{},
		[]antitrunc.ScopeReport{specRep},
	)
	if v.Verdict != "lying" {
		t.Errorf("verdict = %q, want lying", v.Verdict)
	}
}
