package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/config"
	"github.com/RelayOne/r1/internal/engine"
	"github.com/RelayOne/r1/internal/model"
	"github.com/RelayOne/r1/internal/repomap"
	"github.com/RelayOne/r1/internal/taskstate"
	"github.com/RelayOne/r1/internal/verify"
	"github.com/RelayOne/r1/internal/wisdom"
)

// TestExecutePromptCarriesRepoMap is the P2 regression: executePromptWithContext
// computed e.RepoMap.RenderRelevant(...) into a ctxpack item and Pack()ed it,
// but PackResult.Included was never rendered into the returned prompt — so the
// ranked repomap (CLAUDE.md design decision 24) never reached the executor on
// the workflow path. This asserts a non-nil RepoMap yields an execute prompt
// that contains a RenderRelevant symbol.
func TestExecutePromptCarriesRepoMap(t *testing.T) {
	repo := initTestRepo(t)

	// Build a repomap from a small Go tree with distinctive symbols that
	// could not appear in the execute prompt by any path other than repomap
	// injection.
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "widget.go"), []byte(`package widget

type ZzWidgetConfig struct {
	Name string
}

func ZzBuildWidget(name string) *ZzWidgetConfig {
	return &ZzWidgetConfig{Name: name}
}
`), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	rm, err := repomap.Build(srcDir)
	if err != nil {
		t.Fatalf("repomap.Build: %v", err)
	}
	// Sanity: the map itself renders the symbol (isolates the seam under test
	// from a broken fixture).
	if got := rm.RenderRelevant(nil, 2000); !strings.Contains(got, "ZzBuildWidget") {
		t.Fatalf("fixture repomap does not render ZzBuildWidget; got:\n%s", got)
	}

	mock := newMockRunner()
	mock.FilesToWrite = map[string]string{
		"main.go": "package main\n\nfunc main() {}\n",
	}

	policy := config.DefaultPolicy()
	policy.Verification.Build = false
	policy.Verification.Tests = false
	policy.Verification.Lint = false
	policy.Verification.ScopeCheck = false
	policy.Verification.CrossModelReview = false

	wf := Engine{
		RepoRoot:       repo,
		Task:           "Add a handler",
		TaskType:       model.TaskTypeRefactor,
		WorktreeName:   "p2-repomap",
		AuthMode:       engine.AuthModeMode1,
		Policy:         policy,
		Worktrees:      stubManager{repo: repo},
		Runners:        engine.Registry{Claude: engine.NewClaudeRunner("claude")},
		Verifier:       verify.NewPipeline("", "", ""),
		State:          taskstate.NewTaskState("p2-repomap"),
		Wisdom:         wisdom.NewStore(),
		RepoMap:        rm,
		RepoMapBudget:  2000,
		RunnerOverride: mock,
	}

	if _, err := wf.Run(context.Background()); err != nil && !strings.Contains(err.Error(), "merge") {
		t.Fatalf("workflow failed (non-merge): %v", err)
	}

	execPrompts := mock.Prompts["execute"]
	if len(execPrompts) == 0 {
		t.Fatal("execute phase never ran; cannot verify repomap injection")
	}
	first := execPrompts[0]
	if !strings.Contains(first, "ZzBuildWidget") && !strings.Contains(first, "ZzWidgetConfig") {
		t.Errorf("execute prompt does not contain a RenderRelevant symbol; prompt:\n%s", first)
	}
}
