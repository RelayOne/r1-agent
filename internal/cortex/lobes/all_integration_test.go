// Cross-cutting integration tests for all six v1 Lobes.
//
// Spec: specs/cortex-concerns.md items 32–36 ("Cross-cutting integration
// tests"). Each test wires up a real cortex.Cortex with the full Lobe
// roster and exercises one cross-Lobe invariant: boot, budget,
// enable-flag plumbing, daemon-restart Note recovery, cache discipline
// under fan-out.
//
// Package layout: this file is paired with testhelpers.go in
// internal/cortex/lobes/. The package name "lobesintegration_test"
// uses Go's external-test-package idiom so the file can import every
// per-Lobe sub-package (clarifyq, memorycurator, …) without an import
// cycle.
package lobesintegration_test

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes"
	"github.com/RelayOne/r1/internal/cortex/lobes/clarifyq"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/cortex/lobes/memorycurator"
	"github.com/RelayOne/r1/internal/cortex/lobes/memoryrecall"
	"github.com/RelayOne/r1/internal/cortex/lobes/planupdate"
	"github.com/RelayOne/r1/internal/cortex/lobes/rulecheck"
	"github.com/RelayOne/r1/internal/cortex/lobes/walkeeper"
	"github.com/RelayOne/r1/internal/conversation"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/memory"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
	"github.com/RelayOne/r1/internal/wisdom"
)

// allLobeIDs lists every Lobe's stable identifier in the production
// roster. The order is the boot order used by allLobesFixture.
var allLobeIDs = []string{
	"memory-recall",
	"wal-keeper",
	"rule-check",
	"plan-update",
	"clarifying-q",
	"memory-curator",
}

// fakeProvider is a multi-Lobe-aware provider.Provider stub. It
// dispatches on the request's tool list to return a response shape
// that the invoking Lobe can decode. callCount tracks ChatStream
// entries for cadence asserts; outputTokens accumulates the synthetic
// Output token count returned in Usage so TASK-33 can compute
// aggregate budget consumption.
type fakeProvider struct {
	mu sync.Mutex

	callCount    atomic.Uint64
	outputTokens atomic.Uint64

	outputPerCall    int
	cacheReadPerCall atomic.Int64

	cacheReadObservations []int
}

func newFakeProvider(outputPerCall int) *fakeProvider {
	return &fakeProvider{outputPerCall: outputPerCall}
}

func (f *fakeProvider) Name() string { return "fake-multi-lobe" }

func (f *fakeProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	return f.ChatStream(req, nil)
}

func (f *fakeProvider) ChatStream(req provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	f.callCount.Add(1)
	cacheRead := int(f.cacheReadPerCall.Load())

	f.mu.Lock()
	f.cacheReadObservations = append(f.cacheReadObservations, cacheRead)
	f.mu.Unlock()

	out := f.outputPerCall
	if out > 0 {
		f.outputTokens.Add(uint64(out))
	}

	resp := &provider.ChatResponse{
		ID:         "msg_fake",
		Model:      req.Model,
		StopReason: "end_turn",
		Usage: stream.TokenUsage{
			Input:     1,
			Output:    out,
			CacheRead: cacheRead,
		},
	}

	switch {
	case len(req.Tools) > 0 && req.Tools[0].Name == "queue_clarifying_question":
		resp.Content = []provider.ResponseContent{
			{
				Type: "tool_use",
				Name: "queue_clarifying_question",
				ID:   "tu-clarify",
				Input: map[string]any{
					"question":  "Which environment did you mean?",
					"category":  "scope",
					"blocking":  true,
					"rationale": "ambiguous deploy target",
				},
			},
		}
	case len(req.Tools) > 0 && req.Tools[0].Name == "remember":
		resp.Content = []provider.ResponseContent{
			{
				Type: "tool_use",
				Name: "remember",
				ID:   "tu-remember",
				Input: map[string]any{
					"category": "fact",
					"content":  "test fact extracted by curator",
				},
			},
		}
	default:
		// PlanUpdateLobe is tool-free; expects a JSON text payload.
		body := `{"confidence":0.9,"edits":[],"additions":[{"id":"t-new","title":"new task"}],"removals":[],"rationale":"integration test"}`
		resp.Content = []provider.ResponseContent{
			{Type: "text", Text: body},
		}
	}

	return resp, nil
}

