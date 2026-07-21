// ed25519.go — real Ed25519 sign/verify over the canonical record + arbitrary
// bytes. The canonical sign path is real asymmetric Ed25519 so a receipt is
// verifiable standalone by anyone holding the public key — the property every
// product's "verify a receipt without trusting the operator" moment depends on.
package honestcrypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"

	"filippo.io/edwards25519"
)

// KeyPair holds a PEM-encoded Ed25519 keypair (PKCS8 private, SPKI public),
// matching the format the TS port emits so keys are interchangeable.
type KeyPair struct {
	// PrivateKeyPEM is the PKCS8 PEM private key.
	PrivateKeyPEM string
	// PublicKeyPEM is the SPKI PEM public key.
	PublicKeyPEM string
}

// GenerateKeyPair generates a fresh Ed25519 keypair (demo signing keys; prod
// uses KMS/HSM).
func GenerateKeyPair() (KeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, err
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return KeyPair{}, err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return KeyPair{}, err
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	return KeyPair{PrivateKeyPEM: string(privPEM), PublicKeyPEM: string(pubPEM)}, nil
}

// parsePrivateKey decodes a PKCS8 PEM Ed25519 private key.
func parsePrivateKey(pemStr string) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("honestcrypto: invalid private key PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("honestcrypto: private key is not Ed25519")
	}
	return priv, nil
}

// parsePublicKey decodes an SPKI PEM Ed25519 public key.
func parsePublicKey(pemStr string) (ed25519.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("honestcrypto: invalid public key PEM")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("honestcrypto: public key is not Ed25519")
	}
	return pub, nil
}

// SignBytes signs raw bytes with Ed25519 and returns a base64 signature.
func SignBytes(data []byte, privateKeyPEM string) (string, error) {
	priv, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, data)
	return base64.StdEncoding.EncodeToString(sig), nil
}

// DecodeBase64Strict decodes RFC 4648 standard base64 and rejects every
// non-canonical encoding (PORTS.md §7): trailing-bit variants (non-zero unused
// bits in the final significant char), missing/wrong/excess padding,
// whitespace, and any character outside the alphabet (including base64url).
// Go's StdEncoding is lenient about trailing padding bits, and even Strict()
// still ignores \r\n, so this implements the §7 fallback recipe: decode,
// re-encode with the canonical encoder, and require an exact string match —
// reject, never repair or normalize. Exported so every verification-path
// decode in the repo shares the single strict chokepoint.
func DecodeBase64Strict(b64 string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	if base64.StdEncoding.EncodeToString(raw) != b64 {
		return nil, errors.New("honestcrypto: non-canonical base64 encoding (PORTS.md §7)")
	}
	return raw, nil
}

// isSmallOrderPublicKey reports whether pub decodes to a small-order Ed25519
// point (order dividing the cofactor 8) or a non-canonical/off-curve encoding.
// Such public keys enable low-order-key signature forgery (IACR 2020/1244):
// ed25519-dalek's verify_strict rejects them, but Go's crypto/ed25519.Verify
// (like OpenSSL and node:crypto in the sibling ports' default libraries) does
// not. We gate it explicitly so the standalone-receipt verify path is equally
// strict across every honest-crypto port (PORTS.md). The check is
// [8]A == identity ⟺ A lies in the 8-torsion (small-order) subgroup.
func isSmallOrderPublicKey(pub ed25519.PublicKey) bool {
	a, err := new(edwards25519.Point).SetBytes(pub)
	if err != nil {
		return true // non-canonical or off-curve encoding — reject
	}
	cofactorA := new(edwards25519.Point).MultByCofactor(a)
	return cofactorA.Equal(edwards25519.NewIdentityPoint()) == 1
}

// VerifyBytes verifies an Ed25519 signature (canonical base64 only —
// PORTS.md §7) over raw bytes. It returns false (never an error) on any
// malformed or non-canonical input, matching the TS port's
// try/catch-to-false contract. A small-order (8-torsion) or
// non-canonical/off-curve public key is rejected before verification
// (verify_strict-equivalent, IACR 2020/1244).
func VerifyBytes(data []byte, signatureB64, publicKeyPEM string) bool {
	pub, err := parsePublicKey(publicKeyPEM)
	if err != nil {
		return false
	}
	if isSmallOrderPublicKey(pub) {
		return false
	}
	sig, err := DecodeBase64Strict(signatureB64)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, data, sig)
}

// SignRecord signs a canonical record's hash — the standalone-verifiable receipt
// signature.
func SignRecord(r Record, privateKeyPEM string) (string, error) {
	return SignBytes([]byte(r.Hash), privateKeyPEM)
}

// VerifyRecordSignature verifies a record signature AND that its envelope hash is
// internally consistent, so a tampered body fails even if the caller passes a
// matching (hash, signature) pair.
func VerifyRecordSignature(r Record, signatureB64, publicKeyPEM string) bool {
	if !VerifyRecordHash(r) {
		return false
	}
	return VerifyBytes([]byte(r.Hash), signatureB64, publicKeyPEM)
}
