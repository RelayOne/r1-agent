package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Store handles filesystem-backed persistence of nodes and edges.
//
// Two-tier layout (T6 crypto-shred layout):
//
//	{rootDir}/chain/{id}.json   — structural header + content_commitment
//	                              (permanent; never deleted; Merkle linkage
//	                              survives redaction of the content tier)
//	{rootDir}/content/{id}.json — salt + original Content payload
//	                              (erasable; Redact deletes this file,
//	                              leaving the chain entry intact)
//	{rootDir}/edges/...         — directed edges as before
//
// The legacy single-tier {rootDir}/nodes/{id}.json layout is migrated on
// Open (see migrate.go). After migration the nodes/ directory is renamed
// to nodes.bak/ for one release as a safety net.
type Store struct {
	rootDir    string
	chainDir   string
	contentDir string
	edgesDir   string

	// nodesDir is retained purely so pre-split sibling helpers
	// (chainDirFor, contentDirFor) that compute paths via
	// filepath.Dir(s.nodesDir) continue to resolve. It is NOT used
	// as a write target in the new layout. The field keeps older
	// call sites compiling without touching code we don't own in
	// this change.
	nodesDir string
}

// chainRecord is the on-disk payload in {rootDir}/chain/{id}.json. It holds
// the structural header plus the content commitment — everything required
// for chain verification, and nothing sensitive.
type chainRecord struct {
	ID                string `json:"id"`
	Type              string `json:"type"`
	SchemaVersion     int    `json:"schema_version"`
	CreatedAt         string `json:"created_at"`
	CreatedBy         string `json:"created_by"`
	MissionID         string `json:"mission_id,omitempty"`
	ParentHash        string `json:"parent_hash,omitempty"`
	ContentCommitment string `json:"content_commitment"`
}

// contentRecord is the on-disk payload in {rootDir}/content/{id}.json. It
// holds the salt and canonical Content bytes. Deleting this file is the
// crypto-shred primitive; the chain tier still validates because the
// content_commitment is already stamped into the chain record.
type contentRecord struct {
	Salt    string          `json:"salt"`
	Content json.RawMessage `json:"content"`
}

// NewStore opens or creates the filesystem store under rootDir. It creates
// chain/, content/, and edges/ if missing, and runs the one-shot T6
// migration of a pre-existing nodes/ directory (see migrate.go).
func NewStore(rootDir string) (*Store, error) {
	chainDir := filepath.Join(rootDir, "chain")
	contentDir := filepath.Join(rootDir, "content")
	edgesDir := filepath.Join(rootDir, "edges")

	if err := os.MkdirAll(chainDir, 0o755); err != nil {
		return nil, fmt.Errorf("create chain dir: %w", err)
	}
	if err := os.MkdirAll(contentDir, 0o755); err != nil {
		return nil, fmt.Errorf("create content dir: %w", err)
	}
	if err := os.MkdirAll(edgesDir, 0o755); err != nil {
		return nil, fmt.Errorf("create edges dir: %w", err)
	}

	s := &Store{
		rootDir:    rootDir,
		chainDir:   chainDir,
		contentDir: contentDir,
		edgesDir:   edgesDir,
		nodesDir:   filepath.Join(rootDir, "nodes"),
	}

	// One-shot migration: if a legacy nodes/ directory exists but chain/ is
	// empty, translate every nodes/<id>.json into the new split layout and
	// rename nodes/ → nodes.bak/. Safe to run on every Open: once chain/ is
	// populated, the migration becomes a no-op.
	if err := migrateNodesToChainContent(s); err != nil {
		return nil, fmt.Errorf("migrate legacy nodes: %w", err)
	}

	return s, nil
}

