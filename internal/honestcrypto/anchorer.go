// anchorer.go — the flagship swap-seam.
//
// Anchor(hashes) -> Proof, Verify(hash, proof) -> bool, Upgrade(proof) -> Proof.
// The default backend commits the record hashes under a Merkle root and stamps a
// trusted timestamp (a transparency-log shape). An alternative backend may be
// registered later that produces a stronger, operator-independent proof — it
// drops in by config with zero caller changes. The interface is backend-agnostic
// by design; callers only ever use GetAnchorer().
package honestcrypto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// Anchorer commits record hashes and later verifies inclusion under a proof.
type Anchorer interface {
	// Anchor commits a set of record hashes; returns the commitment + inclusion
	// material.
	Anchor(recordHashes []string) (AnchorProof, error)
	// Verify checks that a record hash is included under an anchor proof.
	Verify(recordHash string, proof AnchorProof) bool
	// Upgrade upgrades an existing proof to whatever this backend can now
	// provide (idempotent for equal backends).
	Upgrade(proof AnchorProof) (AnchorProof, error)
	// Backend is the stable backend name stamped into every proof.
	Backend() string
}

// BackendTrustedTimestamp is the default Anchorer backend name.
const BackendTrustedTimestamp = "trusted-timestamp"

// TrustedTimestampAnchorer is the unencumbered default. It commits the record
// hashes under a Merkle root and stamps a trusted timestamp; Verify checks a
// record's inclusion under the committed root. It is honest about what it is:
// proof that the operator committed to these records at a time. A later backend
// can make the same commitment provable without trusting the operator, and every
// product inherits that the moment it is registered — no caller changes.
type TrustedTimestampAnchorer struct {
	// now is injected so tests are deterministic; a real deployment uses an
	// RFC-3161 timestamp authority or a transparency-log witness.
	now func() time.Time
}

// NewTrustedTimestampAnchorer builds the default anchorer. Pass nil for now to
// use the wall clock.
func NewTrustedTimestampAnchorer(now func() time.Time) *TrustedTimestampAnchorer {
	if now == nil {
		now = time.Now
	}
	return &TrustedTimestampAnchorer{now: now}
}

// Backend returns the stable backend name.
func (a *TrustedTimestampAnchorer) Backend() string { return BackendTrustedTimestamp }

// Anchor commits recordHashes under a Merkle root plus a trusted timestamp.
func (a *TrustedTimestampAnchorer) Anchor(recordHashes []string) (AnchorProof, error) {
	leaves := make([]string, len(recordHashes))
	for i, rh := range recordHashes {
		leaves[i] = LeafHash(rh)
	}
	root := MerkleRoot(leaves)
	inclusions := make(map[string]any, len(recordHashes))
	for i, rh := range recordHashes {
		p, ok := InclusionProofFor(leaves, i)
		if !ok {
			continue
		}
		inclusions[rh] = p
	}
	anchoredAt := a.now().UTC().Format(time.RFC3339Nano)
	witnessSum := sha256.Sum256([]byte(BackendTrustedTimestamp + ":" + root + ":" + anchoredAt))
	return AnchorProof{
		Backend: BackendTrustedTimestamp,
		Root:    root,
		Proof: map[string]any{
			"inclusions": inclusions,
			"timestamp":  anchoredAt,
			"witness":    hex.EncodeToString(witnessSum[:]),
		},
		AnchoredAt: anchoredAt,
	}, nil
}

// Verify checks that recordHash is included under proof's committed root.
func (a *TrustedTimestampAnchorer) Verify(recordHash string, proof AnchorProof) bool {
	raw, ok := proof.Proof["inclusions"]
	if !ok {
		return false
	}
	inclusions, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	entry, ok := inclusions[recordHash]
	if !ok {
		return false
	}
	inc, ok := decodeInclusion(entry)
	if !ok {
		return false
	}
	return VerifyInclusion(LeafHash(recordHash), inc, proof.Root)
}

// Upgrade for the default backend is a no-op (nothing stronger to offer). A
// different backend's Upgrade would re-anchor the same root under its stronger
// commitment.
func (a *TrustedTimestampAnchorer) Upgrade(proof AnchorProof) (AnchorProof, error) {
	return proof, nil
}

// decodeInclusion accepts either a typed InclusionProof (freshly produced
// in-process) or a map[string]any (round-tripped through JSON persistence) and
// normalizes it to an InclusionProof.
func decodeInclusion(v any) (InclusionProof, bool) {
	if p, ok := v.(InclusionProof); ok {
		return p, true
	}
	blob, err := json.Marshal(v)
	if err != nil {
		return InclusionProof{}, false
	}
	var p InclusionProof
	if err := json.Unmarshal(blob, &p); err != nil {
		return InclusionProof{}, false
	}
	return p, true
}

// ---- Registry: how a second backend swaps in by config, zero caller changes ----

var (
	anchorerMu sync.RWMutex
	anchorer   Anchorer = NewTrustedTimestampAnchorer(nil)
)

// GetAnchorer is the canonical accessor every caller uses. Callers never name a
// concrete backend.
func GetAnchorer() Anchorer {
	anchorerMu.RLock()
	defer anchorerMu.RUnlock()
	return anchorer
}

// SetAnchorer swaps the backend by config — the one registration point for an
// alternative backend.
func SetAnchorer(next Anchorer) {
	anchorerMu.Lock()
	defer anchorerMu.Unlock()
	anchorer = next
}

// ResetAnchorer restores the unencumbered default (test hygiene).
func ResetAnchorer() {
	anchorerMu.Lock()
	defer anchorerMu.Unlock()
	anchorer = NewTrustedTimestampAnchorer(nil)
}
