// Package ledger — pack_adopted_event.go
//
// C7 cross-product-skill-exchange T10: typed pack.adopted event +
// canonical-form ed25519 signature + persistence helper.
//
// Why ledger-side, not just bus-side: every adoption is an audit
// fact that must survive a process restart. We persist a Node of
// type "pack.adopted" so downstream tooling can replay adoptions
// without consulting the bus WAL. The canonical form + signature
// reuse the pattern in redact_sign.go.
package ledger

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// PackAdoptedPayload is the event payload emitted on the bus and
// persisted as ledger.Node.Content for every successful
// `r1 skills pack adopt` invocation.
type PackAdoptedPayload struct {
	PackID         string `json:"pack_id"`
	PackVersion    string `json:"pack_version"`
	TargetProduct  string `json:"target_product"`
	TenantID       string `json:"tenant_id,omitempty"`
	SignerKeyID    string `json:"signer_key_id,omitempty"`
	AdoptedAt      string `json:"adopted_at"`
	Signer         string `json:"signer,omitempty"`
	SignatureHex   string `json:"signature_hex,omitempty"`
}

// ErrPackAdoptedSignatureInvalid is returned by
// VerifyPackAdoptedSignature on mismatch.
var ErrPackAdoptedSignatureInvalid = errors.New("pack_adopted: signature invalid")

// SignPackAdopted stamps Signer + SignatureHex onto a PackAdoptedPayload
// using priv. The canonical form covers every field EXCEPT the
// signature fields themselves so the signature cannot self-reference.
//
// pack_id, target_product, and adopted_at are required for the
// canonical form; empty values return an error before signing.
func SignPackAdopted(payload PackAdoptedPayload, priv ed25519.PrivateKey) (PackAdoptedPayload, error) {
	if payload.PackID == "" || payload.TargetProduct == "" || payload.AdoptedAt == "" {
		return payload, errors.New("pack_adopted: pack_id + target_product + adopted_at required")
	}
	if len(priv) != ed25519.PrivateKeySize {
		return payload, errors.New("pack_adopted: invalid private key")
	}
	pub := priv.Public().(ed25519.PublicKey)
	payload.Signer = packAdoptedSignerID(pub)
	payload.SignatureHex = ""
	body, err := canonicalizePackAdopted(payload)
	if err != nil {
		return payload, fmt.Errorf("pack_adopted: canonicalize: %w", err)
	}
	sig := ed25519.Sign(priv, body)
	payload.SignatureHex = hex.EncodeToString(sig)
	return payload, nil
}

// VerifyPackAdoptedSignature checks payload's signature against pub.
// Returns nil on match.
func VerifyPackAdoptedSignature(payload PackAdoptedPayload, pub ed25519.PublicKey) error {
	if payload.SignatureHex == "" {
		return fmt.Errorf("%w: missing signature", ErrPackAdoptedSignatureInvalid)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: invalid public key", ErrPackAdoptedSignatureInvalid)
	}
	body, err := canonicalizePackAdopted(payload)
	if err != nil {
		return fmt.Errorf("pack_adopted: canonicalize: %w", err)
	}
	sig, derr := hex.DecodeString(payload.SignatureHex)
	if derr != nil {
		return fmt.Errorf("%w: hex decode: %v", ErrPackAdoptedSignatureInvalid, derr)
	}
	if !ed25519.Verify(pub, body, sig) {
		return ErrPackAdoptedSignatureInvalid
	}
	return nil
}

// PersistPackAdopted writes a ledger Node of type "pack.adopted" for
// the supplied payload. nodeID is the caller-supplied stable
// identifier, typically content-addressed by the caller via an inline
// sha256 (e.g. cmd/r1's deriveAdoptNodeID with its "pack-adopted-"
// prefix).
//
// Returns the Node written so callers can wire it into edges or
// re-emit it on the bus.
func PersistPackAdopted(s *Store, nodeID NodeID, payload PackAdoptedPayload, createdBy string) (Node, error) {
	if s == nil {
		return Node{}, errors.New("pack_adopted: nil store")
	}
	if nodeID == "" {
		return Node{}, errors.New("pack_adopted: node id required")
	}
	content, err := json.Marshal(payload)
	if err != nil {
		return Node{}, fmt.Errorf("pack_adopted: marshal: %w", err)
	}
	n := Node{
		ID:            nodeID,
		Type:          "pack.adopted",
		SchemaVersion: 1,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     createdBy,
		Content:       content,
	}
	if err := s.WriteNode(n); err != nil {
		return Node{}, fmt.Errorf("pack_adopted: write: %w", err)
	}
	return n, nil
}

// packAdoptedCanonicalForm is the JSON shape SignPackAdopted /
// VerifyPackAdoptedSignature both canonicalize over. Excludes the
// signature fields by construction.
type packAdoptedCanonicalForm struct {
	PackID        string `json:"pack_id"`
	PackVersion   string `json:"pack_version"`
	TargetProduct string `json:"target_product"`
	TenantID      string `json:"tenant_id"`
	SignerKeyID   string `json:"signer_key_id"`
	AdoptedAt     string `json:"adopted_at"`
	Signer        string `json:"signer"`
}

func canonicalizePackAdopted(p PackAdoptedPayload) ([]byte, error) {
	c := packAdoptedCanonicalForm{
		PackID:        p.PackID,
		PackVersion:   p.PackVersion,
		TargetProduct: p.TargetProduct,
		TenantID:      p.TenantID,
		SignerKeyID:   p.SignerKeyID,
		AdoptedAt:     p.AdoptedAt,
		Signer:        p.Signer,
	}
	return json.Marshal(c)
}

func packAdoptedSignerID(pub ed25519.PublicKey) string {
	// Reuse the same fingerprint shape as the redaction signer so
	// dashboards can compare signers across signatures.
	return keyFingerHex(pub)
}
