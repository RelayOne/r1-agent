// Package ledger implements an append-only, content-addressed graph for
// persistent reasoning. Nodes are immutable once written. Changes are
// expressed as new nodes with supersedes edges to prior nodes.
//
// The API has no Update, Delete, or Modify operations. The mutating surface
// is AddNode, AddEdge, and Batch -- all append-only.
package ledger

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// NodeID is a content-addressed identifier for a ledger node.
type NodeID = string

// Node is an immutable entry in the ledger graph.
//
// ParentHash links a new node to the SHA256 content hash
// of the previous node in the same mission/stance context,
// forming a Merkle chain per STOKE-002. Empty for:
//   - the first node in a mission (no predecessor exists)
//   - legacy nodes from before the Merkle-chain migration
//     (the migration tool backfills ParentHash by walking
//     creation-order within each mission context)
//
// New nodes written after the migration always set
// ParentHash. Readers validate the chain by comparing each
// node's ParentHash against the SHA256 hash of its
// predecessor's canonical JSON.
type Node struct {
	ID            NodeID          `json:"id"`
	Type          string          `json:"type"`
	SchemaVersion int             `json:"schema_version"`
	CreatedAt     time.Time       `json:"created_at"`
	CreatedBy     string          `json:"created_by"`
	MissionID     string          `json:"mission_id,omitempty"`
	Content       json.RawMessage `json:"content"`
	ParentHash    string          `json:"parent_hash,omitempty"`

	// Salt is a random 16-byte per-node nonce (hex-encoded) that blinds
	// the content commitment. A crypto-shred (delete content/<id>.json)
	// erases the salt along with the canonical content, so an attacker
	// with only the chain tier cannot mount a dictionary attack against
	// the ContentCommitment. AddNode generates Salt; callers MUST NOT
	// set it manually.
	Salt string `json:"salt,omitempty"`

	// ContentCommitment = sha256(salt || canonical(content)), hex-encoded.
	// Stamped into the chain tier; orthogonal to redaction of the content
	// tier. AddNode computes this; callers MUST NOT set it manually.
	ContentCommitment string `json:"content_commitment,omitempty"`
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
	// Open-time consistency probe + self-heal. A crash or an InsertNode failure
	// between store.WriteNode and index.InsertNode leaves a durably-written node
	// invisible to every query forever (RebuildIndex has no other trigger). If
	// the index's node count disagrees with the number of chain records on disk,
	// rebuild the index from the store (the source of truth) so no node stays
	// invisible and so AddNode's ParentHash derivation can't fork the Merkle
	// chain off a missing predecessor (STOKE-002).
	if err := l.healIndexIfStale(); err != nil {
		return nil, fmt.Errorf("ledger index heal: %w", err)
	}
	return l, nil
}

// healIndexIfStale compares the index's node count against the on-disk chain
// record count and rebuilds the index when they disagree. Cheap: one COUNT(*)
// plus one directory listing. Runs once at open.
func (l *Ledger) healIndexIfStale() error {
	indexCount, err := l.index.CountNodes()
	if err != nil {
		return fmt.Errorf("count index nodes: %w", err)
	}
	chainCount, err := l.countChainNodes()
	if err != nil {
		return fmt.Errorf("count chain nodes: %w", err)
	}
	if indexCount == chainCount {
		return nil
	}
	log.Printf("ledger: index/store drift detected (index=%d chain=%d); rebuilding index from store", indexCount, chainCount)
	if err := l.RebuildIndex(); err != nil {
		return fmt.Errorf("rebuild index: %w", err)
	}
	return nil
}

// countChainNodes counts the chain-tier records ({rootDir}/chain/*.json), which
// are the durable source of truth for how many nodes exist. Mirrors the file
// selection ListNodes uses. Kept in this package (not Store) to respect the
// ledger's file layout without widening the Store API.
func (l *Ledger) countChainNodes() (int, error) {
	chainDir := filepath.Join(l.rootDir, "chain")
	entries, err := os.ReadDir(chainDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read chain dir: %w", err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		n++
	}
	return n, nil
}

