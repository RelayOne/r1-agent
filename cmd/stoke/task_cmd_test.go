package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/RelayOne/r1-agent/internal/executor"
	"github.com/RelayOne/r1-agent/internal/router"
)

// newTestRouter builds a router with every scaffolding executor
// registered plus a CodeExecutor rooted at /tmp. Mirrors the
// production buildDefaultTaskRouter but does not touch os.Getwd.
func newTestRouter() *router.Router {
	r := router.New()
	r.Register(executor.TaskCode, executor.NewCodeExecutor("/tmp"))
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

func TestRunTaskCmdCodeHint(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runTaskCmd([]string{"refactor", "the", "sessions", "package"}, &out, &errBuf, newTestRouter())
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (stderr=%q)", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "stoke ship") {
		t.Errorf("code path should hint at `stoke ship`; got %q", out.String())
	}
}

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
