package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/RelayOne/r1/internal/harness"
	"github.com/RelayOne/r1/internal/sharedmem"
)

const findingsBlockType sharedmem.BlockType = "findings"

// spawnDev spawns a dev/proposing stance on the given loop and returns its ID.
func spawnDev(t *testing.T, h *harness.Harness, loop string) string {
	t.Helper()
	handle, err := h.SpawnStance(context.Background(), harness.SpawnRequest{
		Role:    "dev",
		Face:    "proposing",
		LoopRef: loop,
	})
	if err != nil {
		t.Fatalf("SpawnStance(loop=%s): %v", loop, err)
	}
	return handle.ID
}

// blockStrings extracts the []any string list a findings block accumulates.
func blockStrings(t *testing.T, b *sharedmem.Block) []string {
	t.Helper()
	raw, ok := b.Value.([]any)
	if !ok {
		t.Fatalf("block value is %T, want []any", b.Value)
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("block element is %T, want string", v)
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// TestStanceMemory_TwoStancesShareBlock is the headline A070 integration test:
// two concurrently-running stance workers in the same consensus loop share one
// reducer-mediated shared-memory block, and every write records PROV-AGENT
// provenance stamped with the writing stance's ID.
func TestStanceMemory_TwoStancesShareBlock(t *testing.T) {
	h, _, _ := setupWithDeps(t)
	ctx := context.Background()

	// Reducer that merges concurrent additive inserts into one list.
	h.RegisterSharedReducer(findingsBlockType, sharedmem.AddReducer)

	// Two stances on the SAME loop => same collaboration namespace.
	idA := spawnDev(t, h, "loop-alpha")
	idB := spawnDev(t, h, "loop-alpha")

	memA, err := h.StanceMemory(idA)
	if err != nil {
		t.Fatalf("StanceMemory(A): %v", err)
	}
	memB, err := h.StanceMemory(idB)
	if err != nil {
		t.Fatalf("StanceMemory(B): %v", err)
	}

	// Both stances resolve to the same shared namespace.
	if memA.Namespace() != memB.Namespace() {
		t.Fatalf("namespaces differ: A=%q B=%q", memA.Namespace(), memB.Namespace())
	}
	if memA.Namespace() != "loop:loop-alpha" {
		t.Fatalf("namespace = %q, want loop:loop-alpha", memA.Namespace())
	}

	const blockID sharedmem.BlockID = "findings-1"
	if _, err := memA.CreateBlock(ctx, blockID, findingsBlockType, "shared findings", []any{}); err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}

	// Concurrent inserts from both stances, released together.
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		if _, err := memA.Insert(ctx, blockID, []any{"from-A"}, "A observation"); err != nil {
			t.Errorf("A.Insert: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		if _, err := memB.Insert(ctx, blockID, []any{"from-B"}, "B observation"); err != nil {
			t.Errorf("B.Insert: %v", err)
		}
	}()
	close(start)
	wg.Wait()

	// B reads what A seeded and both stances wrote — real sharing.
	final, err := memB.Get(ctx, blockID)
	if err != nil {
		t.Fatalf("B.Get: %v", err)
	}
	got := blockStrings(t, final)
	want := []string{"from-A", "from-B"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("merged block = %v, want %v (reducer must merge both stances' writes)", got, want)
	}

	// Provenance: one create + two inserts, attributed to the right stances.
	if final.Version != 3 {
		t.Errorf("Version = %d, want 3 (create + 2 inserts)", final.Version)
	}
	authors := map[string]int{}
	for _, p := range final.Provenance {
		if p.Timestamp.IsZero() {
			t.Errorf("provenance entry %q has zero timestamp", p.Action)
		}
		if p.AgentID == "" {
			t.Errorf("provenance entry %q has empty AgentID", p.Action)
		}
		if p.Action == "insert" {
			authors[p.AgentID]++
		}
	}
	if authors[idA] != 1 || authors[idB] != 1 {
		t.Errorf("insert provenance authors = %v, want one each from %q and %q", authors, idA, idB)
	}
}

// TestStanceMemory_NamespaceIsolation proves the wiring is real namespace
// scoping, not a passthrough: a stance in a different loop cannot see or write
// another loop's block.
func TestStanceMemory_NamespaceIsolation(t *testing.T) {
	h, _, _ := setupWithDeps(t)
	ctx := context.Background()
	h.RegisterSharedReducer(findingsBlockType, sharedmem.AddReducer)

	owner := spawnDev(t, h, "loop-owner")
	intruder := spawnDev(t, h, "loop-intruder")

	memOwner, err := h.StanceMemory(owner)
	if err != nil {
		t.Fatalf("StanceMemory(owner): %v", err)
	}
	memIntruder, err := h.StanceMemory(intruder)
	if err != nil {
		t.Fatalf("StanceMemory(intruder): %v", err)
	}
	if memOwner.Namespace() == memIntruder.Namespace() {
		t.Fatalf("expected distinct namespaces, both = %q", memOwner.Namespace())
	}

	const blockID sharedmem.BlockID = "secret-1"
	if _, err := memOwner.CreateBlock(ctx, blockID, findingsBlockType, "owner only", []any{"seed"}); err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}

	if _, err := memIntruder.Get(ctx, blockID); !errors.Is(err, sharedmem.ErrNamespaceDenied) {
		t.Errorf("intruder Get err = %v, want ErrNamespaceDenied", err)
	}
	if _, err := memIntruder.Insert(ctx, blockID, []any{"tamper"}, "should fail"); !errors.Is(err, sharedmem.ErrNamespaceDenied) {
		t.Errorf("intruder Insert err = %v, want ErrNamespaceDenied", err)
	}

	// Owner still reads exactly its seed — no leakage from the denied write.
	got, err := memOwner.Get(ctx, blockID)
	if err != nil {
		t.Fatalf("owner Get: %v", err)
	}
	if s := blockStrings(t, got); len(s) != 1 || s[0] != "seed" {
		t.Errorf("owner block = %v, want [seed]", s)
	}
}

