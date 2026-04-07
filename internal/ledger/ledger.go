// Package ledger implements an append-only, content-addressed graph for
// persistent reasoning. Nodes are immutable once written. Changes are
// expressed as new nodes with supersedes edges to prior nodes.
//
// The API has no Update, Delete, or Modify operations. The mutating surface
// is AddNode, AddEdge, and Batch -- all append-only.
package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

// NodeID is a content-addressed identifier for a ledger node.
type NodeID = string

// Node is an immutable entry in the ledger graph.
type Node struct {
	ID            NodeID          `json:"id"`
	Type          string          `json:"type"`
	SchemaVersion int             `json:"schema_version"`
	CreatedAt     time.Time       `json:"created_at"`
	CreatedBy     string          `json:"created_by"`
	MissionID     string          `json:"mission_id,omitempty"`
	Content       json.RawMessage `json:"content"`
}

// EdgeType defines the relationship between two nodes.
type EdgeType string

const (
	EdgeSupersedes  EdgeType = "supersedes"
	EdgeDependsOn   EdgeType = "depends_on"
	EdgeContradicts EdgeType = "contradicts"
	EdgeExtends     EdgeType = "extends"
	EdgeReferences  EdgeType = "references"
	EdgeResolves    EdgeType = "resolves"
	EdgeDistills    EdgeType = "distills"
)

