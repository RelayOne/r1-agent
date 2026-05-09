package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/executor"
	"github.com/RelayOne/r1/internal/router"
)

// newTestRouter builds a router with every scaffolding executor
// registered plus a CodeExecutor rooted at /tmp with a fake
// ExecuteHook that returns a synthetic deliverable. Mirrors the
// production buildDefaultTaskRouter shape (CodeExecutor wired via
// ExecuteHook) but never touches os.Getwd or invokes shipCmd.
func newTestRouter() *router.Router {
	return newTestRouterWithCodeHook(func(ctx context.Context, p executor.Plan, _ executor.EffortLevel) (executor.Deliverable, error) {
		return executor.CodeDeliverable{
			RepoRoot: "/tmp",
			Diff:     "diff --git a/test b/test\n+test-stub\n",
		}, nil
	})
}

// newTestRouterWithCodeHook lets a single test inject a custom
// ExecuteHook for the CodeExecutor without re-wiring every other
// scaffolding executor.
func newTestRouterWithCodeHook(hook func(ctx context.Context, p executor.Plan, effort executor.EffortLevel) (executor.Deliverable, error)) *router.Router {
	r := router.New()
	codeExec := executor.NewCodeExecutor("/tmp")
	codeExec.ExecuteHook = hook
	r.Register(executor.TaskCode, codeExec)
	r.Register(executor.TaskResearch, &executor.ResearchExecutor{})
	r.Register(executor.TaskBrowser, &executor.BrowserExecutor{})
	r.Register(executor.TaskDeploy, &executor.DeployExecutor{})
	r.Register(executor.TaskDelegate, &executor.DelegateExecutor{})
	return r
}

func TestRunTaskCmdClassifyOnly(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runTaskCmd([]string{"--classify-only", "research", "the", "best", "embedding", "models"}, &out, &errBuf, newTestRouter())
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, errBuf.String())
	}
	if got := out.String(); !strings.Contains(got, "task type: research") {
		t.Errorf("stdout = %q, want to contain 'task type: research'", got)
	}
}

func TestRunTaskCmdDispatchNotWired(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runTaskCmd([]string{"deploy", "to", "fly.io"}, &out, &errBuf, newTestRouter())
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (stderr=%q)", code, errBuf.String())
	}
	got := out.String()
	if !strings.Contains(got, "classified as deploy") {
		t.Errorf("stdout = %q, want to mention 'classified as deploy'", got)
	}
	if !strings.Contains(got, "not yet wired") {
		t.Errorf("stdout = %q, want 'not yet wired' hint", got)
	}
}

// TestRunTaskCmdCodeWiredDispatch asserts that a CodeExecutor with
// an installed ExecuteHook actually runs Execute and reports the
// deliverable summary on stdout — this is the regression test for
// the ExecuteHook wiring landed in this commit.
func TestRunTaskCmdCodeWiredDispatch(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runTaskCmd([]string{"refactor", "the", "sessions", "package"}, &out, &errBuf, newTestRouter())
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, errBuf.String())
	}
	got := out.String()
	if !strings.Contains(got, "classified as code") {
		t.Errorf("stdout = %q, want to mention 'classified as code'", got)
	}
	if !strings.Contains(got, "SOW/ship pipeline") {
		t.Errorf("stdout = %q, want to mention 'SOW/ship pipeline'", got)
	}
	if !strings.Contains(got, "deliverable:") {
		t.Errorf("stdout = %q, want to print a 'deliverable:' line", got)
	}
}

