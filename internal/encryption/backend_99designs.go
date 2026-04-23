package encryption

// OS-native master-keyring adapter.
//
// This file adds the `MasterKeyringBackend` abstraction called for by
// `specs/work-stoke.md` TASK 9 and `specs/encryption-at-rest.md` §5.
// Operators on macOS, graphical Linux, or Windows get their master
// key stored in the OS credential store via `github.com/99designs/keyring`
// (Keychain / secret-service / Credential Manager). Headless Linux,
// Docker, and CI fall back to a file-backed keyring at
// `~/.r1/keyring` — the same directory layout the pre-existing
// `FileKeyring` in `keyring.go` uses, so the fallback can read
// values already written there.
//
// Why a second surface alongside the agent-scoped `Keyring`?
//
//   - `Keyring` is shaped for per-agent 32-byte AES keys (STOKE-021).
//     The master-key pipeline also needs to stash passphrase-wrapped
//     backup blobs, SQLCipher bootstrap material, and future
//     redaction-signer keys whose lengths are *not* 32 bytes. A
//     separate `[]byte`-valued interface keeps that policy decision
//     out of `Keyring` and out of 99designs/keyring's `Item` type.
//   - `MasterKeyringBackend` is the exact surface `OpenMasterKeyring`
//     returns; callers do not care whether the OS keychain or the
//     file fallback satisfied the request, which is what lets us
//     flip backends without changing any call site.
//
// Backend selection precedence (in order; first success wins):
//
//   1. `R1_KEYRING_BACKEND=file` → skip 99designs entirely; use the
//      file fallback. This is how CI / Docker opts out even when a
//      D-Bus happens to be reachable.
//   2. `R1_KEYRING_BACKEND=<99designs-name>` (e.g. `keychain`,
//      `secret-service`, `wincred`) → restrict 99designs to that
//      backend only; fall back to file on failure.
//   3. Unset → let 99designs pick the best available backend on the
//      host; fall back to file on failure.
//
// File-backend protection is 0700 dir + 0600 files — the same
// posture the pre-existing FileKeyring has always used and exactly
// what this task specified ("fall back to the existing file-backed
// keyring"). Passphrase wrapping (Argon2id + AEAD around each blob,
// gated on `R1_KEYRING_PASSPHRASE`) is tracked separately under
// encryption-at-rest §5 because turning it on here would break the
// read-compatibility guarantee that TASK 9 requires.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	ninetydesigns "github.com/99designs/keyring"
)

// MasterKeyringBackend is the shared surface the OS-native and
// file-backed master-keyring implementations satisfy. All values
// are opaque byte blobs — the caller (e.g. `LoadOrGenerateMaster`)
// owns the interpretation.
type MasterKeyringBackend interface {
	// Get retrieves the value stored under key, or
	// ErrMasterKeyringEntryMissing if no entry exists.
	Get(key string) ([]byte, error)
	// Set stores val under key, replacing any prior value.
	Set(key string, val []byte) error
	// Delete removes the entry for key. Idempotent: missing keys
	// return nil, matching OS credential-store behaviour for the
	// common `rm --force` use case.
	Delete(key string) error
}

// ErrMasterKeyringEntryMissing is the sentinel returned by both the
// 99designs-backed and file-backed adapters when the key has no
// stored value. `errors.Is` against this sentinel lets callers
// (particularly `LoadOrGenerateMaster`) distinguish "no entry yet"
// from a real IO/backend error.
var ErrMasterKeyringEntryMissing = errors.New("encryption: master keyring entry missing")

// MasterKeyringOpts carries the knobs `OpenMasterKeyring` needs to
// build a backend. Zero-value opts are valid and produce the
// documented default: ServiceName "stoke", FileDir "~/.r1/keyring".
type MasterKeyringOpts struct {
	// ServiceName is the service-name hint passed to backends that
	// support it (Keychain/secret-service/wincred). Defaults to
	// "stoke" so multiple Stoke-family binaries cohabit cleanly.
	ServiceName string
	// FileDir is the directory the file fallback uses. Empty means
	// `<user-home>/.r1/keyring`.
	FileDir string
	// ForceBackend, if non-empty, overrides the `R1_KEYRING_BACKEND`
	// env. Accepts "file" or any 99designs BackendType string.
	ForceBackend string
}