// Edge is an immutable directed relationship between two nodes.
type Edge struct {
	From     NodeID            `json:"from"`
	To       NodeID            `json:"to"`
	Type     EdgeType          `json:"type"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// WalkDirection controls graph traversal direction.
type WalkDirection int

const (
	// Forward follows edges from source to target.
	Forward WalkDirection = iota
	// Backward follows edges from target to source.
	Backward
)

// QueryFilter specifies read-only search criteria.
type QueryFilter struct {
	Type      string     `json:"type,omitempty"`
	MissionID string     `json:"mission_id,omitempty"`
	CreatedBy string     `json:"created_by,omitempty"`
	Since     *time.Time `json:"since,omitempty"`
	Until     *time.Time `json:"until,omitempty"`
	Limit     int        `json:"limit,omitempty"`
}

// BatchOpType distinguishes batch operation kinds.
type BatchOpType int

const (
	BatchAddNode BatchOpType = iota
	BatchAddEdge
)

// BatchOp is a single operation within a Batch call.
type BatchOp struct {
	OpType BatchOpType
	Node   *Node
	Edge   *Edge
}

// validEdgeTypes is the set of recognised edge types.
var validEdgeTypes = map[EdgeType]bool{
	EdgeSupersedes:  true,
	EdgeDependsOn:   true,
	EdgeContradicts: true,
	EdgeExtends:     true,
	EdgeReferences:  true,
	EdgeResolves:    true,
	EdgeDistills:    true,
}

// Ledger is the append-only graph substrate for persistent reasoning.
type Ledger struct {
	rootDir string
	store   *Store
	index   *Index
	mu      sync.Mutex
}

// New opens or creates a ledger rooted at rootDir.
// rootDir is typically ".stoke/ledger/".
func New(rootDir string) (*Ledger, error) {
	s, err := NewStore(rootDir)
	if err != nil {
		return nil, fmt.Errorf("ledger store: %w", err)
	}
	idx, err := NewIndex(rootDir)
	if err != nil {
		return nil, fmt.Errorf("ledger index: %w", err)
	}
	l := &Ledger{
		rootDir: rootDir,
		store:   s,
		index:   idx,
	}
	return l, nil
}

// Close releases the ledger's resources (e.g. the SQLite index).
func (l *Ledger) Close() error {
	return l.index.Close()
}

// computeID produces a content-addressed NodeID from the node's content,
// timestamp, and a nonce derived from the content length.
func computeID(n Node) NodeID {
	h := sha256.New()
	h.Write(n.Content)
	h.Write([]byte(n.CreatedAt.Format(time.RFC3339Nano)))
	h.Write([]byte(n.CreatedBy))
	h.Write([]byte(n.Type))
	sum := hex.EncodeToString(h.Sum(nil))
	prefix := n.Type
	if prefix == "" {
		prefix = "node"
	}
	// Use first 8 hex chars for the suffix.
	if len(sum) > 8 {
		sum = sum[:8]
	}
	return prefix + "-" + sum
}

// AddNode validates, assigns a content-addressed ID, persists to the
// git-tracked store, and updates the index. Returns the assigned NodeID.
func (l *Ledger) AddNode(_ context.Context, node Node) (NodeID, error) {
	if node.Type == "" {
		return "", errors.New("ledger: node type is required")
	}
	if node.Content == nil || len(node.Content) == 0 {
		return "", errors.New("ledger: node content is required")
	}
	if node.SchemaVersion < 1 {
		return "", errors.New("ledger: schema_version must be >= 1")
	}
	if node.CreatedAt.IsZero() {
		node.CreatedAt = time.Now().UTC()
	}

	node.ID = computeID(node)

	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.store.WriteNode(node); err != nil {
		return "", fmt.Errorf("ledger: write node: %w", err)
	}
	if err := l.index.InsertNode(node); err != nil {
		return "", fmt.Errorf("ledger: index node: %w", err)
	}
	return node.ID, nil
}

// AddEdge attaches a new edge between two existing nodes.
// Both endpoints must exist. Edge types must be valid.
func (l *Ledger) AddEdge(_ context.Context, edge Edge) error {
	if edge.From == "" || edge.To == "" {
		return errors.New("ledger: edge from and to are required")
	}
	if !validEdgeTypes[edge.Type] {
		return fmt.Errorf("ledger: unknown edge type %q", edge.Type)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Verify both endpoints exist.
	fromNode, err := l.store.ReadNode(edge.From)
	if err != nil {
		return fmt.Errorf("ledger: from node %q not found: %w", edge.From, err)
	}
	toNode, err := l.store.ReadNode(edge.To)
	if err != nil {
		return fmt.Errorf("ledger: to node %q not found: %w", edge.To, err)
	}

	// Decision log directionality: repo decisions cannot cite internal decisions.
	if fromNode.Type == "decision_repo" && toNode.Type == "decision_internal" {
		return fmt.Errorf("ledger: directionality violation: decision_repo %q cannot have edge to decision_internal %q", edge.From, edge.To)
	}

	// distills edges must go from decision_internal to decision_repo.
	if edge.Type == EdgeDistills {
		if fromNode.Type != "decision_internal" || toNode.Type != "decision_repo" {
			return fmt.Errorf("ledger: distills edges must go from decision_internal to decision_repo; got %s -> %s", fromNode.Type, toNode.Type)
		}
	}

	if err := l.store.WriteEdge(edge); err != nil {
		return fmt.Errorf("ledger: write edge: %w", err)
	}
	if err := l.index.InsertEdge(edge); err != nil {
		return fmt.Errorf("ledger: index edge: %w", err)
	}
	return nil
}

// Get retrieves a node by ID directly from the store.
func (l *Ledger) Get(_ context.Context, id NodeID) (*Node, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.store.ReadNode(id)
	if err != nil {
		return nil, fmt.Errorf("ledger: get %q: %w", id, err)
	}
	return &n, nil
}

// Query performs a read-only search by the given filter criteria.
func (l *Ledger) Query(_ context.Context, filter QueryFilter) ([]Node, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ids, err := l.index.QueryNodes(filter)
	if err != nil {
		return nil, fmt.Errorf("ledger: query index: %w", err)
	}
	var nodes []Node
	for _, id := range ids {
		n, err := l.store.ReadNode(id)
		if err != nil {
			// Integrity violation — index says the node exists but the store
			// cannot find it. Do not silently skip.
			log.Printf("ledger: INTEGRITY VIOLATION: node %s indexed but not on disk: %v", id, err)
			return nil, fmt.Errorf("ledger: integrity violation: index references node %q but store cannot read it: %w", id, err)
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// Resolve follows the supersedes chain from the given node ID to find
// the current effective node.
func (l *Ledger) Resolve(_ context.Context, id NodeID) (*Node, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.resolveUnlocked(id)
}

func (l *Ledger) resolveUnlocked(id NodeID) (*Node, error) {
	current := id
	visited := map[NodeID]bool{}
	for {
		if visited[current] {
			return nil, fmt.Errorf("ledger: cycle detected in supersedes chain at %q", current)
		}
		visited[current] = true

		// Find any node that supersedes current (i.e. an edge where
		// To == current and Type == supersedes).
		successors, err := l.index.EdgesTo(current, EdgeSupersedes)
		if err != nil {
			return nil, fmt.Errorf("ledger: resolve edges: %w", err)
		}
		if len(successors) == 0 {
			// current is the effective node
			n, err := l.store.ReadNode(current)
			if err != nil {
				return nil, fmt.Errorf("ledger: resolve read: %w", err)
			}
			return &n, nil
		}
		// Follow the first superseding node.
		current = successors[0]
	}
}

// Walk traverses the graph starting from id, following edges of the specified
// types in the given direction, returning all reachable nodes.
func (l *Ledger) Walk(_ context.Context, id NodeID, direction WalkDirection, edgeTypes []EdgeType) ([]Node, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	visited := map[NodeID]bool{}
	var result []Node
	queue := []NodeID{id}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if visited[cur] {
			continue
		}
		visited[cur] = true

		n, err := l.store.ReadNode(cur)
		if err != nil {
			log.Printf("ledger: INTEGRITY VIOLATION: node %s referenced but not on disk: %v", cur, err)
			return nil, fmt.Errorf("ledger: integrity violation: node %q referenced in graph but store cannot read it: %w", cur, err)
		}
		result = append(result, n)

		for _, et := range edgeTypes {
			var neighbors []NodeID
			var nerr error
			if direction == Forward {
				neighbors, nerr = l.index.EdgesFrom(cur, et)
			} else {
				neighbors, nerr = l.index.EdgesTo(cur, et)
			}
			if nerr != nil {
				continue
			}
			queue = append(queue, neighbors...)
		}
	}
	return result, nil
}

// Batch atomically writes multiple nodes and edges. All operations succeed
// or none do.
func (l *Ledger) Batch(_ context.Context, ops []BatchOp) error {
	if len(ops) == 0 {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Phase 1: validate and prepare all operations.
	type prepared struct {
		node *Node
		edge *Edge
	}
	var items []prepared

	for i, op := range ops {
		switch op.OpType {
		case BatchAddNode:
			if op.Node == nil {
				return fmt.Errorf("ledger: batch op %d: nil node", i)
			}
			n := *op.Node
			if n.Type == "" {
				return fmt.Errorf("ledger: batch op %d: node type required", i)
			}
			if n.Content == nil || len(n.Content) == 0 {
				return fmt.Errorf("ledger: batch op %d: node content required", i)
			}
			if n.SchemaVersion < 1 {
				return fmt.Errorf("ledger: batch op %d: schema_version must be >= 1", i)
			}
			if n.CreatedAt.IsZero() {
				n.CreatedAt = time.Now().UTC()
			}
			n.ID = computeID(n)
			items = append(items, prepared{node: &n})
		case BatchAddEdge:
			if op.Edge == nil {
				return fmt.Errorf("ledger: batch op %d: nil edge", i)
			}
			e := *op.Edge
			if e.From == "" || e.To == "" {
				return fmt.Errorf("ledger: batch op %d: edge from/to required", i)
			}
			if !validEdgeTypes[e.Type] {
				return fmt.Errorf("ledger: batch op %d: unknown edge type %q", i, e.Type)
			}
			items = append(items, prepared{edge: &e})
		default:
			return fmt.Errorf("ledger: batch op %d: unknown op type", i)
		}
	}

	// Phase 2: write all nodes first so edges can reference them.
	// Collect new node IDs so edge validation can find them.
	newNodeIDs := map[NodeID]bool{}
	for _, it := range items {
		if it.node != nil {
			if err := l.store.WriteNode(*it.node); err != nil {
				return fmt.Errorf("ledger: batch write node: %w", err)
			}
			if err := l.index.InsertNode(*it.node); err != nil {
				return fmt.Errorf("ledger: batch index node: %w", err)
			}
			newNodeIDs[it.node.ID] = true
		}
	}

	// Phase 3: write edges, verifying endpoints exist.
	for _, it := range items {
		if it.edge != nil {
			if !newNodeIDs[it.edge.From] {
				if _, err := l.store.ReadNode(it.edge.From); err != nil {
					return fmt.Errorf("ledger: batch edge from %q not found: %w", it.edge.From, err)
				}
			}
			if !newNodeIDs[it.edge.To] {
				if _, err := l.store.ReadNode(it.edge.To); err != nil {
					return fmt.Errorf("ledger: batch edge to %q not found: %w", it.edge.To, err)
				}
			}
			if err := l.store.WriteEdge(*it.edge); err != nil {
				return fmt.Errorf("ledger: batch write edge: %w", err)
			}
			if err := l.index.InsertEdge(*it.edge); err != nil {
				return fmt.Errorf("ledger: batch index edge: %w", err)
			}
		}
	}

	return nil
}

// RebuildIndex drops and rebuilds the SQLite index from the filesystem store.
func (l *Ledger) RebuildIndex() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.index.Drop(); err != nil {
		return fmt.Errorf("ledger: drop index: %w", err)
	}
	if err := l.index.CreateTables(); err != nil {
		return fmt.Errorf("ledger: create tables: %w", err)
	}

	nodes, err := l.store.ListNodes()
	if err != nil {
		return fmt.Errorf("ledger: list nodes: %w", err)
	}
	for _, n := range nodes {
		if err := l.index.InsertNode(n); err != nil {
			return fmt.Errorf("ledger: reindex node %s: %w", n.ID, err)
		}
	}

	edges, err := l.store.ListEdges()
	if err != nil {
		return fmt.Errorf("ledger: list edges: %w", err)
	}
	for _, e := range edges {
		if err := l.index.InsertEdge(e); err != nil {
			return fmt.Errorf("ledger: reindex edge: %w", err)
		}
	}

	return nil
}

// Verify walks the index and checks that every indexed node can be read from
// the store. Returns an error at the first missing or corrupted file.
// Call this at startup (e.g. `stoke init`, `stoke status`) to catch corruption early.
func (l *Ledger) Verify(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	ids, err := l.index.QueryNodes(QueryFilter{})
	if err != nil {
		return fmt.Errorf("ledger: verify: query index: %w", err)
	}

	for _, id := range ids {
		if _, err := l.store.ReadNode(id); err != nil {
			return fmt.Errorf("ledger: verify: node %q indexed but missing from store: %w", id, err)
		}
	}
	return nil
}
