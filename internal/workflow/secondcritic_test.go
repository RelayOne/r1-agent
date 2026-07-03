package workflow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/RelayOne/r1/internal/config"
	"github.com/RelayOne/r1/internal/engine"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/model"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
	"github.com/RelayOne/r1/internal/taskstate"
	"github.com/RelayOne/r1/internal/verify"
	"github.com/RelayOne/r1/internal/worktree"
)

// fakeCritic returns a canned verdict (or error) and records the input
// it was challenged with.
type fakeCritic struct {
	mu      sync.Mutex
	verdict *CriticVerdict
	err     error
	calls   int
	lastIn  CriticInput
}

func (f *fakeCritic) Challenge(_ context.Context, in CriticInput) (*CriticVerdict, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastIn = in
	if f.err != nil {
		return nil, f.err
	}
	return f.verdict, nil
}

// reviewAwareRunner emits the Read tool-use shape (Input key
// "file_path") the review quality gate tracks, which the base mock
// predates, so the cross-model review path can complete.
type reviewAwareRunner struct {
	*mockRunner
}

func (r *reviewAwareRunner) Run(ctx context.Context, spec engine.RunSpec, onEvent engine.OnEventFunc) (engine.RunResult, error) {
	if spec.Phase.Name == "verify" && onEvent != nil {
		onEvent(stream.Event{
			Type: "assistant",
			ToolUses: []stream.ToolUse{
				{Name: "Read", Input: map[string]interface{}{"file_path": "main.go"}},
			},
		})
	}
	return r.mockRunner.Run(ctx, spec, onEvent)
}

// newCriticEngine builds a full pipeline Engine against a real temp git
// repo with the cross-model review ON so runCrossModelReview executes.
func newCriticEngine(t *testing.T, critic SecondCritic, secondOpinion bool, bus *hub.Bus) (Engine, *mockRunner) {
	t.Helper()
	repo := initTestRepo(t)
	mock := newMockRunner()
	mock.FilesToWrite = map[string]string{
		"main.go": "package main\n\nfunc main() {}\n",
	}

	policy := config.DefaultPolicy()
	policy.Verification.Build = false
	policy.Verification.Tests = false
	policy.Verification.Lint = false
	policy.Verification.ScopeCheck = false
	policy.Verification.CrossModelReview = true
	policy.Verification.SecondOpinion = secondOpinion

	return Engine{
		RepoRoot:       repo,
		Task:           "Investigate the session model and add user authentication with token refresh, updating middleware, storage, and tests",
		TaskType:       model.TaskTypeRefactor,
		WorktreeName:   "critic-test",
		AuthMode:       engine.AuthModeMode1,
		Policy:         policy,
		Worktrees:      worktree.NewManager(repo),
		Runners:        engine.Registry{Claude: engine.NewClaudeRunner("claude")},
		Verifier:       verify.NewPipeline("", "", ""),
		State:          taskstate.NewTaskState("critic-test"),
		SecondCritic:   critic,
		EventBus:       bus,
		RunnerOverride: &reviewAwareRunner{mockRunner: mock},
	}, mock
}

func TestSecondCriticBlockingDissentStopsMerge(t *testing.T) {
	critic := &fakeCritic{verdict: &CriticVerdict{
		Dissent:         true,
		Severity:        "blocking",
		Reasoning:       "auth token never refreshed on expiry",
		RequestedChange: "wire the refresh path",
		Findings:        []CriticFinding{{Severity: "blocking", File: "main.go", Message: "missing refresh"}},
	}}
	wf, _ := newCriticEngine(t, critic, true, nil)

	_, err := wf.Run(context.Background())
	if err == nil {
		t.Fatal("expected blocking dissent to fail the run")
	}
	if !strings.Contains(err.Error(), "second-opinion dissent") {
		t.Errorf("error = %v, want second-opinion dissent", err)
	}
	if phase := wf.State.Phase(); phase != taskstate.Failed {
		t.Errorf("state = %v, want Failed", phase)
	}
	if critic.calls != 1 {
		t.Errorf("critic calls = %d, want 1", critic.calls)
	}
	// The critic must have seen the real change surface.
	if len(critic.lastIn.Files) != 1 || critic.lastIn.Files[0] != "main.go" {
		t.Errorf("critic saw Files = %v, want [main.go]", critic.lastIn.Files)
	}
	if critic.lastIn.PrimaryVerdictJSON == "" {
		t.Error("critic did not receive the primary reviewer's verdict")
	}
}

