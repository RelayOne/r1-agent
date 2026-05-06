// Package main — tracebundle_source.go
//
// Spec 4 §10 T15-T17 caller wiring (BLOCKED-PARTIAL clear-out): the
// production TracebundleSource implementation backed by ledger.Store.
//
// tracebundle.go's serveTracebundle takes a TracebundleSource interface
// so the writer can be tested with a fixture. This file implements
// that interface against the real ledger.Store + the redaction map
// already loaded by Spec 3's redaction.go.
//
// Wiring path:
//   GET /api/session/{id}/export.tracebundle
//     → ledgerTracebundleSource (this file)
//       → ledger.Store.ListNodes / ListEdges / IsRedacted
//       → ledger.Store.RedactionsFor (added by #165 / closed #159)
//
// The DB-side per-session filtering still lives at "future spec"
// status — ledger.Store doesn't expose per-session listing today —
// so this source returns the full ledger contents. Spec 5's fixture
// loader will narrow by session id once that surface lands.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/RelayOne/r1/internal/ledger"
)

// ledgerTracebundleSource adapts a *ledger.Store + sessionID into the
// TracebundleSource interface declared in tracebundle.go.
type ledgerTracebundleSource struct {
	store     *ledger.Store
	sessionID string
	cache     []ledger.Node // populated by Chain(); reused by Content()/IsRedacted().
	cached    bool
}

// newLedgerTracebundleSource constructs the adapter. Caller is
// responsible for ensuring the store is non-nil (the handler 404s
// otherwise).
func newLedgerTracebundleSource(store *ledger.Store, sessionID string) *ledgerTracebundleSource {
	return &ledgerTracebundleSource{store: store, sessionID: sessionID}
}

// SessionID returns the id the bundle is keyed by.
func (s *ledgerTracebundleSource) SessionID() string { return s.sessionID }

// ChainRootHash currently returns "" — the ledger doesn't yet expose
// ChainRootHash returns the per-session Merkle-style root hash via
// Store.ChainRootHashForSession (added by tracebundle-v2-format.md).
// Empty session — or a session with zero nodes — returns "" + nil
// internally; the manifest field is omitempty so it stays absent.
func (s *ledgerTracebundleSource) ChainRootHash() string {
	if s.store == nil {
		return ""
	}
	root, err := s.store.ChainRootHashForSession(s.sessionID)
	if err != nil {
		return ""
	}
	return root
}

// Chain returns every chain-tier node, projected into the export
// shape. Loads via Store.ListNodes; the result is cached so repeated
// calls within one bundle write don't re-walk the directory.
func (s *ledgerTracebundleSource) Chain() []TracebundleNode {
	s.ensureCache()
	out := make([]TracebundleNode, 0, len(s.cache))
	for _, n := range s.cache {
		// Header projection: copy the chain-tier metadata into the
		// header field so consumers have full context without needing
		// to re-derive it from the salted content tier.
		header, _ := json.Marshal(struct {
			ID            ledger.NodeID `json:"id"`
			Type          string        `json:"type"`
			SchemaVersion int           `json:"schema_version"`
			CreatedAt     string        `json:"created_at"`
			CreatedBy     string        `json:"created_by"`
			MissionID     string        `json:"mission_id,omitempty"`
			ParentHash    string        `json:"parent_hash,omitempty"`
		}{
			ID:            n.ID,
			Type:          n.Type,
			SchemaVersion: n.SchemaVersion,
			CreatedAt:     n.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
			CreatedBy:     n.CreatedBy,
			MissionID:     n.MissionID,
			ParentHash:    n.ParentHash,
		})
		out = append(out, TracebundleNode{
			ID:            string(n.ID),
			Type:          n.Type,
			SchemaVersion: n.SchemaVersion,
			CreatedAt:     n.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
			CreatedBy:     n.CreatedBy,
			ParentHash:    n.ParentHash,
			Commitment:    n.ContentCommitment,
			Header:        header,
		})
	}
	return out
}

// Edges returns every edge as TracebundleEdge.
func (s *ledgerTracebundleSource) Edges() []TracebundleEdge {
	if s.store == nil {
		return nil
	}
	all, err := s.store.ListEdgesForSession(s.sessionID)
	if err != nil {
		return nil
	}
	out := make([]TracebundleEdge, 0, len(all))
	for _, e := range all {
		out = append(out, TracebundleEdge{
			From: string(e.From),
			To:   string(e.To),
			Kind: string(e.Type),
		})
	}
	return out
}

// Content returns the raw chain+content payload for a non-redacted
// node. ListNodes already attaches the content blob (or nil for
// redacted), so the cache lookup is sufficient.
func (s *ledgerTracebundleSource) Content(nodeID string) ([]byte, error) {
	s.ensureCache()
	for _, n := range s.cache {
		if string(n.ID) != nodeID {
			continue
		}
		if n.Content == nil {
			return nil, fmt.Errorf("ledger tracebundle: content tier missing for %s", nodeID)
		}
		return n.Content, nil
	}
	return nil, fmt.Errorf("ledger tracebundle: node not found in chain: %s", nodeID)
}

// IsRedacted reports whether the node's content tier was wiped.
// Defers to ledger.Store.IsRedacted which already implements the
// (chain-present + content-absent) → true predicate.
func (s *ledgerTracebundleSource) IsRedacted(nodeID string) bool {
	if s.store == nil {
		return false
	}
	red, err := s.store.IsRedacted(nodeID)
	if err != nil {
		return false
	}
	return red
}

func (s *ledgerTracebundleSource) ensureCache() {
	if s.cached {
		return
	}
	if s.store != nil {
		nodes, _ := s.store.ListNodesForSession(s.sessionID)
		s.cache = nodes
	}
	s.cached = true
}

// serveTracebundleAdapter is the DB-aware mountUI entry point. It
// resolves the session's LedgerDir via DB.GetSession, opens the
// ledger.Store at that path, builds a ledgerTracebundleSource, and
// delegates to serveTracebundle. v2-only — falls through to 404
// when R1_SERVER_UI_V2 isn't set.
func (d *DB) serveTracebundleAdapter(w http.ResponseWriter, r *http.Request) {
	if !v2Enabled() {
		http.NotFound(w, r)
		return
	}
	sid := r.PathValue("id")
	if sid == "" {
		http.NotFound(w, r)
		return
	}
	row, err := d.GetSession(sid)
	if err != nil || row.LedgerDir == "" {
		http.NotFound(w, r)
		return
	}
	store, err := ledger.NewStore(row.LedgerDir)
	if err != nil {
		http.Error(w, "tracebundle: open ledger: "+err.Error(), http.StatusInternalServerError)
		return
	}
	src := newLedgerTracebundleSource(store, sid)
	serveTracebundle(src)(w, r)
}
