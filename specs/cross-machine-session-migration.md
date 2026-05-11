<!-- STATUS: ready -->
<!-- CREATED: 2026-05-11 -->
<!-- DEPENDS_ON: -->
<!-- BUILD_ORDER: 41 -->

# Cross-Machine Session Migration — Implementation Spec

## 1. Overview

Today an r1 session is bound to the daemon process that started it: its journal, bus WAL, ledger entries, lobe state, memory rows, lane state, and skill packs all live on the host where `r1 daemon` was launched. Moving a session to another machine requires shutting down, manually copying `~/.r1/<session>/`, hand-syncing SQLite rows, and praying that hash continuity survives. The `.tracebundle` v2 export (per `specs/r1-server-ui-v2-handlers-and-routes.md` §T15-T17) already ships a per-session-filtered chain + edge + content dump with a deterministic `chain_root_hash` signed via `ledger.CanonicalManifestSignBody` — but that is the *read-only forensic* half: it cannot replay into a live daemon and it does not carry the live bus WAL, lobe state, lanes, or session-scoped memory.

This spec adds the **live half**: a `.r1session` migration bundle that captures everything required to resume the session on a different daemon, a pair of CLI verbs (`r1 session export` / `r1 session import` / `r1 session migrate`), a daemon-to-daemon HTTP migrate-out / migrate-in pair, and an end-to-end **ledger-continuity verification** that re-derives `chain_root_hash` over the imported nodes and rejects the import on mismatch. The bundle reuses `ledger.CanonicalManifestSignBody` exactly so any downstream verifier already wired to check tracebundles can verify migration bundles too. The contract is coordinated-checkpoint with message-logging — the source pauses the session at a quiet point, snapshots everything deterministic, and emits the bus WAL as the replay log; the destination ingests the snapshot, replays the WAL, and verifies the chain root matches before flipping the session live.

Cross-tenant migration is forbidden in v1; the encryption-at-rest spec (STATUS:done) requires that both daemons hold the same master-key material, so v1 only supports same-operator migration. A future KMS-wrapped key-envelope extension is deferred.

## 2. Stack & Versions

- Go (existing toolchain — `go.mod` already pins).
- Stdlib only for archive: `archive/tar`, `compress/gzip`, `encoding/json`, `crypto/sha256`, `crypto/ed25519`.
- HTTP: existing `net/http` + `internal/server` handler chain.
- Auth: reuse `internal/server/auth_middleware.go` bearer + the daemon's existing SSO config (same as `/api/session/{id}/export.tracebundle`).
- Ledger hashing: `internal/ledger.CanonicalManifestSignBody`, `internal/ledger.ChainRootHashForSession`.
- Bus replay: `internal/bus.WAL` + `internal/replay/session.go`.

## 3. Existing Patterns to Follow

- Tracebundle export: `cmd/r1-server/tracebundle.go` (`serveTracebundle`) — streaming tar.gz, manifest.json + chain.ndjson + edges.ndjson, signed with `ledger.CanonicalManifestSignBody`. The migration bundle is a superset of this format; we extend rather than replace.
- Tracebundle import: `cmd/r1-server/import.go` — idempotent, Merkle-root re-verification before any row write, fail-closed on mismatch. Same defensive pattern applies here.
- Session hub: `internal/server/sessionhub/sessionhub.go` (`SessionHub.Create / Get / Delete`) + `internal/server/sessionhub/session.go` (`Session` struct with `ID`, `SessionRoot`, `Workspace`, `journal`, `loop`, lifecycle state).
- Pause/resume: `internal/server/sessionhub/pause_resume_send_test.go` patterns; the existing `State` field on `Session` already supports "paused-reattachable" transitions.
- Checkpointing: `internal/checkpoint/checkpoint.go` — quiet-point capture; `internal/checkpoint/resume.go` — restoration. Reuse the existing `Checkpoint` type for the pre-export safety capture.
- Ledger per-session filter: `internal/ledger/store_session.go` (`ListNodesForSession`, `ListEdgesForSession`, `ChainRootHashForSession`).
- Auth bearer: `internal/server/auth_middleware.go` — same `Authorization: Bearer <token>` pattern as the tracebundle endpoint.
- CLI subcommand idiom: `cmd/r1/sessions.go` — flag parsing, subcommand dispatch, JSON-output toggle.

## 4. Library Preferences

