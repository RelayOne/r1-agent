package workflow

import (
	"testing"

	"github.com/RelayOne/r1/internal/intent"
)

func TestThinkingBudgetForClass(t *testing.T) {
	cases := []struct {
		class intent.Class
		want  int
	}{
		{intent.ClassTrivial, 0},
		{intent.ClassExplicit, 0},
		{intent.ClassExploratory, 4096},
		{intent.ClassAmbiguous, 4096},
		{intent.ClassOpenEnded, 8192},
		{intent.Class("unknown"), 0},
	}
	for _, tc := range cases {
		if got := thinkingBudgetForClass(tc.class); got != tc.want {
			t.Errorf("thinkingBudgetForClass(%s)=%d, want %d", tc.class, got, tc.want)
		}
	}
}

func TestBuildPhasesThinkingBudget(t *testing.T) {
	// Exploratory task (question mark → ClassExploratory): plan and
	// execute get the budget, verify never does.
	e := Engine{Task: "Why does the cache miss on every second request?"}
	phases := buildPhases(e)
	if len(phases) != 3 {
		t.Fatalf("phases=%d, want 3", len(phases))
	}
	byName := map[string]int{}
	for _, p := range phases {
		byName[p.Name] = p.ThinkingBudget
	}
	if byName["plan"] != 4096 {
		t.Errorf("plan budget=%d, want 4096", byName["plan"])
	}
	if byName["execute"] != 4096 {
		t.Errorf("execute budget=%d, want 4096", byName["execute"])
	}
	if byName["verify"] != 0 {
		t.Errorf("verify budget=%d, want 0 (reviews run without thinking)", byName["verify"])
	}

	// Trivial task: no phase gets a budget.
	e2 := Engine{Task: "fix typo in readme"}
	for _, p := range buildPhases(e2) {
		if p.ThinkingBudget != 0 {
			t.Errorf("trivial task: phase %s budget=%d, want 0", p.Name, p.ThinkingBudget)
		}
	}
}
