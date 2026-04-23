// Package ledger — verify.go
//
// Two-level Merkle commitment (chain tier + content tier), MVP slice.
// Per specs/work-stoke.md TASK 6, the full store split (nodes/ into
// chain/ + content/) is deferred — too invasive for a single commit.
// This file delivers the minimum viable slice of that work: a
// `(*Store).VerifyChain` method that walks the ledger's chain linkage
// using ONLY the structural header of each node, so verification is
// orthogonal to whether the content tier is present or redacted.
//
// Spec deviation (documented in the commit body):
//
//	The spec calls for node_id = SHA256(canonical(structural_header) ||
//	content_commitment). The current single-tier layout stores nodes
//	as one JSON file per node with content embedded. Until the
//	two-tier WriteNode lands, VerifyChain treats
//	canonical(node-minus-content) as a SURROGATE structural header —
//	equivalent for the redaction-resilience property we care about
//	(verify the chain without needing the content bytes).
//
// Design choices:
//
//  1. The verifier MUST NOT need the content tier. A node that has been
//     crypto-shredded via Store.Redact (content/{id}.json tombstoned)
//     verifies identically to a non-redacted node, because the chain
//     hash is computed from the structural header alone.
//
//  2. Chain linkage is already written into Node.ParentHash by
//     (*Ledger).AddNode and by the STOKE-002 migration. VerifyChain
//     walks that existing linkage — it does NOT require a schema bump
//     or a new on-disk field.
//
//  3. Ordering: nodes within a mission are walked in CreatedAt order
//     (matching migrate.go / the package-level VerifyChain). The first
//     node per mission has an empty ParentHash by contract; it is
//     accepted as the chain root and not checked against a predecessor.
//
//  4. Tamper detection: flipping any byte inside a node's structural
//     header changes its structural-header hash, which means the NEXT
//     node's ParentHash no longer matches — VerifyChain returns a
//     descriptive error identifying the mission and the sequence index
//     of the mismatch so operators can locate the break quickly.
package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// VerifyChain walks the ledger's chain tier (structural headers only)
// and returns nil when every node's ParentHash matches the SHA-256 of
// its predecessor's structural header. VerifyChain does NOT require
// content-tier blobs to be present — redacted nodes verify the same as
// non-redacted, because the structural header hash excludes Content.
//
// The ctx argument is honoured at each mission boundary so callers can
// abort verification of a large ledger early.
//
// Returned errors identify the mission and the per-mission sequence
// index of the break. Example:
//
//	ledger verify: chain break in mission "m-abc" at seq 7: parent_hash
//	    mismatch (expected "9f8e...", got "0000...")
//
// VerifyChain is intentionally narrower than the package-level
// migrate.go VerifyChain (which returns a slice of ChainBreak entries
// and treats legacy empty-parent_hash nodes as informational). This
// method returns the first hard violation because the spec's acceptance
// criteria are pass/fail: "VerifyChain returns nil" or "returns an
// error naming the sequence".
func (s *Store) VerifyChain(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("ledger verify: nil store")
	}
	nodes, err := s.ListNodes()
	if err != nil {
		return fmt.Errorf("ledger verify: list nodes: %w", err)
	}

	byMission := map[string][]Node{}
	for _, n := range nodes {
		byMission[n.MissionID] = append(byMission[n.MissionID], n)
	}

	// Walk missions in deterministic order so repeated failures report
	// the same mission first. Map iteration is randomised in Go, which
	// would otherwise produce non-reproducible error messages.
	missionIDs := make([]string, 0, len(byMission))
	for m := range byMission {
		missionIDs = append(missionIDs, m)
	}
	sort.Strings(missionIDs)

	for _, mission := range missionIDs {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("ledger verify: context cancelled: %w", err)
			}
		}
		bucket := byMission[mission]
		sort.SliceStable(bucket, func(i, j int) bool {
			return bucket[i].CreatedAt.Before(bucket[j].CreatedAt)
		})
		for i := 1; i < len(bucket); i++ {
			prev := bucket[i-1]
			current := bucket[i]
			if current.ParentHash == "" {
				// Empty ParentHash mid-chain is a structural break: a
				// node that landed after the first-in-mission MUST
				// link to its predecessor.
				return fmt.Errorf(
					"ledger verify: chain break in mission %q at seq %d (node %q): empty parent_hash mid-chain",
					mission, i, current.ID,
				)
			}
			expected, err := hashStructuralHeader(prev)
			if err != nil {
				return fmt.Errorf(
					"ledger verify: hash predecessor in mission %q at seq %d: %w",
					mission, i, err,
				)
			}
			if current.ParentHash != expected {
				return fmt.Errorf(
					"ledger verify: chain break in mission %q at seq %d (node %q): parent_hash mismatch (expected %q, got %q)",
					mission, i, current.ID, expected, current.ParentHash,
				)
			}
		}
	}
	return nil
}

// hashStructuralHeader is the chain-tier link hash used by
// Store.VerifyChain. With the two-tier layout in place, Content and Salt
// live in the erasable content tier — they MUST be excluded so a
// crypto-shred (delete content/<id>.json) does not invalidate any
// successor's ParentHash. ID and ParentHash are excluded to avoid the
// chicken-and-egg loop where a node's own ID would depend on itself.
//
// Kept in lockstep with migrate.go:hashNode — both functions compute the
// same bytes, so VerifyChain and the package-level chain verifier agree.
func hashStructuralHeader(n Node) (string, error) {
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
