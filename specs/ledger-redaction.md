<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-21 -->
<!-- DEPENDS_ON: (none — refactors existing internal/ledger/) -->
<!-- BUILD_ORDER: 24 -->

# Ledger Redaction — Two-Level Merkle Commitment Refactor

## 1. Overview

The Stoke ledger at `internal/ledger/` stores reasoning nodes with a
content-addressed identifier computed by `computeID(n)` over
`content || created_at || created_by || type` and a Merkle-style
`ParentHash` chain that points at the full canonical JSON of the prior
node (`hashNodeForChain`). Today every byte of user content is part of
the identity of the node and part of the hash consumed by the next
node's `ParentHash`. If a retention policy wipes any node's content,
its ID recomputes, every downstream `ParentHash` no longer matches,
and the Merkle chain fails verification. This is incompatible with
GDPR/CCPA right-to-erasure and with any "prompts-and-responses wipe
after session" retention policy.

The target state implements correction 3 from
`r1-server-research-validated.md` (the AWS QLDB / C2PA pattern): a
**two-level commitment**. Each node is split across two tiers — a
permanent `chain/` tier holding a structural header plus a salted
content commitment (`SHA256(salt || canonical_content)`), and an
erasable `content/` tier holding the salt and the raw canonical
content. The node ID is derived only from the structural header and
the content commitment, so wiping the content tier leaves the ID,
every downstream `ParentHash`, and the entire chain byte-identical.
Crypto-shredding (destroying salt + content) is indistinguishable to
downstream verifiers from normal operation; only the explicit
redaction event in the chain tier signals to a human that content
once existed there.

## 2. The Tamper-Evidence Invariant

The invariant is: **operator deletion of user content must leave
`Store.Verify()` passing**. Today's single-tier design fails this
invariant — ID is a function of content, so content wipe equals
identity change equals chain break. Two-level commitment preserves
the invariant because (a) the content commitment is fixed at write
time before the salt is ever destroyed, (b) the commitment appears in
the permanent chain tier, and (c) the node ID is
`SHA256(canonical(header) || content_commitment)` — a function only
of the permanent tier. This is the mechanism AWS QLDB uses for its
digest proofs under the GDPR "erasure of committed data" carve-out,
and the same mechanism C2PA uses for redactable content credentials:
the commitment is permanent, the pre-image is erasable, and the
chain survives the pre-image's destruction.

## 3. Schema + Disk Layout Migration

```
<repo>/.stoke/ledger/
    chain/{id}.json          permanent tier: header + content_commitment + (optional) redaction marker
    content/{id}.json        erasable tier: salt + canonical content
    edges/{from}-{to}-{type}.json   unchanged — edges carry no content sensitivity
    nodes/{id}.json          LEGACY single-tier files; read-only shadow during migration window
    index.sqlite             unchanged path; schema gains columns (see §4)
```

Edges are untouched. The `nodes/` directory is preserved **read-only**
for one release cycle as a legacy fallback; after that cycle it is
removed.

Chain-tier record (`chain/{id}.json`) canonical shape:

```json
{
  "id": "prompt-ab12cd34",
  "header": {
    "version": 2,
    "type": "prompt",
    "parent_hash": "sha256-hex-of-previous-node-id",
    "timestamp": "2026-04-21T12:00:00.000Z",
    "trace_id": "trace-...",
    "seq": 17,
    "non_sensitive_meta": { "mission_id": "mis-...", "created_by": "cto-0" }
  },
  "content_commitment": "sha256-hex-of-salt-concat-content",
  "legacy_id": "prompt-oldid01",        // only present on migrated nodes
  "redaction": null                      // or {…} after Redact(); see §6
}
```

Content-tier record (`content/{id}.json`) canonical shape:

```json
{
  "id": "prompt-ab12cd34",
  "salt": "base64(16 random bytes)",
  "content": { ... original canonical content JSON ... }
}
```

## 4. Type Changes in `internal/ledger/`

New public types in `ledger.go`:

