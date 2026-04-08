package mission

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/internal/prompts"
)

// WorkType classifies what kind of work a node performs.
type WorkType string

const (
	WorkResearch  WorkType = "research"  // investigate a specific question
	WorkImplement WorkType = "implement" // write code for a specific scope
	WorkTest      WorkType = "test"      // write/run tests for a specific scope
	WorkReview    WorkType = "review"    // adversarial review of specific scope
	WorkValidate  WorkType = "validate"  // verify specific criterion
	WorkDecompose WorkType = "decompose" // break parent into children (meta-task)
)

// WorkStatus tracks execution state.
type WorkStatus string

const (
	WorkPending  WorkStatus = "pending"
	WorkReady    WorkStatus = "ready"    // all deps satisfied
	WorkRunning  WorkStatus = "running"
	WorkComplete WorkStatus = "complete"
	WorkFailed   WorkStatus = "failed"
	WorkBlocked  WorkStatus = "blocked" // dep failed
)

// WorkNode is a single unit of work in the DAG.
// Scope should be MINIMAL — if a node's scope is too large,
// the executor should return children instead of results.
type WorkNode struct {
	ID          string            `json:"id"`
	ParentID    string            `json:"parent_id,omitempty"` // empty for root nodes
	Type        WorkType          `json:"type"`
	Scope       string            `json:"scope"`      // precise description of minimum-scope work
	DependsOn   []string          `json:"depends_on"` // IDs of nodes that must complete first
	Files       []string          `json:"files"`      // files this node will touch (for conflict detection)
	Status      WorkStatus        `json:"status"`
	Result      *WorkResult       `json:"result,omitempty"`
	Children    []string          `json:"children,omitempty"` // IDs of spawned sub-nodes
	Priority    int               `json:"priority"`           // higher = more critical (computed from downstream deps)
	Depth       int               `json:"depth"`              // depth in tree (root=0)
	MaxDepth    int               `json:"max_depth"`          // prevent infinite recursion
	Timeout     time.Duration     `json:"timeout,omitempty"`  // per-node timeout (0 = no limit, inherits context)
	MaxRetries  int               `json:"max_retries"`        // retry on failure (0 = no retries)
	Attempt     int               `json:"attempt"`            // current attempt number (0-indexed)
	CreatedAt   time.Time         `json:"created_at"`
	StartedAt   *time.Time        `json:"started_at,omitempty"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
}

// WorkResult is what executing a work node produces.
type WorkResult struct {
	Summary      string            `json:"summary"`
	FilesChanged []string          `json:"files_changed,omitempty"`
	Artifacts    map[string]string `json:"artifacts,omitempty"`
	Gaps         []string          `json:"gaps,omitempty"`     // gaps discovered during this work
	Children     []WorkNode        `json:"children,omitempty"` // recursive decomposition
	Duration     time.Duration     `json:"duration"`
	Agent        string            `json:"agent"`
}

// WorkExecutor is called to execute a single minimum-scope work node.
// If the scope is still too large, return Children in the result instead of doing the work.
type WorkExecutor func(ctx context.Context, node *WorkNode, missionCtx prompts.MissionContext) (*WorkResult, error)

// DAGResult aggregates the outcome of a full DAG execution.
type DAGResult struct {
	NodesTotal    int           `json:"nodes_total"`
	NodesComplete int           `json:"nodes_complete"`
	NodesFailed   int           `json:"nodes_failed"`
	NodesBlocked  int           `json:"nodes_blocked"`
	FilesChanged  []string      `json:"files_changed"`
	Gaps          []string      `json:"gaps"`
	Duration      time.Duration `json:"duration"`
}

// WorkDAG manages parallel execution of a dependency-aware work graph.
type WorkDAG struct {
	mu        sync.Mutex
	nodes     map[string]*WorkNode
	rootIDs   []string // top-level nodes (no parent)
	executor  WorkExecutor
	maxWorkers int
	maxDepth   int // default 5, prevents infinite recursion

	// fileLocks tracks file → running node ID for conflict detection.
	fileLocks map[string]string

	// running tracks which nodes are currently executing.
	running map[string]bool

	// Callbacks
	OnNodeComplete func(node *WorkNode, result *WorkResult)
	OnNodeFailed   func(node *WorkNode, err error)
}

// NewWorkDAG creates a DAG with defaults (maxDepth=5, maxWorkers from param).
func NewWorkDAG(executor WorkExecutor, maxWorkers int) *WorkDAG {
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	return &WorkDAG{
		nodes:      make(map[string]*WorkNode),
		executor:   executor,
		maxWorkers: maxWorkers,
		maxDepth:   5,
		fileLocks:  make(map[string]string),
		running:    make(map[string]bool),
	}
}

// SetMaxDepth overrides the default max recursion depth.
func (d *WorkDAG) SetMaxDepth(depth int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.maxDepth = depth
}

// AddNode adds a node to the DAG. Validates ID uniqueness and that all
// dependencies reference existing nodes.
func (d *WorkDAG) AddNode(node WorkNode) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.addNodeLocked(node)
}

func (d *WorkDAG) addNodeLocked(node WorkNode) error {
	if node.ID == "" {
		return fmt.Errorf("workdag: node has empty ID")
	}
	if _, exists := d.nodes[node.ID]; exists {
		return fmt.Errorf("workdag: duplicate node ID %q", node.ID)
	}
	for _, dep := range node.DependsOn {
		if _, exists := d.nodes[dep]; !exists {
			return fmt.Errorf("workdag: node %q depends on unknown node %q", node.ID, dep)
		}
	}
	// Cycle detection: adding this node must not create a cycle.
	// A cycle exists if any dependency can reach back to this node's ID
	// through the existing dependency graph. Since the node isn't added yet,
	// check if any dep transitively depends on this node's ID (which would
	// only happen if this ID appears in deps of deps — possible via children).
	if len(node.DependsOn) > 0 {
		if err := d.detectCycle(node.ID, node.DependsOn); err != nil {
			return err
		}
	}
	if node.Status == "" {
		node.Status = WorkPending
	}
	if node.CreatedAt.IsZero() {
		node.CreatedAt = time.Now()
	}
	if node.MaxDepth == 0 {
		node.MaxDepth = d.maxDepth
	}
	n := node // copy
	d.nodes[node.ID] = &n
	if node.ParentID == "" {
		d.rootIDs = append(d.rootIDs, node.ID)
	}
	return nil
}

// detectCycle checks if adding edges from nodeID → deps would create a cycle.
// A cycle exists if nodeID is reachable from any of its deps via DependsOn edges.
// Must be called with mu held.
func (d *WorkDAG) detectCycle(nodeID string, deps []string) error {
	// DFS from each dep to see if we can reach nodeID.
	visited := make(map[string]bool)
	var dfs func(id string) bool
	dfs = func(id string) bool {
		if id == nodeID {
			return true
		}
		if visited[id] {
			return false
		}
		visited[id] = true
		n, ok := d.nodes[id]
		if !ok {
			return false
		}
		for _, dep := range n.DependsOn {
			if dfs(dep) {
				return true
			}
		}
		return false
	}
	for _, dep := range deps {
		// Reset visited for each starting dep (they're independent paths).
		for k := range visited {
			delete(visited, k)
		}
		if dfs(dep) {
			return fmt.Errorf("workdag: adding node %q would create a cycle (via %q)", nodeID, dep)
		}
	}
	return nil
}

// AddNodes batch-adds nodes. Nodes are added in order, so later nodes
// can depend on earlier nodes in the same batch.
func (d *WorkDAG) AddNodes(nodes []WorkNode) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, n := range nodes {
		if err := d.addNodeLocked(n); err != nil {
			return err
		}
	}
	return nil
}

// Run is the main execution loop. It dispatches ready nodes in parallel,
// handles recursive decomposition, propagates failures, and returns
// an aggregated DAGResult.
func (d *WorkDAG) Run(ctx context.Context, mCtx prompts.MissionContext) (*DAGResult, error) {
	start := time.Now()

	d.mu.Lock()
	d.computePriorities()
	d.mu.Unlock()

	sem := make(chan struct{}, d.maxWorkers)
	var wg sync.WaitGroup

	// resultCh carries completed node IDs (or failed) back to the main loop.
	type nodeResult struct {
		id     string
		result *WorkResult
		err    error
	}
	resultCh := make(chan nodeResult, len(d.nodes)+100) // extra buffer for children

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return d.buildResult(start), ctx.Err()
		default:
		}

		// Drain completed results.
		drained := true
		for drained {
			select {
			case nr := <-resultCh:
				wg.Done()
				<-sem // release semaphore slot
				d.mu.Lock()
				d.handleResult(nr.id, nr.result, nr.err)
				d.mu.Unlock()
			default:
				drained = false
			}
		}

		// Check if we're done.
		d.mu.Lock()
		if d.allDone() {
			d.mu.Unlock()
			wg.Wait()
			// Drain any remaining results.
			close(resultCh)
			for nr := range resultCh {
				wg.Done()
				<-sem
				d.mu.Lock()
				d.handleResult(nr.id, nr.result, nr.err)
				d.mu.Unlock()
			}
			return d.buildResult(start), nil
		}

		// Find and dispatch ready nodes. Only dispatch as many as we can
		// acquire semaphore slots for (non-blocking), to ensure file locks
		// and concurrency limits are respected synchronously.
		ready := d.readyNodes()
		for _, node := range ready {
			// Re-check file conflicts — a previously dispatched node in this
			// same loop iteration may have acquired overlapping files.
			if d.hasFileConflict(node) {
				continue
			}

			// Try to acquire semaphore slot without blocking.
			gotSlot := false
			select {
			case sem <- struct{}{}:
				gotSlot = true
			default:
			}
			if !gotSlot {
				break
			}

			node.Status = WorkRunning
			now := time.Now()
			node.StartedAt = &now
			d.running[node.ID] = true
			d.acquireFiles(node)

			wg.Add(1)
			go func(n *WorkNode) {
				execCtx := ctx
				var cancel context.CancelFunc
				if n.Timeout > 0 {
					execCtx, cancel = context.WithTimeout(ctx, n.Timeout)
				}
				result, err := d.executor(execCtx, n, mCtx)
				if cancel != nil {
					cancel()
				}
				resultCh <- nodeResult{id: n.ID, result: result, err: err}
			}(node)
		}
		d.mu.Unlock()

		// If nothing is running and nothing is ready, check for deadlock/completion.
		d.mu.Lock()
		anyRunning := len(d.running) > 0
		d.mu.Unlock()

		if !anyRunning {
			d.mu.Lock()
			if d.allDone() {
				d.mu.Unlock()
				break
			}
			// Check for deadlock: remaining nodes but none dispatchable.
			remaining := d.countRemaining()
			d.mu.Unlock()
			if remaining > 0 {
				// Propagate blocks for any nodes whose deps have failed.
				d.mu.Lock()
				propagated := d.propagateBlocks()
				d.mu.Unlock()
				if propagated == 0 {
					wg.Wait()
					return d.buildResult(start), fmt.Errorf("workdag: deadlock: %d nodes undispatchable", remaining)
				}
				continue
			}
			break
		}

		// Wait for at least one result before trying again.
		d.mu.Lock()
		readyNext := d.readyNodes()
		d.mu.Unlock()
		if len(readyNext) == 0 && anyRunning {
			nr := <-resultCh
			wg.Done()
			<-sem // release semaphore slot
			d.mu.Lock()
			d.handleResult(nr.id, nr.result, nr.err)
			d.mu.Unlock()
		}
	}

	wg.Wait()
	return d.buildResult(start), nil
}

// handleResult processes the result of a completed node. Must be called with mu held.
func (d *WorkDAG) handleResult(id string, result *WorkResult, err error) {
	node, ok := d.nodes[id]
	if !ok {
		return
	}

	delete(d.running, id)
	d.releaseFiles(id)

	if err != nil {
		if node.Attempt < node.MaxRetries {
			// Retry: reset to pending for re-dispatch.
			node.Attempt++
			node.Status = WorkPending
			node.StartedAt = nil
			return
		}
		node.Status = WorkFailed
		now := time.Now()
		node.CompletedAt = &now
		if d.OnNodeFailed != nil {
			d.OnNodeFailed(node, err)
		}
		d.propagateBlocks()
		return
	}

	// Check for recursive decomposition.
	if len(result.Children) > 0 && node.Depth+1 <= node.MaxDepth {
		if addErr := d.addChildren(id, result.Children); addErr == nil {
			// Parent waits for children — mark as pending (will be re-completed
			// when all children are done).
			node.Status = WorkPending
			node.Result = result
			d.computePriorities()
			return
		}
		// If addChildren fails, treat as direct completion.
	}

	node.Status = WorkComplete
	now := time.Now()
	node.CompletedAt = &now
	node.Result = result

	// Check if this node's completion finishes a parent's children.
	d.checkParentCompletion(node)

	if d.OnNodeComplete != nil {
		d.OnNodeComplete(node, result)
	}
}

// checkParentCompletion checks if all children of a parent are complete,
// and if so marks the parent complete. Must be called with mu held.
func (d *WorkDAG) checkParentCompletion(node *WorkNode) {
	if node.ParentID == "" {
		return
	}
	parent, ok := d.nodes[node.ParentID]
	if !ok || parent.Status != WorkPending {
		return
	}
	// Check if all children are complete.
	allDone := true
	anyFailed := false
	for _, childID := range parent.Children {
		child, exists := d.nodes[childID]
		if !exists {
			continue
		}
		if child.Status == WorkFailed || child.Status == WorkBlocked {
			anyFailed = true
		}
		if child.Status != WorkComplete && child.Status != WorkFailed && child.Status != WorkBlocked {
			allDone = false
			break
		}
	}
	if !allDone {
		return
	}
	if anyFailed {
		parent.Status = WorkFailed
		now := time.Now()
		parent.CompletedAt = &now
		if d.OnNodeFailed != nil {
			d.OnNodeFailed(parent, fmt.Errorf("child node(s) failed"))
		}
	} else {
		parent.Status = WorkComplete
		now := time.Now()
		parent.CompletedAt = &now
		if d.OnNodeComplete != nil && parent.Result != nil {
			d.OnNodeComplete(parent, parent.Result)
		}
	}
	// Recurse up.
	d.checkParentCompletion(parent)
}

// addChildren adds child nodes from recursive decomposition. Must be called with mu held.
func (d *WorkDAG) addChildren(parentID string, children []WorkNode) error {
	parent, ok := d.nodes[parentID]
	if !ok {
		return fmt.Errorf("workdag: parent %q not found", parentID)
	}

	childIDs := make([]string, 0, len(children))
	for i := range children {
		child := children[i]
		child.ParentID = parentID
		child.Depth = parent.Depth + 1
		child.MaxDepth = parent.MaxDepth
		if child.Status == "" {
			child.Status = WorkPending
		}
		if child.CreatedAt.IsZero() {
			child.CreatedAt = time.Now()
		}
		if err := d.addNodeLocked(child); err != nil {
			return fmt.Errorf("workdag: add child %q: %w", child.ID, err)
		}
		childIDs = append(childIDs, child.ID)
	}
	parent.Children = append(parent.Children, childIDs...)
	return nil
}

// computePriorities computes GRPW weights for all nodes. Must be called with mu held.
func (d *WorkDAG) computePriorities() {
	// Build dependents map: node → list of nodes that depend on it.
	dependents := make(map[string][]string)
	for _, n := range d.nodes {
		for _, dep := range n.DependsOn {
			dependents[dep] = append(dependents[dep], n.ID)
		}
		// Parent's children implicitly depend on parent completing,
		// but for GRPW we also count children as downstream work.
		if n.ParentID != "" {
			dependents[n.ParentID] = append(dependents[n.ParentID], n.ID)
		}
	}

	weights := make(map[string]int)
	var weight func(string) int
	weight = func(id string) int {
		if w, ok := weights[id]; ok {
			return w
		}
		w := 1
		for _, dep := range dependents[id] {
			w += weight(dep)
		}
		weights[id] = w
		return w
	}
	for id := range d.nodes {
		weight(id)
	}
	for id, n := range d.nodes {
		n.Priority = weights[id]
	}
}

// readyNodes returns nodes whose deps are all complete and have no file
// conflicts with running nodes. Must be called with mu held.
func (d *WorkDAG) readyNodes() []*WorkNode {
	var ready []*WorkNode
	for _, n := range d.nodes {
		if n.Status != WorkPending {
			continue
		}
		if !d.depsComplete(n) {
			continue
		}
		if d.hasFileConflict(n) {
			continue
		}
		// For nodes that are parents waiting for children, skip them.
		if len(n.Children) > 0 {
			continue
		}
		ready = append(ready, n)
	}

	// Sort by priority descending (GRPW: highest first).
	sort.Slice(ready, func(i, j int) bool {
		return ready[i].Priority > ready[j].Priority
	})
	return ready
}

// depsComplete checks if all dependencies of a node are complete. Must be called with mu held.
func (d *WorkDAG) depsComplete(n *WorkNode) bool {
	for _, dep := range n.DependsOn {
		depNode, ok := d.nodes[dep]
		if !ok {
			return false
		}
		if depNode.Status != WorkComplete {
			return false
		}
	}
	return true
}

// hasFileConflict checks if any of the node's files are locked by a running node.
// Must be called with mu held.
func (d *WorkDAG) hasFileConflict(n *WorkNode) bool {
	for _, f := range n.Files {
		if owner, ok := d.fileLocks[f]; ok && owner != n.ID {
			return true
		}
	}
	return false
}

// acquireFiles locks files for a running node. Must be called with mu held.
func (d *WorkDAG) acquireFiles(n *WorkNode) {
	for _, f := range n.Files {
		d.fileLocks[f] = n.ID
	}
}

// releaseFiles unlocks files held by a node. Must be called with mu held.
func (d *WorkDAG) releaseFiles(nodeID string) {
	for f, owner := range d.fileLocks {
		if owner == nodeID {
			delete(d.fileLocks, f)
		}
	}
}

// propagateBlocks marks nodes as blocked if any dependency has failed or is blocked.
// Returns the number of newly blocked nodes. Must be called with mu held.
func (d *WorkDAG) propagateBlocks() int {
	blocked := 0
	for _, n := range d.nodes {
		if n.Status != WorkPending {
			continue
		}
		for _, dep := range n.DependsOn {
			depNode, ok := d.nodes[dep]
			if !ok {
				continue
			}
			if depNode.Status == WorkFailed || depNode.Status == WorkBlocked {
				n.Status = WorkBlocked
				now := time.Now()
				n.CompletedAt = &now
				blocked++
				break
			}
		}
	}
	return blocked
}

// allDone returns true if every node is in a terminal state. Must be called with mu held.
func (d *WorkDAG) allDone() bool {
	for _, n := range d.nodes {
		switch n.Status {
		case WorkComplete, WorkFailed, WorkBlocked:
			continue
		default:
			return false
		}
	}
	return true
}

// countRemaining returns nodes not in a terminal state. Must be called with mu held.
func (d *WorkDAG) countRemaining() int {
	count := 0
	for _, n := range d.nodes {
		switch n.Status {
		case WorkComplete, WorkFailed, WorkBlocked:
		default:
			count++
		}
	}
	return count
}

// buildResult aggregates the final DAGResult. Must be called after execution is done.
func (d *WorkDAG) buildResult(start time.Time) *DAGResult {
	d.mu.Lock()
	defer d.mu.Unlock()

	r := &DAGResult{
		Duration: time.Since(start),
	}
	filesSet := make(map[string]bool)
	var gaps []string

	for _, n := range d.nodes {
		r.NodesTotal++
		switch n.Status {
		case WorkComplete:
			r.NodesComplete++
		case WorkFailed:
			r.NodesFailed++
		case WorkBlocked:
			r.NodesBlocked++
		}
		if n.Result != nil {
			for _, f := range n.Result.FilesChanged {
				filesSet[f] = true
			}
			gaps = append(gaps, n.Result.Gaps...)
		}
	}

	for f := range filesSet {
		r.FilesChanged = append(r.FilesChanged, f)
	}
	sort.Strings(r.FilesChanged)
	r.Gaps = gaps
	return r
}
