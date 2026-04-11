package plan

import (
	"strings"
	"testing"
)

func TestScrubCommand_StripsRepoUrlGitClone(t *testing.T) {
	in := `cd $(mktemp -d) && git clone $REPO_URL . && pnpm install && pnpm build --filter=@sentinel/types`
	out, changes := scrubCommand(in)
	if strings.Contains(out, "REPO_URL") {
		t.Errorf("still contains REPO_URL: %q", out)
	}
	if strings.Contains(out, "git clone") {
		t.Errorf("still contains git clone: %q", out)
	}
	if !strings.Contains(out, "pnpm install") || !strings.Contains(out, "pnpm build") {
		t.Errorf("lost real commands: %q", out)
	}
	if len(changes) == 0 {
		t.Error("expected change diagnostic")
	}
}

func TestScrubCommand_StripsOrEchoOK(t *testing.T) {
	in := `cd apps/web && pnpm exec axe . --exit 2>/dev/null || echo 'axe check passed'`
	out, changes := scrubCommand(in)
	if strings.Contains(out, "echo 'axe check passed'") {
		t.Errorf("still contains fallback: %q", out)
	}
	if !strings.Contains(out, "axe . --exit") {
		t.Errorf("lost real command: %q", out)
	}
	if len(changes) < 1 {
		t.Error("expected changes")
	}
}

func TestScrubCommand_StripsOrTrue(t *testing.T) {
	in := `test -d node_modules || true`
	out, _ := scrubCommand(in)
	if strings.Contains(out, "|| true") {
		t.Errorf("still contains || true: %q", out)
	}
	if !strings.Contains(out, "test -d node_modules") {
		t.Errorf("lost real command: %q", out)
	}
}

func TestScrubCommand_UnwrapsNpx(t *testing.T) {
	in := `cd apps/web && npx next build`
	out, changes := scrubCommand(in)
	if strings.Contains(out, "npx") {
		t.Errorf("still contains npx: %q", out)
	}
	if !strings.Contains(out, "next build") {
		t.Errorf("lost the real command: %q", out)
	}
	if len(changes) == 0 {
		t.Error("expected changes")
	}
}

func TestScrubCommand_UnwrapsPnpmExec(t *testing.T) {
	in := `pnpm exec vitest run`
	out, _ := scrubCommand(in)
	if strings.Contains(out, "pnpm exec") {
		t.Errorf("still contains pnpm exec: %q", out)
	}
	if !strings.Contains(out, "vitest run") {
		t.Errorf("lost real command: %q", out)
	}
}

func TestScrubCommand_NoopOnCleanCommand(t *testing.T) {
	in := `pnpm install && pnpm build --filter=@sentinel/types`
	out, changes := scrubCommand(in)
	if out != in {
		t.Errorf("clean command mutated: %q -> %q", in, out)
	}
	if len(changes) > 0 {
		t.Errorf("clean command produced changes: %v", changes)
	}
}

func TestScrubSOW_AppliesAcrossSessions(t *testing.T) {
	sow := &SOW{
		ID:   "test",
		Name: "test",
		Sessions: []Session{
			{
				ID:    "S1",
				Title: "Foundation",
				Tasks: []Task{{ID: "T1", Description: "init"}},
				AcceptanceCriteria: []AcceptanceCriterion{
					{ID: "AC1", Description: "build", Command: "cd $(mktemp -d) && git clone $REPO_URL . && pnpm install"},
					{ID: "AC2", Description: "lint", Command: "pnpm lint || echo ok"},
				},
			},
			{
				ID:    "S2",
				Title: "Core",
				Tasks: []Task{{ID: "T2", Description: "core"}},
				AcceptanceCriteria: []AcceptanceCriterion{
					{ID: "AC3", Description: "clean", Command: "pnpm build"},
				},
			},
		},
	}
	_, diag := ScrubSOW(sow)
	if len(diag) < 2 {
		t.Errorf("expected >=2 diagnostic lines, got %d: %v", len(diag), diag)
	}
	if strings.Contains(sow.Sessions[0].AcceptanceCriteria[0].Command, "REPO_URL") {
		t.Error("S1/AC1 not scrubbed")
	}
	if strings.Contains(sow.Sessions[0].AcceptanceCriteria[1].Command, "|| echo") {
		t.Error("S1/AC2 not scrubbed")
	}
	// S2/AC3 was already clean; diagnostic should not mention it.
	for _, d := range diag {
		if strings.HasPrefix(d, "S2/AC3") {
			t.Errorf("S2/AC3 was clean but got diagnostic: %q", d)
		}
	}
}