```go
// StructuralHeader is the permanent, non-sensitive portion of a node.
// Every field here survives redaction.
type StructuralHeader struct {
    Version          int                    `json:"version"`
    Type             string                 `json:"type"`
    ParentHash       string                 `json:"parent_hash,omitempty"`
    Timestamp        time.Time              `json:"timestamp"`
    TraceID          string                 `json:"trace_id,omitempty"`
    Seq              uint64                 `json:"seq"`
    NonSensitiveMeta map[string]any         `json:"non_sensitive_meta,omitempty"`
}

// WriteOptions is passed to Store.WriteNode for the two-tier write.
type WriteOptions struct {
    // NonSensitiveMeta are fields the operator explicitly declares safe
    // to keep in the permanent tier after redaction.
    NonSensitiveMeta map[string]any
    // Sensitivity overrides the default sensitivity for the node type
    // (see NonSensitiveTypes). When false, skip content-tier write.
    Sensitive *bool
}
```

`Node` struct (edits to existing struct in `ledger.go`):

```go
type Node struct {
    ID                NodeID          `json:"id"`
    Type              string          `json:"type"`
    SchemaVersion     int             `json:"schema_version"`
    CreatedAt         time.Time       `json:"created_at"`
    CreatedBy         string          `json:"created_by"`
    MissionID         string          `json:"mission_id,omitempty"`
    ParentHash        string          `json:"parent_hash,omitempty"`
    Content           json.RawMessage `json:"content,omitempty"`          // lazy-loaded; nil when redacted
    ContentCommitment string          `json:"content_commitment,omitempty"` // read-only once set
    Redacted          bool            `json:"redacted,omitempty"`
    LegacyID          string          `json:"legacy_id,omitempty"`
}
```

`Content` becomes **lazy-loaded** from the content tier; `ReadNode`
populates it when the content file exists and leaves it `nil` with
`Redacted=true` otherwise. `ContentCommitment` is set at write time
and never mutated.

SQLite index schema additions (via migration):
- `nodes.content_commitment TEXT NOT NULL DEFAULT ''`
- `nodes.redacted INTEGER NOT NULL DEFAULT 0`
- `nodes.legacy_id TEXT NOT NULL DEFAULT ''`
- `nodes.header_json TEXT NOT NULL DEFAULT ''` (full header for Verify)

## 5. API

```go
// WriteNode writes both tiers atomically. Replaces the current
// single-tier WriteNode. Computes salt, commitment, header, ID.
func (s *Store) WriteNode(n *Node, opts WriteOptions) error

// ReadNode returns a fully-populated node when content tier exists.
// Returns a node with Content=nil and Redacted=true when the content
// tier was wiped. Returns an error only when the chain tier is missing
// or unparseable.
func (s *Store) ReadNode(id NodeID) (*Node, error)

// Redact signs a redaction event, writes it into the chain tier's
// redaction field, and wipes the content tier for id. The chain file
// is otherwise byte-identical. The node's ID does not change.
func (s *Store) Redact(ctx context.Context, id NodeID, reason string, signer ed25519.PrivateKey) error

// Verify walks the chain tier and verifies:
//   recomputed := SHA256(canonical(header) || content_commitment)
//   recomputed == node_id
//   header.parent_hash == previous_node_id
// MUST succeed on a fully-redacted chain.
func (s *Store) Verify(ctx context.Context) error
```

Node-creation algorithm (pseudocode, per the user-paste):

```
Node creation:
  salt = random(16)
  content_commitment = SHA256(salt || canonical_content)
  structural_header = {version, type, parent_hash, timestamp, trace_id, seq, non_sensitive_meta}
  node_id = SHA256(canonical(structural_header) || content_commitment)

  chain_store.put(node_id, structural_header, content_commitment)  // permanent
  content_store.put(node_id, salt, canonical_content)              // erasable

Redaction:
  1. Sign a redaction event (before destroying anything)
  2. Destroy salt + content from content_store (crypto-shredding)
  3. Mark chain_store entry as redacted (informational)
  Result: node_id unchanged. content_commitment unchanged.
  Every downstream parent_hash byte-identical.

Verification (works on redacted chain):
  for each node:
    recomputed = SHA256(canonical(header) || content_commitment)
    assert recomputed == node_id
    assert header.parent_hash == previous_node_id
```

Canonicalisation uses the existing `hashNode`-style canonical JSON
(sorted keys, no insignificant whitespace, RFC3339Nano timestamps).

## 6. Redaction Event Format

```json
{
  "type": "redaction",
  "target_id": "prompt-ab12cd34",
  "reason": "retention_policy:prompts_and_responses:wipe_after_session",
  "redacted_at": "2026-04-21T12:34:56.000Z",
  "signer": "operator|retention-daemon|r1-server",
  "signature": "ed25519-sig-base64..."
}
```

