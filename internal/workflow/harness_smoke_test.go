package workflow

// harness_smoke_test.go — end-to-end harness smoke test (HARD2).
//
// This is the cross-system wiring regression net: one mission driven
// through Engine.Run with the existing mock runner, WITH the V2
// governance layer observing the hub bus and WITH the deterministic
// cortex lobes running a real round, asserting every subsystem left
// verifiable evidence:
//
//   - the task completes (plan -> execute -> verify -> review -> merge),
//     including one verification-failure retry so the failure/wisdom
//     paths are exercised, not just the happy path;
//   - governance ledger nodes are written (task lifecycle nodes,
//     review.agree from the cross-model review event, and
//     verification_evidence from the build-outcome events);
//   - a cortex round ran: Engine threads CortexEnabled into the RunSpec,
//     and the runner drives the same 4-lobe deterministic cortex the
//     native runner builds (memoryrecall, walkeeper, rulecheck,
//     antitrunc) through MidturnNote against the shared hub bus — the
//     antitrunc lobe fires on a seeded truncation phrase, proving the
//     round dispatched, the lobe published, and the workspace drained;
//   - wisdom is recorded on BOTH sides of the retry: a Gotcha from the
//     attempt-1 verification failure and a Decision from the attempt-2
//     "succeeded after retry" success path;
//   - no goroutine leaks: the goroutine count settles back to the
//     pre-test baseline (goleak-style manual check with a settle loop).

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/config"
	"github.com/RelayOne/r1/internal/cortex"
	antitrunclobe "github.com/RelayOne/r1/internal/cortex/lobes/antitrunc"
	"github.com/RelayOne/r1/internal/cortex/lobes/memoryrecall"
	"github.com/RelayOne/r1/internal/cortex/lobes/rulecheck"
	"github.com/RelayOne/r1/internal/cortex/lobes/walkeeper"
	"github.com/RelayOne/r1/internal/engine"
	"github.com/RelayOne/r1/internal/governance"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/memory"
	"github.com/RelayOne/r1/internal/model"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
	"github.com/RelayOne/r1/internal/taskstate"
	"github.com/RelayOne/r1/internal/verify"
	"github.com/RelayOne/r1/internal/wisdom"
	"github.com/RelayOne/r1/internal/worktree"
)

// smokeStubProvider satisfies provider.Provider with the smallest
// possible surface. cortex.New requires a non-nil Provider (for the
// Router and pre-warm pump); the deterministic lobes never call it, and
// the pre-warm interval is set to an hour so at most the single
// best-effort Start pre-warm touches it.
type smokeStubProvider struct{}

func (smokeStubProvider) Name() string { return "smoke-stub" }
func (smokeStubProvider) Chat(_ provider.ChatRequest) (*provider.ChatResponse, error) {
	return &provider.ChatResponse{}, nil
}
func (smokeStubProvider) ChatStream(_ provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	return &provider.ChatResponse{}, nil
}

// cortexAwareRunner wraps the existing mockRunner and, mirroring the
// native runner's production wiring (engine.buildDeterministicCortex +
// the spec.CortexEnabled gate in NativeRunner.Run), builds the 4-lobe
// deterministic cortex on the SHARED hub bus and drives one round via
// MidturnNote during each execute phase. It also emits the Read
// tool-use event shape the review quality gate tracks (Input key
// "file_path"), which the base mock predates.
type cortexAwareRunner struct {
	*mockRunner
	t   *testing.T
	bus *hub.Bus

	mu               sync.Mutex
	sawCortexEnabled bool
	roundsCompleted  int
	cortexNotes      []string
}

func (r *cortexAwareRunner) Run(ctx context.Context, spec engine.RunSpec, onEvent engine.OnEventFunc) (engine.RunResult, error) {
	if spec.Phase.Name == "execute" && spec.CortexEnabled {
		r.mu.Lock()
		r.sawCortexEnabled = true
		r.mu.Unlock()
		r.runCortexRound(ctx)
	}
	if spec.Phase.Name == "verify" && onEvent != nil {
		// The review quality gate counts unique changed files the
		// reviewer Read via Input["file_path"]; emit one for the file
		// the execute phase writes so the gate passes 1/1.
		onEvent(stream.Event{
			Type: "assistant",
			ToolUses: []stream.ToolUse{
				{Name: "Read", Input: map[string]interface{}{"file_path": "main.go"}},
			},
		})
	}
	return r.mockRunner.Run(ctx, spec, onEvent)
}