// Close releases the ledger's resources (e.g. the SQLite index).
func (l *Ledger) Close() error {
	return l.index.Close()
}

// newSalt returns a fresh hex-encoded 16-byte random salt for blinding a
// node's content commitment. Read-from-rand failures are extraordinarily
// rare; we surface them as errors so AddNode can refuse to persist a
// weakly-committed node rather than silently proceeding with a zero salt.
func newSalt() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("ledger: generate salt: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// contentCommitment = sha256(salt || canonical(content)). The salt blinds
// the commitment so an attacker who has only the chain tier cannot recover
// the content via dictionary attack, and the commitment binds the chain
// tier to the content tier so a swapped content blob is immediately
// detectable.
//
// The commitment is computed over the CANONICAL (whitespace-compacted) form
// of the content, not the raw bytes. This is what makes verification on read
// (Store.ReadNode) possible: the content tier is persisted via
// json.MarshalIndent, which reformats the stored JSON, so the bytes read back
// are NOT byte-identical to the bytes originally supplied. Compacting both at
// commit time and at verify time cancels that reformatting out, so an
// untampered node always verifies while any change to the content VALUE (the
// only kind of tamper that matters) still flips the commitment. Real callers
// pass content produced by json.Marshal (already compact), so compaction is a
// no-op for them and the on-disk commitment/IDs are unchanged by this.
func contentCommitment(salt string, content json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(salt))
	h.Write(canonicalContent(content))
	return hex.EncodeToString(h.Sum(nil))
}

// ComputeContentCommitment returns the content commitment for a node's
// (salt, content) pair — sha256(salt || canonical(content)), hex-encoded —
// the exact value ReadNode's fail-closed content verification checks against.
// Exported so callers that construct Node values directly instead of through
// AddNode (the migration import path, tools, tests) can stamp a commitment
// that will verify on read. AddNode and Batch call the unexported form.
func ComputeContentCommitment(salt string, content json.RawMessage) string {
	return contentCommitment(salt, content)
}

// canonicalContent returns the whitespace-compacted form of content so the
// commitment is stable across the JSON reformatting the content tier
// undergoes on write. If content is not valid JSON (json.Compact fails), the
// raw bytes are returned unchanged so a non-JSON payload still commits
// deterministically to itself.
func canonicalContent(content json.RawMessage) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, content); err != nil {
		return content
	}
	return buf.Bytes()
}

// canonicalHeaderBytes returns the canonical JSON of the structural header
// (everything except Content / Salt / ContentCommitment / ID / ParentHash).
// ParentHash is excluded from the ID because a node's own ID cannot depend
// on itself; ParentHash links the node to its predecessor but is not part
// of the self-ID hash input.
func canonicalHeaderBytes(n Node) ([]byte, error) {
	// Minimal struct with deterministic field order — encoding/json writes
	// struct fields in declaration order, giving stable canonical bytes
	// without any third-party canonical-JSON library.
	type headerOnly struct {
		Type          string    `json:"type"`
		SchemaVersion int       `json:"schema_version"`
		CreatedAt     time.Time `json:"created_at"`
		CreatedBy     string    `json:"created_by"`
		MissionID     string    `json:"mission_id,omitempty"`
	}
	return json.Marshal(headerOnly{
		Type:          n.Type,
		SchemaVersion: n.SchemaVersion,
		CreatedAt:     n.CreatedAt.UTC(),
		CreatedBy:     n.CreatedBy,
		MissionID:     n.MissionID,
	})
}

