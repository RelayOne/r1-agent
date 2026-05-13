# Cross-Machine Session Migration

## Overview

R1's sessions are normally bound to the daemon process that started them:
journal, bus WAL, ledger entries, lobe state, memory rows, and skill packs
all live on the host where `r1 serve` (or the legacy `r1-server`) was
launched. The `.r1session` migration bundle adds the ability to **move a
session to another machine** with verified ledger continuity.

The bundle format is a superset of the `.tracebundle` export — it carries
the same per-session ledger chain + edges + content blobs PLUS the live bus
WAL, scoped memory rows, deterministic Lobe snapshots, lane state, a
pre-export checkpoint, and a manifest signed via the same canonical body
(`ledger.CanonicalManifestSignBody`) tracebundles use. Downstream verifiers
that already check tracebundle signatures verify migration bundles
unchanged.

Spec: [`specs/cross-machine-session-migration.md`](../../specs/cross-machine-session-migration.md)

## When to use

- Moving a long-running session between two of your own machines (laptop ↔
  workstation, dev box ↔ staging host).
- Recovering a session after a host failure when the bundle was previously
  exported (the destination treats the bundle as authoritative).
- Forensic replay on a different machine while preserving the original
  daemon's state read-only (`--park`).

## When NOT to use (v1 boundaries)

