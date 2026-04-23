<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-21 -->
<!-- DEPENDS_ON: memory-bus (encrypts its content column), ledger-redaction (provides signing key for redaction events) -->
<!-- BUILD_ORDER: 25 -->

# Encryption at Rest — Implementation Spec

## 1. Overview

Stoke today stores sensitive reasoning material in the clear on the operator's disk. Concretely, three surfaces persist raw user prompts, tool outputs, and model responses: the SQLite database used by `internal/wisdom` (holding `wisdom_learnings`, `stoke_memories`, the `stoke_memory_bus` content column, and — once `specs/r1-server.md` lands — the r1-server session tables), the NDJSON stream files written by `internal/streamjson` (the Claude-Code-compatible event log teed into `<repo>/.stoke/stream.jsonl`), and the ledger content tier at `<repo>/.stoke/ledger/content/{id}.json`. Anyone with filesystem read is currently able to reconstruct an entire mission's conversation. That is unacceptable for regulated adopters (healthcare, finance, legal) who are the primary consumers of persistent agent memory.

This spec adds encryption at rest without breaking SQL queryability. SQLite is migrated to SQLCipher via a drop-in fork that preserves FTS5, JSON1, indexes, and joins at roughly 5–15% latency overhead. JSONL stream files move to per-line XChaCha20-Poly1305 so appends do not require rewriting prior lines and each line remains independently decryptable. The ledger's content tier is encrypted per-entry so crypto-shredding (wiping a DEK) renders content unrecoverable while the structural chain remains publicly verifiable. All encryption keys derive from a single 32-byte master key held by the OS keyring (macOS Keychain, Linux secret-service, Windows Credential Manager) with an encrypted `FileBackend` at `~/.r1/keyring` for headless Linux, Docker, and CI. The master key also feeds an HKDF-derived Ed25519 signer that `specs/ledger-redaction.md` consumes to sign retention-driven redaction events. The full system is flag-gated behind `STOKE_ENCRYPTION=1` and defaults off; when on, Stoke fails closed if the keyring is unreachable.

## 2. What gets encrypted where

| Surface | Mechanism | Files |
|---|---|---|
| Wisdom SQLite DB | SQLCipher (ChaCha20 + HMAC) | `~/.r1/wisdom.db`, `<repo>/.stoke/wisdom.db` |
| r1-server SQLite DB | SQLCipher (ChaCha20 + HMAC) | `~/.r1/server.db` |
| `stoke_memory_bus.content_encrypted` | per-row XChaCha20-Poly1305 | same DB, new column alongside `content` |
| `stoke.jsonl` stream files | per-line XChaCha20-Poly1305, base64 | `<repo>/.stoke/stream.jsonl[.enc]` |
| Ledger content tier | per-entry DEK (delegated to `specs/ledger-redaction.md`) | `<repo>/.stoke/ledger/content/{id}.json[.enc]` |
| Ledger chain tier | NOT encrypted — public commitment + structural header only | `<repo>/.stoke/ledger/chain/*.json` |
| Event bus WAL | NOT encrypted — structural only | `<repo>/.stoke/bus/wal/*` |
| Checkpoint timeline | NOT encrypted — structural + git sha only | `<repo>/.stoke/checkpoints/*` |

Rationale for the "NOT encrypted" rows: the chain, WAL, and checkpoint timeline carry structural metadata (hash pointers, node IDs, git refs, timestamps, event types) but no user-authored content. Leaving them plaintext preserves external verifiability of the commitment chain without leaking private reasoning.

## 3. SQLCipher migration (from research Correction 2)

**Driver swap.** The canonical Go SQLCipher drop-in is the `sqlite3mc` branch of `github.com/jgiannuzzi/go-sqlite3`. It is ABI-compatible with `github.com/mattn/go-sqlite3`: no code changes at call sites, only DSN additions. We pin it via a `replace` directive in `go.mod`:

```
replace github.com/mattn/go-sqlite3 => github.com/jgiannuzzi/go-sqlite3 v1.14.X-0.<date>-<sha>
```

Research step: the agent MUST run `go list -m -versions github.com/jgiannuzzi/go-sqlite3` against the module proxy, pick the newest pseudo-version on the `sqlite3mc` branch (not `master`), document the exact SHA it resolved in a comment next to the `replace` directive, and capture the resolved version in `docs/dependencies.md`. Do not pick a floating `@latest` — pin so CI is reproducible.