// runCortexRound builds and drives the deterministic cortex exactly the
// way engine.buildDeterministicCortex does for the native loop: one
// shared Workspace, the 4 deterministic lobes, the shared hub bus, the
// pre-warm pump suppressed. The seeded history contains a truncation
// phrase so the antitrunc lobe publishes a critical Note — positive
// proof the round dispatched to the lobes and the workspace drained.
func (r *cortexAwareRunner) runCortexRound(ctx context.Context) {
	ws := cortex.NewWorkspace(r.bus, nil)

	memStore, err := memory.NewStore(memory.Config{})
	if err != nil {
		r.t.Errorf("cortex memory store: %v", err)
		return
	}
	wisStore := wisdom.NewStore()

	live, err := cortex.New(cortex.Config{
		SessionID: "harness-smoke",
		EventBus:  r.bus,
		Provider:  smokeStubProvider{},
		Lobes: []cortex.Lobe{
			memoryrecall.NewMemoryRecallLobe(ws, memStore, wisStore, r.bus),
			walkeeper.NewWALKeeperLobe(r.bus, nil, ws, walkeeper.WALFraming{}),
			rulecheck.NewRuleCheckLobe(nil, ws),
			antitrunclobe.NewAntiTruncLobe(ws, "", ""),
		},
		Workspace:       ws,
		PreWarmInterval: time.Hour,
	})
	if err != nil {
		r.t.Errorf("cortex.New: %v", err)
		return
	}
	if err := live.Start(ctx); err != nil {
		r.t.Errorf("cortex.Start: %v", err)
		return
	}
	defer live.Stop(context.Background())

	history := []agentloop.Message{
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "Implement the feature completely."}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{{
			Type: "text",
			Text: "I'll stop here and hand off the rest to a follow-up session.",
		}}},
	}
	note := live.MidturnNote(history, 1)

	r.mu.Lock()
	defer r.mu.Unlock()
	if cur := live.Round().Current(); cur >= 1 {
		r.roundsCompleted++
	} else {
		r.t.Errorf("cortex round did not complete: Round().Current()=%d", cur)
	}
	if note != "" {
		r.cortexNotes = append(r.cortexNotes, note)
	}
}

