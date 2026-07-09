// Package ledger — anchor.go (canonical honest-crypto seam).
//
// R1's ledger emits the canonical honest-crypto record shape (honest-record/v1)
// and anchors record hashes through the canonical Anchorer seam
// (internal/honestcrypto, the Go port of RelayOne's @honest/crypto per PORTS.md).
// Because every product in the Honest Stack emits the same envelope and anchors
// through the same seam, one anchored commitment spans the stack and one verify
// works cross-language.
//
// The previous bespoke Merkle anchor-log (interval + empty-interval commitments)
// was archived to archive/ledger-anchor-pre-honestcrypto/ (package legacyanchor)
// — see that directory's NOTE.md. It is preserved, not deleted, because it is a
// distinct tamper-evidence feature; the canonical seam here is the go-forward
// anchoring path.
package ledger

import (
	"time"

	"github.com/RelayOne/r1/internal/honestcrypto"
)

// Producer is R1's stable producer id in the canonical honest-crypto record.
const Producer = "r1"

// CanonicalRecord converts a ledger Node into the canonical honest-crypto record
// (honest-record/v1). The body carries the node's tamper-evident commitment
// surface — its content commitment, not the raw content — so a crypto-shredded
// node (content tier erased) still produces a stable, verifiable record. The
// chain link (PrevHash) mirrors the node's ParentHash.
func CanonicalRecord(subject string, n Node) (honestcrypto.Record, error) {
	body := map[string]any{
		"nodeId":            string(n.ID),
		"nodeType":          n.Type,
		"createdBy":         n.CreatedBy,
		"contentCommitment": n.ContentCommitment,
	}
	if n.MissionID != "" {
		body["missionId"] = n.MissionID
	}
	var prev *string
	if n.ParentHash != "" {
		p := n.ParentHash
		prev = &p
	}
	return honestcrypto.MakeRecord(honestcrypto.NewRecordInput{
		Subject:  subject,
		Producer: Producer,
		Kind:     "ledger." + n.Type,
		TS:       n.CreatedAt.UTC().Format(time.RFC3339Nano),
		Body:     body,
		PrevHash: prev,
	})
}

// CanonicalRecords maps a slice of ledger nodes to canonical records under one
// subject, in order.
func CanonicalRecords(subject string, nodes []Node) ([]honestcrypto.Record, error) {
	out := make([]honestcrypto.Record, 0, len(nodes))
	for _, n := range nodes {
		r, err := CanonicalRecord(subject, n)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// AnchorRecords anchors a set of canonical record hashes through the active
// canonical Anchorer (honestcrypto.GetAnchorer()). Callers never name a concrete
// backend, so a stronger anchor backend swaps in by config with zero caller
// changes (see the swap-proof test). The default backend is trusted-timestamp.
func AnchorRecords(records []honestcrypto.Record) (honestcrypto.AnchorProof, error) {
	hashes := make([]string, len(records))
	for i, r := range records {
		hashes[i] = r.Hash
	}
	return honestcrypto.GetAnchorer().Anchor(hashes)
}

// AnchorNodes is the convenience path: build canonical records for the nodes and
// anchor their hashes, returning both the records and the anchor proof.
func AnchorNodes(subject string, nodes []Node) ([]honestcrypto.Record, honestcrypto.AnchorProof, error) {
	records, err := CanonicalRecords(subject, nodes)
	if err != nil {
		return nil, honestcrypto.AnchorProof{}, err
	}
	proof, err := AnchorRecords(records)
	if err != nil {
		return nil, honestcrypto.AnchorProof{}, err
	}
	return records, proof, nil
}

// VerifyAnchoredRecord verifies a record's inclusion under a proof through the
// active canonical Anchorer.
func VerifyAnchoredRecord(record honestcrypto.Record, proof honestcrypto.AnchorProof) bool {
	return honestcrypto.GetAnchorer().Verify(record.Hash, proof)
}
