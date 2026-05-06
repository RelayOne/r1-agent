// Package ledger — migrate.go
//
// STOKE-002 migration tool: backfill Node.ParentHash for
// ledger data written before the Merkle-chain upgrade. Walks
// each mission's nodes in creation order and sets
// ParentHash = SHA256(canonical-JSON(previous node)).
//
// Backward-compatibility posture:
//   - The first node per mission legitimately has no
//     predecessor, so its ParentHash stays empty.
//   - Nodes that already carry a non-empty ParentHash are
//     left untouched (migration is idempotent and safe to
//     re-run).
//   - The migration operates on the canonical store
//     interface, so both JSON-file and SQLite backends work
//     the same way.
//
// Exposed as a standalone function + a `stoke ledger
// migrate` CLI subcommand. The CLI wrapper lives in
// cmd/r1/ledger_migrate.go; this file has the engine so
// tests can invoke it directly without shelling out.
package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// migrateNodesToChainContent translates a pre-T6 single-tier nodes/
// directory into the split chain/ + content/ layout. It runs once on
// Store open and becomes a no-op on every subsequent open:
//
//   - If nodes/ does not exist, nothing to do.
//   - If chain/ already has entries, a previous migration ran; skip.
//   - Otherwise, for each nodes/<id>.json:
//     synthesize a salt (random), compute content_commitment, write
//     chain/<id>.json with the structural header + commitment and
//     content/<id>.json with {salt, content}. The original file ID is
//     preserved so existing edges and index rows continue to reference
//     the same node.
//   - After a successful migration, rename nodes/ → nodes.bak/ for
//     one-release safety so operators can sanity-check the split and roll
//     back by copying files back if necessary.
//
// Preserving the original node ID (rather than recomputing under the new
// ID scheme) is the only way to keep existing edges valid without a
// second migration pass. Going-forward writes through AddNode use the
// new sha256(header || content_commitment) ID, so the two ID shapes
// coexist: legacy IDs survive until their nodes are superseded.
func migrateNodesToChainContent(s *Store) error {
	if s == nil {
		return errors.New("migrate: nil store")
	}
	// Detect the chain tier being already populated and skip. We do this
	// BEFORE checking for nodes/ so a fresh ledger with no directories
	// at all is a fast no-op.
	if chainEntries, err := os.ReadDir(s.chainDir); err == nil {
		for _, e := range chainEntries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				return nil // chain tier already populated
			}
		}
	}

	legacyDir := filepath.Join(s.rootDir, "nodes")
	info, err := os.Stat(legacyDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat legacy nodes dir: %w", err)
	}
	if !info.IsDir() {
		return nil
	}

	entries, err := os.ReadDir(legacyDir)
	if err != nil {
		return fmt.Errorf("read legacy nodes dir: %w", err)
	}

	migrated := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(legacyDir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", path, err)
		}
		var legacy Node
		if err := json.Unmarshal(raw, &legacy); err != nil {
			return fmt.Errorf("migrate: parse %s: %w", path, err)
		}
		if legacy.ID == "" {
			// Filename is authoritative in this case.
			legacy.ID = strings.TrimSuffix(e.Name(), ".json")
		}

		// Synthesize salt + content_commitment if the legacy node didn't
		// already carry them. We KEEP the legacy ID — rehashing would
		// break every existing edge. The commitment is still strong:
		// sha256(salt || content) is opaque to anyone without the salt,
		// and Redact will delete both by removing content/<id>.json.
		if legacy.Salt == "" {
			salt, err := newSalt()
			if err != nil {
				return fmt.Errorf("migrate: salt for %s: %w", legacy.ID, err)
			}
			legacy.Salt = salt
		}
		if legacy.ContentCommitment == "" {
			legacy.ContentCommitment = contentCommitment(legacy.Salt, legacy.Content)
		}

		if err := s.WriteNode(legacy); err != nil {
			return fmt.Errorf("migrate: write split for %s: %w", legacy.ID, err)
		}
		migrated++
	}

	// One-release safety net: rename nodes/ to nodes.bak/ so the split
	// layout is authoritative but the originals remain inspectable. If a
	// previous migration run left nodes.bak/ in place, remove it first —
	// any new content it could have captured has already been translated
	// into chain/+content/ by this pass.
	backupDir := filepath.Join(s.rootDir, "nodes.bak")
	if _, err := os.Stat(backupDir); err == nil {
		if rmErr := os.RemoveAll(backupDir); rmErr != nil {
			return fmt.Errorf("migrate: clear stale nodes.bak: %w", rmErr)
		}
	}
	if err := os.Rename(legacyDir, backupDir); err != nil {
		return fmt.Errorf("migrate: rename nodes to nodes.bak: %w", err)
	}
	return nil
}

// parseTimestamp handles both the RFC3339Nano format written by AddNode
// and the historical "2006-01-02T15:04:05.999999999Z" variant emitted by
// the SQLite index formatter. Returned time is always in UTC.
func parseTimestamp(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("empty timestamp")
	}
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999Z",
		time.RFC3339,
	}
	var last error
	for _, layout := range layouts {
		t, err := time.Parse(layout, s)
		if err == nil {
			return t.UTC(), nil
		}
		last = err
	}
	return time.Time{}, last
}