// pollLedger queries the governor's ledger for nodes of the given type
// until at least want are present or the deadline passes. Governance
// observes the hub in ModeObserve (async goroutines), so node writes
// can land shortly after Engine.Run returns.
func pollLedger(t *testing.T, g *governance.Governor, nodeType string, want int) []ledger.Node {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		nodes, err := g.Ledger().Query(context.Background(), ledger.QueryFilter{Type: nodeType})
		if err != nil {
			t.Fatalf("ledger query %q: %v", nodeType, err)
		}
		if len(nodes) >= want || time.Now().After(deadline) {
			return nodes
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestE2EHarnessSmoke_GovernanceCortexWisdom runs one full mission
// through Engine.Run with governance enabled and cortex deterministic
// lobes ON. The build gate is flaky-by-design (fails once, then
// passes) so the mission exercises the verification-failure retry path
// before completing on attempt 2.
func TestE2EHarnessSmoke_GovernanceCortexWisdom(t *testing.T) {
	baseGoroutines := runtime.NumGoroutine()

	repo := initTestRepo(t)
	eventBus := hub.New()

	gov, err := governance.New(context.Background(), t.TempDir(), "smoke-mission", 0)
	if err != nil {
		t.Fatalf("governance.New: %v", err)
	}
	eventBus.Register(gov.HubSubscriber())

	mock := newMockRunner()
	mock.FilesToWrite = map[string]string{
		"main.go": "package main\n\nfunc main() {}\n",
	}
	runner := &cortexAwareRunner{mockRunner: mock, t: t, bus: eventBus}

	ws := wisdom.NewStore()
	state := taskstate.NewTaskState("harness-smoke")

	// Flaky build command: fails the first invocation (attempt 1),
	// passes thereafter (attempt 2). The marker lives OUTSIDE the
	// per-attempt worktrees so it survives the clean-worktree retry.
	marker := filepath.Join(t.TempDir(), "attempt-1-done")
	buildCmd := fmt.Sprintf("test -f %q || { touch %q; echo 'smoke: injected build failure'; exit 1; }", marker, marker)

	policy := config.DefaultPolicy()
	policy.Verification.Build = true // the flaky gate above
	policy.Verification.Tests = false
	policy.Verification.Lint = false
	policy.Verification.ScopeCheck = false
	policy.Verification.CrossModelReview = true // mock verify passes -> review.agree node

	wf := Engine{
		RepoRoot:       repo,
		CortexEnabled:  true,
		Task:           "Harness smoke: full mission with governance and cortex",
		TaskType:       model.TaskTypeRefactor,
		WorktreeName:   "harness-smoke",
		AuthMode:       engine.AuthModeMode1,
		Policy:         policy,
		Pools:          nil,
		// REAL worktree manager (not stubManager): the smoke covers the
		// full commit -> validate -> merge tail, so the Decision wisdom
		// ("succeeded on attempt 2 after retry") and task.completed
		// governance evidence — both recorded only after a real merge —
		// are assertable.
		Worktrees:      worktree.NewManager(repo),
		Runners:        engine.Registry{Claude: engine.NewClaudeRunner("claude")},
		Verifier:       verify.NewPipeline(buildCmd, "", ""),
		State:          state,
		Wisdom:         ws,
		EventBus:       eventBus,
		RunnerOverride: runner,
	}

	result, err := wf.Run(context.Background())
	if err != nil {
		t.Fatalf("workflow failed: %v", err)
	}

	// --- Task completed, across the retry ---
	if mock.Calls["plan"] != 1 {
		t.Errorf("plan calls=%d, want 1", mock.Calls["plan"])
	}
	if mock.Calls["execute"] != 2 {
		t.Errorf("execute calls=%d, want 2 (attempt 1 fails flaky build, attempt 2 passes)", mock.Calls["execute"])
	}
	if mock.Calls["verify"] != 1 {
		t.Errorf("verify (review) calls=%d, want 1 (review only runs after verification passes)", mock.Calls["verify"])
	}
	if phase := state.Phase(); phase != taskstate.Committed {
		t.Errorf("state=%v, want Committed (real worktree manager merged to main)", phase)
	}
	if len(result.Steps) < 3 {
		t.Errorf("steps=%d, want at least 3 (plan + 2 executes)", len(result.Steps))
	}
	if len(result.FilesChanged) != 1 || result.FilesChanged[0] != "main.go" {
		t.Errorf("FilesChanged=%v, want [main.go]", result.FilesChanged)
	}

	// --- Cortex round ran ---
	runner.mu.Lock()
	sawCortex, rounds, notes := runner.sawCortexEnabled, runner.roundsCompleted, runner.cortexNotes
	runner.mu.Unlock()
	if !sawCortex {
		t.Error("Engine did not thread CortexEnabled=true into the execute RunSpec")
	}
	if rounds < 2 {
		t.Errorf("cortex rounds completed=%d, want 2 (one per execute attempt)", rounds)
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "[CORTEX NOTES") {
		t.Errorf("no cortex note block drained from the workspace; notes=%q", joined)
	}
	if !strings.Contains(joined, "antitrunc") {
		t.Errorf("antitrunc lobe did not fire on the seeded truncation phrase; notes=%q", joined)
	}

	// --- Wisdom recorded on both sides of the retry ---
	var sawGotcha, sawDecision bool
	for _, l := range ws.Learnings() {
		switch l.Category {
		case wisdom.Gotcha:
			sawGotcha = true
		case wisdom.Decision:
			sawDecision = true
		}
	}
	if !sawGotcha {
		t.Errorf("no wisdom Gotcha recorded from the attempt-1 verification failure; learnings=%+v", ws.Learnings())
	}
	if !sawDecision {
		t.Errorf("no wisdom Decision recorded from the attempt-2 retry success; learnings=%+v", ws.Learnings())
	}

	// --- Governance ledger nodes written ---
	taskNodes := pollLedger(t, gov, "task", 1)
	if len(taskNodes) < 1 {
		t.Error("no task lifecycle nodes in the governance ledger")
	}
	for _, n := range taskNodes {
		if n.MissionID != "smoke-mission" {
			t.Errorf("task node MissionID=%q, want smoke-mission", n.MissionID)
		}
	}
	if agree := pollLedger(t, gov, "review.agree", 1); len(agree) < 1 {
		t.Error("no review.agree node from the cross-model review event")
	}
	if ev := pollLedger(t, gov, "verification_evidence", 2); len(ev) < 2 {
		t.Errorf("verification_evidence nodes=%d, want >=2 (failing + passing build outcomes)", len(ev))
	}

	if err := gov.Close(); err != nil {
		t.Errorf("governor close: %v", err)
	}

	// --- No goroutine leaks ---
	// Everything with a lifecycle (cortex, governor supervisor/bus/
	// ledger) has been stopped; async hub observe goroutines are
	// short-lived. Allow a settle window, then require the count back
	// within a small slack of the pre-test baseline.
	deadline := time.Now().Add(5 * time.Second)
	slack := 5
	for {
		if runtime.NumGoroutine() <= baseGoroutines+slack {
			break
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			n := runtime.Stack(buf, true)
			t.Errorf("goroutine leak: baseline=%d now=%d (slack %d)\n%s",
				baseGoroutines, runtime.NumGoroutine(), slack, buf[:n])
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}