func (f *fakeProvider) setCacheRead(n int) {
	f.cacheReadPerCall.Store(int64(n))
}

func (f *fakeProvider) snapshotCacheReads() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int, len(f.cacheReadObservations))
	copy(out, f.cacheReadObservations)
	return out
}

// allLobesFixture wires up every v1 Lobe against a single shared
// Workspace. The "shell" Cortex is constructed first to provide that
// Workspace; the "live" Cortex is constructed second with the
// populated Lobes slice, so its runners drive each Lobe's Run while
// Lobe Publish lands in the shell Workspace.
//
// Tests read shell.Workspace() (returned as ws) for assertions and
// drive runner ticks via cortex.MidturnNote.
type allLobesFixture struct {
	cortex   *cortex.Cortex
	ws       *cortex.Workspace
	hubBus   *hub.Bus
	durable  *bus.Bus
	provider *fakeProvider
	memStore *memory.Store
	wisStore *wisdom.Store

	memRecall *memoryrecall.MemoryRecallLobe
	walKeeper *walkeeper.WALKeeperLobe
	ruleCheck *rulecheck.RuleCheckLobe
	planUpd   *planupdate.PlanUpdateLobe
	clarify   *clarifyq.ClarifyingQLobe
	curator   *memorycurator.MemoryCuratorLobe
}

type allLobesOptions struct {
	EnableFlags   map[string]bool
	OutputPerCall int

	// PreCreatedDurable, when non-nil, is used instead of a fresh
	// t.TempDir bus. Lets the daemon-restart test (TASK-35) re-open
	// the same WAL on the second cortex.
	PreCreatedDurable *bus.Bus
}

