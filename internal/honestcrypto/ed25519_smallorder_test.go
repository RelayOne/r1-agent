package honestcrypto

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"
)

// spkiPEMForRawEd25519 wraps a raw 32-byte Ed25519 public key value in an SPKI
// PEM block WITHOUT validating that it is a legitimate curve point — exactly
// how an attacker would present a chosen small-order key to the verifier.
// x509.MarshalPKIXPublicKey does not validate the point, so this faithfully
// simulates a hostile public key reaching VerifyBytes/VerifyRecordSignature.
func spkiPEMForRawEd25519(t *testing.T, raw []byte) string {
	t.Helper()
	if len(raw) != ed25519.PublicKeySize {
		t.Fatalf("raw key must be %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	der, err := x509.MarshalPKIXPublicKey(ed25519.PublicKey(raw))
	if err != nil {
		t.Fatalf("marshal SPKI: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// TestVerifyRejectsSmallOrderPublicKey is the mandatory negative conformance
// gate (PORTS.md low-order-key forgery, IACR 2020/1244): the standalone-receipt
// VERIFY path MUST reject a small-order (8-torsion) public key, matching
// ed25519-dalek's verify_strict. Go's crypto/ed25519.Verify does NOT reject
// these by default, so isSmallOrderPublicKey must.
func TestVerifyRejectsSmallOrderPublicKey(t *testing.T) {
	// The Ed25519 identity point: the neutral element, encoded as y=1, sign 0,
	// i.e. 32 bytes {0x01, 0x00, ..., 0x00}. It is the canonical small-order
	// point of order 1; [8]·identity == identity, so verify must reject it.
	identity := make([]byte, ed25519.PublicKeySize)
	identity[0] = 0x01

	if !isSmallOrderPublicKey(ed25519.PublicKey(identity)) {
		t.Fatal("identity point must be classified small-order")
	}

	identityPEM := spkiPEMForRawEd25519(t, identity)

	// Any signature string over this key must fail — never verify true.
	// Use a well-formed (canonical base64, correct length) signature so the
	// rejection is attributable to the small-order key gate, not to strict
	// base64 or length checks downstream.
	dummySig := base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	if VerifyBytes([]byte("hello"), dummySig, identityPEM) {
		t.Fatal("VerifyBytes MUST reject a small-order (identity) public key")
	}

	// The record-level verify path must reject it too.
	r, _ := MakeRecord(NewRecordInput{
		Subject: "s", Producer: "relaygate", Kind: "receipt.model_call",
		TS: "t", Body: map[string]any{"cost": 5}, PrevHash: nil,
	})
	recSig := base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	if VerifyRecordSignature(r, recSig, identityPEM) {
		t.Fatal("VerifyRecordSignature MUST reject a small-order (identity) public key")
	}
}

// TestVerifyRejectsOtherSmallOrderPoints covers the remaining canonical
// low-order encodings from the Ed25519 8-torsion subgroup (RFC 8032 test
// vectors / ed25519-dalek EIGHT_TORSION), so the gate is not a one-off
// identity special-case.
func TestVerifyRejectsOtherSmallOrderPoints(t *testing.T) {
	// Canonical encodings of small-order points (orders 1,2,4,8) on Ed25519.
	// Source: ed25519-dalek EIGHT_TORSION constants (hex, little-endian y||sign).
	smallOrderHex := []string{
		"0100000000000000000000000000000000000000000000000000000000000000", // order 1 (identity)
		"ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", // order 2 (-1)
		"0000000000000000000000000000000000000000000000000000000000000080", // order 4
		"0000000000000000000000000000000000000000000000000000000000000000", // order 4
		"c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a", // order 8
		"c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa", // order 8
		"26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05", // order 8
		"26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85", // order 8
	}
	for i, h := range smallOrderHex {
		raw := mustHex(t, h)
		if !isSmallOrderPublicKey(ed25519.PublicKey(raw)) {
			t.Fatalf("small-order point #%d (%s) must be classified small-order", i, h)
		}
		pem := spkiPEMForRawEd25519(t, raw)
		sig := base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
		if VerifyBytes([]byte("hello"), sig, pem) {
			t.Fatalf("VerifyBytes MUST reject small-order point #%d (%s)", i, h)
		}
	}
}

// TestVerifyAcceptsFullOrderKeyRejectsTamper is the paired positive control:
// a freshly generated full-order keypair's real signature MUST still verify
// true, and a tampered message MUST verify false — proving the small-order
// gate did not break legitimate verification.
func TestVerifyAcceptsFullOrderKeyRejectsTamper(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("the receipt body")
	sig, err := SignBytes(msg, kp.PrivateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	// Sanity: a real generated key is NOT small-order.
	pub, err := parsePublicKey(kp.PublicKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if isSmallOrderPublicKey(pub) {
		t.Fatal("a freshly generated keypair must be full-order")
	}
	if !VerifyBytes(msg, sig, kp.PublicKeyPEM) {
		t.Fatal("a real full-order signature MUST still verify true")
	}
	if VerifyBytes([]byte("tampered body"), sig, kp.PublicKeyPEM) {
		t.Fatal("a tampered message MUST verify false")
	}
}

func mustHex(t *testing.T, h string) []byte {
	t.Helper()
	if len(h) != 64 {
		t.Fatalf("hex must be 64 chars (32 bytes), got %d", len(h))
	}
	b := make([]byte, 32)
	for i := 0; i < 32; i++ {
		var hi, lo byte
		var err error
		if hi, err = hexNibble(h[2*i]); err != nil {
			t.Fatalf("bad hex at %d: %v", 2*i, err)
		}
		if lo, err = hexNibble(h[2*i+1]); err != nil {
			t.Fatalf("bad hex at %d: %v", 2*i+1, err)
		}
		b[i] = hi<<4 | lo
	}
	return b
}

func hexNibble(c byte) (byte, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, nil
	}
	return 0, errBadHex
}

var errBadHex = errBadHexErr("bad hex nibble")

type errBadHexErr string

func (e errBadHexErr) Error() string { return string(e) }
