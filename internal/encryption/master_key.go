package encryption

// Master-key derivation and load/generate glue for the
// encryption-at-rest pipeline described in
// `specs/encryption-at-rest.md` §5 (keyring) and §Key derivation.
//
// The at-rest pipeline consumes three distinct symmetric/asymmetric
// keys — the per-line JSONL stream cipher, the SQLCipher page cipher,
// and the Ed25519 signer for ledger redaction events — all derived
// deterministically from a single 32-byte master key held in the OS
// keyring (or the file-backed fallback on headless Linux/CI/Docker).
//
// Rather than ask each subsystem to reach into the keyring, we pin
// the master key to one slot and expose HKDF-SHA256 (`DeriveKey`) so
// downstream callers get purpose-specific keys that:
//
//   - are independent (compromising the JSONL stream key does not
//     weaken the SQLCipher key or the redaction signer), and
//   - rotate in lockstep with the master key (rekeying the master
//     rotates every derived key without bespoke plumbing).
//
// `LoadOrGenerateMaster` is the default entry point: callers get a
// 32-byte master key, generated on first use and persisted under
// `account=master-key` in the configured Keyring. For tests and
// alternative deployments, `LoadOrGenerateMasterFrom` lets the
// caller inject any Keyring implementation.

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/hkdf"
)

// MasterKeyAccount is the Keyring slot (the agentID, in the
// `Keyring` interface vocabulary) under which the master key is
// persisted. The spec pins this to `master-key` so SQLCipher's
// bootstrap logic and the redaction-signer derivation agree on the
// lookup key regardless of which backend the operator is on.
const MasterKeyAccount = "master-key"

// MasterKeySize is the fixed length of the master key in bytes (256
// bits). DeriveKey emits outputs of the same length so downstream
// ciphers (AES-256-GCM, XChaCha20-Poly1305, SQLCipher ChaCha20) each
// get full-strength key material regardless of purpose.
const MasterKeySize = 32

// Purpose labels for DeriveKey. Keep these in sync with
// specs/encryption-at-rest.md — the label is mixed into HKDF's
// `info` parameter so changing a string silently re-derives a
// different key and makes already-encrypted data unreadable.
const (
	PurposeJSONLStream        = "jsonl-stream"
	PurposeSQLiteCipher       = "sqlite-cipher"
	PurposeLedgerRedactionSig = "ledger-redaction-sign"
)

// defaultMasterKeyringMu guards the lazily-initialized default
// Keyring used by LoadOrGenerateMaster. Tests can swap it in via
// SetDefaultMasterKeyring.
var (
	defaultMasterKeyringMu sync.Mutex
	defaultMasterKeyring   Keyring
)

// SetDefaultMasterKeyring overrides the Keyring used by
// LoadOrGenerateMaster. Intended for tests and for production
// bootstrap code that wants to inject an HSM-backed implementation.
// Passing nil resets the override and reverts to the filesystem
// default (a FileKeyring under ~/.r1/keyring).
func SetDefaultMasterKeyring(kr Keyring) {
	defaultMasterKeyringMu.Lock()
	defer defaultMasterKeyringMu.Unlock()
	defaultMasterKeyring = kr
}

// defaultKeyringDir returns the filesystem path for the fallback
// FileKeyring: `<user-home>/.r1/keyring`. Kept as a function rather
// than a const so that tests swapping HOME see the change.
func defaultKeyringDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("encryption: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".r1", "keyring"), nil
}

// resolveDefaultMasterKeyring returns the injected default or, on
// first call without an override, constructs a FileKeyring at
// `~/.r1/keyring`. Callers hold defaultMasterKeyringMu.
func resolveDefaultMasterKeyring() (Keyring, error) {
	if defaultMasterKeyring != nil {
		return defaultMasterKeyring, nil
	}
	dir, err := defaultKeyringDir()
	if err != nil {
		return nil, err
	}
	fk, err := NewFileKeyring(dir)
	if err != nil {
		return nil, err
	}
	defaultMasterKeyring = fk
	return fk, nil
}

// LoadOrGenerateMaster fetches the 32-byte master key from the
// default keyring. If no key exists, it generates a fresh one via
// crypto/rand, stores it, and returns it. Subsequent calls read the
// stored key back. A non-nil error means neither load nor generate
// succeeded — callers treat that as fail-closed per the spec.
func LoadOrGenerateMaster() ([]byte, error) {
	defaultMasterKeyringMu.Lock()
	kr, err := resolveDefaultMasterKeyring()
	defaultMasterKeyringMu.Unlock()
	if err != nil {
		return nil, err
	}
	return LoadOrGenerateMasterFrom(kr)
}

// LoadOrGenerateMasterFrom is the injection-friendly form of
// LoadOrGenerateMaster. The caller supplies the Keyring (handy for
// tests, HSM integrations, or when the operator has already resolved
// the OS-specific backend). Returns the master key's 32 bytes.
func LoadOrGenerateMasterFrom(kr Keyring) ([]byte, error) {
	if kr == nil {
		return nil, errors.New("encryption: nil keyring")
	}
	k, err := kr.Get(MasterKeyAccount)
	if err == nil {
		b := k.Bytes()
		out := make([]byte, MasterKeySize)
		copy(out, b[:])
		return out, nil
	}
	if !errors.Is(err, ErrKeyNotFound) {
		return nil, fmt.Errorf("encryption: load master key: %w", err)
	}
	// Not present — generate and persist. We go through NewKey so
	// crypto/rand failures surface the same way as elsewhere, and we
	// use Put (not GetOrCreate) so a concurrent writer racing us is
	// handled deterministically: whichever Put lands last wins, and
	// the subsequent Get — which every caller after the first does —
	// returns that value. A single-process caller never races itself.
	fresh, err := NewKey()
	if err != nil {
		return nil, fmt.Errorf("encryption: generate master key: %w", err)
	}
	if err := kr.Put(MasterKeyAccount, fresh); err != nil {
		return nil, fmt.Errorf("encryption: store master key: %w", err)
	}
	b := fresh.Bytes()
	out := make([]byte, MasterKeySize)
	copy(out, b[:])
	return out, nil
}

// DeriveKey extracts a 32-byte purpose-specific key from `master`
// via HKDF-SHA256. The `purpose` label is mixed into HKDF's `info`
// field, giving each subsystem an independent key even though they
// all share a single root.
//
// Contract:
//
//   - Deterministic: same (master, purpose) → same output.
//   - Domain-separated: different purposes → cryptographically
//     independent outputs (HKDF's info guarantee).
//   - Fail-closed on degenerate inputs: rejects non-32-byte masters
//     and empty purpose strings rather than silently producing a
//     weak or collision-prone key.
//
// No salt is used — the master key is already uniform random from
// crypto/rand, which is exactly the scenario HKDF-Expand alone (or
// HKDF with a nil salt) is specified for (RFC 5869 §3.3).
func DeriveKey(master []byte, purpose string) ([]byte, error) {
	if len(master) != MasterKeySize {
		return nil, fmt.Errorf("encryption: master key must be %d bytes, got %d",
			MasterKeySize, len(master))
	}
	if purpose == "" {
		return nil, errors.New("encryption: derive key: empty purpose")
	}
	out := make([]byte, MasterKeySize)
	r := hkdf.New(sha256.New, master, nil, []byte(purpose))
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, fmt.Errorf("encryption: hkdf expand: %w", err)
	}
	return out, nil
}