**DSN construction.** Open calls move from:

```go
sql.Open("sqlite3", path+"?_journal_mode=WAL")
```

to a helper `internal/wisdom.buildDSN(path, keyHex string) string` that emits:

```
file:<path>?_journal_mode=WAL&cipher=chacha20&cipher_page_size=4096&kdf_iter=256000&_key=<hex>
```

The `_key=` parameter is passed as a hex-encoded 32-byte key (64 hex chars). Never log the DSN; the key leaks into logs/cores if you do. Helper returns the DSN *and* a redacted version for any diagnostic path that must show the connection string.

**PRAGMA settings.** The three parameters above correspond to these PRAGMAs applied automatically by sqlite3mc when it sees the query string. Additionally, set `PRAGMA cipher_memory_security = ON;` on every new connection to zero key material on free.

**Migration path.** New DBs are created encrypted. For existing plaintext DBs we ship `stoke encryption enable`:

1. Open the DB with the `mattn` driver (plaintext) and verify integrity (`PRAGMA integrity_check;`).
2. Close, reopen with sqlite3mc and the new master key, run `PRAGMA rekey = 'x''<hex>'''` — sqlite3mc performs an in-place conversion.
3. Read-back validation: run a representative `SELECT` against `wisdom_learnings` and `stoke_memories`; compare row counts before/after.
4. Write a sentinel row `stoke_meta.encryption_version = 1` and update `~/.r1/config.yaml` `encryption.enabled: true`.
5. On subsequent opens, if `encryption.enabled=true` but the DB opens without `_key=`, refuse with `encryption: master key required but keyring unreachable`.

**Performance target.** Per research, SQLCipher adds 5–15% overhead. The benchmark in `internal/wisdom/bench_cipher_test.go` runs a 10k-row insert + 1k random reads + 1k FTS5 queries both ways and fails CI if the p50 read regression exceeds 15%.

## 4. Per-line JSONL encryption

**Line format.** Each plaintext JSON object is encrypted into a single base64 line:

```
base64( nonce(24) || ciphertext || auth_tag(16) ) '\n'
```