The signed payload is the canonical JSON of the object with
`signature` omitted. Redaction events live **only** in the chain tier
(embedded in `chain/{target_id}.json` under the `redaction` field).
They are themselves content-addressable — an append-only redaction
event node of `type = "redaction"` is also added to the chain tier at
a new node ID, with the event payload as its header's
`non_sensitive_meta`. Because redaction event nodes carry no user
content (content tier never written), they are never subject to
redaction themselves.

## 7. Migration Path

The existing ledger has `<repo>/.stoke/ledger/nodes/{id}.json` files
computed by the old `computeID`. The new ID is derived from a
different pre-image, so **new IDs will differ from old IDs** for
every node. The migration strategy:

1. Detect `nodes/` present AND `chain/` absent at open time; do **not**
   auto-migrate — print a pointer to `stoke ledger migrate`.
2. `stoke ledger migrate [--dry-run]` walks `nodes/` in
   creation-order within each `MissionID`, and for each old node:
   - Constructs `StructuralHeader` from existing fields
     (`Type`, `ParentHash`, `CreatedAt` → `Timestamp`, `CreatedBy`
     + `MissionID` → `NonSensitiveMeta`, `Seq` = creation rank).
   - Computes `content_commitment = SHA256(salt || canonical(old.Content))`.
   - Computes new `node_id = SHA256(canonical(header) || content_commitment)`.
   - Writes `chain/{new_id}.json` with `legacy_id = old.ID`.
   - Writes `content/{new_id}.json` with `salt` + `content`.
   - Rewrites each successor's `header.parent_hash` to point at the
     predecessor's **new** id (the chain re-links during migration —
     expected, one-time).
3. The old `nodes/` directory is left intact as a read-only shadow.
   `Store.ReadNode(id)` accepts either the new ID or the `legacy_id`
   during the transition window; when called with a legacy ID it
   resolves through an in-memory `legacy_id → new_id` map built at
   open time from the chain tier.
4. `Verify()` during the transition window accepts either the
   old-legacy chain hash (over the old `nodes/{id}.json`) or the
   new two-tier chain hash for each node.
5. One release cycle after migration (gated by
   `STOKE_LEDGER_V1=0`), `nodes/` and the legacy-id fallback are
   removed.

`--dry-run` prints the full migration plan (every old→new ID mapping,
every rewritten `parent_hash`) without writing anything.

## 8. Per-Node Sensitivity Flags

Not every node type carries user content. Purely structural nodes
**skip the content-tier write entirely** and live in the chain tier
only. Their `content` field is always empty; there is nothing to
redact.

Canonical sensitivity table:

| Node type                | Tier          | Reason                                     |
|--------------------------|---------------|--------------------------------------------|
| `prompt`                 | chain+content | raw user prompt; retention-sensitive       |
| `response`               | chain+content | LLM output; retention-sensitive            |
| `tool_call`              | chain+content | may contain pasted secrets / file paths    |
| `tool_result`            | chain+content | may contain file contents                  |
| `context_window`         | chain+content | raw context packing; sensitive             |
| `mission`                | chain+content | user-authored goal text                    |
| `decision_internal`      | chain+content | may quote user content                     |
| `decision_repo`          | chain+content | may quote commit messages                  |
| `agree`                  | chain-only    | structural vote; no user content           |
| `dissent`                | chain-only    | references IDs only; no quoted content     |
| `loop`                   | chain-only    | consensus-loop state transition            |
| `skill_loaded`           | chain-only    | skill manifest reference                   |
| `verification_evidence`  | chain-only    | evidence IDs + pass/fail booleans          |
| `redaction`              | chain-only    | signed redaction event (see §6)            |
| `rule_fired`             | chain-only    | supervisor rule outcome                    |
| `convergence_vote`       | chain-only    | vote tally                                 |

Exported as:

```go
// NonSensitiveTypes is the canonical set of node types that skip the
// content-tier write entirely. Callers MAY override via
// WriteOptions.Sensitive.
var NonSensitiveTypes = map[string]bool{
    "agree":                 true,
    "dissent":               true,
    "loop":                  true,
    "skill_loaded":          true,
    "verification_evidence": true,
    "redaction":             true,
    "rule_fired":            true,
    "convergence_vote":      true,
}
```

