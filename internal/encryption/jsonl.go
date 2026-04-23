package encryption

// Per-line XChaCha20-Poly1305 encryption for JSONL streams.
//
// Implements §4 of specs/encryption-at-rest.md: each plaintext JSON
// object is sealed independently so that
//
//   - appending a new line never touches prior lines (preserves
//     append-only semantics of `<repo>/.stoke/stream.jsonl[.enc]`),
//   - a corrupted or truncated line does not poison later lines, and
//   - per-line Poly1305 tags detect tampering on a single record.
//
// Wire format for one line (before base64):
//
//   nonce(24) || ciphertext || auth_tag(16)
//
// The 24-byte random nonce makes XChaCha20-Poly1305 safe for the
// long lifetimes (~10^9 events) a Stoke install may emit; reusing
// stdlib AES-GCM here would force us to manage a counter to avoid
// nonce collisions.
//
// The base64 encoding (standard alphabet, padded) keeps each line
// ASCII-safe so operators can `tail -f` an encrypted stream and pipe
// it through a decrypter without binary framing.

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
)

// EncryptLine encrypts a single JSONL line with the provided 32-byte
// key. Returns: base64(nonce(24) || ciphertext || auth_tag(16)).
//
// The returned string never contains a newline; callers append '\n'
// when writing to the stream file. An empty plaintext is permitted
// and round-trips correctly (the AEAD still produces a 24+16 = 40
// byte envelope encoded as 56 base64 chars).
func EncryptLine(key []byte, plaintext []byte) (string, error) {
	if len(key) != chacha20poly1305.KeySize {
		return "", fmt.Errorf("encryption: jsonl key must be %d bytes, got %d",
			chacha20poly1305.KeySize, len(key))
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", fmt.Errorf("encryption: jsonl aead init: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("encryption: jsonl nonce: %w", err)
	}
	// Seal returns nonce || ciphertext || tag because we pass `nonce`
	// as the dst prefix.
	sealed := aead.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// DecryptLine reverses EncryptLine. Returns the plaintext bytes.
// Returns an error if the auth tag doesn't verify (wrong key,
// tampered ciphertext, truncated input, or invalid base64).
//
// A trailing '\n' on encoded is tolerated so callers can hand the
// raw line read from the stream file straight in.
func DecryptLine(key []byte, encoded string) ([]byte, error) {
	if len(key) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("encryption: jsonl key must be %d bytes, got %d",
			chacha20poly1305.KeySize, len(key))
	}
	// Trim a single trailing newline (and an optional preceding \r) so
	// callers can pass raw line reads. Don't strip arbitrary
	// whitespace — that would mask real corruption.
	if n := len(encoded); n > 0 && encoded[n-1] == '\n' {
		encoded = encoded[:n-1]
		if n := len(encoded); n > 0 && encoded[n-1] == '\r' {
			encoded = encoded[:n-1]
		}
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("encryption: jsonl base64 decode: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("encryption: jsonl aead init: %w", err)
	}
	if len(raw) < aead.NonceSize()+aead.Overhead() {
		return nil, fmt.Errorf("encryption: jsonl line truncated (%d bytes, need >= %d)",
			len(raw), aead.NonceSize()+aead.Overhead())
	}
	nonce := raw[:aead.NonceSize()]
	ct := raw[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("encryption: jsonl auth tag mismatch: %w", err)
	}
	return plain, nil
}