- Validation: existing stdlib + struct-tag JSON.
- HTTP client: stdlib `net/http` with the existing `apiclient` helpers.
- Compression: stdlib `compress/gzip` (NO 3rd-party deps — match the tracebundle export's "no new deps" constraint per `specs/r1-server-ui-v2-handlers-and-routes.md` line 300).
- Hashing: `crypto/sha256` + `encoding/hex` (match `ledger.ChainRootHashForSession`).
- Signing: `crypto/ed25519` via `internal/crypto/keyring.go` (`GetRedactionSigner` already returns the master-derived signer; reuse the same derivation slot).

## 5. Data Models

### 5.1 `.r1session` archive layout

`gzip-tar` with the following entries (all paths are POSIX-relative inside the archive):

```
manifest.json              -- top-level metadata + signature
ledger/chain.ndjson        -- per-session chain-tier nodes (one JSON node per line)
ledger/edges.ndjson        -- per-session edges (one JSON edge per line)
ledger/content/<id>.json   -- per-session content-tier blobs (non-redacted)
ledger/content/redacted.json -- list of {node_id, redaction_event_ids} for redacted nodes
bus/wal.ndjson             -- bus events from session start to export quiet-point (NDJSON, replay-ordered by seq)
bus/wal.index.json         -- {first_seq, last_seq, count, sha256, wal_checkpoint_hashes: [{seq, partial_chain_root}]}
memory/rows.ndjson         -- per-session memory rows (encrypted-at-rest preserved as-is)
memory/index.json          -- {count, scope_targets, sha256}
skills/pack-refs.json      -- [{pack_id, content_hash, version}] -- references, NOT bodies
lobe-state/<lobe_id>.json  -- deterministic Cortex Lobe snapshots per spec cortex-core.md persist section
lanes/snapshot.json        -- Lane state snapshot per spec lanes-protocol.md (r1.lanes.get)
checkpoint/pre-export.json -- internal/checkpoint.Checkpoint emitted right before quiet-point
signature.ed25519          -- ed25519 signature over CanonicalManifestSignBody(...) of manifest.json
```

### 5.2 `manifest.json` schema

```json
{
  "format":            "r1session",
  "schema_version":    1,
  "version":           1,
  "source_host":       "<daemon hostname or stable host_id>",
  "source_daemon_id":  "<daemon-uuid>",
  "source_session_id": "<session-id-on-source>",
  "tenant_id":         "<tenant>",
  "exported_at":       "<RFC3339Nano UTC>",
  "chain_root_hash":   "<sha256 hex>",
  "wal_sha256":        "<sha256 of bus/wal.ndjson bytes>",
  "memory_sha256":     "<sha256 of memory/rows.ndjson bytes>",
  "node_count":        0,
  "edge_count":        0,
  "wal_first_seq":     0,
  "wal_last_seq":      0,
  "model":             "<provider/model id>",
  "skill_pack_refs":   [{"pack_id":"...","content_hash":"...","version":"..."}],
  "signer":            "<ed25519 public key fingerprint>",
  "signature_hex":     "<ed25519 sig over CanonicalManifestSignBody(format, schema_version, source_session_id, chain_root_hash, exported_at, signer)>"
}
```

The signature body is exactly `ledger.CanonicalManifestSignBody("r1session", 1, source_session_id, chain_root_hash, exported_at, signer)` — same function the tracebundle path uses. Downstream verifiers therefore need zero new code to verify migration bundles' manifest signatures.

### 5.3 Source session lifecycle states (extension)

`Session.State` (in `internal/server/sessionhub/session.go`) gains two new transitions:

- `migrating-out` — set by `SessionHub.BeginMigrateOut(id)`, blocks new turns, allows the current turn to finish if any.
- `migrated-out` — set after a successful export; the source session is parked read-only (still reachable for re-export but cannot accept new user input).

The destination session is created in state `migrating-in` and transitions to `idle` only after `chain_root_hash` is re-verified post-replay.

## 6. API Endpoints

### 6.1 `POST /api/session/{id}/migrate-out`

**Auth:** required — bearer matching `auth_middleware.go` (same-tenant or admin role).

**Request:** empty body, optional `?force=1` to override "session is mid-turn" guard.

**Response (200):** `Content-Type: application/gzip`, `Content-Disposition: attachment; filename="<session_id>.r1session"`. Streams the bundle bytes.

**Errors:**
| Status | When | Body |
|--------|------|------|
| 404 | Unknown session_id | `{"error":"session_not_found"}` |
| 409 | Session mid-turn, no `?force=1` | `{"error":"session_busy","state":"<state>"}` |
| 409 | Session has unsigned legacy redactions | `{"error":"unsigned_redactions_present","node_ids":[...]}` |
| 401 | Bad / missing bearer | `{"error":"unauthorized"}` |
| 403 | Cross-tenant attempt | `{"error":"cross_tenant_forbidden"}` |
| 500 | Export internal failure | `{"error":"export_failed","detail":"..."}` |

### 6.2 `POST /api/session/migrate-in`

**Auth:** required — same bearer rules.

**Request:** `Content-Type: application/gzip`, body = `.r1session` bundle bytes (streamed).

**Response (201):** `{"new_session_id":"<dest-side id>","chain_root_hash":"<hex>","node_count":<n>,"wal_replayed":<n>,"verified":true}`.

**Errors:**
| Status | When | Body |
|--------|------|------|
| 400 | Bundle parse failure / missing manifest | `{"error":"bundle_invalid"}` |
| 400 | `schema_version` not supported | `{"error":"schema_version_unsupported","got":<n>,"want":1}` |
| 401 | Bad bearer | `{"error":"unauthorized"}` |
| 403 | Cross-tenant attempt | `{"error":"cross_tenant_forbidden"}` |
| 200 | Re-import of identical manifest (idempotency) | `{"new_session_id":"<existing>","idempotent":true}` |
| 422 | `chain_root_hash` mismatch post-replay | `{"error":"chain_root_hash_mismatch","expected":"...","actual":"...","divergent_at_seq":<n>}` |
| 422 | Missing skill packs | `{"error":"missing_skill_packs","packs":[{pack_id,content_hash}, ...]}` |
| 500 | Internal failure | `{"error":"import_failed","detail":"..."}` |

### 6.3 Verification of ledger continuity (shared by §6.2 and `r1 session import`)

After WAL replay completes:

1. Call `ledger.ChainRootHashForSession(dest_session_id)` against the destination's freshly-hydrated ledger.
2. Compare against `manifest.chain_root_hash`.
3. On mismatch: roll back via the pre-import savepoint, emit a `session.migrate.divergent` bus event with `{expected, actual, source_session_id, dest_session_id, divergent_at_seq}`, return 422. Write an audit row via `internal/audit`.
4. On match: flip `dest_session.State` from `migrating-in` to `idle`; emit `session.migrate.completed` event with `{source_session_id, dest_session_id, chain_root_hash, node_count, wal_replayed}`.

## 7. Business Logic

### 7.1 Export (`r1 session export`)

1. **Resolve session** via `SessionHub.Get(id)` on the local daemon.
2. **Quiet-point latch**: call `SessionHub.BeginMigrateOut(id)` which:
   - If `Session.State` is in {`running`, `tool-in-flight`} and `--force` is unset, return `409`.
   - Else set `Session.State = "migrating-out"`; the agent loop's `MidturnCheckFn` (already wired per `specs/cortex-core.md`) sees the new state and yields after the current `assistant`/`tool_result` pair completes (no orphaned `tool_use`).
   - Wait up to `5s` for quiescence (configurable). On timeout: revert state, return `409` unless `--force`.
3. **Pre-export checkpoint**: `checkpoint.WriteCheckpoint(repo, "pre-migrate-export")` drops a recoverable savepoint on disk in case the export aborts.
4. **Bundle assembly** (streaming, never buffers >64 KB):
   - Compute `chain_root_hash` via `ledger.ChainRootHashForSession(id)`.
   - Tar-write `manifest.json` last (after counts + hashes are known) but reserve a manifest slot via two-pass: write all data files, then sign the manifest, then append `manifest.json` + `signature.ed25519` to the tar trailer. Tar allows this — entries are streamed sequentially; readers don't require manifest-first.
   - **Source `ledger/chain.ndjson` + `ledger/edges.ndjson`**: reuse the producers in `cmd/r1-server/tracebundle.go` verbatim — same data, same encoding.
   - **`bus/wal.ndjson`**: read `~/.r1/<session>/bus.wal` from `wal_first_seq=0` (session start) to `wal_last_seq=Bus.LastSeq()`; each line is `bus.Event` JSON. Compute SHA-256 over the file bytes for `wal_sha256`.
   - **`memory/rows.ndjson`**: query `internal/memory` for rows with `scope_target = "session:<id>"`. Preserve encryption envelopes byte-for-byte (DEKs are derived from the shared master key on the destination — see §10).
   - **`skills/pack-refs.json`**: list loaded pack IDs from the session's `cortex.Workspace.Skills` registry; record `{pack_id, content_hash, version}` only — NOT the bodies.
   - **`lobe-state/<lobe_id>.json`**: call each Lobe's `Snapshot() ([]byte, error)` method (per `specs/cortex-core.md` persist section). Deterministic — replaying with the same inputs yields the same Lobe state.
   - **`lanes/snapshot.json`**: call the daemon's `r1.lanes.get` handler (per `specs/lanes-protocol.md` line 719) with `since_seq=0` for the session to get the current lane snapshot.
   - **`checkpoint/pre-export.json`**: include the pre-export checkpoint file.
5. **Sign manifest**: `ed25519.Sign(redactionSigner, ledger.CanonicalManifestSignBody("r1session", 1, sessionID, chainRootHash, exportedAt, signerFP))`. Reuse the existing redaction signer key — derived from the master key via HKDF info `"r1-redaction-signer"` per `specs/encryption-at-rest.md` line 129.
6. **Return** the gzip-tar bytes to the caller; resume the session (`Session.State` to `idle`) unless `--park` was passed (in which case the source is left in `migrated-out`).
7. **Audit**: emit `session.migrate.exported` bus event.

### 7.2 Import (`r1 session import`)

1. **Open bundle**: stream `gzip.NewReader` + `tar.NewReader`. Buffer the manifest (small) in memory; stream the rest.
2. **Verify signature**: re-derive `CanonicalManifestSignBody`, fetch the ed25519 public key (master-derived, must match locally — fails closed if it doesn't), `ed25519.Verify`. Mismatch → reject with `bundle_invalid`.
3. **Idempotency check**: hash the manifest bytes; look up in `migration_imports` SQLite table (created by this spec). If present, return the existing `new_session_id` with HTTP 200 `{"idempotent":true}`.
4. **Schema-version gate**: `schema_version == 1`; else 400.
5. **Skill-pack check**: for each `skill_pack_refs[i]`, verify pack is installed locally (matches `pack_id + content_hash`). If any missing, return 422 with the missing-packs list. The operator must `r1 skills pack install <pack_id>@<content_hash>` and retry.
6. **Allocate new session_id**: `SessionHub.Create(...)` with state `migrating-in`. Workspace dir per the destination daemon's policy.
7. **Hydrate ledger**: tar-read each `ledger/chain.ndjson` line and `INSERT INTO chain` (with `ON CONFLICT(id) DO UPDATE` per `cmd/r1-server/import.go` idempotency pattern). Same for `ledger/edges.ndjson`. Re-map `MissionID` (= source_session_id) to the new dest `session_id` in node + edge metadata. Then write `ledger/content/*.json` blobs.
8. **Hydrate memory**: insert `memory/rows.ndjson` with `scope_target = "session:<new_session_id>"`. Encryption envelopes are preserved as-is; destination's keyring decrypts on read (shared master key required — see §10).
9. **Replay WAL** with running hash check:
   - For each line in `bus/wal.ndjson`, parse `bus.Event`, call `Bus.Replay(event)` (which is the existing `internal/replay/session.go` path).
   - After every Nth event (configurable; default N=100, also at end-of-stream), recompute `ledger.ChainRootHashForSession(new_session_id)` and compare against the expected partial chain root at that point. The expected partial root is itself derived deterministically: source records `wal_checkpoint_hashes: [{seq, partial_chain_root}]` in `bus/wal.index.json` at the same intervals during export.
   - On divergence: abort, emit `session.migrate.divergent` with `{seq, expected_partial_root, actual_partial_root}`. Treat as data corruption. Roll back the destination session via the savepoint.
10. **Restore lobe state**: read `lobe-state/<lobe_id>.json` and call each Lobe's `Restore([]byte) error` method.
11. **Restore lanes**: replay `lanes/snapshot.json` into the destination's lane WAL — the lanes-protocol spec already supports this via `since_seq=0` snapshot ingest.
12. **Final chain-root check** (§6.3 step 1-4).
13. **Idempotency record**: insert `(manifest_sha256, new_session_id, imported_at)` into `migration_imports`.
14. **Audit**: emit `session.migrate.imported` + `session.migrate.completed`.

### 7.3 One-step migrate (`r1 session migrate <id> --to <dest-url>`)

1. POST source's `/api/session/<id>/migrate-out` with the local bearer; stream response body.
2. Pipe directly into POST `<dest-url>/api/session/migrate-in` with the configured remote bearer (loaded from `~/.r1/config.json`'s `remote_daemons[<dest-url>].bearer`).
3. Print the returned `new_session_id` + `chain_root_hash`.
4. On non-2xx from either side: print structured error to stderr, exit 1; the source session remains in `migrating-out` and the operator can retry.

## 8. Error Handling

| Failure | Strategy | User Sees |
|---------|----------|-----------|
| Source mid-turn, no `--force` | Refuse export | "session busy; pass --force to interrupt or wait for turn end" |
| Source has unsigned legacy redactions | Refuse export | "session contains legacy untrusted redactions; cannot migrate" |
| Bundle signature invalid | Refuse import | "bundle signature verification failed" |
| Bundle `chain_root_hash` mismatch post-replay | Roll back; emit divergent event | "chain root hash mismatch; import aborted; see audit row <id>" |
| Skill pack missing on destination | Refuse import; list packs | "missing skill packs: pack-a@<hash>, pack-b@<hash>; install and retry" |
| Master key mismatch (cross-tenant) | Refuse import | "encryption key material does not match source; cross-tenant migration is forbidden in v1" |
| Re-import of same bundle | Return existing session_id (idempotent) | "session already imported as <id>" |
| Destination disk full mid-import | Roll back savepoint; refuse | "import_failed: no space left on device" |
| Network failure mid-stream in `r1 session migrate` | Source remains `migrating-out`; retry safe | "transfer failed; source parked; rerun migrate to retry" |

## 9. Boundaries — What NOT To Do

- **No cross-tenant migration in v1.** The bundle assumes both daemons hold the same master key; cross-tenant requires a KMS-wrapped key envelope spec that is out of scope.
- **No migration of sessions with unsigned legacy redactions** (per `specs/ledger-redaction.md` — legacy untrusted entries cannot prove their content). Refuse with `unsigned_redactions_present`.
- **No partial migration in v1.** Bundle covers the full session from `seq=0` to export point. No event-range subsets, no slice-and-resume.
- **No migration without verified `chain_root_hash` round-trip.** Skipping the post-replay verify is forbidden; the destination MUST recompute and compare.
- **No mutation of source ledger during export.** Export is strictly read-only against the source's content tier.
- **No re-keying during migration.** DEK envelopes are preserved byte-for-byte; the destination decrypts using its own copy of the shared master key.
- **No skill-pack body shipment.** Refs only. Pre-stage packs via existing `r1 skills pack install`.
- **Do NOT amend the v2 tracebundle manifest schema.** Migration uses its own `format: "r1session"` discriminator; tracebundle format remains untouched.
- **Do NOT support same-host re-import of the bundle as a "fork".** Forking is `internal/session/fork.go`'s job and stays separate.
- **Do NOT migrate `migrating-out` sessions.** Source must be in `paused`, `idle`, or `completed` state.

## 10. Encryption / Key Material Trade-off (v1)

Both daemons MUST hold the same 32-byte master key (per `specs/encryption-at-rest.md`'s OS-keyring layout, or via a shared `STOKE_MASTER_KEY_FILE` env). The bundle carries:

- Encrypted memory rows (DEK envelopes preserved).
- Encrypted ledger content blobs (per-entry DEKs preserved).

The destination decrypts using its locally-held master key. If the keys differ then decryption fails on first read and import aborts with `key_material_mismatch`.

A future spec (`session-migration-kms-envelope.md`, NOT in scope) will:
- Wrap session-scoped DEKs with the destination's public KEK using ECIES.
- Allow cross-tenant migration via signed key-transfer ceremony.

This trade-off is documented inline in `docs/operations/session-migration.md` (T27).

## 11. Testing

### T-test1 — Migration bundle format (round-trip)

- [ ] Happy: build a 5-node, 10-edge, 50-event session → export → assert `.r1session` archive contains all 14 expected paths from §5.1; manifest signature verifies via `ed25519.Verify`.
- [ ] Error: tampered manifest (flip 1 byte of `chain_root_hash`) → signature verification fails on import.
- [ ] Edge: empty session (0 nodes, 0 events) → exports cleanly; `chain_root_hash` is empty string; import succeeds and creates an empty destination session.

### T-test2 — Export CLI

- [ ] Happy: `r1 session export <id> -o /tmp/foo.r1session` writes a valid bundle.
- [ ] Error: session mid-turn, no `--force` → exits 1 with "session busy".
- [ ] Edge: `-o -` streams to stdout (operator pipes into ssh).

### T-test3 — Import CLI

- [ ] Happy: `r1 session import /tmp/foo.r1session` → prints new session_id + verified chain root.
- [ ] Error: corrupted gzip → exits 1 "bundle_invalid".
- [ ] Edge: re-import same file → prints existing session_id + "idempotent: true".

### T-test4 — Ledger continuity verification

- [ ] Happy: hash matches post-replay → import flips state to idle.
- [ ] Error: synthetically corrupt one node in `ledger/chain.ndjson` → hash mismatches → 422 + audit row written.
- [ ] Edge: WAL has zero events but ledger has nodes → expected chain root is from the snapshot alone; matches.

### T-test5 — Daemon-to-daemon HTTP

- [ ] Happy: 2 in-process test daemons; POST migrate-out → pipe into migrate-in → 201 with new_session_id.
- [ ] Error: dest daemon missing skill pack → 422 with missing-packs list.
- [ ] Edge: bearer of wrong tenant → 403 cross_tenant_forbidden.

### T-test6 — One-step migrate

- [ ] Happy: `r1 session migrate <id> --to http://dest:8080` → prints new session_id; source flips to `migrated-out`.
- [ ] Error: dest unreachable → exits 1, source remains `migrating-out`, retryable.

### T-test7 — Active-session migration safety

- [ ] Happy: session in `idle` → migrate succeeds.
- [ ] Error: session with in-flight `tool_use` block, no `--force` → refused.
- [ ] Edge: `--force` mid-tool → tool result is appended to bundle (the export waits for the quiet point but at most 5s); orphaned tool_use is forbidden per RT-CANCEL-INTERRUPT.md.

### T-test8 — Replay divergence detection

- [ ] Force a divergence at event 47 by altering one ledger node post-export but pre-import → `session.migrate.divergent` event emitted with `divergent_at_seq=47`.

### T-test9 — Skill-pack continuity

- [ ] Happy: dest has all packs → import succeeds.
- [ ] Error: dest missing 2 packs → 422 lists both.
- [ ] After `r1 skills pack install` of the missing packs → re-import succeeds.

### T-test10 — Memory continuity + encryption

- [ ] Happy: shared master key, 100 encrypted memory rows → import decrypts correctly on read.
- [ ] Error: destination has a different master key → import aborts with `key_material_mismatch`.

### T-test11 — Integration: 1000-turn round trip

- [ ] Two daemons; seed source with 1000 turns; migrate; assert: destination's `chain_root_hash` byte-equals source's; replaying any Lobe with the same deterministic input on destination yields the same Note IDs as source; total wall-clock <60s on local network for a ≤100MB bundle.

### T-test12 — Idempotency

- [ ] Re-importing the same bundle returns the existing `new_session_id`; no duplicate ledger nodes, no duplicate memory rows.

## 12. Acceptance Criteria

- WHEN a 1000-turn session is exported and a 100MB bundle is transferred over a local network THE SYSTEM SHALL complete `r1 session migrate` end-to-end in <60s.
- WHEN a bundle is imported THE SYSTEM SHALL produce a destination session whose ledger nodes (id, content_commitment) byte-equal the source's, modulo timestamps that are themselves part of the canonical hash via content addressing.
- WHEN the same bundle is imported a second time THE SYSTEM SHALL return the existing `new_session_id` (idempotent — HTTP 200, not 201).
- WHEN the post-replay `chain_root_hash` does not match the manifest's THE SYSTEM SHALL abort the import, write an audit row, and emit a `session.migrate.divergent` bus event.
- WHEN the source session is mid-`tool_use` AND `--force` is not passed THE SYSTEM SHALL refuse to export with status 409 `session_busy`.
- WHEN the destination daemon is missing any skill pack referenced in the bundle THE SYSTEM SHALL refuse to import with 422 `missing_skill_packs` and list the missing packs.
- WHEN cross-tenant migration is attempted THE SYSTEM SHALL refuse with 403 `cross_tenant_forbidden`.
- WHEN signature verification fails THE SYSTEM SHALL refuse to import with 400 `bundle_invalid` and emit zero ledger writes.

## 13. Implementation Checklist (28 items — self-contained)

### Format + signing

1. [ ] **T1 — Define `.r1session` archive layout.** Write `internal/migration/bundle.go` declaring the `Manifest` struct (fields per §5.2), constants for the 14 archive paths (per §5.1), and helpers `WriteBundle(w io.Writer, src BundleSource) error` + `ReadBundle(r io.Reader) (*Manifest, *tar.Reader, error)`. Tar entries are emitted in a fixed order; manifest + signature are last. NO 3rd-party deps; stdlib `archive/tar` + `compress/gzip` + `encoding/json` only. Unit test: `TestBundle_RoundTrip` builds a bundle from fixture, reads it back, asserts field equality + archive-path completeness.

2. [ ] **T2 — Add migration-manifest signing.** Extend `internal/migration/bundle.go` with `SignManifest(m *Manifest, signer ed25519.PrivateKey) ([]byte, error)` and `VerifyManifest(m *Manifest, pub ed25519.PublicKey) error`, both delegating the canonical body to `ledger.CanonicalManifestSignBody("r1session", 1, m.SourceSessionID, m.ChainRootHash, m.ExportedAt, m.Signer)`. The signing key is the same Ed25519 key returned by `internal/crypto/keyring.GetRedactionSigner` — DO NOT introduce a new key. Unit test: `TestSignAndVerify_Manifest` — sign with key A, verify succeeds; verify with key B fails; tamper with one byte of `chain_root_hash` after signing → verify fails.

### Source-side export

3. [ ] **T3 — `cmd/r1/session_export_cmd.go`.** New CLI subcommand `r1 session export <id> [-o file] [--force] [--park]`. Wires to local daemon via `internal/apiclient` (loopback bearer). Streams the bundle to `-o` file or stdout (`-o -`). Flag-parsing idiom matches `cmd/r1/sessions.go`. Test: `TestSessionExportCmd_HappyPath` against an in-process test daemon.

4. [ ] **T4 — Quiet-point latch on the source.** Extend `internal/server/sessionhub/sessionhub.go` with `BeginMigrateOut(id string, force bool) error` and `EndMigrateOut(id string, parked bool) error`. The Begin call validates `Session.State` is in {`paused`, `idle`, `completed`} (or force) and flips to `"migrating-out"`. The agent loop's existing `MidturnCheckFn` is taught to return `loop.Yield` when state is `"migrating-out"` — modify `internal/agentloop/loop.go`'s midturn check to consult `SessionHub.IsMigrating(id)`. Unit test: spawn a session with a fake tool loop, call `BeginMigrateOut(force=false)` mid-turn → returns error; after `Pause()` → returns nil.

5. [ ] **T5 — Pre-export checkpoint.** Before bundle assembly, call `internal/checkpoint.WriteCheckpoint(repo, "pre-migrate-export")`. Persist the checkpoint path inside the bundle as `checkpoint/pre-export.json`. On export failure or operator Ctrl-C, the source session is restorable via `r1 sessions inspect <CP>`. Unit test: synthesise an export failure (disk full) → assert the checkpoint exists on disk and `r1 sessions list` shows it.

6. [ ] **T6 — Streaming bundle assembly.** Implement `cmd/r1-server/migrate_out.go` `serveMigrateOut(d *DB)` HTTP handler at `POST /api/session/{id}/migrate-out`. Reuses `cmd/r1-server/tracebundle.go`'s streaming pattern verbatim for ledger/chain.ndjson + ledger/edges.ndjson + ledger/content/*. Adds: `bus/wal.ndjson` from `~/.r1/<session>/bus.wal`, `memory/rows.ndjson` from the memory store with `scope_target = "session:<id>"`, `skills/pack-refs.json` from `cortex.Workspace.SkillRegistry`, `lobe-state/*` from each Lobe's `Snapshot()`, `lanes/snapshot.json` from `r1.lanes.get(since_seq=0)`. Buffer cap 64 KB. `Content-Type: application/gzip`, `Content-Disposition: attachment; filename="<session_id>.r1session"`. Unit test: `TestMigrateOut_StreamingShape` asserts archive paths + manifest fields + signature verifies.

7. [ ] **T7 — Partial-chain-root checkpoints in WAL index.** During WAL emission, every 100 events record `{seq, partial_chain_root: ChainRootHashForSession_at_that_seq}` in `bus/wal.index.json.wal_checkpoint_hashes[]`. The "at that seq" is computed by walking the ledger up to the ts/seq corresponding to that WAL event. This enables T14's incremental divergence detection during replay. Unit test: 500-event session with checkpoints at seq=100,200,300,400 → all four roots match the destination's incremental recompute.

### Destination-side import

8. [ ] **T8 — `cmd/r1/session_import_cmd.go`.** New CLI subcommand `r1 session import <file>`. Opens the file, streams into `POST /api/session/migrate-in` on the local daemon, prints `{new_session_id, chain_root_hash}`. Test: round-trip via in-process daemons.

9. [ ] **T9 — Migrate-in handler.** Implement `cmd/r1-server/migrate_in.go` `serveMigrateIn(d *DB)` at `POST /api/session/migrate-in`. Pipeline: gzip-tar-decode → buffer manifest → verify signature → idempotency check → schema-version check → skill-pack check → allocate dest session via `SessionHub.Create(..., State: "migrating-in")` → hydrate ledger (chain, edges, content) → hydrate memory → replay WAL with incremental hash check → restore lobe state → restore lanes → final chain-root verify → flip state to `idle`. Wrap in a SQLite savepoint for transactional rollback. Idempotency table created in T10. Unit test: `TestMigrateIn_HappyPath` + `TestMigrateIn_ChainRootMismatch`.

10. [ ] **T10 — Idempotency table.** Add migration `001_migration_imports.sql` creating `migration_imports (manifest_sha256 TEXT PRIMARY KEY, new_session_id TEXT NOT NULL, imported_at TEXT NOT NULL, source_session_id TEXT NOT NULL, source_host TEXT NOT NULL)`. Migration runs via the existing SQLite migration runner in `cmd/r1-server/db.go`. Test: re-import same bundle → returns existing row.

11. [ ] **T11 — WAL replay path.** Extend `internal/replay/session.go` with `ReplayBundle(bundle io.Reader, destSessionID string, onProgress func(seq uint64, partialRoot string)) error`. Calls existing `bus.Replay` per event. After every 100 events (and at EOF) invokes `onProgress` for the incremental hash check. Unit test: 1000-event WAL replays in <1s on dev hardware, all incremental roots match.

12. [ ] **T12 — Skill-pack pre-flight.** In `serveMigrateIn`, after schema-version check, iterate `manifest.SkillPackRefs[]` and call `internal/skill.Registry.HasPack(pack_id, content_hash)`. Missing packs → 422 with the full list. NO automatic fetching in v1 — operator must `r1 skills pack install`. Test: synthesise a missing pack → 422 body matches schema.

### Verification + audit

13. [ ] **T13 — Final chain-root verify.** In `serveMigrateIn` after WAL replay, compute `ledger.ChainRootHashForSession(destSessionID)`; compare with `manifest.ChainRootHash`. Mismatch → rollback savepoint, emit `session.migrate.divergent` event with `{expected, actual, source_session_id, dest_session_id, divergent_at_seq: 0}`. Insert audit row via `internal/audit.Write(...)`. Test: tamper with one node after import (between hydrate and verify steps) → mismatch detected.

14. [ ] **T14 — Incremental replay divergence detection.** During WAL replay (T11's `onProgress`), recompute the destination's current `chain_root_hash` and compare with `wal_checkpoint_hashes[i].partial_chain_root`. On divergence: abort, emit `session.migrate.divergent` with `{seq: i, expected, actual}`, return 422. Test: corrupt event 47 → divergence detected at the seq-100 checkpoint.

### Daemon-to-daemon convenience

15. [ ] **T15 — Auth gating.** Both `/api/session/{id}/migrate-out` and `/api/session/migrate-in` go through `internal/server/auth_middleware.AuthRequired`. Same-tenant check: parse the bearer's tenant claim; reject 403 if `manifest.TenantID != bearer.TenantID`. Test: cross-tenant bearer → 403.

16. [ ] **T16 — `r1 session migrate <id> --to <dest-url>`.** New `cmd/r1/session_migrate_cmd.go`. Reads `~/.r1/config.json`'s `remote_daemons[<dest-url>].bearer`. Streams local `migrate-out` to remote `migrate-in` over a single piped HTTP request. Prints the dest's `new_session_id`. On dest failure, prints structured error to stderr; source remains in `migrating-out` for retry. Test: 2 in-process daemons; happy path returns new session id; dest down → source stays parked.

### State + safety

17. [ ] **T17 — Active-session migration guard.** Extend `Session.State` machine (in `internal/server/sessionhub/session.go`) with `migrating-out`, `migrated-out`, `migrating-in`. Add `Session.IsMigratable() (bool, string)` returning `(false, "session_busy")` for `running` / `tool-in-flight` states. The agent loop's `MidturnCheckFn` consults this; on `migrating-out` the loop yields after the current `assistant`+`tool_result` pair. Test: state machine transitions, no orphaned tool_use messages.

18. [ ] **T18 — Refuse export on unsigned legacy redactions.** Before bundle assembly, scan `ledger.ListNodesForSession(id)` for any node with `Redaction != nil && Redaction.Signature == ""` (legacy untrusted per `specs/ledger-redaction.md`). If any → return 409 `unsigned_redactions_present` with the node IDs. Test: fixture with 1 legacy redaction → export refused.

### Memory + skills

19. [ ] **T19 — Skill-pack continuity contract.** `internal/skill.Registry.HasPack(pack_id, content_hash string) bool` already exists per `specs/oss-hub.md`; no new code, just call it from T12. Document the contract in `internal/migration/bundle.go` doc comment.

20. [ ] **T20 — Memory continuity + key-material trade-off doc.** Memory rows are written/read via `internal/memory.Store.ListForSession(session_id)` and `Insert`. Encryption envelopes (per `specs/encryption-at-rest.md`) are preserved byte-for-byte in `memory/rows.ndjson` — the content column is already-encrypted bytes. Destination decrypts on read using its locally-held master key. If decryption fails on first read → import aborts with `key_material_mismatch`. Document the "shared master key required in v1" trade-off prominently in T27's runbook. Test: same-key happy path; different-key error path.

### Tests

21. [ ] **T21 — Unit suite.** One `_test.go` per new file: `bundle_test.go`, `migrate_out_test.go`, `migrate_in_test.go`, `session_export_cmd_test.go`, `session_import_cmd_test.go`, `session_migrate_cmd_test.go`. Each covers happy + error + edge per §11.

22. [ ] **T22 — Integration: 1000-turn round-trip.** New `integration/migrate_roundtrip_test.go` spawns 2 in-process daemons via `internal/server/serve.go` test helpers. Seeds source with 1000 deterministic turns. Migrates. Asserts: destination's `ChainRootHashForSession` byte-equals source's; each Lobe's `Snapshot()` on dest byte-equals source; total wall-clock <60s for ≤100MB bundle. Race-detector enabled.

23. [ ] **T23 — Determinism property test.** Build the same source bundle 3 times in a row (no other changes); assert all 3 bundles' manifest signatures verify and all 3 produce the same `chain_root_hash`. This catches accidental non-determinism in the snapshot pipeline.

24. [ ] **T24 — Idempotency property test.** Import the same bundle 5 times; assert exactly 1 destination session is created; ledger node count is constant after the first import.

### Audit + observability

25. [ ] **T25 — Bus events.** Add three new `bus.EventType` constants to `internal/bus/bus.go`: `EvtSessionMigrateExported`, `EvtSessionMigrateImported`, `EvtSessionMigrateDivergent`. Document each with the same comment style as existing events. Wire emission from `serveMigrateOut` + `serveMigrateIn`. Test: tap the bus during a round-trip and assert all three events appear in order.

26. [ ] **T26 — Audit rows.** Emit `internal/audit.Write` rows for every migrate-in + every divergent + every refused (busy / cross-tenant / missing-packs). Schema: `{action, actor, source_session_id, dest_session_id, source_host, dest_host, chain_root_hash, outcome, detail}`. Test: divergent path leaves exactly one audit row; idempotent re-import leaves zero new audit rows.

### Docs

27. [ ] **T27 — Operator runbook.** New `docs/operations/session-migration.md` covering: (a) export workflow (`r1 session export`, the `--force` semantics, the `--park` flag); (b) transfer workflow (scp, ssh tunnel, direct daemon-to-daemon `r1 session migrate`); (c) import workflow + skill-pack pre-staging; (d) verifying `chain_root_hash` post-import via `r1 ledger verify --session <new_id>`; (e) troubleshooting hash mismatches (corruption vs key material vs schema drift); (f) key-material requirements + the cross-tenant deferral; (g) audit query examples for the migrate events. Include a worked example: 1000-turn session, export, scp, import, verify.

28. [ ] **T28 — API reference update.** Append the two new endpoints to `specs/r1-api.yaml` (OpenAPI 3.0). Schemas: `MigrationBundle` (gzip body), `MigrationManifest`, `MigrateInResponse`, `MigrationError`. CI lint via `npx @redocly/openapi-cli lint` must pass.

## 14. Sequencing

- T1 → T2 (format + signing) is the foundation.
- T3-T7 (export) and T8-T12 (import) parallelize after T2.
- T13 + T14 (verification) depend on T2 + T11.
- T17 + T18 (safety guards) parallelize with T3-T7.
- T21-T24 (tests) gate on the corresponding source items.
- T27-T28 (docs) gate on T22 passing.

## 15. Out of Scope (Future Specs)

- Cross-tenant migration with KMS-wrapped key envelopes.
- Partial / event-range migration ("migrate seq 500..1000 only").
- Live multi-daemon session ownership (a session simultaneously hot on 2 hosts) — that's `specs/cloudswarm-protocol.md`.
- Migrating sessions across `schema_version` boundaries — when v2 lands, a separate migrator must convert v1 bundles, not this code.
- Pack-registry auto-fetch on import — pre-stage manually in v1.

## 16. References

- `specs/r1-server-ui-v2-handlers-and-routes.md` — tracebundle v2 export (T15-T17), `chain_root_hash` semantics, signing body.
- `specs/r1d-server.md` — `SessionHub` design, bearer auth.
- `specs/cortex-core.md` — Lobe `Snapshot()` / `Restore()` contract.
- `specs/lanes-protocol.md` — `r1.lanes.get(since_seq=0)` snapshot semantics.
- `specs/encryption-at-rest.md` — master-key derivation, redaction signer reuse.
- `specs/ledger-redaction.md` — legacy-unsigned-redactions semantics.
- `specs/research/raw/RT-CANCEL-INTERRUPT.md` — orphaned-tool_use prohibition.
- `internal/ledger/store_session.go` — `CanonicalManifestSignBody`, `ChainRootHashForSession`.
- `internal/checkpoint/checkpoint.go` — pre-export savepoint.
- `cmd/r1-server/tracebundle.go` — streaming archive pattern to reuse.
- `cmd/r1-server/import.go` — idempotent re-verification pattern to reuse.

## 17. Research Notes

Industry practice (per checkpoint-restore research surveyed May 2026) classifies migration into **coordinated checkpointing with message logging**: source quiesces, takes a coordinated snapshot, ships the snapshot + message log to the destination, destination restores + replays the log, both sides verify continuity. This spec follows that pattern: the quiet-point latch is the coordinator, the bus WAL is the message log, and the `chain_root_hash` round-trip is the continuity proof. Modern event-sourced systems (e.g. EventSourcingDB, Trillian transparency-log) confirm the merkle-root-of-replayed-events pattern as the canonical continuity check — any single-bit corruption cascades into a different root, giving us strong tamper detection without per-event signatures.

Key references:
- EventSourcingDB "Proving Without Revealing" (2025-11-17): predecessor-hash chain + Merkle root as integrity proof.
- Trillian transparency-log verifiable data structures: hash continuity as canonical replay-verification primitive.
- Distributed-systems checkpoint/restore (Eunomia, 2025-05-11): coordinated-checkpoint + message-logging is the standard for cross-host process migration.
