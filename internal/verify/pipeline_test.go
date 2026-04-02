package verify

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunSkipsUnconfigured(t *testing.T) {
	p := NewPipeline("", "", "")
	outcomes, err := p.Run(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range outcomes {
		if !o.Skipped {
			t.Errorf("%s should be skipped", o.Name)
		}
		if !o.Success {
			t.Errorf("%s skipped should still be success", o.Name)
		}
	}
}

func TestRunBuildSuccess(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0644)

	p := NewPipeline("echo build-ok", "echo test-ok", "echo lint-ok")
	outcomes, err := p.Run(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 3 {
		t.Fatalf("outcomes=%d", len(outcomes))
	}
	for _, o := range outcomes {
		if !o.Success {
			t.Errorf("%s failed: %s", o.Name, o.Output)
		}
	}
}

func TestRunBuildFailure(t *testing.T) {
	p := NewPipeline("false", "", "")
	outcomes, err := p.Run(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("expected error for failing build")
	}
	if outcomes[0].Success {
		t.Error("build should have failed")
	}
}

func TestAnalyzeOutcomesAllPass(t *testing.T) {
	outcomes := []Outcome{
		{Name: "build", Success: true},
		{Name: "test", Success: true},
		{Name: "lint", Success: true},
	}
	a := AnalyzeOutcomes(outcomes)
	if a != nil {
		t.Errorf("expected nil analysis for all-pass, got %v", a)
	}
}

func TestAnalyzeOutcomesBuildFail(t *testing.T) {
	outcomes := []Outcome{
		{Name: "build", Success: false, Output: "src/main.ts(10,5): error TS2339: Property 'x' does not exist"},
		{Name: "test", Skipped: true, Success: true},
	}
	a := AnalyzeOutcomes(outcomes)
	if a == nil {
		t.Fatal("expected analysis")
	}
	if a.Class != "BuildFailed" {
		t.Errorf("class=%q", a.Class)
	}
}

func TestHasCommands(t *testing.T) {
	if NewPipeline("", "", "").HasCommands() {
		t.Error("empty pipeline should not have commands")
	}
	if !NewPipeline("go build", "", "").HasCommands() {
		t.Error("pipeline with build should have commands")
	}
}

func TestCheckProtectedFiles(t *testing.T) {
	protected := []string{".claude/", ".stoke/", "CLAUDE.md", ".env*", "stoke.policy.yaml"}

	tests := []struct {
		file string
		want bool
	}{
		{".claude/settings.json", true},
		{".stoke/session.json", true},
		{"CLAUDE.md", true},
		{".env", true},
		{".env.local", true},
		{".env.production", true},
		{"stoke.policy.yaml", true},
		{"src/auth.ts", false},
		{"README.md", false},
	}

	for _, tt := range tests {
		violations := CheckProtectedFiles([]string{tt.file}, protected)
		got := len(violations) > 0
		if got != tt.want {
			t.Errorf("CheckProtectedFiles(%q) = %v, want %v", tt.file, got, tt.want)
		}
	}
}

func TestCheckScope(t *testing.T) {
	allowed := []string{"src/auth/middleware.ts", "src/types/auth.ts"}
	modified := []string{"src/auth/middleware.ts", "src/routes/index.ts"}

	violations := CheckScope(modified, allowed)
	if len(violations) != 1 || violations[0] != "src/routes/index.ts" {
		t.Errorf("violations=%v, want [src/routes/index.ts]", violations)
	}
}

func TestCheckScopeStrictNoSiblings(t *testing.T) {
	// Declaring "src/auth/middleware.ts" must NOT allow "src/auth/types.ts"
	allowed := []string{"src/auth/middleware.ts"}
	modified := []string{"src/auth/middleware.ts", "src/auth/types.ts"}

	violations := CheckScope(modified, allowed)
	if len(violations) != 1 || violations[0] != "src/auth/types.ts" {
		t.Errorf("strict scope: violations=%v, want [src/auth/types.ts]", violations)
	}
}

func TestCheckScopeDirGrant(t *testing.T) {
	// Trailing "/" grants entire directory
	allowed := []string{"src/auth/"}
	modified := []string{"src/auth/middleware.ts", "src/auth/types.ts", "src/routes/index.ts"}

	violations := CheckScope(modified, allowed)
	if len(violations) != 1 || violations[0] != "src/routes/index.ts" {
		t.Errorf("dir grant: violations=%v, want [src/routes/index.ts]", violations)
	}
}

func TestCheckScopeNoRestriction(t *testing.T) {
	violations := CheckScope([]string{"anything.ts"}, nil)
	if len(violations) != 0 {
		t.Errorf("no restriction should return no violations")
	}
}
