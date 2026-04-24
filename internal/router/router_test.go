package router

import (
	"errors"
	"testing"

	"github.com/RelayOne/r1/internal/executor"
)

func TestDefaultClassifier(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  executor.TaskType
	}{
		// Deploy matches.
		{"deploy verb", "deploy the app to fly.io", executor.TaskDeploy},
		{"provision verb", "please provision the staging env", executor.TaskDeploy},
		{"vercel hint", "push a vercel release", executor.TaskDeploy},
		{"docker compose", "spin up docker compose", executor.TaskDeploy},

		// Research matches.
		{"research verb", "research the best embedding models", executor.TaskResearch},
		{"compare verb", "compare redis and dragonfly", executor.TaskResearch},
		{"what is", "what is SSE back-pressure?", executor.TaskResearch},
		{"how does x work", "how does prompt caching work in anthropic", executor.TaskResearch},

		// Browser matches.
		{"browse verb", "browse news.ycombinator.com and summarize", executor.TaskBrowser},
		{"screenshot", "take a screenshot of example.com", executor.TaskBrowser},
		{"click verb", "click the signup button", executor.TaskBrowser},

		// Delegate matches.
		{"hire verb", "hire a translator for the marketing copy", executor.TaskDelegate},
		{"generate image", "generate image for the homepage", executor.TaskDelegate},
		{"outsource", "outsource this transcription job", executor.TaskDelegate},

		// Code defaults and explicit hints.
		{"default code", "refactor the sessions package", executor.TaskCode},
		{"sow path", "build from specs/task-19.md (sow)", executor.TaskCode},
		{"spec path", "implement spec in .md", executor.TaskCode},
		{"negative deploy look-alike", "deployment_test.go is red", executor.TaskCode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DefaultClassifier(tc.input)
			if got != tc.want {
				t.Errorf("DefaultClassifier(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// fakeRouterExec embeds CodeExecutor to inherit the Executor
// method set, then overrides TaskType so the router sees the
// intended classification. Router tests never call Execute — they
// only exercise Register/Dispatch/Classify paths.
type fakeRouterExec struct {
	*executor.CodeExecutor
	tt executor.TaskType
}

func (f *fakeRouterExec) TaskType() executor.TaskType { return f.tt }

func newFake(t executor.TaskType) executor.Executor {
	return &fakeRouterExec{CodeExecutor: executor.NewCodeExecutor(""), tt: t}
}

func TestRouterRegisterAndDispatch(t *testing.T) {
	r := New()
	r.Register(executor.TaskResearch, newFake(executor.TaskResearch))

	e, tt, err := r.Dispatch("research the best embedding models")
	if err != nil {
		t.Fatalf("Dispatch err = %v, want nil", err)
	}
	if tt != executor.TaskResearch {
		t.Errorf("Dispatch returned TaskType = %v, want TaskResearch", tt)
	}
	if e == nil || e.TaskType() != executor.TaskResearch {
		t.Errorf("Dispatch returned executor = %+v, want TaskResearch executor", e)
	}
}

func TestRouterDispatchNoExecutor(t *testing.T) {
	r := New()
	// Empty map — classification succeeds but nothing is registered.
	e, tt, err := r.Dispatch("deploy to fly.io")
	if !errors.Is(err, ErrNoExecutor) {
		t.Fatalf("Dispatch err = %v, want ErrNoExecutor", err)
	}
	if e != nil {
		t.Errorf("Dispatch executor = %+v, want nil", e)
	}
	if tt != executor.TaskDeploy {
		t.Errorf("Dispatch TaskType = %v, want TaskDeploy", tt)
	}
}

func TestRouterDispatchEmpty(t *testing.T) {
	r := New()
	_, tt, err := r.Dispatch("   ")
	if !errors.Is(err, ErrEmptyInput) {
		t.Fatalf("Dispatch err = %v, want ErrEmptyInput", err)
	}
	if tt != executor.TaskUnknown {
		t.Errorf("Dispatch TaskType = %v, want TaskUnknown", tt)
	}
}

func TestRouterClassifyEmpty(t *testing.T) {
	r := New()
	if got := r.Classify(""); got != executor.TaskUnknown {
		t.Errorf("Classify(\"\") = %v, want TaskUnknown", got)
	}
	if got := r.Classify("   \t\n"); got != executor.TaskUnknown {
		t.Errorf("Classify(whitespace) = %v, want TaskUnknown", got)
	}
}

func TestRouterSetClassifier(t *testing.T) {
	r := New()
	// Force every input into TaskBrowser.
	r.SetClassifier(func(_ string) executor.TaskType { return executor.TaskBrowser })
	if got := r.Classify("refactor the sessions package"); got != executor.TaskBrowser {
		t.Errorf("custom classifier ignored; got %v want TaskBrowser", got)
	}
	// nil resets to default.
	r.SetClassifier(nil)
	if got := r.Classify("deploy to fly.io"); got != executor.TaskDeploy {
		t.Errorf("SetClassifier(nil) should restore default; got %v", got)
	}
}

func TestRouterRegisterLastWriteWins(t *testing.T) {
	r := New()
	first := newFake(executor.TaskCode)
	second := newFake(executor.TaskCode)
	r.Register(executor.TaskCode, first)
	r.Register(executor.TaskCode, second)
	got, _, err := r.Dispatch("refactor session.go")
	if err != nil {
		t.Fatalf("Dispatch err = %v", err)
	}
	if got != second {
		t.Errorf("Dispatch returned %p, want last-registered %p", got, second)
	}
}

