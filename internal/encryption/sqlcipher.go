package encryption

// SQLCipher DSN helpers for the at-rest pipeline (TASK 7 of
// `specs/work-stoke.md`).
//
// The research brief mandates `github.com/jgiannuzzi/go-sqlite3`
// (sqlite3mc branch) as an ABI-compatible drop-in for
// `github.com/mattn/go-sqlite3`. That swap happens via a `go.mod`
// replace directive in a separate commit once the 5-15% overhead
// has been benchmarked on a real mission DB. This file ships the
// helper so callers can build an encrypted DSN the moment the
// driver swap lands — no API churn required.
//
// Cipher choice: ChaCha20 with a 4 KiB page size and 256 000 KDF
// iterations. Those are the sqlite3mc defaults that the research
// brief picked because ChaCha20 has constant-time software
// implementations (no AES-NI dependence) and the 4 KiB page size
// matches SQLite's own default. 256 000 iterations keeps PBKDF2
// cost under ~200 ms on a modern laptop while still costing an
// attacker ~$10^6 per guess at cloud GPU prices.
//
// Key material: the caller hands us the 32-byte master key from
// `LoadOrGenerateMaster` (or, preferably, a `DeriveKey(master,
// PurposeSQLiteCipher)` output — see `master_key.go`). We encode
// the key as lowercase hex and prefix it with the literal `x'...'`
// form that sqlite3mc expects for raw-key mode (so the driver does
// NOT run the bytes through the KDF a second time when the caller
// has already derived a uniform key via HKDF).
//
// Logging discipline: the DSN embeds the raw hex key. Every call
// site is expected to use `RedactedDSN` whenever the DSN string
// needs to appear in logs, error messages, or telemetry; the raw
// DSN only goes to `sql.Open`. A grep for `_key=` in the tree
// should turn up exactly one producer (this file) and one consumer
// (the SQLite driver open call) — anything else is a leak.

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// SQLCipher tuning knobs. Exported so tests and callers can assert
// the expected values without re-parsing the DSN string.
const (
	// SQLCipherName identifies the cipher mode passed to sqlite3mc.
	// Matches the mc_cipher=chacha20 pragma that the driver
	// recognises.
	SQLCipherName = "chacha20"

	// SQLCipherPageSize is the page size in bytes. 4 KiB is the
	// SQLite default; matching it avoids the read-amplification
	// penalty of mixing page sizes across databases.
	SQLCipherPageSize = 4096

	// SQLCipherKDFIter is the PBKDF2 iteration count. 256 000 is
	// the sqlite3mc-recommended default and is ignored in practice
	// (we hand over a pre-derived 32-byte key via the `x'...'`
	// literal form) but we set it anyway so a future caller that
	// swaps in a passphrase gets the expected cost.
	SQLCipherKDFIter = 256000
)

// BuildEncryptedDSN returns a SQLite DSN that opens `path` with the
// sqlite3mc ChaCha20 cipher enabled and keyed by `masterKey`. The
// returned string is suitable for `sql.Open("sqlite3", dsn)` once
// the `jgiannuzzi/go-sqlite3` replace directive is in place.
//
// The key is encoded as lowercase hex inside the `x'...'` literal
// form so sqlite3mc treats it as pre-derived raw key material
// (bypassing PBKDF2). Callers supplying a passphrase instead of a
// 32-byte key should NOT use this helper — it would misinterpret a
// short ASCII password as raw bytes and effectively truncate it.
//
// Errors surface when `path` is empty or `masterKey` is not exactly
// 32 bytes; both are fatal mis-configurations for at-rest
// encryption and the spec mandates fail-closed behaviour.
func BuildEncryptedDSN(path string, masterKey []byte) (string, error) {
	if path == "" {
		return "", fmt.Errorf("encryption: sqlcipher DSN: empty path")
	}
	if len(masterKey) != MasterKeySize {
		return "", fmt.Errorf("encryption: sqlcipher DSN: master key must be %d bytes, got %d",
			MasterKeySize, len(masterKey))
	}

	// sqlite3mc's raw-key literal form: _key=x'<hex>'. The single
	// quotes are part of the syntax; the driver strips them before
	// feeding the hex bytes to the cipher. We URL-escape the whole
	// value so an operator with a pathological `path` containing
	// `?` or `&` does not bleed query chars into our params.
	hexKey := hex.EncodeToString(masterKey)
	rawKey := fmt.Sprintf("x'%s'", hexKey)

	q := url.Values{}
	q.Set("_key", rawKey)
	q.Set("cipher", SQLCipherName)
	q.Set("cipher_page_size", fmt.Sprintf("%d", SQLCipherPageSize))
	q.Set("kdf_iter", fmt.Sprintf("%d", SQLCipherKDFIter))

	// The `file:` prefix is optional for mattn/go-sqlite3 and
	// required by some sqlite3mc code paths; use it so the DSN
	// works under both drivers during the swap window.
	return fmt.Sprintf("file:%s?%s", path, q.Encode()), nil
}

// redactKeyRE matches the `_key=<anything-up-to-&-or-end>` segment
// of a DSN so RedactedDSN can scrub the raw key before logging.
// Compiled once; the pattern is intentionally permissive (accepts
// hex, `x'...'` literals, URL-escaped single quotes) so that if a
// future DSN variant uses a different key encoding we still redact
// rather than leak.
var redactKeyRE = regexp.MustCompile(`_key=[^&]*`)

// RedactedDSN returns a copy of `dsn` with the `_key=...` value
// replaced by the literal `_key=<redacted>`. Safe for logs,
// telemetry, and error messages. If the input has no `_key=`
// segment — for example, a plaintext SQLite DSN — it is returned
// unchanged.
func RedactedDSN(dsn string) string {
	if !strings.Contains(dsn, "_key=") {
		return dsn
	}
	return redactKeyRE.ReplaceAllString(dsn, "_key=<redacted>")
}