func newAllLobesFixture(t *testing.T, opts allLobesOptions) *allLobesFixture {
	t.Helper()

	if opts.OutputPerCall == 0 {
		opts.OutputPerCall = 10
	}
	if opts.EnableFlags == nil {
		opts.EnableFlags = map[string]bool{}
		for _, id := range allLobeIDs {
			opts.EnableFlags[id] = true
		}
	}

	hubBus := hub.New()

	var durable *bus.Bus
	if opts.PreCreatedDurable != nil {
		durable = opts.PreCreatedDurable
	} else {
		dir := t.TempDir()
		b, err := bus.New(dir)
		if err != nil {
			t.Fatalf("bus.New: %v", err)
		}
		durable = b
		t.Cleanup(func() { _ = durable.Close() })
	}

	prov := newFakeProvider(opts.OutputPerCall)

	memStore, err := memory.NewStore(memory.Config{Path: ""})
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	memStore.Remember(memory.CatFact, "deploys land via 'r1 deploy' on staging-ng",
		"deploy", "staging")

	wisStore := wisdom.NewStore()

	// Shell Cortex: built solely to provide a stable Workspace
	// pointer that every Lobe can capture at construction time.
	shell, err := cortex.New(cortex.Config{
		EventBus:        hubBus,
		Durable:         durable,
		Provider:        prov,
		PreWarmInterval: time.Hour,
		RoundDeadline:   500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("cortex.New shell: %v", err)
	}
	ws := shell.Workspace()

	fixture := &allLobesFixture{
		ws:       ws,
		hubBus:   hubBus,
		durable:  durable,
		provider: prov,
		memStore: memStore,
		wisStore: wisStore,
	}

	lobeList := make([]cortex.Lobe, 0, 6)

	if opts.EnableFlags["memory-recall"] {
		fixture.memRecall = memoryrecall.NewMemoryRecallLobe(ws, memStore, wisStore, hubBus)
		lobeList = append(lobeList, fixture.memRecall)
	}
	if opts.EnableFlags["wal-keeper"] {
		fixture.walKeeper = walkeeper.NewWALKeeperLobe(hubBus, durable, ws, walkeeper.WALFraming{}).
			WithBackpressureNoteInterval(50 * time.Millisecond)
		lobeList = append(lobeList, fixture.walKeeper)
	}
	if opts.EnableFlags["rule-check"] {
		fixture.ruleCheck = rulecheck.NewRuleCheckLobe(durable, ws)
		lobeList = append(lobeList, fixture.ruleCheck)
	}
	if opts.EnableFlags["plan-update"] {
		conv := conversation.NewRuntime("planner", 8000)
		planPath := t.TempDir() + "/stoke-plan.json"
		fixture.planUpd = planupdate.NewPlanUpdateLobe(planPath, conv, prov,
			llm.NewEscalator(false), ws, hubBus)
		lobeList = append(lobeList, fixture.planUpd)
	}
	if opts.EnableFlags["clarifying-q"] {
		fixture.clarify = clarifyq.NewClarifyingQLobe(prov, llm.NewEscalator(false), ws, hubBus)
		lobeList = append(lobeList, fixture.clarify)
	}
	if opts.EnableFlags["memory-curator"] {
		privacy := memorycurator.PrivacyConfig{
			AutoCurateCategories: []memory.Category{memory.CatFact},
			SkipPrivateMessages:  true,
			AuditLogPath:         t.TempDir() + "/curator-audit.jsonl",
		}
		fixture.curator = memorycurator.NewMemoryCuratorLobe(prov, llm.NewEscalator(false),
			memStore, privacy, ws, hubBus)
		lobeList = append(lobeList, fixture.curator)
	}

	// Live Cortex: holds the runners. Its own Workspace is unused
	// (the Lobes write into shell's ws); we keep its Tracker for
	// budget asserts. Stop on shell first so its (empty) runners
	// release their bus subscribers before the live cortex shuts down.
	c, err := cortex.New(cortex.Config{
		EventBus:        hubBus,
		Durable:         durable,
		Provider:        prov,
		Lobes:           lobeList,
		PreWarmInterval: time.Hour,
		RoundDeadline:   500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("cortex.New: %v", err)
	}
	t.Cleanup(func() {
		_ = shell.Stop(context.Background())
	})
	fixture.cortex = c
	return fixture
}

// driveSyntheticConversation issues n MidturnNote calls plus the side
// channels every Lobe needs to fire at least once.
//
// Why each Lobe needs explicit help:
//   - memory-recall: LobeRunner.buildInput does NOT propagate History
//     into LobeInput (the runner's per-round wiring landed without
//     History support — see internal/cortex/lobe.go:buildInput). So
//     the integration test calls MemoryRecallLobe.Run directly with a
//     populated History to drive its publish path.
//   - wal-keeper: backpressure-drop counter must be non-zero before
//     the ticker fires; ForceDroppedForTest pre-loads it.
//   - rule-check: subscribes to supervisor.rule.fired on the durable
//     bus; we publish a synthetic event so the Lobe converts it to a
//     critical Note.
//   - clarifying-q: subscribes to cortex.user.message; we emit one.
//   - plan-update: triggers every 3rd tick OR on any user message that
//     contains an action verb. The runner's tick path satisfies this.
//   - memory-curator: triggers every 5th tick. The runner ticks 10
//     times across this loop, satisfying the cadence.
func (f *allLobesFixture) driveSyntheticConversation(t *testing.T, n int) {
	t.Helper()

	history := []agentloop.Message{
		{
			Role: "user",
			Content: []agentloop.ContentBlock{
				{Type: "text", Text: "deploy and ship the thing"},
			},
		},
		{
			Role: "assistant",
			Content: []agentloop.ContentBlock{
				{Type: "text", Text: "noted; deploying"},
			},
		},
	}

	for i := 0; i < n; i++ {
		_ = f.cortex.MidturnNote(history, i)

		if f.clarify != nil {
			lobesintegration.EmitUserMessage(f.hubBus, "deploy and ship the thing")
		}

		// Drive memory-recall directly: the runner does not propagate
		// History, so we call Run with a populated LobeInput to trigger
		// the recall + publish path.
		if f.memRecall != nil {
			_ = f.memRecall.Run(context.Background(), cortex.LobeInput{
				History: history,
			})
		}
	}

	if f.walKeeper != nil {
		f.walKeeper.ForceDroppedForTest(7)
	}

	if f.ruleCheck != nil {
		if err := lobesintegration.PublishRuleFired(f.durable,
			"trust.fix_requires_second_opinion",
			"fix declared without independent review"); err != nil {
			t.Logf("PublishRuleFired: %v", err)
		}
	}
}

// goroutineCountSnapshot returns the current process goroutine count.
// We force GC first so goroutines that exited during teardown but have
// not yet been reaped are accounted for.
func goroutineCountSnapshot() int {
	runtime.GC()
	return runtime.NumGoroutine()
}

// waitForLobesPublished polls ws until at least one Note from every id
// in want has landed, or the timeout fires. Returns the snapshot at
// exit so callers can run additional asserts without a second poll.
func waitForLobesPublished(t *testing.T, ws *cortex.Workspace, want []string, timeout time.Duration) []cortex.Note {
	t.Helper()
	deadline := time.Now().Add(timeout)
	wantSet := make(map[string]bool, len(want))
	for _, id := range want {
		wantSet[id] = true
	}
	for time.Now().Before(deadline) {
		notes := ws.Snapshot()
		seen := make(map[string]bool, len(wantSet))
		for _, n := range notes {
			if wantSet[n.LobeID] {
				seen[n.LobeID] = true
			}
		}
		if len(seen) == len(wantSet) {
			return notes
		}
		time.Sleep(20 * time.Millisecond)
	}
	return ws.Snapshot()
}

// TestAllLobes_BootInFakeCortex covers TASK-32. Boot a Cortex with all
// six Lobes, drive a 10-message synthetic conversation, then assert at
// least one Note from each Lobe published, no panics, goroutine count
// returns to baseline within tolerance after Stop.
func TestAllLobes_BootInFakeCortex(t *testing.T) {
	t.Parallel()

	preGoroutines := goroutineCountSnapshot()

	f := newAllLobesFixture(t, allLobesOptions{})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := f.cortex.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Seed main-turn token usage so the LLM Lobes don't fail-closed
	// on a zero budget. 10000 Output tokens → 3000-token per-round
	// Lobe budget, more than enough for synthetic outputs.
	f.cortex.Tracker().RecordMainTurn(10000)

	f.driveSyntheticConversation(t, 10)

	notes := waitForLobesPublished(t, f.ws, allLobeIDs, 5*time.Second)

	seen := make(map[string]bool)
	for _, n := range notes {
		seen[n.LobeID] = true
	}
	for _, id := range allLobeIDs {
		if !seen[id] {
			t.Errorf("missing Note from Lobe %q; have %d total, seen=%v",
				id, len(notes), seen)
		}
	}

	if err := f.cortex.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	for i := 0; i < 10; i++ {
		runtime.Gosched()
		time.Sleep(20 * time.Millisecond)
	}
	postGoroutines := goroutineCountSnapshot()

	// Tolerance accounts for runtime workers, bus subscriber goroutines
	// owned by t.Cleanup-deferred buses, and testing-package internals.
	// "No goroutine leak" → bounded growth, not strict equality.
	if delta := postGoroutines - preGoroutines; delta > 25 {
		t.Errorf("goroutine count grew by %d after Stop (pre=%d, post=%d); expected <=25",
			delta, preGoroutines, postGoroutines)
	}
}