When a node type is in `NonSensitiveTypes` (or
`opts.Sensitive == &false`), `WriteNode` writes `chain/{id}.json`
with the canonical content embedded verbatim inside
`header.non_sensitive_meta.content`, computes the commitment over an
empty content pre-image (`content_commitment = SHA256(salt || "")`),
and skips `content/{id}.json`. `ReadNode` returns the content from
the header when the content-tier file is absent and `Redacted` is
`false`.

## 9. Implementation Checklist

1. Create `internal/ledger/chain.go` with `StructuralHeader` type and `canonicalHeaderJSON(h StructuralHeader) ([]byte, error)` using the existing canonical-JSON helper.
2. In `chain.go`, add `type chainRecord struct { ID string; Header StructuralHeader; ContentCommitment string; LegacyID string; Redaction *RedactionEvent }` with JSON marshalling.
3. In `chain.go`, add `writeChain(dir, id string, rec chainRecord) error` using `os.WriteFile` with `0o644` and atomic rename.
4. In `chain.go`, add `readChain(dir, id string) (chainRecord, error)`.
5. In `chain.go`, add `listChain(dir string) ([]chainRecord, error)` mirroring `Store.ListNodes`.
6. Create `internal/ledger/content.go` with `type contentRecord struct { ID, Salt string; Content json.RawMessage }`.
7. In `content.go`, add `writeContent(dir, id string, rec contentRecord) error` (atomic rename; permission `0o600`).
8. In `content.go`, add `readContent(dir, id string) (contentRecord, bool, error)` — second return is `false` when the content tier was wiped but chain tier exists.
9. In `content.go`, add `wipeContent(dir, id string) error` using `os.Remove` and verifying absence afterward.
10. Edit `internal/ledger/store.go`: add `chainDir`, `contentDir` to `Store` struct; create both in `NewStore`.
11. Edit `store.go`: keep `nodesDir` as the **legacy** read-only dir for backward compat.
12. Edit `store.go`: replace `WriteNode(n Node) error` signature with `WriteNode(n *Node, opts WriteOptions) error`; keep a thin `WriteNodeLegacy` shim for one release cycle during rollout (callers migrate off it).
13. In the new `WriteNode`, generate `salt = random 16 bytes` via `crypto/rand`; error out on short read.
14. In the new `WriteNode`, compute canonical content bytes via the existing `canonicalJSON` helper applied to `n.Content`; for non-sensitive types use `[]byte("")` as pre-image.
15. In the new `WriteNode`, compute `content_commitment = hex(sha256(salt || canonical_content))`.
16. In the new `WriteNode`, assemble `StructuralHeader{Version:2, Type:n.Type, ParentHash:n.ParentHash, Timestamp:n.CreatedAt, TraceID:…, Seq:…, NonSensitiveMeta:opts.NonSensitiveMeta}`; inject `created_by` + `mission_id` into `NonSensitiveMeta` when caller didn't.
17. In the new `WriteNode`, compute `n.ID = "{type}-" + hex(sha256(canonical(header) || content_commitment))[:8]` matching the existing 8-char suffix convention.
18. In the new `WriteNode`, call `writeChain(s.chainDir, n.ID, rec)` first; if that succeeds and the type is sensitive, call `writeContent(s.contentDir, n.ID, …)`; if content write fails, roll back the chain write (delete the chain file) so we never leak a commitment without its pre-image.
19. Edit `store.go`: replace `ReadNode(id string) (Node, error)` with `ReadNode(id string) (*Node, error)`; it reads the chain tier first; for sensitive types it then attempts the content tier and sets `Redacted=true` on ENOENT.
20. Edit `store.go`: `ReadNode` falls back to the legacy `nodes/{id}.json` path when chain tier is absent AND `id` matches a legacy ID in the in-memory map built at open time.
21. Edit `store.go`: add `openLegacyIndex()` called from `NewStore` that scans `chain/` for records with non-empty `legacy_id` and builds the `legacy_id → new_id` map.
22. Create `internal/ledger/redact.go` with `type RedactionEvent struct { Type, TargetID, Reason, Signer, Signature string; RedactedAt time.Time }`.
23. In `redact.go`, add `signRedaction(ev RedactionEvent, key ed25519.PrivateKey) (RedactionEvent, error)` that serialises the event sans `Signature`, signs, and sets `Signature` to base64 of the sig.
24. In `redact.go`, add `verifyRedactionSignature(ev RedactionEvent, pub ed25519.PublicKey) error`.
25. In `redact.go`, add `(s *Store) Redact(ctx, id, reason string, key ed25519.PrivateKey) error` that: (1) reads chain record, (2) errors if already redacted, (3) builds + signs `RedactionEvent`, (4) writes an **append-only** redaction-type node to the chain via `writeChain` (new ID, no content tier), (5) rewrites the target's `chain/{id}.json` with `redaction` field populated, (6) calls `wipeContent`.
26. In `redact.go`, add `(s *Store) IsRedacted(id string) (bool, error)`.
27. Document that the ed25519 key is passed in by the caller; for now the CLI sources it from `$STOKE_LEDGER_REDACT_KEY` (base64 32-byte seed) or a passphrase-derived seed via `argon2id` (cost params: 64MB/3/4); `specs/encryption-at-rest.md` will supersede this once the keyring lands.
28. Create `internal/ledger/verify.go` with `(s *Store) Verify(ctx context.Context) error`.
29. In `verify.go`, walk `chain/` in filesystem order; for each record assert `recomputed := hex(sha256(canonical(header) || content_commitment))[:8]`; assert `record.ID == record.Header.Type + "-" + recomputed`.
30. In `verify.go`, group chain records by `NonSensitiveMeta.mission_id` and sort by `Header.Seq`; assert each record's `ParentHash` equals the prior record's ID (empty for first).
31. In `verify.go`, accept either the new-derived ID OR the `LegacyID` for chain integrity during the transition window (gated by env var `STOKE_LEDGER_V1=1`).
32. In `verify.go`, when a chain record has a non-nil `Redaction`, verify its signature against a configured public key (passed to `NewStore` via a `VerifyOptions` struct; skip signature check when no key configured, emit a `log.Printf` warning).
33. In `verify.go`, return a descriptive `fmt.Errorf` on the first failing record identifying the ID and the mismatch kind (commitment, parent_hash, signature).
34. Create `internal/ledger/migrate.go` alongside the existing `migrate.go` — since that file is taken, name the new file `migrate_v2.go`; export `MigrateToV2(store *Store, opts MigrateOptions) (MigrateReport, error)`.
35. In `migrate_v2.go`, load every `nodes/{id}.json` via the legacy path, group by `MissionID`, sort by `CreatedAt`, re-derive headers + commitments + new IDs, and rewrite `parent_hash` to point at the predecessor's **new** id.
36. In `migrate_v2.go`, honor `opts.DryRun` — when true, return a report containing every old→new mapping and every rewritten parent without touching disk.
37. In `migrate_v2.go`, emit progress to `log.Printf` every 1000 nodes; emit a final summary line with counts (migrated, skipped, errored).
38. In `migrate_v2.go`, rebuild the SQLite index (drop + repopulate via `RebuildIndex`) after a non-dry-run migration completes.
39. Edit `internal/ledger/index.go`: add columns `content_commitment TEXT`, `redacted INTEGER`, `legacy_id TEXT`, `header_json TEXT`; write an `ALTER TABLE` migration guarded by a version-check on the existing index schema.
40. Edit `internal/ledger/index.go`: `InsertNode` populates the new columns from the chain record.
41. Edit `internal/ledger/ledger.go`: remove `Node.Content` from `computeID`; `computeID` now wraps header+commitment and returns `type + "-" + hex(sha256(canonical(header)||commitment))[:8]`.
42. Edit `ledger.go`: `AddNode` calls `store.WriteNode(&n, WriteOptions{NonSensitiveMeta: …})`; delete the old `hashNodeForChain` call — `ParentHash` is now simply the predecessor's **ID** (which is itself a hash of header+commitment), matching the spec's `assert header.parent_hash == previous_node_id`.
43. Edit `ledger.go`: `Ledger.Verify` delegates to `store.Verify`; keep the existing index-vs-store reachability check as a second pass.
44. Add `cmd/r1/ledger_migrate.go`: subcommand `stoke ledger migrate [--dry-run]` wires `MigrateToV2`.
45. Add `cmd/r1/ledger_verify.go`: subcommand `stoke ledger verify` calls `Store.Verify` and prints pass/fail with the first failing ID.
46. Add `cmd/r1/ledger_redact.go`: subcommand `stoke ledger redact --id X --reason "..." [--key-file PATH]` — loads the ed25519 key, calls `Store.Redact`, prints the new redaction-event node ID.
47. Register all three subcommands under the existing `stoke ledger` command group in `cmd/r1/main.go`.
48. Add unit tests `internal/ledger/chain_test.go`, `content_test.go`, `redact_test.go`, `verify_test.go`, `migrate_v2_test.go` — each ≥80% line coverage of its own file.
49. Add an integration test `internal/ledger/ledger_redaction_integration_test.go` that (a) writes 100 mixed-sensitivity nodes across 3 missions, (b) redacts 50 of them, (c) asserts `Store.Verify()` returns nil, (d) flips one byte in a random `chain/{id}.json` and asserts `Verify()` returns an error naming that ID.
50. Add a migration fixture at `internal/ledger/testdata/migrate_v1_fixture/` with ~20 legacy `nodes/{id}.json` files across 2 missions; add `migrate_v2_fixture_test.go` asserting end-to-end migration + `Verify()` pass with `STOKE_LEDGER_V1=1`.

