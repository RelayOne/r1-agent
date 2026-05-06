package ledger

// redact_sign.go — ed25519 signatures over redaction-event log
// entries. Spec: signed-redaction.md.
//
// Closes the "signed" half of ledger-redaction.md §6 that was
// originally deferred to "the encryption-at-rest spec". This file
// owns:
//
//   * LoadOrGenerateSigningKey — file-backed ed25519 keypair
//   * SignRecord / VerifyRecord — canonical-form signature + verify
//   * Store.RedactAndLog signing path — automatic when a key exists
//   * Store.RedactionsForVerified — read path that flags tampered
//     entries via a Verified bool

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ErrSignatureMismatch is returned by VerifyRecord when the
// signature exists but doesn't validate against the record body or
// the configured public key.
var ErrSignatureMismatch = errors.New("ledger redaction sign: signature mismatch")

// ErrUnsigned is returned by VerifyRecord when the record carries
// no Signer / SignatureHex (legacy unsigned entry from before this
// spec landed). Distinct from ErrSignatureMismatch so the side
// panel can render a "legacy unsigned" tooltip rather than a
// "tampered!" warning.
var ErrUnsigned = errors.New("ledger redaction sign: record is unsigned")

// signingKeyFiles names the on-disk key paths under <root>/redactions/.
const (
	signingPrivFile = "sign-priv.pem"
	signingPubFile  = "sign-pub.pem"
)

// LoadOrGenerateSigningKey returns the signing keypair for the
// supplied store root. On first call it generates a fresh ed25519
// keypair, persists both halves under <root>/redactions/, and
// returns. Subsequent calls re-read the persisted private key
// (the public key is reconstructed from the private if the pub
// file is missing — useful when an operator hand-distributes the
// pub for verification but loses the persisted copy).
//
// Returns the priv key + a 12-char hex fingerprint of the pub
// (first 6 bytes of sha256). The fingerprint is what gets stamped
// into SignedRedactionEvent.Signer so multiple keys can co-exist
// in the same audit trail across rotations.
//
// File modes: priv 0600, pub 0644. Directory created at 0755 if
// it doesn't yet exist.
func LoadOrGenerateSigningKey(rootDir string) (ed25519.PrivateKey, string, error) {
	dir := filepath.Join(rootDir, "redactions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", fmt.Errorf("redaction sign: mkdir: %w", err)
	}
	privPath := filepath.Join(dir, signingPrivFile)
	pubPath := filepath.Join(dir, signingPubFile)

	priv, err := readPrivPEM(privPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	if priv == nil {
		_, generatedPriv, gerr := ed25519.GenerateKey(rand.Reader)
		if gerr != nil {
			return nil, "", fmt.Errorf("redaction sign: generate: %w", gerr)
		}
		priv = generatedPriv
		if werr := writePrivPEM(privPath, priv); werr != nil {
			return nil, "", werr
		}
	}
	pub := priv.Public().(ed25519.PublicKey)
	if _, statErr := os.Stat(pubPath); errors.Is(statErr, os.ErrNotExist) {
		if werr := writePubPEM(pubPath, pub); werr != nil {
			return nil, "", werr
		}
	}
	return priv, keyFingerHex(pub), nil
}

// fingerprint returns a stable 12-char hex prefix of sha256(pub).
// Stamped into SignedRedactionEvent.Signer.
func keyFingerHex(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:6])
}

func readPrivPEM(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("redaction sign: %s not PEM", path)
	}
	if len(block.Bytes) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("redaction sign: %s wrong size %d (want %d)", path, len(block.Bytes), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(block.Bytes), nil
}

func writePrivPEM(path string, priv ed25519.PrivateKey) error {
	body := pem.EncodeToMemory(&pem.Block{Type: "ED25519 PRIVATE KEY", Bytes: priv})
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("redaction sign: write priv: %w", err)
	}
	return nil
}

func writePubPEM(path string, pub ed25519.PublicKey) error {
	body := pem.EncodeToMemory(&pem.Block{Type: "ED25519 PUBLIC KEY", Bytes: pub})
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("redaction sign: write pub: %w", err)
	}
	return nil
}

// canonicalForm is the JSON shape SignRecord/VerifyRecord both
// canonicalize over. Excludes SignatureHex by construction (so the
// signature doesn't sign itself) but INCLUDES Signer (so swapping
// the signer can't reattribute a record).
type canonicalForm struct {
	NodeID     string `json:"node_id"`
	RedactedAt string `json:"redacted_at"`
	Reason     string `json:"reason"`
	Signer     string `json:"signer"`
}