func TestSecondCriticAdvisoryDissentProceeds(t *testing.T) {
	critic := &fakeCritic{verdict: &CriticVerdict{
		Dissent:   true,
		Severity:  "advisory",
		Reasoning: "could use more tests",
	}}
	wf, _ := newCriticEngine(t, critic, true, nil)

	if _, err := wf.Run(context.Background()); err != nil {
		t.Fatalf("advisory dissent must not block the merge: %v", err)
	}
	if phase := wf.State.Phase(); phase != taskstate.Committed {
		t.Errorf("state = %v, want Committed", phase)
	}
}

func TestSecondCriticErrorFailsClosed(t *testing.T) {
	critic := &fakeCritic{err: fmt.Errorf("provider unreachable")}
	wf, _ := newCriticEngine(t, critic, true, nil)

	_, err := wf.Run(context.Background())
	if err == nil {
		t.Fatal("expected critic error to fail closed")
	}
	if !strings.Contains(err.Error(), "second-opinion critic error") {
		t.Errorf("error = %v, want second-opinion critic error", err)
	}
	if phase := wf.State.Phase(); phase != taskstate.Failed {
		t.Errorf("state = %v, want Failed", phase)
	}
}

func TestSecondCriticNilPassesThrough(t *testing.T) {
	wf, _ := newCriticEngine(t, nil, true, nil)
	if _, err := wf.Run(context.Background()); err != nil {
		t.Fatalf("nil critic must keep single-reviewer behavior: %v", err)
	}
	if phase := wf.State.Phase(); phase != taskstate.Committed {
		t.Errorf("state = %v, want Committed", phase)
	}
}

func TestSecondCriticPolicyKillSwitch(t *testing.T) {
	critic := &fakeCritic{verdict: &CriticVerdict{
		Dissent:  true,
		Severity: "blocking",
		Findings: []CriticFinding{{File: "main.go", Message: "x"}},
	}}
	wf, _ := newCriticEngine(t, critic, false, nil) // second_opinion: false

	if _, err := wf.Run(context.Background()); err != nil {
		t.Fatalf("policy kill-switch off must skip the critic: %v", err)
	}
	if critic.calls != 0 {
		t.Errorf("critic calls = %d, want 0 with second_opinion disabled", critic.calls)
	}
}

func TestSecondCriticEmitsEventOnBlockingDissent(t *testing.T) {
	bus := hub.New()
	var mu sync.Mutex
	var got []*hub.Event
	bus.Register(hub.Subscriber{
		ID:     "test-second-opinion-recorder",
		Events: []hub.EventType{hub.EventVerifySecondOpinion},
		Mode:   hub.ModeObserve,
		Handler: func(_ context.Context, ev *hub.Event) *hub.HookResponse {
			mu.Lock()
			got = append(got, ev)
			mu.Unlock()
			return nil
		},
	})

	critic := &fakeCritic{verdict: &CriticVerdict{
		Dissent:   true,
		Severity:  "blocking",
		Reasoning: "broken edge case",
		Findings:  []CriticFinding{{File: "main.go", Message: "x"}},
	}}
	wf, _ := newCriticEngine(t, critic, true, bus)

	if _, err := wf.Run(context.Background()); err == nil {
		t.Fatal("expected blocking dissent error")
	}

	evs := waitForReviewEvents(func() []*hub.Event {
		mu.Lock()
		defer mu.Unlock()
		out := make([]*hub.Event, len(got))
		copy(out, got)
		return out
	}, 1)
	if len(evs) != 1 {
		t.Fatalf("EventVerifySecondOpinion count = %d, want 1 (must emit even on blocking dissent)", len(evs))
	}
	ev := evs[0]
	if ev.Lifecycle == nil || ev.Lifecycle.State != "dissent" || ev.Lifecycle.Entity != "second_opinion" {
		t.Errorf("Lifecycle = %+v, want second_opinion/dissent", ev.Lifecycle)
	}
	if sev, _ := ev.Custom["severity"].(string); sev != "blocking" {
		t.Errorf("Custom severity = %q, want blocking", sev)
	}
	if reason, _ := ev.Custom["reasoning"].(string); reason != "broken edge case" {
		t.Errorf("Custom reasoning = %q", reason)
	}
}