// sharedMemExecutor routes a stance's tool call into the harness shared-memory
// store, proving the A070 wiring end to end through the real StanceRunner:
// runner -> ToolExecutor -> StanceMemory -> NamespacedStore -> reducer ->
// provenance. It inserts the stance's own ID so contributions are attributable.
type sharedMemExecutor struct {
	h       *harness.Harness
	blockID sharedmem.BlockID
}

func (e *sharedMemExecutor) Execute(ctx context.Context, stanceID string, call harness.ToolCall) (string, error) {
	mem, err := e.h.StanceMemory(stanceID)
	if err != nil {
		return "", err
	}
	blk, err := mem.Insert(ctx, e.blockID, []any{stanceID}, "noted via "+call.Name)
	if err != nil {
		return "", err
	}
	list, _ := blk.Value.([]any)
	return fmt.Sprintf("recorded; block now holds %d findings", len(list)), nil
}

// TestStanceRunner_SharedMemoryCollaboration drives two StanceRunners
// concurrently; each stance's authorized ledger_write tool call is routed by a
// shared-memory executor into one reducer-mediated block, so the two live
// runners collaborate through STOKE-017 shared memory.
func TestStanceRunner_SharedMemoryCollaboration(t *testing.T) {
	h, _, _ := setupWithDeps(t)
	ctx := context.Background()
	h.RegisterSharedReducer(findingsBlockType, sharedmem.AddReducer)

	idA := spawnDev(t, h, "loop-run")
	idB := spawnDev(t, h, "loop-run")

	// Seed the shared block from one stance before the runners execute.
	const blockID sharedmem.BlockID = "run-findings"
	seed, err := h.StanceMemory(idA)
	if err != nil {
		t.Fatalf("StanceMemory(seed): %v", err)
	}
	if _, err := seed.CreateBlock(ctx, blockID, findingsBlockType, "run findings", []any{}); err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}

	exec := &sharedMemExecutor{h: h, blockID: blockID}

	// Each stance: one turn issuing an authorized ledger_write tool call,
	// then a final turn with no tool calls (loop terminates).
	newMock := func() *harness.MockProvider {
		return &harness.MockProvider{
			Responses: []*harness.ChatResponse{
				{
					Content: "recording my finding",
					ToolCalls: []harness.ToolCall{
						{Name: "ledger_write", Args: json.RawMessage(`{}`)},
					},
				},
				{Content: "done"},
			},
		}
	}

	var wg sync.WaitGroup
	for _, id := range []string{idA, idB} {
		wg.Add(1)
		go func(stanceID string) {
			defer wg.Done()
			runner := h.NewStanceRunner(newMock(), exec, harness.RunnerConfig{MaxTurns: 4})
			out, err := runner.Run(ctx, stanceID, "collaborate")
			if err != nil {
				t.Errorf("Run(%s): %v", stanceID, err)
				return
			}
			if out.ToolCallsTotal != 1 || out.ToolCallsDenied != 0 {
				t.Errorf("Run(%s) tool calls = %d/%d denied, want 1/0", stanceID, out.ToolCallsTotal, out.ToolCallsDenied)
			}
		}(id)
	}
	wg.Wait()

	final, err := seed.Get(ctx, blockID)
	if err != nil {
		t.Fatalf("final Get: %v", err)
	}
	got := blockStrings(t, final)
	want := []string{idA, idB}
	sort.Strings(want)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("collaborated block = %v, want %v", got, want)
	}
	// Both runner-driven writes carry provenance from their stance.
	insertAuthors := map[string]bool{}
	for _, p := range final.Provenance {
		if p.Action == "insert" {
			insertAuthors[p.AgentID] = true
		}
	}
	if !insertAuthors[idA] || !insertAuthors[idB] {
		t.Errorf("insert provenance authors = %v, want both %q and %q", insertAuthors, idA, idB)
	}
}
