// Package honestcrypto is the Go port of RelayOne's canonical @honest/crypto
// library. It mirrors the interfaces, the canonical record shape, and — most
// importantly — the byte-exact hashing rule defined in
// RelayOne/packages/honest-crypto/PORTS.md, so a record hashed here produces the
// identical hash the TypeScript canonicalizer produces. One anchored commitment
// then spans the whole stack and one verify works cross-language.
//
// record.go carries the canonical hash-chained audit record: one envelope shape
// emitted by every product's audit / receipt / provenance log, chained per
// subject via prevHash, with product-specific detail under an opaque body.
package honestcrypto

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
)

// Schema is the literal envelope version every canonical record carries.
const Schema = "honest-record/v1"

// Record is the canonical audit record. Every field is stable across the
// portfolio; hash links this record to the previous one via prevHash, forming an
// append-only hash chain per subject (org / tenant / agent). Product-specific
// detail lives under body (an opaque, canonicalized object) so the envelope stays
// uniform while payloads differ.
type Record struct {
	// RecordID is the content-addressed id; equals Hash once sealed.
	RecordID string `json:"recordId"`
	// Schema is the envelope version, literal "honest-record/v1".
	Schema string `json:"schema"`
	// Subject is the portfolio-uniform subject the chain belongs to
	// (RelayOne OIDC subject / org / tenant).
	Subject string `json:"subject"`
	// Producer is the emitting product surface, e.g. "relayone", "relaygate",
	// "r1", "truecom".
	Producer string `json:"producer"`
	// Kind is the record kind within the producer, e.g. "audit.event",
	// "receipt.model_call".
	Kind string `json:"kind"`
	// TS is an RFC-3339 UTC timestamp.
	TS string `json:"ts"`
	// Body is the canonicalized product-specific payload. It never affects the
	// envelope shape.
	Body map[string]any `json:"body"`
	// PrevHash is the previous record's Hash in this subject's chain; nil (JSON
	// null) at genesis.
	PrevHash *string `json:"prevHash"`
	// Hash is the sha256-hex of the canonical preimage (see PORTS.md section 1).
	Hash string `json:"hash"`
	// Anchor is set once anchored; the proof produced by the active Anchorer.
	Anchor *AnchorProof `json:"anchor,omitempty"`
}

// AnchorProof is the proof an Anchorer returns. Backend names which
// implementation produced it.
type AnchorProof struct {
	// Backend is the stable backend name stamped into every proof.
	Backend string `json:"backend"`
	// Root is the Merkle root (or equivalent commitment) this record was
	// anchored under.
	Root string `json:"root"`
	// Proof is backend-specific inclusion/timestamp material, opaque to callers.
	Proof map[string]any `json:"proof"`
	// AnchoredAt is the RFC-3339 time the anchor was produced.
	AnchoredAt string `json:"anchoredAt"`
}

// NewRecordInput carries the fields a caller supplies to build a sealed record.
type NewRecordInput struct {
	Subject  string
	Producer string
	Kind     string
	TS       string
	Body     map[string]any
	PrevHash *string
}

// Canonicalize returns the deterministic canonical JSON of value: object keys
// sorted recursively (lexicographic byte order — the envelope uses ASCII field
// names, so this reproduces the TS canonicalizer's UTF-16 code-unit sort),
// arrays in order, no insignificant whitespace, standard JSON string escaping.
// This is the byte-exact rule every port must reproduce.
func Canonicalize(value any) ([]byte, error) {
	var b bytes.Buffer
	if err := writeCanonical(&b, value); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func writeCanonical(b *bytes.Buffer, v any) error {
	switch val := v.(type) {
	case map[string]any:
		b.WriteByte('{')
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeScalar(b, k); err != nil {
				return err
			}
			b.WriteByte(':')
			if err := writeCanonical(b, val[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
		return nil
	case []any:
		b.WriteByte('[')
		for i, e := range val {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeCanonical(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
		return nil
	default:
		return writeScalar(b, v)
	}
}

// writeScalar emits a JSON scalar (string, number, bool, null) with HTML
// escaping disabled so the output matches JS JSON.stringify byte-for-byte for
// the ASCII envelope. json.Encoder appends a newline; it is trimmed.
func writeScalar(b *bytes.Buffer, v any) error {
	var tmp bytes.Buffer
	enc := json.NewEncoder(&tmp)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	b.Write(bytes.TrimRight(tmp.Bytes(), "\n"))
	return nil
}

// preimage builds the exact field set the record hash commits to, in the shape
// {schema, subject, producer, kind, ts, body, prevHash}. RecordID, Hash and
// Anchor are deliberately excluded.
func preimage(subject, producer, kind, ts string, body map[string]any, prevHash *string) map[string]any {
	var prev any
	if prevHash != nil {
		prev = *prevHash
	}
	return map[string]any{
		"schema":   Schema,
		"subject":  subject,
		"producer": producer,
		"kind":     kind,
		"ts":       ts,
		"body":     body,
		"prevHash": prev,
	}
}

// ComputeRecordHash returns the sha256-hex of the canonical preimage.
func ComputeRecordHash(subject, producer, kind, ts string, body map[string]any, prevHash *string) (string, error) {
	canon, err := Canonicalize(preimage(subject, producer, kind, ts, body, prevHash))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}

// MakeRecord builds a fully-formed, hash-sealed record (RecordID == Hash).
func MakeRecord(in NewRecordInput) (Record, error) {
	hash, err := ComputeRecordHash(in.Subject, in.Producer, in.Kind, in.TS, in.Body, in.PrevHash)
	if err != nil {
		return Record{}, err
	}
	return Record{
		RecordID: hash,
		Schema:   Schema,
		Subject:  in.Subject,
		Producer: in.Producer,
		Kind:     in.Kind,
		TS:       in.TS,
		Body:     in.Body,
		PrevHash: in.PrevHash,
		Hash:     hash,
	}, nil
}

// VerifyRecordHash recomputes and compares the hash of a record (envelope
// integrity), and checks RecordID == Hash.
func VerifyRecordHash(r Record) bool {
	got, err := ComputeRecordHash(r.Subject, r.Producer, r.Kind, r.TS, r.Body, r.PrevHash)
	if err != nil {
		return false
	}
	return got == r.Hash && r.RecordID == r.Hash
}

// VerifyChain verifies an ordered chain: each record's PrevHash must equal the
// prior record's Hash, and every record's own hash must recompute. Returns the
// index of the first broken link, or -1 if the whole chain is intact.
func VerifyChain(records []Record) int {
	var prev *string
	for i := range records {
		r := records[i]
		if !VerifyRecordHash(r) {
			return i
		}
		if !prevEqual(r.PrevHash, prev) {
			return i
		}
		h := r.Hash
		prev = &h
	}
	return -1
}

func prevEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// Ptr is a small helper for building a *string prevHash link.
func Ptr(s string) *string { return &s }

// ErrEmptyHash is returned by decoders that require a sealed record.
var ErrEmptyHash = errors.New("honestcrypto: record has empty hash")
