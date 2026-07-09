// honest_anchor.go — truecom's canonical honest-crypto seam.
//
// The audit-anchoring path emits the canonical honest-crypto record shape
// (honest-record/v1) and GatewayAnchorer implements the canonical Anchorer
// interface (internal/honestcrypto) by committing record hashes under a local
// Merkle root (for standalone inclusion proofs) and submitting that root to the
// gateway for an external timestamp receipt. Register it with
// honestcrypto.SetAnchorer and every caller anchors through the gateway with
// zero call-site changes (see the swap-proof test).
package truecom

import (
	"context"
	"time"

	"github.com/RelayOne/r1/internal/honestcrypto"
)

// CanonicalAuditRecord expresses an audit-root submission as the canonical
// honest-crypto record so audit anchoring emits the same envelope every other
// product in the stack emits.
func CanonicalAuditRecord(subject string, root AuditRoot) (honestcrypto.Record, error) {
	body := map[string]any{
		"ledgerId": root.LedgerID,
		"rootHash": root.RootHash,
	}
	for k, v := range root.Meta {
		body["meta."+k] = v
	}
	ts := root.EmittedAt
	if ts.IsZero() {
		ts = time.Now()
	}
	return honestcrypto.MakeRecord(honestcrypto.NewRecordInput{
		Subject:  subject,
		Producer: "r1",
		Kind:     "audit.anchor",
		TS:       ts.UTC().Format(time.RFC3339Nano),
		Body:     body,
		PrevHash: nil,
	})
}

// BackendGatewayTimestamp is GatewayAnchorer's stable backend name.
const BackendGatewayTimestamp = "gateway-timestamp"

// GatewayAnchorer implements honestcrypto.Anchorer by committing record hashes
// under a local Merkle root (the source of standalone inclusion proofs) and
// submitting that root to a truecom Client's audit pipeline for an external
// timestamp receipt. It is a swap-in backend for the canonical seam.
type GatewayAnchorer struct {
	client  Client
	subject string
	// local builds the Merkle commitment + inclusion proofs; the gateway adds
	// the external timestamp on top of the same committed root.
	local honestcrypto.Anchorer
	now   func() time.Time
}

// NewGatewayAnchorer builds a GatewayAnchorer over the given client and subject.
// Pass nil for now to use the wall clock.
func NewGatewayAnchorer(client Client, subject string, now func() time.Time) *GatewayAnchorer {
	if now == nil {
		now = time.Now
	}
	return &GatewayAnchorer{
		client:  client,
		subject: subject,
		local:   honestcrypto.NewTrustedTimestampAnchorer(now),
		now:     now,
	}
}

// Backend returns the stable backend name.
func (g *GatewayAnchorer) Backend() string { return BackendGatewayTimestamp }

// Anchor commits recordHashes locally (Merkle root + inclusions) then submits the
// root to the gateway, stamping the gateway receipt into the proof.
func (g *GatewayAnchorer) Anchor(recordHashes []string) (honestcrypto.AnchorProof, error) {
	proof, err := g.local.Anchor(recordHashes)
	if err != nil {
		return honestcrypto.AnchorProof{}, err
	}
	anchor, err := g.client.AnchorAudit(context.Background(), AuditRoot{
		LedgerID:  g.subject,
		RootHash:  proof.Root,
		EmittedAt: g.now().UTC(),
	})
	if err != nil {
		return honestcrypto.AnchorProof{}, err
	}
	proof.Backend = g.Backend()
	proof.Proof["gatewayAnchorId"] = anchor.AnchorID
	proof.Proof["gatewayRef"] = anchor.TrustPlaneRef
	return proof, nil
}

// Verify checks a record hash's inclusion under the committed root (the gateway
// receipt is an additional timestamp, not required for inclusion).
func (g *GatewayAnchorer) Verify(recordHash string, proof honestcrypto.AnchorProof) bool {
	return g.local.Verify(recordHash, proof)
}

// Upgrade is a no-op for this backend.
func (g *GatewayAnchorer) Upgrade(proof honestcrypto.AnchorProof) (honestcrypto.AnchorProof, error) {
	return proof, nil
}