- **Cross-tenant.** Both daemons MUST hold the same master key material
  (see [Key material](#key-material) below). Cross-tenant migration is
  blocked by the bundle's tenant claim — a future spec
  (`session-migration-kms-envelope.md`) will lift this with KMS-wrapped key
  envelopes.
- **Partial / event-range migrations.** The bundle covers the full session
  from `seq=0` to export point. No subset slices.
- **Multi-host live ownership.** A migrated session is owned by the
  destination after import. Multi-host concurrent ownership is
  `specs/cloudswarm-protocol.md`'s domain, not C1's.

## CLI verbs

Three subcommands live under `r1 session <verb>` — note the singular form
(distinct from `r1 sessions`, which is the read-only checkpoint browser):

```
r1 session export <id> [-o file] [--force] [--park]
r1 session import <file>
r1 session migrate <id> --to <dest-url>
```

### Export workflow

```bash
# Export to <id>.r1session in the current directory:
r1 session export sess-abc123

# Stream to stdout (useful with ssh pipes):
r1 session export sess-abc123 -o - | ssh dest 'r1 session import -'

# Force interrupt a mid-turn session at the next quiet point:
r1 session export sess-abc123 --force

# Leave the source session parked (read-only, migrated-out) after success:
r1 session export sess-abc123 --park
```

`--force` waits at most 5 seconds for the agent loop's mid-turn observer
to yield after the current `assistant`/`tool_result` pair completes (no
orphaned `tool_use` per RT-CANCEL-INTERRUPT). On timeout the export
returns 409 `session_busy`.

### Transfer workflow

Operators have three options:

1. **`scp`** the file. Plain disk-to-disk copy. Idempotent: re-importing the
   same bundle returns the existing destination session id with HTTP 200
   and `idempotent:true`.
2. **`ssh` tunnel.** Stream stdout from export through ssh, pipe stdin into
   the destination's `r1 session import -`. No intermediate file.
3. **`r1 session migrate <id> --to <dest-url>`.** One-step daemon-to-daemon
   piping. The destination's bearer is loaded from
   `~/.r1/config.json`'s `remote_daemons[<dest-url>].bearer`. On dest-side
   failure, the source remains in `migrating-out` for retry.

### Import workflow

```bash
# Local file:
r1 session import /tmp/sess-abc123.r1session

# Streaming from another command:
ssh source-host 'r1 session export sess-abc123 -o -' | r1 session import /dev/stdin
```

On success, the import prints:

```json
{
  "new_session_id": "migrated-1715472330000000000",
  "chain_root_hash": "<sha256 hex>",
  "node_count": 27,
  "wal_replayed": 100,
  "verified": true
}
```

If the destination is missing any skill pack referenced by the bundle, the
import refuses with HTTP 422 `missing_skill_packs` listing the missing
`{pack_id, content_hash}` pairs. Pre-stage with:

```bash
r1 skills pack install <pack_id>@<content_hash>
```

then re-run the import.

## Verifying chain-root continuity post-import

The import path re-derives `chain_root_hash` over the destination's freshly-
hydrated ledger and compares against the manifest's. On mismatch the import
aborts with HTTP 422 `chain_root_hash_mismatch` and emits a
`session.migrate.divergent` bus event with `{expected, actual,
divergent_at_seq}`. An audit row lands in the daemon's audit table for
operator inspection.

To re-verify manually after the fact:

```bash
r1 ledger verify --session <new_session_id>
```

The check recomputes the chain root and compares against the manifest's
recorded value; any divergence indicates post-import corruption (rare,
usually caused by a partial restore of the destination's data directory).

## Troubleshooting hash mismatches

| Symptom | Likely cause | Fix |
|--------|--------------|-----|
| `chain_root_hash_mismatch` post-replay | Bundle bytes altered between export and import | Re-export from the source; check disk integrity on the transfer medium |
| `bundle_invalid` (signature) | Destination master key differs from source's | See [Key material](#key-material); v1 requires same master key |
| `bundle_invalid` (gzip) | Truncated transfer | Re-run `scp` / re-pipe with `--checksum` enabled |
| `schema_version_unsupported` | Bundle from a future r1 version | Upgrade the destination daemon; see Release Notes |
| `key_material_mismatch` | Destination's keyring lacks the source's master key | Provision via `STOKE_MASTER_KEY_FILE` or `~/.r1/master.key`; see encryption-at-rest spec |

## Key material

Both daemons MUST agree on a single 32-byte master key. The v1 bundle path
reuses the redaction signing key (Ed25519, derived from the master key per
`encryption-at-rest.md` HKDF info `"r1-redaction-signer"`) to sign the
manifest. The destination's verifier loads the same Ed25519 key from its
local keyring; if the keys differ, manifest verification fails closed and
the import returns 400 `bundle_invalid`.

Memory rows + ledger content blobs travel byte-for-byte with their
encrypted DEK envelopes intact. The destination's keyring decrypts on read
with its own copy of the master key. If decryption fails, the dashboard
surfaces `key_material_mismatch` and the row is unreadable on the
destination — re-provision the master key and retry.

A future spec (`session-migration-kms-envelope.md`) lifts the shared-key
requirement via signed key-transfer ceremonies for cross-tenant moves.
That work is out of scope for v1.

## Audit query examples

The three new bus events land in `~/.r1/events.db` (or the daemon's
configured event log) under the migration namespace. Sample queries:

```sql
-- Every migration that landed in the last hour:
SELECT instance_id, ts, event_type, raw
FROM session_events
WHERE event_type IN (
  'session.migrate.exported',
  'session.migrate.imported',
  'session.migrate.divergent'
)
AND ts > datetime('now', '-1 hour')
ORDER BY ts DESC;

-- Every divergent event (data corruption alarms):
SELECT instance_id, ts, json_extract(raw, '$.payload') AS payload
FROM session_events
WHERE event_type = 'session.migrate.divergent'
ORDER BY ts DESC;
```

## Worked example: 1000-turn round-trip

```bash
# Source host (laptop):
r1 session export sess-long-running -o sess-long-running.r1session
# wrote sess-long-running.r1session (4194304 bytes)

# Transfer:
scp sess-long-running.r1session workstation:/tmp/

# Destination host (workstation):
r1 session import /tmp/sess-long-running.r1session
# {
#   "new_session_id": "migrated-1715472330000000000",
#   "chain_root_hash": "a3f5...c1",
#   "node_count": 1000,
#   "wal_replayed": 100,
#   "verified": true
# }

# Verify:
r1 ledger verify --session migrated-1715472330000000000
# chain root: a3f5...c1 ✓
```

Total wall-clock <60 seconds for a ≤100MB bundle over a local network is
the spec's acceptance criterion (`specs/cross-machine-session-migration.md`
§12). The integration test `internal/migration/integration_roundtrip_test.go`
(`-tags integration_session_migrate`) exercises this in CI.