// WriteNode persists a node using the two-tier layout. The chain tier is
// always written. The content tier is written whenever Content is
// non-empty. If a node with the same ID already exists on the chain tier,
// WriteNode is a no-op (content-addressed dedup) — this keeps retries and
// Batch re-plays idempotent.
func (s *Store) WriteNode(n Node) error {
	if n.ID == "" {
		return errors.New("ledger: WriteNode: node ID required")
	}
	chainPath := filepath.Join(s.chainDir, n.ID+".json")
	if _, err := os.Stat(chainPath); err == nil {
		// Chain record already exists; treat as dedup. Do NOT touch the
		// content tier either — we can't distinguish "previously redacted"
		// from "never written" and must not resurrect a redacted payload.
		return nil
	}

	cr := chainRecord{
		ID:                n.ID,
		Type:              n.Type,
		SchemaVersion:     n.SchemaVersion,
		CreatedAt:         n.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		CreatedBy:         n.CreatedBy,
		MissionID:         n.MissionID,
		ParentHash:        n.ParentHash,
		ContentCommitment: n.ContentCommitment,
	}
	chainData, err := json.MarshalIndent(cr, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal chain record: %w", err)
	}
	if err := os.WriteFile(chainPath, chainData, 0o600); err != nil {
		return fmt.Errorf("write chain record: %w", err)
	}

	if len(n.Content) > 0 {
		contentPath := filepath.Join(s.contentDir, n.ID+".json")
		cr := contentRecord{Salt: n.Salt, Content: n.Content}
		contentData, err := json.MarshalIndent(cr, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal content record: %w", err)
		}
		if err := os.WriteFile(contentPath, contentData, 0o600); err != nil {
			return fmt.Errorf("write content record: %w", err)
		}
	}

	// CS-3 stdout-event hook — no content in the payload, just the
	// structural fields CloudSwarm renders in its workspace pane.
	fireLedgerAppendHook(LedgerAppendEvent{
		NodeID:     string(n.ID),
		Type:       n.Type,
		ParentHash: n.ParentHash,
	})
	return nil
}

// ReadNode loads a node by merging its chain tier + (optional) content
// tier. A node whose content has been crypto-shredded returns a Node with
// empty Content (and no error) — callers that require content must check
// len(n.Content) > 0.
func (s *Store) ReadNode(id NodeID) (Node, error) {
	chainPath := filepath.Join(s.chainDir, id+".json")
	chainData, err := os.ReadFile(chainPath)
	if err != nil {
		return Node{}, fmt.Errorf("read chain %s: %w", id, err)
	}
	var cr chainRecord
	if err := json.Unmarshal(chainData, &cr); err != nil {
		return Node{}, fmt.Errorf("unmarshal chain %s: %w", id, err)
	}
	n := Node{
		ID:                cr.ID,
		Type:              cr.Type,
		SchemaVersion:     cr.SchemaVersion,
		CreatedBy:         cr.CreatedBy,
		MissionID:         cr.MissionID,
		ParentHash:        cr.ParentHash,
		ContentCommitment: cr.ContentCommitment,
	}
	if cr.CreatedAt != "" {
		// Use the same flexible parse path used by the migration so values
		// written by older builds still round-trip cleanly.
		if t, perr := parseTimestamp(cr.CreatedAt); perr == nil {
			n.CreatedAt = t
		}
	}

	contentPath := filepath.Join(s.contentDir, id+".json")
	contentBytes, err := os.ReadFile(contentPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Crypto-shredded or never had content — return the header-only node.
			return n, nil
		}
		return Node{}, fmt.Errorf("read content %s: %w", id, err)
	}
	var ctr contentRecord
	if err := json.Unmarshal(contentBytes, &ctr); err != nil {
		// Corrupt content tier is surfaced rather than silently erased so
		// operators can investigate.
		return Node{}, fmt.Errorf("unmarshal content %s: %w", id, err)
	}
	n.Salt = ctr.Salt
	n.Content = ctr.Content
	return n, nil
}

// WriteEdge persists an edge as a JSON file.
func (s *Store) WriteEdge(e Edge) error {
	filename := e.From + "-" + e.To + "-" + string(e.Type) + ".json"
	path := filepath.Join(s.edgesDir, filename)
	if _, err := os.Stat(path); err == nil {
		return nil // already exists
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal edge: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

// ListNodes reads every chain record and reconstitutes the full Node (with
// content, when the content tier is present). Redacted nodes surface with
// Content == nil.
func (s *Store) ListNodes() ([]Node, error) {
	entries, err := os.ReadDir(s.chainDir)
	if err != nil {
		return nil, fmt.Errorf("read chain dir: %w", err)
	}
	nodes := make([]Node, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		n, err := s.ReadNode(id)
		if err != nil {
			continue
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// ListEdges reads all edge files from the store.
func (s *Store) ListEdges() ([]Edge, error) {
	entries, err := os.ReadDir(s.edgesDir)
	if err != nil {
		return nil, fmt.Errorf("read edges dir: %w", err)
	}
	edges := make([]Edge, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.edgesDir, e.Name()))
		if err != nil {
			continue
		}
		var edge Edge
		if err := json.Unmarshal(data, &edge); err != nil {
			continue
		}
		edges = append(edges, edge)
	}
	return edges, nil
}