## 10. Acceptance Criteria

- `go build ./cmd/r1`, `go test ./...`, `go vet ./...` all pass.
- `Store.Verify(ctx)` returns `nil` on (a) an empty ledger, (b) a fresh 100-node chain, (c) a 100-node chain where exactly 50 randomly-chosen sensitive nodes have been redacted.
- `Store.Verify(ctx)` returns a `fmt.Errorf` naming the offending node ID when any byte of any `chain/{id}.json` is flipped.
- `Store.Redact(ctx, id, reason, key)` leaves `chain/{id}.json` readable, populates its `redaction` field with a valid ed25519 signature, removes `content/{id}.json`, and appends a `redaction`-type node to the chain tier.
- After `Redact`, re-reading the node via `Store.ReadNode(id)` returns `Redacted=true, Content=nil` and no error.
- `stoke ledger migrate` on the fixture corpus produces a chain tier where every node has `legacy_id` populated, every downstream `parent_hash` points at the predecessor's **new** ID, and `Store.Verify()` passes with `STOKE_LEDGER_V1=1`.
- `stoke ledger migrate --dry-run` writes zero bytes to disk and prints the full mapping.

## 11. Testing

- Per-file unit tests for `chain.go`, `content.go`, `redact.go`, `verify.go`, `migrate_v2.go`.
- Seeded-corpus integration test: 100 nodes, mixed sensitivity (30 sensitive, 70 structural), 3 missions, 50 redactions, `Verify()` passes.
- Tamper test: byte-flip any chain file → `Verify()` identifies it.
- Migration test: golden `testdata/migrate_v1_fixture/` corpus migrates deterministically; `Verify()` passes; re-migration is a no-op.
- Signature test: redaction signed with key A fails verification with key B; unsigned redaction is rejected by `Verify()` when a public key is configured.
- Concurrency test: 10 goroutines calling `WriteNode` in parallel produce 10 unique IDs with correct `ParentHash` linkage (reuse the existing `l.mu` discipline — no new locks needed).