// TestRunTaskCmdCodeHookCalled asserts the wiring threads the input
// description into the ExecuteHook's Plan and that an error from
// the hook surfaces as exit code 1 with the hook's error on stderr.
func TestRunTaskCmdCodeHookCalled(t *testing.T) {
	var out, errBuf bytes.Buffer
	var seenDesc string
	r := newTestRouterWithCodeHook(func(_ context.Context, p executor.Plan, _ executor.EffortLevel) (executor.Deliverable, error) {
		seenDesc = p.Task.Description
		return executor.CodeDeliverable{RepoRoot: "/tmp", Diff: "x"}, nil
	})
	code := runTaskCmd([]string{"add", "a", "logging", "helper"}, &out, &errBuf, r)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, errBuf.String())
	}
	if seenDesc != "add a logging helper" {
		t.Errorf("ExecuteHook saw description %q, want %q", seenDesc, "add a logging helper")
	}
}

// TestRunTaskCmdCodeHookError asserts that when the ExecuteHook
// returns an error the runner exits 1 and propagates the error
// text on stderr.
func TestRunTaskCmdCodeHookError(t *testing.T) {
	var out, errBuf bytes.Buffer
	r := newTestRouterWithCodeHook(func(_ context.Context, _ executor.Plan, _ executor.EffortLevel) (executor.Deliverable, error) {
		return nil, errExecuteHookSentinel
	})
	code := runTaskCmd([]string{"refactor", "x"}, &out, &errBuf, r)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr=%q)", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "execute-hook-failed") {
		t.Errorf("stderr = %q, want to contain hook error text", errBuf.String())
	}
}

// errExecuteHookSentinel is a stable sentinel for hook-error tests so
// the assertion can match without coupling to the exact error wrapping.
var errExecuteHookSentinel = newTestErr("execute-hook-failed")

func newTestErr(msg string) error { return &testErr{msg: msg} }

type testErr struct{ msg string }

func (e *testErr) Error() string { return e.msg }

func TestRunTaskCmdEmptyInput(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runTaskCmd([]string{}, &out, &errBuf, newTestRouter())
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "usage") {
		t.Errorf("stderr = %q, want usage line", errBuf.String())
	}
}

func TestRunTaskCmdWhitespaceInput(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runTaskCmd([]string{"   ", "\t"}, &out, &errBuf, newTestRouter())
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

func TestRunTaskCmdForceType(t *testing.T) {
	var out, errBuf bytes.Buffer
	// Plain code-shaped input but --type=research forces the route.
	code := runTaskCmd([]string{"--type=research", "refactor", "sessions.go"}, &out, &errBuf, newTestRouter())
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (stderr=%q)", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "classified as research") {
		t.Errorf("--type override failed: stdout = %q", out.String())
	}
}

func TestRunTaskCmdForceTypeInvalid(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runTaskCmd([]string{"--type=bogus", "anything"}, &out, &errBuf, newTestRouter())
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "unknown --type") {
		t.Errorf("stderr = %q, want 'unknown --type' diagnostic", errBuf.String())
	}
}

func TestRunTaskCmdNoExecutorRegistered(t *testing.T) {
	// Construct a router with zero executors — classification succeeds
	// but dispatch should report ErrNoExecutor with exit 2.
	r := router.New()
	var out, errBuf bytes.Buffer
	code := runTaskCmd([]string{"deploy", "to", "fly.io"}, &out, &errBuf, r)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "no executor is registered") {
		t.Errorf("stderr = %q, want 'no executor is registered'", errBuf.String())
	}
}

func TestParseTaskType(t *testing.T) {
	cases := map[string]executor.TaskType{
		"code":     executor.TaskCode,
		"CODE":     executor.TaskCode,
		" code ":   executor.TaskCode, //nolint:gocritic // mapKey: intentional whitespace tests trimming
		"research": executor.TaskResearch,
		"browser":  executor.TaskBrowser,
		"deploy":   executor.TaskDeploy,
		"delegate": executor.TaskDelegate,
		"chat":     executor.TaskChat,
		"":         executor.TaskUnknown,
		"bogus":    executor.TaskUnknown,
	}
	for in, want := range cases {
		if got := parseTaskType(in); got != want {
			t.Errorf("parseTaskType(%q) = %v, want %v", in, got, want)
		}
	}
}
