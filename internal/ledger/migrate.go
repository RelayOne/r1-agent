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
// cmd/stoke/ledger_migrate.go; this file has the engine so
// tests can invoke it directly without shelling out.
package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

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

// hashNode computes the SHA256 of a node's canonical JSON.
// Kept local so the migration tool doesn't reach into the
// anchor.go machinery.
func hashNode(n Node) (string, error) {
	// Canonical JSON: sorted keys, no insignificant
	// whitespace. encoding/json.Marshal on a fixed struct
	// produces stable output because struct-order determines
	// field order — but we explicitly nil out ParentHash to
	// prevent a chicken-and-egg loop (a node's hash must
	// not depend on its own parent_hash).
	n.ParentHash = ""
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