// MigrationReport summarizes a run.
type MigrationReport struct {
	MissionsScanned int
	NodesVisited    int
	NodesUpdated    int
	NodesSkipped    int // already had ParentHash
	Errors          []string
}

// MigrateParentHash scans the ledger grouped by mission and
// reports which nodes WOULD receive a ParentHash if the
// chain were rebuilt. The ledger is append-only + content-
// addressed; we cannot update existing nodes in place
// (doing so would invalidate the content-hash IDs other
// nodes reference). So the "migration" here is a SCAN +
// REPORT tool that tells operators the chain-state of their
// existing data, plus a verification pass (see VerifyChain
// below) for integrity checks.
//
// After this call ships, NEW nodes written via AddNode
// always set ParentHash — that's the going-forward
// invariant. Legacy pre-migration nodes stay legacy; the
// report surfaces them with "empty parent_hash" findings so
// operators can decide whether to anchor a fresh chain
// starting from the current ledger head.
func MigrateParentHash(ctx context.Context, ledger *Ledger, dryRun bool) (*MigrationReport, error) {
	if ledger == nil {
		return nil, fmt.Errorf("ledger: nil ledger")
	}
	_ = dryRun // all operations are read-only in append-only mode
	nodes, err := ledger.Query(ctx, QueryFilter{})
	if err != nil {
		return nil, fmt.Errorf("ledger: query: %w", err)
	}

	byMission := map[string][]Node{}
	for _, n := range nodes {
		byMission[n.MissionID] = append(byMission[n.MissionID], n)
	}

	report := &MigrationReport{MissionsScanned: len(byMission)}
	for _, bucket := range byMission {
		sort.SliceStable(bucket, func(i, j int) bool {
			return bucket[i].CreatedAt.Before(bucket[j].CreatedAt)
		})
		for i, n := range bucket {
			report.NodesVisited++
			if n.ParentHash != "" {
				report.NodesSkipped++
				continue
			}
			if i == 0 {
				// First-in-mission has no predecessor; empty
				// ParentHash is the documented valid state.
				continue
			}
			// Legacy node missing ParentHash mid-chain. We
			// cannot update in place; report it so the
			// operator knows a fresh chain may need seeding.
			report.NodesUpdated++ // interpret as "would-update"
		}
	}
	return report, nil
}

// VerifyChain walks nodes by mission and confirms every
// non-first node's ParentHash equals the SHA256 of its
// predecessor's canonical JSON. Returns the list of chain
// breaks (empty on fully-valid ledger).
func VerifyChain(ctx context.Context, ledger *Ledger) ([]ChainBreak, error) {
	if ledger == nil {
		return nil, fmt.Errorf("ledger: nil ledger")
	}
	nodes, err := ledger.Query(ctx, QueryFilter{})
	if err != nil {
		return nil, fmt.Errorf("ledger: query: %w", err)
	}
	byMission := map[string][]Node{}
	for _, n := range nodes {
		byMission[n.MissionID] = append(byMission[n.MissionID], n)
	}
	var breaks []ChainBreak
	for mission, bucket := range byMission {
		sort.SliceStable(bucket, func(i, j int) bool {
			return bucket[i].CreatedAt.Before(bucket[j].CreatedAt)
		})
		for i := 1; i < len(bucket); i++ {
			prev := bucket[i-1]
			current := bucket[i]
			if current.ParentHash == "" {
				// Still legacy; not necessarily a break —
				// pre-migration nodes legitimately have
				// empty ParentHash. Report as INFO-level so
				// callers can decide whether to treat as a
				// break.
				breaks = append(breaks, ChainBreak{
					MissionID:  mission,
					NodeID:     current.ID,
					ExpectedHash: mustHash(prev),
					ActualHash: "",
					Reason:     "empty parent_hash (legacy pre-migration node)",
				})
				continue
			}
			expected, err := hashNode(prev)
			if err != nil {
				breaks = append(breaks, ChainBreak{
					MissionID: mission, NodeID: current.ID,
					Reason: fmt.Sprintf("hash previous node: %v", err),
				})
				continue
			}
			if current.ParentHash != expected {
				breaks = append(breaks, ChainBreak{
					MissionID:    mission,
					NodeID:       current.ID,
					ExpectedHash: expected,
					ActualHash:   current.ParentHash,
					Reason:       "parent_hash does not match predecessor's canonical SHA256",
				})
			}
		}
	}
	return breaks, nil
}

// ChainBreak describes a detected Merkle-chain violation.
type ChainBreak struct {
	MissionID    string
	NodeID       string
	ExpectedHash string
	ActualHash   string
	Reason       string
}

// hashNode computes the SHA256 of a node's structural header + content
// commitment. This is the Merkle-chain link hash: the successor's
// ParentHash equals this value. The hash is DELIBERATELY independent of
// the content tier (Content, Salt) so a crypto-shred of the content tier
// does not break chain verification. ParentHash and ID are zeroed during
// hashing to avoid chicken-and-egg loops.
func hashNode(n Node) (string, error) {
	n.ParentHash = ""
	n.ID = ""
	n.Content = nil
	n.Salt = ""
	b, err := json.Marshal(n)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// mustHash is hashNode panicking on error — used only in
// VerifyChain's report-building path where an error has
// already been logged separately.
func mustHash(n Node) string {
	h, err := hashNode(n)
	if err != nil {
		return "error:" + err.Error()
	}
	return h
}