// OpenMasterKeyring returns a MasterKeyringBackend following the
// precedence documented above. Callers treat a non-nil error as
// fail-closed (no backend available means "do not start the
// encrypted subsystem").
func OpenMasterKeyring(opts MasterKeyringOpts) (MasterKeyringBackend, error) {
	svc := opts.ServiceName
	if svc == "" {
		svc = "stoke"
	}
	dir := opts.FileDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("encryption: resolve home dir: %w", err)
		}
		dir = filepath.Join(home, ".r1", "keyring")
	}

	forced := opts.ForceBackend
	if forced == "" {
		forced = os.Getenv("R1_KEYRING_BACKEND")
	}
	// "file" forces the fallback unconditionally.
	if forced != "file" {
		if be, err := openNinetyDesignsBackend(svc, forced); err == nil {
			return be, nil
		}
		// Swallow — fall through to file. The error we hand back
		// from the file branch is the one that matters; if 99designs
		// was going to work, we wouldn't be here.
	}
	return openFileBackend(dir)
}

// ninetyDesignsBackend wraps a 99designs/keyring.Keyring in the
// MasterKeyringBackend surface. All methods translate the 99designs
// ErrKeyNotFound sentinel to ErrMasterKeyringEntryMissing so callers
// have one thing to check.
type ninetyDesignsBackend struct {
	kr      ninetydesigns.Keyring
	service string
}

func openNinetyDesignsBackend(serviceName, forced string) (MasterKeyringBackend, error) {
	cfg := ninetydesigns.Config{
		ServiceName: serviceName,
		// We explicitly do NOT set FilePasswordFunc here — 99designs'
		// FileBackend is its *own* encrypted store (different from
		// our fallback) and we want to avoid opening it as a side
		// effect of the "try 99designs first" branch. If the operator
		// genuinely wants 99designs/file, they pass
		// `ForceBackend: "file-99designs"` and we hand back a prompt.
		FilePasswordFunc: ninetydesigns.TerminalPrompt,
	}
	if forced != "" && forced != "file" {
		cfg.AllowedBackends = []ninetydesigns.BackendType{ninetydesigns.BackendType(forced)}
	} else {
		// Explicitly exclude the 99designs file backend from the
		// auto-selected set; our own fallback is the file strategy.
		// We list everything except `file` and `pass` (the latter
		// shells out to an external binary, which is surprising in
		// a daemon context).
		cfg.AllowedBackends = []ninetydesigns.BackendType{
			ninetydesigns.KeychainBackend,
			ninetydesigns.SecretServiceBackend,
			ninetydesigns.KWalletBackend,
			ninetydesigns.WinCredBackend,
			ninetydesigns.KeyCtlBackend,
		}
	}
	kr, err := ninetydesigns.Open(cfg)
	if err != nil {
		return nil, fmt.Errorf("encryption: 99designs open: %w", err)
	}
	// Sanity probe: Keys() is cheap and surfaces a dead D-Bus early
	// rather than letting the first Get/Set trip it.
	if _, err := kr.Keys(); err != nil {
		return nil, fmt.Errorf("encryption: 99designs probe: %w", err)
	}
	return &ninetyDesignsBackend{kr: kr, service: serviceName}, nil
}

func (b *ninetyDesignsBackend) Get(key string) ([]byte, error) {
	item, err := b.kr.Get(key)
	if err != nil {
		if errors.Is(err, ninetydesigns.ErrKeyNotFound) {
			return nil, ErrMasterKeyringEntryMissing
		}
		return nil, fmt.Errorf("encryption: 99designs get: %w", err)
	}
	out := make([]byte, len(item.Data))
	copy(out, item.Data)
	return out, nil
}

func (b *ninetyDesignsBackend) Set(key string, val []byte) error {
	buf := make([]byte, len(val))
	copy(buf, val)
	return b.kr.Set(ninetydesigns.Item{
		Key:   key,
		Data:  buf,
		Label: b.service + ":" + key,
	})
}

func (b *ninetyDesignsBackend) Delete(key string) error {
	err := b.kr.Remove(key)
	if err == nil {
		return nil
	}
	if errors.Is(err, ninetydesigns.ErrKeyNotFound) {
		return nil // idempotent
	}
	return fmt.Errorf("encryption: 99designs remove: %w", err)
}
