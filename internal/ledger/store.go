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

// atomicWriteFile writes data to path via a same-directory tmp file +
// fsync + rename, so a crash mid-write can never leave a truncated or
// half-written record. Chain-file presence is WriteNode's dedup commit
// point, so its write in particular must be all-or-nothing (audit A021).
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".ledger-tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// WriteNode persists a node using the two-tier layout. The content tier
// is written FIRST, then the chain record: chain-file presence is the
// dedup commit point (see below), so committing it last closes the
// crash window where a chain record existed without its content — a
// state indistinguishable from crypto-shredding that made every retry a
// silent no-op and the content unrecoverable (audit A021). If a node
// with the same ID already exists on the chain tier, WriteNode is a
// no-op (content-addressed dedup) — this keeps retries and Batch
// re-plays idempotent.
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

	// Content tier FIRST (see doc comment): if the process dies here,
	// no chain record exists, so a retry rewrites everything.
	var contentPath string
	if len(n.Content) > 0 {
		contentPath = filepath.Join(s.contentDir, n.ID+".json")
		cr := contentRecord{Salt: n.Salt, Content: n.Content}
		contentData, err := json.MarshalIndent(cr, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal content record: %w", err)
		}
		if err := atomicWriteFile(contentPath, contentData, 0o600); err != nil {
			return fmt.Errorf("write content record: %w", err)
		}
	}

	// Chain record LAST — this is the commit point.
	if err := atomicWriteFile(chainPath, chainData, 0o600); err != nil {
		if contentPath != "" {
			os.Remove(contentPath) // don't leave an orphan content blob
		}
		return fmt.Errorf("write chain record: %w", err)
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

// WriteContentBlob persists a raw content-envelope blob for a node
// whose chain-tier record is already on disk. The blob is the
// JSON-encoded `{salt, content}` shape contentRecord uses
// internally; callers (the migration import path) re-marshal the
// envelope on the source side via the same shape so the bytes
// round-trip verbatim.
//
// This is the migration-package's complement to WriteNode for
// situations where the chain tier was written separately (e.g.
// via WriteNode with a header-only node) and only the content tier
// needs to land. Encrypted DEK envelopes pass through opaquely —
// the content bytes are written byte-for-byte without inspection.
//
// Returns an error if nodeID is empty or if the underlying file
// write fails. Re-writing an existing content tier is allowed (and
// idempotent) so a re-import overwrites cleanly.
func (s *Store) WriteContentBlob(nodeID string, envelope []byte) error {
	if nodeID == "" {
		return errors.New("ledger: WriteContentBlob: empty node id")
	}
	if len(envelope) == 0 {
		return nil
	}
	path := filepath.Join(s.contentDir, nodeID+".json")
	return atomicWriteFile(path, envelope, 0o600)
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
	if err = json.Unmarshal(chainData, &cr); err != nil {
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

	// Anti-deception foundation: verify the content tier against the
	// commitment stamped into the chain tier. Without this, a modified
	// content/<id>.json (a swapped or edited payload) reads back silently —
	// the whole point of stamping ContentCommitment into the chain is to make
	// that undetectable tamper detectable. Fail CLOSED on any mismatch.
	//
	// The commitment is computed over the canonical (compacted) content, which
	// cancels out the JSON reformatting the content tier undergoes on write
	// (see contentCommitment). A ContentCommitment of "" means the chain tier
	// predates the commitment scheme (legacy migrated node); there is nothing
	// to verify against, so it is accepted rather than rejected.
	if cr.ContentCommitment != "" {
		got := contentCommitment(ctr.Salt, ctr.Content)
		if got != cr.ContentCommitment {
			return Node{}, fmt.Errorf(
				"ledger: content tamper detected for node %s: commitment mismatch (chain=%s content=%s)",
				id, cr.ContentCommitment, got,
			)
		}
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
	return atomicWriteFile(path, data, 0o600)
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
