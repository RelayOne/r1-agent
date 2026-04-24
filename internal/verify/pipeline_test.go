package verify

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/env"
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
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0o600)

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

// --- Env dispatch tests ---

// mockEnv is a test double for env.Environment that records exec calls.
type mockEnv struct {
	execCalls [][]string
	execFn    func(cmd []string) (*env.ExecResult, error)
}

func (m *mockEnv) Provision(_ context.Context, _ env.Spec) (*env.Handle, error) {
	return &env.Handle{ID: "mock-1", Backend: "mock", WorkDir: "/workspace", CreatedAt: time.Now()}, nil
}
func (m *mockEnv) Exec(_ context.Context, _ *env.Handle, cmd []string, _ env.ExecOpts) (*env.ExecResult, error) {
	m.execCalls = append(m.execCalls, cmd)
	if m.execFn != nil {
		return m.execFn(cmd)
	}
	return &env.ExecResult{ExitCode: 0, Stdout: "ok\n"}, nil
}
func (m *mockEnv) CopyIn(_ context.Context, _ *env.Handle, _, _ string) error {
	return nil
}
func (m *mockEnv) CopyOut(_ context.Context, _ *env.Handle, _, _ string) error {
	return nil
}
func (m *mockEnv) Service(_ context.Context, _ *env.Handle, _ string) (env.ServiceAddr, error) {
	return env.ServiceAddr{}, env.ErrServiceNotFound
}
func (m *mockEnv) Teardown(_ context.Context, _ *env.Handle) error { return nil }
func (m *mockEnv) Cost(_ context.Context, _ *env.Handle) (env.CostEstimate, error) {
	return env.CostEstimate{}, nil
}

func TestRunInEnvSuccess(t *testing.T) {
	mock := &mockEnv{}
	h := &env.Handle{ID: "test-env", WorkDir: "/workspace"}

	p := NewPipeline("go build ./...", "go test ./...", "go vet ./...")
	ep := p.WithEnvironment(mock, h)

	outcomes, err := ep.Run(context.Background(), "/ignored")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// All 3 commands should have been dispatched to the env.
	if len(mock.execCalls) != 3 {
		t.Fatalf("exec calls=%d, want 3", len(mock.execCalls))
	}

	for i, o := range outcomes {
		if !o.Success {
			t.Errorf("outcome[%d] %s failed", i, o.Name)
		}
		if o.Skipped {
			t.Errorf("outcome[%d] should not be skipped", i)
		}
	}

	// Verify commands were wrapped in bash -lc.
	for _, call := range mock.execCalls {
		if len(call) < 3 || call[0] != "bash" || call[1] != "-lc" {
			t.Errorf("call should be [bash -lc <cmd>], got %v", call)
		}
	}
}

func TestRunInEnvBuildFailure(t *testing.T) {
	mock := &mockEnv{
		execFn: func(cmd []string) (*env.ExecResult, error) {
			if strings.Contains(strings.Join(cmd, " "), "build") {
				return &env.ExecResult{ExitCode: 1, Stderr: "build error"}, nil
			}
			return &env.ExecResult{ExitCode: 0, Stdout: "ok"}, nil
		},
	}
	h := &env.Handle{ID: "test-env", WorkDir: "/workspace"}

	p := NewPipeline("go build", "go test", "").WithEnvironment(mock, h)
	outcomes, err := p.Run(context.Background(), "/ignored")
	if err == nil {
		t.Fatal("expected error for failing build")
	}
	if outcomes[0].Success {
		t.Error("build should have failed")
	}
}

func TestRunInEnvExecError(t *testing.T) {
	mock := &mockEnv{
		execFn: func(cmd []string) (*env.ExecResult, error) {
			return nil, fmt.Errorf("connection lost")
		},
	}
	h := &env.Handle{ID: "test-env", WorkDir: "/workspace"}

	p := NewPipeline("go build", "", "").WithEnvironment(mock, h)
	outcomes, err := p.Run(context.Background(), "/ignored")
	if err == nil {
		t.Fatal("expected error")
	}
	if outcomes[0].Success {
		t.Error("build should have failed")
	}
	if !strings.Contains(outcomes[0].Output, "env exec error") {
		t.Errorf("output should mention env exec error: %s", outcomes[0].Output)
	}
}

func TestWithEnvironmentPreservesCommands(t *testing.T) {
	p := NewPipeline("build-cmd", "test-cmd", "lint-cmd")
	ep := p.WithEnvironment(&mockEnv{}, &env.Handle{})

	b, te, l := ep.Commands()
	if b != "build-cmd" || te != "test-cmd" || l != "lint-cmd" {
		t.Errorf("commands not preserved: build=%q test=%q lint=%q", b, te, l)
	}
}

func TestRunInEnvSkipsEmpty(t *testing.T) {
	mock := &mockEnv{}
	h := &env.Handle{ID: "test-env", WorkDir: "/workspace"}

	p := NewPipeline("go build", "", "").WithEnvironment(mock, h)
	outcomes, err := p.Run(context.Background(), "/ignored")
	if err != nil {
		t.Fatal(err)
	}

	// Only build should have been dispatched to env.
	if len(mock.execCalls) != 1 {
		t.Errorf("exec calls=%d, want 1 (only build)", len(mock.execCalls))
	}
	if !outcomes[1].Skipped {
		t.Error("test should be skipped when empty")
	}
	if !outcomes[2].Skipped {
		t.Error("lint should be skipped when empty")
	}
}

func TestRunFallsBackToLocalWithoutEnv(t *testing.T) {
	// Pipeline without WithEnvironment should use local exec.
	p := NewPipeline("echo local-build", "", "")
	outcomes, err := p.Run(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outcomes[0].Output, "local-build") {
		t.Errorf("should run locally: %s", outcomes[0].Output)
	}
}