We use XChaCha20-Poly1305 (libsodium or `golang.org/x/crypto/chacha20poly1305`'s `NewX`) because the 24-byte random nonce has negligible collision probability across the ~10^9 events a long-lived Stoke install may emit. Per-line AEAD gives us:

- **Append-only semantics preserved.** Writing a new line never touches prior lines.
- **Independent decryption.** A truncated or corrupted line does not poison later lines.
- **Per-line integrity.** The Poly1305 tag detects tampering; a flipped byte raises `stream: auth tag mismatch on line N` with the line offset.

**Acceptable leakage.** Line count, per-line ciphertext length (± 16), and write timestamps (via inode mtime) are leaked. This is documented as acceptable for traces — full length-hiding would break incremental `tail -f`, which operators rely on during debugging.

**API.** New package `internal/crypto/stream.go`:

```go
func EncryptLine(plaintext, key []byte) ([]byte, error) // returns base64 line WITHOUT newline
func DecryptLine(line, key []byte) ([]byte, error)      // accepts with or without trailing \n
```

**Emitter toggle.** `internal/streamjson/emitter.go` gains a `key []byte` field populated from the master key when `STOKE_ENCRYPT_STREAM=1`. When set, `writeEvent` routes through `crypto.EncryptLine` before writing. File suffix switches to `.jsonl.enc`. The r1-server scanner opens `<path>.jsonl.enc` first and falls back to `<path>.jsonl` if absent, so mixed-age installs keep working.

## 5. Key management (from research Correction 2)

**Library choice.** `github.com/99designs/keyring` is the de-facto Go abstraction over OS credential stores and is used by AWS CLI, HashiCorp tools, and gh. It handles the four backends we need and ships a portable encrypted `FileBackend`. Document the choice in `internal/crypto/keyring.go` package doc, citing https://github.com/99designs/keyring.

**Backend precedence.**

1. `runtime.GOOS == "darwin"` → Keychain (service=`r1`, account=`master-key`).
2. `runtime.GOOS == "linux"` and `os.Getenv("DBUS_SESSION_BUS_ADDRESS") != ""` and secret-service is reachable → secret-service (GNOME Keyring / KDE Wallet).
3. `runtime.GOOS == "linux"` headless, Docker (`/.dockerenv` exists), or CI (`os.Getenv("CI") != ""`) → encrypted `FileBackend` at `~/.r1/keyring`. Passphrase from `R1_KEYRING_PASSPHRASE` env; if unset and stdin is a TTY, prompt interactively; if unset and headless, fail with `keyring: passphrase required (set R1_KEYRING_PASSPHRASE)`.
4. `runtime.GOOS == "windows"` → Credential Manager (DPAPI-backed). Known gotcha: DPAPI scope is per-user, so a service running as `LocalSystem` cannot read a key written by a user session — document this in the runbook and recommend `FileBackend` for Windows service installs.

**API.** `internal/crypto/keyring.go`:

```go
func GetMasterKey() ([]byte, error)   // returns 32 bytes; first call generates+stores
func RotateMasterKey() error          // writes new key, triggers SQLCipher rekey + XChaCha roll
func GetRedactionSigner() (ed25519.PrivateKey, error) // HKDF-derived, see section 6
```

**First-run behavior.** If no master-key entry exists, generate 32 random bytes, store, and emit a one-time banner on stderr:

```
[stoke] Generated new encryption master key in keyring '<backend>'.
        Back it up: `stoke encryption export-backup > master.key.enc`
        Losing this key makes encrypted data unrecoverable. No escape hatch is shipped.
```

**Lost-key behavior.** If `encryption.enabled=true` and `GetMasterKey()` returns `keyring: entry not found`, Stoke refuses to open the DB with `database is encrypted; keyring entry missing at <backend>:<service>:<account>`. No recovery mode. This is deliberate — operators must own their backup story. The `stoke encryption export-backup` command above emits a passphrase-wrapped copy of the master key for the operator to store out-of-band.

## 6. Content redaction handshake with `specs/ledger-redaction.md`

`specs/ledger-redaction.md` owns content-tier wiping. It needs an Ed25519 signing key to author redaction ledger events. This spec provides it, derived deterministically from the master key so rotating the master key rotates the signer automatically.

```go
func GetRedactionSigner() (ed25519.PrivateKey, error) {
    mk, err := GetMasterKey()
    if err != nil { return nil, err }
    seed := make([]byte, 32)
    _, err = io.ReadFull(hkdf.New(sha256.New, mk, nil, []byte("r1-redaction-signer")), seed)
    if err != nil { return nil, err }
    return ed25519.NewKeyFromSeed(seed), nil
}
```

`ledger.Store.Redact(subject, signer)` accepts this signer; the verify path uses the matching `ed25519.PublicKey` derived from the same HKDF call. This spec does NOT design the wipe semantics, the per-subject KEK rotation, or the Merkle-redaction tombstones — all that lives in `specs/ledger-redaction.md`.

## 7. API + files

New files:

- `internal/crypto/keyring.go` — keyring abstraction, master-key CRUD, HKDF signer derivation.
- `internal/crypto/stream.go` — per-line JSONL encrypt/decrypt helpers.
- `internal/crypto/keyring_test.go`, `stream_test.go` — unit tests.
- `internal/wisdom/sqlite_cipher_test.go` — SQLCipher open/roundtrip/rekey tests.
- `cmd/stoke/encryption_cmd.go` — `stoke encryption enable|disable|rotate|status|export-backup`.
- `docs/runbooks/encryption.md` — operator runbook (first-run, backup, recovery failure modes, Windows service caveat).

Edits:

- `go.mod` — add `replace github.com/mattn/go-sqlite3 => github.com/jgiannuzzi/go-sqlite3 <pinned>`; add direct deps on `github.com/99designs/keyring` and `golang.org/x/crypto`.
- `internal/wisdom/sqlite.go` — DSN builder, auto-open with master key when `encryption.enabled=true`, PRAGMA emission.
- `internal/streamjson/emitter.go` — `SetEncryptionKey(key []byte)`; when set, route `writeEvent` through `crypto.EncryptLine`; switch suffix to `.jsonl.enc`.
- `internal/memory/bus.go` — add `content_encrypted BLOB` column; populate when encryption on; `GetContent` decrypts transparently.
- `internal/config/config.go` — add `Encryption{ Enabled bool; Mode string }` block to YAML schema.

## 8. Implementation checklist

1. [ ] Research the exact `sqlite3mc`-branch pseudo-version via `go list -m -versions github.com/jgiannuzzi/go-sqlite3`; pick the newest branch pseudo-version; record the SHA in a comment above the replace directive.
2. [ ] Add the `replace github.com/mattn/go-sqlite3 => github.com/jgiannuzzi/go-sqlite3 <ver>` directive to `go.mod` and run `go mod tidy`.
3. [ ] Verify drop-in parity: `go build ./...` and `go test ./internal/wisdom/...` pass unchanged before any cipher code is wired.
4. [ ] Add `github.com/99designs/keyring` to direct deps; `go mod tidy`; confirm no transitive CGO requirement pulled in unexpectedly.
5. [ ] Add `golang.org/x/crypto` to direct deps (needed for `chacha20poly1305`, `hkdf`, `ed25519`).
6. [ ] Create `internal/crypto/keyring.go` with the backend-precedence selector and `GetMasterKey`/`RotateMasterKey`/`GetRedactionSigner` signatures.
7. [ ] Implement macOS Keychain backend selection, guarded by `runtime.GOOS == "darwin"`.
8. [ ] Implement Linux secret-service backend selection, guarded by `DBUS_SESSION_BUS_ADDRESS` presence and a reachability probe.
9. [ ] Implement Linux FileBackend fallback at `~/.r1/keyring` with passphrase from `R1_KEYRING_PASSPHRASE`.
10. [ ] Implement interactive passphrase prompt for TTY sessions using `golang.org/x/term.ReadPassword`; fall through to the env var when not a TTY.
11. [ ] Implement headless-refusal path when `R1_KEYRING_PASSPHRASE` is unset and stdin is not a TTY; error exactly `keyring: passphrase required (set R1_KEYRING_PASSPHRASE)`.
12. [ ] Implement Docker detection via `/.dockerenv` presence; treat as headless.
13. [ ] Implement CI detection via `os.Getenv("CI") != ""`; treat as headless.
14. [ ] Implement Windows Credential Manager backend selection, guarded by `runtime.GOOS == "windows"`.
15. [ ] Document the Windows `LocalSystem` DPAPI gotcha in the package doc and in `docs/runbooks/encryption.md`.
16. [ ] Implement first-run key generation: 32 bytes from `crypto/rand`; store under `service=r1`, `account=master-key`.
17. [ ] Emit the one-time stderr backup banner only on first generation; gate via a boolean returned from the store call.
18. [ ] Implement `RotateMasterKey`: generate new key, read old, call SQLCipher `PRAGMA rekey` on each open DB, re-encrypt any streaming key material.
19. [ ] Implement `GetRedactionSigner` via HKDF-SHA256 over the master key with info `"r1-redaction-signer"`; return `ed25519.NewKeyFromSeed`.
20. [ ] Unit test keyring backend precedence with a fake `runtime.GOOS` + env shim; cover all four branches.
21. [ ] Unit test missing-passphrase headless path returns the exact documented error string.
22. [ ] Unit test `RotateMasterKey` idempotence: rotating twice produces two distinct keys and the last one is fetched by `GetMasterKey`.
23. [ ] Unit test `GetRedactionSigner` determinism: same master key → same Ed25519 private key bytes.
24. [ ] Unit test master-key rotation rotates the redaction signer's public key.
25. [ ] Create `internal/crypto/stream.go` with `EncryptLine(plaintext, key []byte) ([]byte, error)` and `DecryptLine(line, key []byte) ([]byte, error)`.
26. [ ] Use `chacha20poly1305.NewX(key)` to get an XChaCha20-Poly1305 AEAD; enforce 32-byte key length with a named error.
27. [ ] In `EncryptLine`, generate a 24-byte nonce via `crypto/rand`, seal, concatenate `nonce||ciphertext||tag`, base64 std-encode, return WITHOUT trailing newline (caller writes `\n`).
28. [ ] In `DecryptLine`, accept lines with or without trailing `\n`; trim; base64 decode; split nonce/ciphertext; `Open` with the AEAD.
29. [ ] Surface a named `ErrAuthTag` on Poly1305 mismatch so callers can log line offset.
30. [ ] Unit test `EncryptLine`/`DecryptLine` roundtrip on a 1-byte, 1-KiB, and 1-MiB plaintext.
31. [ ] Unit test flip-one-byte produces `ErrAuthTag` with descriptive message.
32. [ ] Unit test cross-key decrypt produces `ErrAuthTag` (not a garbled success).
33. [ ] Unit test 10k-line throughput within 2x of plain JSON marshal for the same payload.
34. [ ] Add DSN builder `internal/wisdom.buildDSN(path, keyHex string) (dsn, redacted string)` that emits the documented cipher query-string and returns a second redacted copy for logs.
35. [ ] Add unit test `buildDSN` redaction: redacted string must contain `_key=REDACTED`, not the hex key.
36. [ ] Modify `NewSQLiteStore` to optionally accept a master key; when encryption enabled, build the cipher DSN.
37. [ ] Emit `PRAGMA cipher_memory_security = ON;` as the first statement after open.
38. [ ] Add `stoke_meta` table schema with `encryption_version INTEGER` and populate on first open.
39. [ ] Implement `rekeyDB(path, oldPlainPath, newKeyHex)` helper: opens plaintext, runs `PRAGMA rekey`, verifies read-back, updates sentinel.
40. [ ] Unit test rekey: create plaintext DB, populate 1000 rows, rekey, reopen with cipher DSN, SELECT count matches.
41. [ ] Unit test wrong-key open fails with a descriptive error containing `"file is not a database"` or equivalent sqlite3mc error.
42. [ ] Add `internal/wisdom/bench_cipher_test.go` with p50 read-latency benchmark over 10k rows; fail threshold 15%.
43. [ ] Add `internal/memory/bus.go` migration: `ALTER TABLE stoke_memory_bus ADD COLUMN content_encrypted BLOB`.
44. [ ] Populate `content_encrypted` when `encryption.enabled=true`; leave `content` NULL or truncated.
45. [ ] `bus.GetContent` prefers `content_encrypted` when non-NULL; decrypts via master key; falls back to `content`.
46. [ ] Unit test memory-bus roundtrip: write under encryption, close, reopen, read back identical plaintext.
47. [ ] Modify `internal/streamjson/emitter.go` to accept a key via `SetEncryptionKey([]byte)`; gate via `STOKE_ENCRYPT_STREAM=1`.
48. [ ] When encryption on, change target file suffix to `.jsonl.enc`; when off, keep `.jsonl`.
49. [ ] Modify `writeEvent` to route the marshaled JSON through `crypto.EncryptLine` before writing when a key is set.
50. [ ] Unit test emitter roundtrip: encrypt, reopen with a `bufio.Scanner`, `DecryptLine` each line, compare to original events.
51. [ ] r1-server scanner: open `<path>.jsonl.enc` first; fall back to `<path>.jsonl`; document precedence in the scanner doc comment.
52. [ ] Add `cmd/stoke/encryption_cmd.go` with subcommands `enable`, `disable`, `rotate`, `status`, `export-backup`.
53. [ ] `enable` implements the full migration path: integrity check → rekey → read-back → sentinel → config flip.
54. [ ] `disable` warns loudly and requires `--yes-i-understand-the-risk`; runs `PRAGMA rekey=''` to decrypt in place; flips config.
55. [ ] `rotate` calls `RotateMasterKey()` then rekeys every DB under management.
56. [ ] `status` prints backend in use, key-presence flag, per-DB encryption version, and stream-encryption flag.
57. [ ] `export-backup` writes an `age`-encrypted copy of the master key to stdout; passphrase via `R1_BACKUP_PASSPHRASE`.
58. [ ] Add config schema entry `encryption: { enabled: bool, mode: "sqlcipher+xchacha" }` to `internal/config/config.go`.
59. [ ] Fail-closed startup: when `encryption.enabled=true` and `GetMasterKey()` errors, the process exits 1 with `database is encrypted; keyring entry missing at <backend>`.
60. [ ] Add `docs/runbooks/encryption.md` covering first-run, backup, rotation, Windows-service caveat, lost-key unrecoverability, CI setup.
61. [ ] CI matrix: add a job that builds with `-tags sqlite_fts5` on linux/macos/windows against the sqlite3mc driver and runs the full cipher test suite.

## 9. Acceptance criteria

- `go build ./cmd/stoke`, `go test ./...`, and `go vet ./...` all pass on linux-amd64, macos-arm64, and windows-amd64 CI runners.
- `stoke encryption enable` on an existing plaintext `wisdom.db` produces a cipher DB that answers every pre-existing integration test's SELECTs identically, with p50 read latency ≤ 1.15× the plaintext baseline on the 10k-row benchmark.
- A JSONL line whose Poly1305 tag has been flipped raises `stream: auth tag mismatch on line N` with the offset included; the scanner surfaces this rather than silently skipping.
- With `encryption.enabled=true` and the keyring entry deleted out-of-band, Stoke exits 1 at startup with exactly `database is encrypted; keyring entry missing at <backend>:<service>:<account>`.
- `stoke encryption status` on a fully encrypted install prints backend, all managed DB paths with their `encryption_version`, and `stream: enabled`.
- A redaction event signed via `GetRedactionSigner()` verifies against the deterministically-derived Ed25519 public key; rotating the master key invalidates prior signatures (tested by `specs/ledger-redaction.md`'s test suite — this spec's tests only cover the HKDF derivation determinism).

## 10. Testing

Per-file unit tests:

- `internal/crypto/keyring_test.go` — backend precedence (all four branches via a shim), missing-passphrase headless error, first-run banner fires exactly once, rotation determinism, HKDF signer determinism, HKDF signer rotates with master.
- `internal/crypto/stream_test.go` — roundtrip across 1-byte/1-KiB/1-MiB payloads, auth-tag mismatch, cross-key mismatch, 10k-line throughput bound.
- `internal/wisdom/sqlite_cipher_test.go` — open with correct key reads 1000 seeded rows; open with wrong key fails with sqlite3mc "file is not a database"; rekey roundtrip preserves row count; FTS5 MATCH works post-rekey; `PRAGMA cipher_memory_security` observable.
- `internal/memory/bus_encrypted_test.go` — write plaintext → reopen encrypted → read matches; content column NULL when encrypted column populated.
- `internal/streamjson/emitter_encrypt_test.go` — emit 100 events with `STOKE_ENCRYPT_STREAM=1`, reopen with scanner + `DecryptLine`, full equality.

End-to-end test `cmd/stoke/encryption_e2e_test.go`:

1. Build a throwaway repo with plaintext wisdom + plaintext stream.
2. Run `stoke encryption enable`.
3. Close, restart, run `stoke wisdom list` and `stoke stream tail` — both return identical content.
4. Rotate key; repeat step 3; still identical.
5. Delete the keyring entry; next startup fails with the documented error string.

## 11. Rollout

Flag: `STOKE_ENCRYPTION=1` env OR `encryption.enabled: true` in `~/.r1/config.yaml`. Both routes converge on the same runtime flag; env wins for ephemeral CI runs.

Default: **off**. Existing installs see no behavior change until an operator runs `stoke encryption enable`. This is deliberate: encryption has an irreversible failure mode (lost master key = lost data), and we want operators to opt in consciously.

When on, startup runs a keyring reachability probe before any DB open. If the probe fails, Stoke exits 1 with the documented error. This is fail-closed by design — a soft-fallback to plaintext would silently leak reasoning to disk in exactly the environments regulated operators care about.

Primary adopters are operators with regulated data: healthcare (HIPAA), finance (SOX / PCI adjacency), legal (privilege-bound discovery material). The runbook at `docs/runbooks/encryption.md` covers:

- First-run key generation and mandatory backup step.
- Backup rotation and restoration via `stoke encryption export-backup`.
- Key rotation cadence and operational impact (rekey is an offline operation; schedule it during a maintenance window on large DBs).
- The Windows `LocalSystem` DPAPI caveat with the recommended `FileBackend` workaround.
- The "lost key = lost data" irrecoverability contract and the absence of an escape hatch.
- CI setup: set `R1_KEYRING_PASSPHRASE` as a masked secret; use `FileBackend` automatically; do NOT check `~/.r1/keyring` into version control.

## 12. Boundaries — what NOT to do

- Do NOT design the ledger's content-tier wipe or per-entry DEK lifecycle. That is `specs/ledger-redaction.md`. This spec only provides the signing key.
- Do NOT design the retention policy engine itself. That is `specs/retention-policies.md`. This spec only supplies the primitives retention will call into.
- Do NOT change the ledger edge schema or structural-header schema. They are NOT encrypted and are relied upon by external verifiers.
- Do NOT add a key-escrow or key-recovery escape hatch by default. Operators own their backup story; shipping an escape hatch would undermine the threat model for regulated adopters.
- Do NOT log the DSN, the master key, derived keys, or decrypted plaintext at any log level. The redacted-DSN helper exists precisely so diagnostic paths can't accidentally leak.