func TestNormalizeCriticVerdict(t *testing.T) {
	anchored := []CriticFinding{{File: "a.go", Message: "m"}}
	tests := []struct {
		name string
		in   CriticVerdict
		want string
	}{
		{"no dissent clears severity", CriticVerdict{Dissent: false, Severity: "blocking"}, ""},
		{"blocking with anchor kept", CriticVerdict{Dissent: true, Severity: "blocking", Findings: anchored}, "blocking"},
		{"cased blocking normalized", CriticVerdict{Dissent: true, Severity: " BLOCKING ", Findings: anchored}, "blocking"},
		{"blocking without file anchor demoted", CriticVerdict{Dissent: true, Severity: "blocking"}, "advisory"},
		{"unknown severity demoted", CriticVerdict{Dissent: true, Severity: "critical", Findings: anchored}, "advisory"},
		{"empty severity demoted", CriticVerdict{Dissent: true}, "advisory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := tt.in
			normalizeCriticVerdict(&v)
			if v.Severity != tt.want {
				t.Errorf("severity = %q, want %q", v.Severity, tt.want)
			}
		})
	}
}

// criticFakeProvider is a minimal provider.Provider for Challenge tests.
type criticFakeProvider struct {
	text string
	err  error
}

func (f *criticFakeProvider) Name() string { return "fake" }
func (f *criticFakeProvider) Chat(provider.ChatRequest) (*provider.ChatResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &provider.ChatResponse{
		Content: []provider.ResponseContent{{Type: "text", Text: f.text}},
	}, nil
}
func (f *criticFakeProvider) ChatStream(req provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	return f.Chat(req)
}

func TestLLMSecondCriticChallenge(t *testing.T) {
	in := CriticInput{Task: "t", Files: []string{"a.go"}, Diff: "diff", PrimaryEngine: "codex", PrimaryVerdictJSON: `{"pass":true}`}

	tests := []struct {
		name        string
		prov        provider.Provider
		wantErr     bool
		wantDissent bool
		wantSev     string
	}{
		{
			name:        "blocking dissent parsed",
			prov:        &criticFakeProvider{text: `{"dissent":true,"severity":"BLOCKING","reasoning":"r","requested_change":"c","findings":[{"severity":"blocking","file":"a.go","message":"m"}]}`},
			wantDissent: true,
			wantSev:     "blocking",
		},
		{
			name:        "agree parsed",
			prov:        &criticFakeProvider{text: `{"dissent":false}`},
			wantDissent: false,
			wantSev:     "",
		},
		{
			name:    "garbage output errors",
			prov:    &criticFakeProvider{text: "I think it looks fine!"},
			wantErr: true,
		},
		{
			name:    "provider error propagates",
			prov:    &criticFakeProvider{err: fmt.Errorf("boom")},
			wantErr: true,
		},
		{
			name:    "nil provider errors",
			prov:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &LLMSecondCritic{Provider: tt.prov}
			v, err := c.Challenge(context.Background(), in)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Challenge: %v", err)
			}
			if v.Dissent != tt.wantDissent || v.Severity != tt.wantSev {
				t.Errorf("verdict = %+v, want dissent=%v severity=%q", v, tt.wantDissent, tt.wantSev)
			}
		})
	}
}

func TestBuildCriticPromptBounded(t *testing.T) {
	in := CriticInput{
		Task:               "t",
		Files:              []string{"a.go"},
		Diff:               strings.Repeat("d", 64*1024),
		PrimaryEngine:      "codex",
		PrimaryVerdictJSON: strings.Repeat("v", 32*1024),
	}
	prompt := buildCriticPrompt(in)
	max := len(criticInstruction) + criticDiffCap + 2*criticVerdictCap + 4096
	if len(prompt) > max {
		t.Errorf("prompt length %d exceeds bound %d — repo-derived text not capped", len(prompt), max)
	}
	if !strings.Contains(prompt, "(truncated)") {
		t.Error("oversized blocks were not truncated")
	}
}