func canonicalize(rec SignedRedactionEvent) ([]byte, error) {
	c := canonicalForm{
		NodeID:     rec.NodeID,
		RedactedAt: rec.RedactedAt,
		Reason:     rec.Reason,
		Signer:     rec.Signer,
	}
	return json.Marshal(c)
}

// SignRecord stamps Signer (fingerprint of priv's pub) +
// SignatureHex onto rec and returns the result. Original rec is
// unchanged. Empty NodeID/RedactedAt/Reason returns an error
// before signing — those fields are required and must be present
// in the canonical form.
func SignRecord(rec SignedRedactionEvent, priv ed25519.PrivateKey) (SignedRedactionEvent, error) {
	if rec.NodeID == "" || rec.RedactedAt == "" || rec.Reason == "" {
		return rec, errors.New("redaction sign: node_id + redacted_at + reason required")
	}
	if len(priv) != ed25519.PrivateKeySize {
		return rec, errors.New("redaction sign: invalid private key")
	}
	rec.Signer = keyFingerHex(priv.Public().(ed25519.PublicKey))
	rec.SignatureHex = ""
	body, err := canonicalize(rec)
	if err != nil {
		return rec, fmt.Errorf("redaction sign: canonicalize: %w", err)
	}
	sig := ed25519.Sign(priv, body)
	rec.SignatureHex = hex.EncodeToString(sig)
	return rec, nil
}

// VerifyRecord checks rec's signature against pub. Returns nil on
// match, ErrUnsigned when SignatureHex is empty, ErrSignatureMismatch
// otherwise.
func VerifyRecord(rec SignedRedactionEvent, pub ed25519.PublicKey) error {
	if rec.SignatureHex == "" {
		return ErrUnsigned
	}
	if len(pub) != ed25519.PublicKeySize {
		return errors.New("redaction sign: invalid public key")
	}
	body, err := canonicalize(rec)
	if err != nil {
		return fmt.Errorf("redaction sign: canonicalize: %w", err)
	}
	sig, err := hex.DecodeString(rec.SignatureHex)
	if err != nil {
		return fmt.Errorf("%w: hex decode: %v", ErrSignatureMismatch, err)
	}
	if !ed25519.Verify(pub, body, sig) {
		return ErrSignatureMismatch
	}
	return nil
}

// VerifiedRedactionEvent extends SignedRedactionEvent with a
// runtime Verified flag. Returned by RedactionsForVerified so the
// dashboard side panel can render a "signature ⚠ unverified"
// warning next to tampered or unsigned entries.
type VerifiedRedactionEvent struct {
	SignedRedactionEvent
	Verified bool   `json:"verified"`
	VerifyErr string `json:"verify_err,omitempty"`
}

// signingState caches the loaded keypair on the Store so the disk
// reads happen once per process. sync.Once-protected.
type signingState struct {
	once sync.Once
	priv ed25519.PrivateKey
	fing string
	err  error
}

var redactionSigningState sync.Map // store-root → *signingState

func (s *Store) signingKey() (ed25519.PrivateKey, string, error) {
	root := filepath.Dir(s.nodesDir)
	val, _ := redactionSigningState.LoadOrStore(root, &signingState{})
	st := val.(*signingState)
	st.once.Do(func() {
		st.priv, st.fing, st.err = LoadOrGenerateSigningKey(root)
	})
	return st.priv, st.fing, st.err
}

// RedactionsForVerified is the verification-aware sibling of
// RedactionsFor. Each returned event carries Verified=true if the
// signature checks out, false otherwise (legacy unsigned entries
// also flag false with VerifyErr indicating the cause).
func (s *Store) RedactionsForVerified(nodeID string) ([]VerifiedRedactionEvent, error) {
	raw, err := s.RedactionsFor(nodeID)
	if err != nil {
		return nil, err
	}
	priv, _, kerr := s.signingKey()
	if kerr != nil {
		// No key available — every entry's Verified stays false.
		out := make([]VerifiedRedactionEvent, 0, len(raw))
		for _, ev := range raw {
			out = append(out, VerifiedRedactionEvent{SignedRedactionEvent: ev, Verified: false, VerifyErr: "no signing key configured"})
		}
		return out, nil
	}
	pub := priv.Public().(ed25519.PublicKey)
	out := make([]VerifiedRedactionEvent, 0, len(raw))
	for _, ev := range raw {
		v := VerifiedRedactionEvent{SignedRedactionEvent: ev}
		if verr := VerifyRecord(ev, pub); verr != nil {
			v.Verified = false
			v.VerifyErr = verr.Error()
		} else {
			v.Verified = true
		}
		out = append(out, v)
	}
	return out, nil
}