## 12. Rollout

- Phase 1 (weeks 1–2): gated by `STOKE_LEDGER_V2=1`. Default path remains the existing single-tier `computeID` + `nodes/`. CI runs both paths.
- Phase 2: after Phase 1 passes the ladder suite and one operator has migrated real history successfully, flip the default on. Provide `STOKE_LEDGER_V1=1` as opt-back kill-switch for one additional release cycle (full compatibility: legacy reads, legacy writes, no new `chain/` dirs created).
- Phase 3: remove the v1 code path, remove `WriteNodeLegacy`, remove the legacy-id fallback in `ReadNode` and `Verify`, remove the read-only `nodes/` shadow from new installs, remove `STOKE_LEDGER_V1`. `STOKE_LEDGER_V2` becomes a no-op env var removed one release later.

## Scope Notes

- This spec does NOT design the retention-policy SWEEP job. That's `specs/retention-policies.md`. This spec owns only `WriteNode` / `ReadNode` / `Redact` / `Verify` / `Migrate` primitives.
- This spec does NOT design the keyring. Callers pass an `ed25519.PrivateKey` directly. `specs/encryption-at-rest.md` will wire the keyring source for production redaction signers.
- This spec does NOT touch edges — they carry no content sensitivity and stay at `edges/{from}-{to}-{type}.json`.
