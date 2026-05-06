package ledger

// store_session.go — per-session filtering + chain-root hashing for
// tracebundle v2. Spec: tracebundle-v2-format.md.
//
// The store is currently mission-agnostic at the disk layout level —
// every chain entry lives in <root>/chain/<id>.json regardless of
// MissionID. Per-session filtering is therefore a post-load filter
// over the full chain. Future: actual disk-level partitioning.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// ListNodesForSession returns chain nodes whose MissionID matches
// sessionID. Empty sessionID returns the full ListNodes (matches
// pre-v2 behavior so backward-compat callers don't need to change).
func (s *Store) ListNodesForSession(sessionID string) ([]Node, error) {
	all, err := s.ListNodes()
	if err != nil {
		return nil, err
	}
	if sessionID == "" {
		return all, nil
	}
	out := make([]Node, 0, len(all))
	for _, n := range all {
		if n.MissionID == sessionID {
			out = append(out, n)
		}
	}
	return out, nil
}

// ListEdgesForSession returns edges whose Metadata["session_id"]
// matches sessionID. Edges that don't carry a session_id metadata
// key are returned regardless (matches the conservative behavior of
// "include if we can't tell otherwise"). Empty sessionID returns
// the full ListEdges.
func (s *Store) ListEdgesForSession(sessionID string) ([]Edge, error) {
	all, err := s.ListEdges()
	if err != nil {
		return nil, err
	}
	if sessionID == "" {
		return all, nil
	}
	out := make([]Edge, 0, len(all))
	for _, e := range all {
		// Edge.Metadata is map[string]string; nil-safe via the
		// zero-value-friendly lookup pattern.
		got, hasKey := e.Metadata["session_id"]
		if !hasKey {
			out = append(out, e)
			continue
		}
		if got == sessionID {
			out = append(out, e)
		}
	}
	return out, nil
}

// ChainRootHashForSession returns a stable SHA256 chain-hash over
// the session's nodes in chronological order. Empty session →
// empty string + nil error (no nodes, nothing to hash). Single
// node → SHA256 of just that node's metadata.
//
// Algorithm: sort by (CreatedAt, ID); for each node compute
// sha256(prev_hash || node_id || content_commitment); the final
// step's hex is the result. Deterministic + downstream-verifiable
// without re-loading the ledger.
func (s *Store) ChainRootHashForSession(sessionID string) (string, error) {
	nodes, err := s.ListNodesForSession(sessionID)
	if err != nil {
		return "", err
	}
	if len(nodes) == 0 {
		return "", nil
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].CreatedAt.Equal(nodes[j].CreatedAt) {
			return nodes[i].ID < nodes[j].ID
		}
		return nodes[i].CreatedAt.Before(nodes[j].CreatedAt)
	})
	var prev []byte
	for _, n := range nodes {
		h := sha256.New()
		h.Write(prev)
		h.Write([]byte(n.ID))
		h.Write([]byte(n.ContentCommitment))
		prev = h.Sum(nil)
	}
	return hex.EncodeToString(prev), nil
}

// CanonicalManifestSignBody returns the bytes a v2 tracebundle
// manifest signs over. Caller owns the actual signing — this just
// canonicalizes the manifest content (everything except the
// signature_hex field) so signing + verifying produce the same
// input deterministically.
//
// Used by cmd/r1-server/tracebundle.go's sign + verify paths.
// Lives in the ledger package so the same canonical form is
// reachable from downstream verifiers without depending on
// cmd/r1-server.
func CanonicalManifestSignBody(format string, version int, sessionID, chainRootHash, generatedAt, signer string) []byte {
	// Layout is fixed by struct declaration order, no extra options
	// — same idiom as redact_sign.go.
	type m struct {
		Format        string `json:"format"`
		Version       int    `json:"version"`
		SessionID     string `json:"session_id"`
		ChainRootHash string `json:"chain_root_hash,omitempty"`
		GeneratedAt   string `json:"generated_at"`
		Signer        string `json:"signer,omitempty"`
	}
	c := m{
		Format:        format,
		Version:       version,
		SessionID:     sessionID,
		ChainRootHash: chainRootHash,
		GeneratedAt:   generatedAt,
		Signer:        signer,
	}
	body, err := json.Marshal(c)
	if err != nil {
		// Should never fail with this struct shape; if it does,
		// the bundle's signature won't verify either way and the
		// downstream check catches it. Surface a sentinel byte
		// to prevent silent zero-length signing.
		return []byte(fmt.Sprintf("ERROR_MANIFEST_CANONICAL: %v", err))
	}
	return body
}
