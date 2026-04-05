package mission

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/prompts"
)

// simpleExecutor returns a WorkExecutor that records execution order and completes immediately.
func simpleExecutor(order *[]string, mu *sync.Mutex) WorkExecutor {
	return func(ctx context.Context, node *WorkNode, mCtx prompts.MissionContext) (*WorkResult, error) {
		mu.Lock()
		*order = append(*order, node.ID)
		mu.Unlock()
		return &WorkResult{
			Summary: "done: " + node.ID,
			Agent:   "test",
		}, nil
	}
}

func TestWorkDAGSequential(t *testing.T) {
	// A→B→C: C depends on B, B depends on A. Must execute A, then B, then C.
	var order []string
	var mu sync.Mutex
	dag := NewWorkDAG(simpleExecutor(&order, &mu), 4)

	err := dag.AddNodes([]WorkNode{
		{ID: "A", Type: WorkImplement, Scope: "step A"},
		{ID: "B", Type: WorkImplement, Scope: "step B", DependsOn: []string{"A"}},
		{ID: "C", Type: WorkImplement, Scope: "step C", DependsOn: []string{"B"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := dag.Run(context.Background(), prompts.MissionContext{})
	if err != nil {
		t.Fatal(err)
	}

	if result.NodesComplete != 3 {
		t.Errorf("expected 3 complete, got %d", result.NodesComplete)
	}
	if result.NodesTotal != 3 {
		t.Errorf("expected 3 total, got %d", result.NodesTotal)
	}

	// Verify order: A before B before C.
	mu.Lock()
	defer mu.Unlock()
	idxA, idxB, idxC := -1, -1, -1
	for i, id := range order {
		switch id {
		case "A":
			idxA = i
		case "B":
			idxB = i
		case "C":
			idxC = i
		}
	}
	if idxA >= idxB || idxB >= idxC {
		t.Errorf("expected A < B < C, got A=%d B=%d C=%d", idxA, idxB, idxC)
	}
}

func TestWorkDAGParallel(t *testing.T) {
	// Three independent nodes should be able to run concurrently.
	var running int64
	var maxRunning int64
	var mu sync.Mutex

	executor := func(ctx context.Context, node *WorkNode, mCtx prompts.MissionContext) (*WorkResult, error) {
		cur := atomic.AddInt64(&running, 1)
		// Track max concurrency.
		mu.Lock()
		if cur > maxRunning {
			maxRunning = cur
		}
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt64(&running, -1)
		return &WorkResult{Summary: "done: " + node.ID, Agent: "test"}, nil
	}

	dag := NewWorkDAG(executor, 4)
	err := dag.AddNodes([]WorkNode{
		{ID: "X", Type: WorkImplement, Scope: "independent X"},
		{ID: "Y", Type: WorkImplement, Scope: "independent Y"},
		{ID: "Z", Type: WorkImplement, Scope: "independent Z"},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := dag.Run(context.Background(), prompts.MissionContext{})
	if err != nil {
		t.Fatal(err)
	}

	if result.NodesComplete != 3 {
		t.Errorf("expected 3 complete, got %d", result.NodesComplete)
	}

	mu.Lock()
	max := maxRunning
	mu.Unlock()
	if max < 2 {
		t.Errorf("expected at least 2 concurrent, got max %d", max)
	}
}

func TestWorkDAGDiamond(t *testing.T) {
	// Diamond: A depends on B and C. B and C are independent.
	// B and C run in parallel, A runs after both.
	var order []string
	var mu sync.Mutex

	executor := func(ctx context.Context, node *WorkNode, mCtx prompts.MissionContext) (*WorkResult, error) {
		// Small sleep so B and C overlap.
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		order = append(order, node.ID)
		mu.Unlock()
		return &WorkResult{Summary: "done: " + node.ID, Agent: "test"}, nil
	}

	dag := NewWorkDAG(executor, 4)
	err := dag.AddNodes([]WorkNode{
		{ID: "B", Type: WorkImplement, Scope: "branch B"},
		{ID: "C", Type: WorkImplement, Scope: "branch C"},
		{ID: "A", Type: WorkImplement, Scope: "merge A", DependsOn: []string{"B", "C"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := dag.Run(context.Background(), prompts.MissionContext{})
	if err != nil {
		t.Fatal(err)
	}

	if result.NodesComplete != 3 {
		t.Errorf("expected 3 complete, got %d", result.NodesComplete)
	}

	// A must be last.
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 {
		t.Fatalf("expected 3 entries in order, got %d", len(order))
	}
	if order[2] != "A" {
		t.Errorf("expected A last, got order %v", order)
	}
}

func TestWorkDAGRecursiveDecomposition(t *testing.T) {
	// A node returns children instead of completing directly.
	// Children get executed, parent completes after all children.
	callCount := 0
	var mu sync.Mutex

	executor := func(ctx context.Context, node *WorkNode, mCtx prompts.MissionContext) (*WorkResult, error) {
		mu.Lock()
		callCount++
		mu.Unlock()

		if node.ID == "parent" {
			return &WorkResult{
				Summary: "decomposed",
				Agent:   "test",
				Children: []WorkNode{
					{ID: "child-1", Type: WorkImplement, Scope: "child 1 work"},
					{ID: "child-2", Type: WorkImplement, Scope: "child 2 work"},
				},
			}, nil
		}
		return &WorkResult{
			Summary:      "done: " + node.ID,
			FilesChanged: []string{node.ID + ".go"},
			Agent:        "test",
		}, nil
	}

	dag := NewWorkDAG(executor, 4)
	err := dag.AddNode(WorkNode{ID: "parent", Type: WorkDecompose, Scope: "big task"})
	if err != nil {
		t.Fatal(err)
	}

	result, err := dag.Run(context.Background(), prompts.MissionContext{})
	if err != nil {
		t.Fatal(err)
	}

	// parent + 2 children = 3 nodes total, all complete.
	if result.NodesTotal != 3 {
		t.Errorf("expected 3 total nodes, got %d", result.NodesTotal)
	}
	if result.NodesComplete != 3 {
		t.Errorf("expected 3 complete, got %d", result.NodesComplete)
	}

	// Executor called 3 times: parent + 2 children.
	mu.Lock()
	if callCount != 3 {
		t.Errorf("expected 3 executor calls, got %d", callCount)
	}
	mu.Unlock()

	if len(result.FilesChanged) != 2 {
		t.Errorf("expected 2 files changed, got %d: %v", len(result.FilesChanged), result.FilesChanged)
	}
}

func TestWorkDAGMaxDepth(t *testing.T) {
	// A node that always tries to decompose. At maxDepth, decomposition is
	// rejected and the node completes as-is.
	dag := NewWorkDAG(nil, 2)
	dag.maxDepth = 2

	var execCount int32
	dag.executor = func(ctx context.Context, node *WorkNode, mCtx prompts.MissionContext) (*WorkResult, error) {
		atomic.AddInt32(&execCount, 1)
		// Always try to return children.
		return &WorkResult{
			Summary: "decompose: " + node.ID,
			Agent:   "test",
			Children: []WorkNode{
				{ID: node.ID + "-sub", Type: WorkDecompose, Scope: "recursive"},
			},
		}, nil
	}

	err := dag.AddNode(WorkNode{ID: "root", Type: WorkDecompose, Scope: "deep task", Depth: 0})
	if err != nil {
		t.Fatal(err)
	}

	result, err := dag.Run(context.Background(), prompts.MissionContext{})
	if err != nil {
		t.Fatal(err)
	}

	// Depth 0: root decomposes → root-sub (depth 1)
	// Depth 1: root-sub decomposes → root-sub-sub (depth 2)
	// Depth 2: root-sub-sub tries to decompose but depth+1=3 > maxDepth=2, so completes as-is.
	if result.NodesTotal != 3 {
		t.Errorf("expected 3 total nodes (root, root-sub, root-sub-sub), got %d", result.NodesTotal)
	}
	if result.NodesComplete != 3 {
		t.Errorf("expected 3 complete, got %d", result.NodesComplete)
	}
}

func TestWorkDAGFailurePropagation(t *testing.T) {
	// A fails, B depends on A → B should be blocked.
	executor := func(ctx context.Context, node *WorkNode, mCtx prompts.MissionContext) (*WorkResult, error) {
		if node.ID == "A" {
			return nil, fmt.Errorf("A failed intentionally")
		}
		return &WorkResult{Summary: "done: " + node.ID, Agent: "test"}, nil
	}

	dag := NewWorkDAG(executor, 4)
	err := dag.AddNodes([]WorkNode{
		{ID: "A", Type: WorkImplement, Scope: "failing task"},
		{ID: "B", Type: WorkImplement, Scope: "dependent task", DependsOn: []string{"A"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := dag.Run(context.Background(), prompts.MissionContext{})
	if err != nil {
		t.Fatal(err)
	}

	if result.NodesFailed != 1 {
		t.Errorf("expected 1 failed, got %d", result.NodesFailed)
	}
	if result.NodesBlocked != 1 {
		t.Errorf("expected 1 blocked, got %d", result.NodesBlocked)
	}
	if result.NodesComplete != 0 {
		t.Errorf("expected 0 complete, got %d", result.NodesComplete)
	}
}

func TestWorkDAGFileConflict(t *testing.T) {
	// Two nodes touch the same file. Only one should run at a time.
	var running int64
	var maxRunning int64
	var mu sync.Mutex

	executor := func(ctx context.Context, node *WorkNode, mCtx prompts.MissionContext) (*WorkResult, error) {
		cur := atomic.AddInt64(&running, 1)
		mu.Lock()
		if cur > maxRunning {
			maxRunning = cur
		}
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt64(&running, -1)
		return &WorkResult{Summary: "done: " + node.ID, Agent: "test"}, nil
	}

	dag := NewWorkDAG(executor, 4)
	err := dag.AddNodes([]WorkNode{
		{ID: "W1", Type: WorkImplement, Scope: "write 1", Files: []string{"shared.go"}},
		{ID: "W2", Type: WorkImplement, Scope: "write 2", Files: []string{"shared.go"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := dag.Run(context.Background(), prompts.MissionContext{})
	if err != nil {
		t.Fatal(err)
	}

	if result.NodesComplete != 2 {
		t.Errorf("expected 2 complete, got %d", result.NodesComplete)
	}

	// Max concurrency for these two should be 1 (file conflict).
	mu.Lock()
	max := maxRunning
	mu.Unlock()
	if max > 1 {
		t.Errorf("expected max 1 concurrent (file conflict), got %d", max)
	}
}

func TestWorkDAGPriority(t *testing.T) {
	// GRPW: node with most downstream work dispatches first.
	//   root → mid → leaf (chain of 3)
	//   solo (independent)
	// root has weight 3 (root + mid + leaf), solo has weight 1.
	// root should dispatch before solo when both are ready.
	var order []string
	var mu sync.Mutex

	// Use a sequential executor (maxWorkers=1) to observe priority ordering.
	executor := func(ctx context.Context, node *WorkNode, mCtx prompts.MissionContext) (*WorkResult, error) {
		mu.Lock()
		order = append(order, node.ID)
		mu.Unlock()
		return &WorkResult{Summary: "done: " + node.ID, Agent: "test"}, nil
	}

	dag := NewWorkDAG(executor, 1)
	err := dag.AddNodes([]WorkNode{
		{ID: "root", Type: WorkImplement, Scope: "root of chain"},
		{ID: "mid", Type: WorkImplement, Scope: "middle", DependsOn: []string{"root"}},
		{ID: "leaf", Type: WorkImplement, Scope: "leaf", DependsOn: []string{"mid"}},
		{ID: "solo", Type: WorkImplement, Scope: "independent"},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := dag.Run(context.Background(), prompts.MissionContext{})
	if err != nil {
		t.Fatal(err)
	}

	if result.NodesComplete != 4 {
		t.Errorf("expected 4 complete, got %d", result.NodesComplete)
	}

	// root (weight 3) should come before solo (weight 1) in the first dispatch.
	mu.Lock()
	defer mu.Unlock()
	if len(order) < 2 {
		t.Fatal("expected at least 2 entries in order")
	}
	if order[0] != "root" {
		t.Errorf("expected root first (GRPW weight 3 > solo weight 1), got %v", order)
	}
}

func TestWorkDAGDuplicateID(t *testing.T) {
	dag := NewWorkDAG(nil, 1)
	err := dag.AddNode(WorkNode{ID: "A", Type: WorkImplement, Scope: "first"})
	if err != nil {
		t.Fatal(err)
	}
	err = dag.AddNode(WorkNode{ID: "A", Type: WorkImplement, Scope: "duplicate"})
	if err == nil {
		t.Fatal("expected error for duplicate ID")
	}
}

func TestWorkDAGUnknownDep(t *testing.T) {
	dag := NewWorkDAG(nil, 1)
	err := dag.AddNode(WorkNode{ID: "A", Type: WorkImplement, Scope: "first", DependsOn: []string{"nonexistent"}})
	if err == nil {
		t.Fatal("expected error for unknown dependency")
	}
}
