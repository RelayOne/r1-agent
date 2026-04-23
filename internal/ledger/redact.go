package ledger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RedactionRecord is the audit-trail return value from Store.Redact. It
// describes the redaction event without exposing any of the wiped content.
//
// Per spec/ledger-redaction.md §6, a fully-formed redaction event also
// carries an ed25519 signature. That signing path is the responsibility of
// the encryption-at-rest spec; this MVP returns the unsigned record so the
// caller can persist or sign it however the surrounding system prefers.
type RedactionRecord struct {
	NodeID     string    `json:"node_id"`
	RedactedAt time.Time `json:"redacted_at"`
	Reason     string    `json:"reason"`
}

// chainDirFor returns the chain-tier directory for the store.
func (s *Store) chainDirFor() string {
	if s.chainDir != "" {
		return s.chainDir
	}
	return filepath.Join(filepath.Dir(s.nodesDir), "chain")
}

// contentDirFor returns the content-tier directory for the store.
func (s *Store) contentDirFor() string {
	if s.contentDir != "" {
		return s.contentDir
	}
	return filepath.Join(filepath.Dir(s.nodesDir), "content")
}

// Redact crypto-shreds the content-tier blob for nodeID by DELETING the
// content/<id>.json file outright. The chain-tier file is left untouched,
// preserving the Merkle proof: any successor's parent_hash that referenced
// this node continues to verify because the chain record (which includes
// the content_commitment) never changed.
//
// Contrast with a tombstone: deletion is the stronger crypto-shred
// primitive because it removes the salt and canonical content from the
// filesystem entirely, leaving nothing for an attacker to brute-force
// against the commitment. A successful Redact is indistinguishable on
// disk from "this node never had content."
//
// Redact returns the RedactionRecord describing the event for downstream
// signing or audit logging. It returns an error when:
//   - nodeID is empty
//   - the content file exists but cannot be removed (filesystem permission)
//   - reason is empty (a redaction without justification is rejected; both
//     the retention-policy SWEEP and the GDPR right-to-erasure paths
//     require a reason string for the audit trail)
//
// Removing an already-absent content file is NOT an error — Redact is
// idempotent. Callers can re-run the policy sweep without tripping over
// previously-redacted rows.
func (s *Store) Redact(ctx context.Context, nodeID string, reason string) (RedactionRecord, error) {
	if nodeID == "" {
		return RedactionRecord{}, errors.New("ledger redact: nodeID is required")
	}
	if reason == "" {
		return RedactionRecord{}, errors.New("ledger redact: reason is required for audit trail")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return RedactionRecord{}, fmt.Errorf("ledger redact: context cancelled: %w", err)
		}
	}

	now := time.Now().UTC()
	record := RedactionRecord{
		NodeID:     nodeID,
		RedactedAt: now,
		Reason:     reason,
	}

	contentPath := filepath.Join(s.contentDirFor(), nodeID+".json")
	if err := os.Remove(contentPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return RedactionRecord{}, fmt.Errorf("ledger redact: remove content tier: %w", err)
	}
	return record, nil
}

// IsRedacted reports whether the content-tier blob for nodeID has been
// crypto-shredded. Truth table:
//
//	chain exists && content exists  → false (intact)
//	chain exists && content missing → true  (redacted)
//	chain missing                   → false (never written; not a redaction)
//
// IsRedacted does not distinguish "never had content" from "content was
// deleted" when the chain entry is absent; in the chain-absent case the
// node simply doesn't exist as far as the ledger is concerned.
func (s *Store) IsRedacted(nodeID string) (bool, error) {
	if nodeID == "" {
		return false, errors.New("ledger isredacted: nodeID is required")
	}
	chainPath := filepath.Join(s.chainDirFor(), nodeID+".json")
	if _, err := os.Stat(chainPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("ledger isredacted: stat chain tier: %w", err)
	}
	contentPath := filepath.Join(s.contentDirFor(), nodeID+".json")
	if _, err := os.Stat(contentPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, fmt.Errorf("ledger isredacted: stat content tier: %w", err)
	}
	return false, nil
}