// computeID derives a NodeID = sha256(canonical(header) || content_commitment).
// The node MUST already have a valid ContentCommitment — callers typically
// populate it immediately beforehand via contentCommitment(salt, content).
// The returned ID is prefixed with the node Type so legacy string-prefix
// assertions elsewhere in the codebase continue to pass.
func computeID(n Node) NodeID {
	hb, err := canonicalHeaderBytes(n)
	if err != nil {
		// canonical marshaling of a fixed struct with primitive fields only
		// fails in pathological cases (extremely rare). Fall back to a
		// deterministic string so the caller still gets a usable ID; any
		// follow-up verification will surface the corruption.
		hb = []byte(fmt.Sprintf("type=%s;sv=%d;at=%s;by=%s;m=%s",
			n.Type, n.SchemaVersion, n.CreatedAt.UTC().Format(time.RFC3339Nano), n.CreatedBy, n.MissionID))
	}
	h := sha256.New()
	h.Write(hb)
	h.Write([]byte(n.ContentCommitment))
	sum := hex.EncodeToString(h.Sum(nil))
	prefix := n.Type
	if prefix == "" {
		prefix = "node"
	}
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
	if len(node.Content) == 0 {
		return "", errors.New("ledger: node content is required")
	}
	if node.SchemaVersion < 1 {
		return "", errors.New("ledger: schema_version must be >= 1")
	}
	if node.CreatedAt.IsZero() {
		node.CreatedAt = time.Now().UTC()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// STOKE-002 Merkle-chain linkage: if the caller didn't
	// supply a ParentHash, auto-fill it with the SHA256 of
	// the most recent node in the same mission context.
	// First-in-mission nodes legitimately have no
	// predecessor, so ParentHash stays empty.
	if node.ParentHash == "" {
		if prev := l.latestInMissionUnlocked(node.MissionID); prev != nil {
			if h, err := hashNodeForChain(*prev); err == nil {
				node.ParentHash = h
			}
		}
	}

	// T6 two-tier layout: generate a random salt, compute
	// content_commitment = sha256(salt || canonical(content)),
	// and derive node.ID = sha256(canonical(header) || content_commitment).
	// Salt + Content live in the erasable content tier; the chain tier
	// records only the commitment, so Store.Redact can crypto-shred by
	// deleting the content file without breaking chain verification.
	// A caller-supplied Salt/ContentCommitment is preserved (used by
	// Batch to predict IDs for forward-referencing edges).
	if node.Salt == "" {
		salt, err := newSalt()
		if err != nil {
			return "", err
		}
		node.Salt = salt
	}
	if node.ContentCommitment == "" {
		node.ContentCommitment = contentCommitment(node.Salt, node.Content)
	}
	node.ID = computeID(node)

	if err := l.store.WriteNode(node); err != nil {
		return "", fmt.Errorf("ledger: write node: %w", err)
	}
	// The node is now durable in the store. If indexing fails (transient
	// SQLITE_BUSY, a wedged handle), retry once, then rebuild the index from the
	// store before giving up -- otherwise this node is durably written but
	// invisible to every query, and a later AddNode would fork the Merkle chain
	// off a missing predecessor. Rebuild reindexes this node from the store.
	if err := l.index.InsertNode(node); err != nil {
		if retryErr := l.index.InsertNode(node); retryErr != nil {
			if rbErr := l.rebuildIndexUnlocked(); rbErr != nil {
				return "", fmt.Errorf("ledger: index node (retry then rebuild failed): insert=%w rebuild=%v", err, rbErr)
			}
		}
	}
	return node.ID, nil
}

// latestInMissionUnlocked returns the most-recently-created
// node in the mission, or nil when none exist. Caller must
// hold l.mu. Uses the index's QueryNodes + resolveUnlocked
// chain so we don't scan the store from disk on every
// AddNode call.
func (l *Ledger) latestInMissionUnlocked(missionID string) *Node {
	ids, err := l.index.QueryNodes(QueryFilter{MissionID: missionID})
	if err != nil || len(ids) == 0 {
		return nil
	}
	var latest *Node
	for _, id := range ids {
		n, err := l.resolveUnlocked(id)
		if err != nil || n == nil {
			continue
		}
		if n.MissionID != missionID {
			continue
		}
		// Total order (CreatedAt, then ID): pick the node that is greatest
		// in the SAME order VerifyChain sorts by, so the parent chosen for a
		// new node is exactly the predecessor verification will expect. On
		// equal CreatedAt the ID tiebreak is decisive and deterministic —
		// without it, linkage fell back to index-query order while verify
		// fell back to filesystem order, so the two could disagree (STOKE-002
		// ordering ambiguity: a valid chain could be rejected or a reordered
		// one accepted).
		if latest == nil ||
			n.CreatedAt.After(latest.CreatedAt) ||
			(n.CreatedAt.Equal(latest.CreatedAt) && n.ID > latest.ID) {
			latest = n
		}
	}
	return latest
}

// hashNodeForChain matches the migration tool's hashNode:
// canonical JSON with ParentHash stripped, SHA256, hex.
// Kept here (rather than imported from migrate.go) so
// AddNode doesn't pull the migration package into the hot
// path — in Go both functions in the same package resolve
// directly without import cost.
func hashNodeForChain(n Node) (string, error) {
	return hashNode(n)
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
	if fromNode.Type == nodeTypeDecisionRepo && toNode.Type == nodeTypeDecisionInternal {
		return fmt.Errorf("ledger: directionality violation: decision_repo %q cannot have edge to decision_internal %q", edge.From, edge.To)
	}

	// Validate edge-type-to-node-type combinations via the matrix.
	if err := validateEdgeMatrix(edge.Type, fromNode.Type, toNode.Type); err != nil {
		return err
	}

	if err := l.store.WriteEdge(edge); err != nil {
		return fmt.Errorf("ledger: write edge: %w", err)
	}
	// Edge is durable in the store; mirror AddNode's retry-then-rebuild so a
	// transient index failure can't leave the edge invisible to Walk/EdgesFrom.
	if err := l.index.InsertEdge(edge); err != nil {
		if retryErr := l.index.InsertEdge(edge); retryErr != nil {
			if rbErr := l.rebuildIndexUnlocked(); rbErr != nil {
				return fmt.Errorf("ledger: index edge (retry then rebuild failed): insert=%w rebuild=%v", err, rbErr)
			}
		}
	}
	return nil
}

// Get retrieves a node by ID directly from the store.
func (l *Ledger) Get(_ context.Context, id NodeID) (*Node, error) {
	// No mutex needed for reads — nodes are immutable once written,
	// so ReadNode is safe without holding the lock.
	n, err := l.store.ReadNode(id)
	if err != nil {
		return nil, fmt.Errorf("ledger: get %q: %w", id, err)
	}
	return &n, nil
}

// Query performs a read-only search by the given filter criteria.
func (l *Ledger) Query(_ context.Context, filter QueryFilter) ([]Node, error) {
	// Hold the lock only for the index query; release before filesystem reads
	// to avoid serializing all reads through the mutex.
	l.mu.Lock()
	ids, err := l.index.QueryNodes(filter)
	l.mu.Unlock()

	if err != nil {
		return nil, fmt.Errorf("ledger: query index: %w", err)
	}
	nodes := make([]Node, 0, len(ids))
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

// QueryNodes returns matching node IDs without loading full node payloads.
// Callers that need content can follow up with ReadNode for each ID.
func (l *Ledger) QueryNodes(filter QueryFilter) ([]NodeID, error) {
	return l.index.QueryNodes(filter)
}

// ReadNode loads a single node by ID from the underlying store.
func (l *Ledger) ReadNode(id NodeID) (Node, error) {
	return l.store.ReadNode(id)
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
	// Walk holds the lock for the full traversal because each iteration
	// interleaves index queries (EdgesFrom/EdgesTo) with store reads.
	// Splitting the lock would require collecting all reachable IDs first,
	// which would need a separate index-only walk. Acceptable since walks
	// are infrequent compared to point queries.
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

// Batch writes multiple nodes and edges after validating EVERY operation
// up front — validation failures (malformed ops, missing edge endpoints,
// directionality/matrix violations) write nothing. The store is
// append-only by design, so there is no rollback-by-deletion: a
// mid-batch I/O failure (disk full) can leave earlier ops committed;
// WriteNode's dedup keeps a retried batch idempotent for those.
// (The previous doc claimed full atomicity while edges were validated
// only AFTER all nodes were persisted — audit A020.)
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
	// Node IDs (and types) staged earlier in this batch — edges may
	// reference them before they hit the store.
	newNodeTypes := map[NodeID]string{}

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
			if len(n.Content) == 0 {
				return fmt.Errorf("ledger: batch op %d: node content required", i)
			}
			if n.SchemaVersion < 1 {
				return fmt.Errorf("ledger: batch op %d: schema_version must be >= 1", i)
			}
			if n.CreatedAt.IsZero() {
				n.CreatedAt = time.Now().UTC()
			}
			// T6: generate salt + commitment if the caller didn't pre-supply
			// them. Honouring caller-supplied Salt/ContentCommitment lets
			// batches include forward edges that reference a
			// pre-computed node ID — same pattern already used with
			// caller-supplied ParentHash above.
			if n.Salt == "" {
				salt, err := newSalt()
				if err != nil {
					return fmt.Errorf("ledger: batch op %d: %w", i, err)
				}
				n.Salt = salt
			}
			if n.ContentCommitment == "" {
				n.ContentCommitment = contentCommitment(n.Salt, n.Content)
			}
			n.ID = computeID(n)
			newNodeTypes[n.ID] = n.Type
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
			// Validate endpoints + the same directionality/matrix rules
			// AddEdge enforces, BEFORE anything is written (audit A020):
			// endpoints must be earlier in this batch or already stored.
			fromType, ok := newNodeTypes[e.From]
			if !ok {
				n, err := l.store.ReadNode(e.From)
				if err != nil {
					return fmt.Errorf("ledger: batch op %d: edge from %q not found: %w", i, e.From, err)
				}
				fromType = n.Type
			}
			toType, ok := newNodeTypes[e.To]
			if !ok {
				n, err := l.store.ReadNode(e.To)
				if err != nil {
					return fmt.Errorf("ledger: batch op %d: edge to %q not found: %w", i, e.To, err)
				}
				toType = n.Type
			}
			if fromType == nodeTypeDecisionRepo && toType == nodeTypeDecisionInternal {
				return fmt.Errorf("ledger: batch op %d: directionality violation: decision_repo %q cannot have edge to decision_internal %q", i, e.From, e.To)
			}
			if err := validateEdgeMatrix(e.Type, fromType, toType); err != nil {
				return fmt.Errorf("ledger: batch op %d: %w", i, err)
			}
			items = append(items, prepared{edge: &e})
		default:
			return fmt.Errorf("ledger: batch op %d: unknown op type", i)
		}
	}

	// Phase 2: write all nodes first so edges can reference them.
	// All validation already happened in Phase 1.
	for _, it := range items {
		if it.node != nil {
			if err := l.store.WriteNode(*it.node); err != nil {
				return fmt.Errorf("ledger: batch write node: %w", err)
			}
			if err := l.index.InsertNode(*it.node); err != nil {
				return fmt.Errorf("ledger: batch index node: %w", err)
			}
		}
	}

	// Phase 3: write edges.
	for _, it := range items {
		if it.edge != nil {
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
	return l.rebuildIndexUnlocked()
}

// rebuildIndexUnlocked drops and repopulates the index from the store. The
// caller MUST hold l.mu -- AddNode/AddEdge call this from inside their locked
// critical sections, so it must not re-acquire the mutex.
func (l *Ledger) rebuildIndexUnlocked() error {
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
		if iErr := l.index.InsertNode(n); iErr != nil {
			return fmt.Errorf("ledger: reindex node %s: %w", n.ID, iErr)
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
