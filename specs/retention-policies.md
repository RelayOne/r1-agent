<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-21 -->
<!-- DEPENDS_ON: memory-bus (expires_at column), ledger-redaction (Redact primitive), encryption-at-rest (redaction signer + content wipe) -->
<!-- BUILD_ORDER: 26 -->

# Retention Policies

## 1. Overview

Stoke today retains every memory row, stream file, ledger node, checkpoint entry, and prompt/response record indefinitely. This posture is incompatible with the compliance frameworks that operators ask us about: HIPAA's six-year retention ceiling on PHI-adjacent audit data, GDPR Articles 5(1)(e) and 17 (storage limitation and right-to-erasure), SOC 2 CC6.5 (secure disposal), and — most concretely — **EU AI Act Article 12 (record-keeping), which becomes enforceable for general-purpose AI systems on 2026-08-02**. Without configurable retention, regulated operators cannot deploy Stoke at all, and unregulated operators cannot honor individual erasure requests.

The target state is a **retention-policy engine** that reads operator config at `~/.r1/config.yaml` (with per-repo override at `<repo>/.stoke/config.yaml`), maps each retained surface (ephemeral/session/persistent/permanent memories, stream files, ledger content, checkpoint files, prompts) to a `Duration`, and enforces the policy in two passes: (a) **on-session-end** via a hook in `cmd/r1/sow_native.go`, and (b) **hourly sweep** via a background goroutine in `cmd/r1-server/main.go`. Content wipes never touch the ledger chain tier — they call `ledger.Store.Redact()` from `specs/ledger-redaction.md`, which crypto-shreds the content tier and leaves the Merkle chain verifiable. The engine ships behind `STOKE_RETENTION=1` so default behavior stays retain-forever until operators opt in.

## 2. Policy types and defaults

```yaml
retention:
  ephemeral_memories:    wipe_after_session   # default
  session_memories:      retain_30_days       # default
  persistent_memories:   retain_forever       # default
  permanent_memories:    retain_forever       # IMMUTABLE (not operator-configurable)
  stream_files:          retain_90_days       # default
  ledger_nodes:          retain_forever       # chain tier always forever (Merkle integrity)
  ledger_content:        retain_forever       # content tier configurable per sensitivity
  checkpoint_files:      retain_30_days       # default
  prompts_and_responses: retain_forever       # default permissive; override for regulated envs
```

Duration enum (Go):

```go
type Duration string

const (
    WipeAfterSession Duration = "wipe_after_session"
    Retain7Days      Duration = "retain_7_days"
    Retain30Days     Duration = "retain_30_days"
    Retain90Days     Duration = "retain_90_days"
    RetainForever    Duration = "retain_forever"
)
```

`permanent_memories` is hard-coded to `RetainForever` and rejects any override at parse time. `ledger_nodes` likewise rejects any value other than `RetainForever` — the chain tier invariant is non-negotiable. `ledger_content` accepts all five values; `wipe_after_session` on it means "redact the content-tier blob of sensitive-type nodes the moment the owning session ends."

## 3. Config surface

Reuse the existing YAML parser in `internal/config/policy.go` — extend `Policy` with a `Retention RetentionConfig` field and add a `RetentionConfig` struct in `internal/retention/policy.go` (not in `internal/config/` — keep retention types owned by the retention package and import them from config).

Load order:
1. Load `~/.r1/config.yaml` → `global.Retention`.
2. If `<repo>/.stoke/config.yaml` exists and has a `retention:` block, load it → `repo.Retention`.
3. Merge per-key: repo value wins for each key that is explicitly set; unset keys inherit from global.
4. Unset global keys inherit from `retention.Defaults()`.

Hot-reload: every call to `EnforceOnSessionEnd` and `EnforceSweep` re-reads and re-merges the two files. A parse error logs a warning and **keeps using the last-good policy** (fail-soft so a typo doesn't disable retention).

Validation: unknown duration strings return `fmt.Errorf("retention.%s: invalid duration %q, must be one of [wipe_after_session retain_7_days retain_30_days retain_90_days retain_forever]", key, val)`. `permanent_memories` or `ledger_nodes` set to anything other than `retain_forever` returns `fmt.Errorf("retention.%s: must be retain_forever (immutable)", key)`.

## 4. Implementation package

```
internal/retention/
  policy.go         // Policy struct, Duration enum, parser, merger, Defaults()
  policy_test.go
  enforce.go        // EnforceOnSessionEnd, EnforceSweep, per-surface helpers
  enforce_test.go
  signer.go         // GetSigner() wraps crypto.GetRedactionSigner() with fallback
  signer_test.go
```

Integration sites:

- **`cmd/r1/sow_native.go`** — at the session-end path (after `ended_at` is stamped) call `retention.EnforceOnSessionEnd(ctx, session.ID)`. Errors are logged and **do not fail the session** — retention is best-effort on the hot path.
- **`cmd/r1-server/main.go`** — spawn `go retention.SweepLoop(ctx)` which runs `EnforceSweep(ctx)` on an hourly `time.NewTicker(time.Hour)`. Respect `ctx.Done()` for graceful shutdown; log one line per sweep with counts per surface.

## 5. Per-surface enforcement logic

### Ephemeral memories
```sql
DELETE FROM stoke_memory_bus
 WHERE scope IN ('session', 'session_step', 'worker')
   AND memory_type = 'ephemeral'
   AND session_id = ?
```
Runs only in `EnforceOnSessionEnd`. No TTL stamping — straight delete.

### Session memories (TTL-based)
On session-end, if `policy.SessionMemories != RetainForever`:
```sql
UPDATE stoke_memory_bus
   SET expires_at = ?
 WHERE session_id = ?
   AND memory_type = 'session'
   AND expires_at IS NULL
```
The sweep deletes:
```sql
DELETE FROM stoke_memory_bus WHERE expires_at IS NOT NULL AND expires_at < ?
```

### Persistent memories
Never auto-wiped by either path. Deleted only via the r1-server UI's per-row delete action (out of scope for this spec).

### Permanent memories
Immutable. The enforcement functions **never touch rows where `memory_type = 'permanent'`** — not even when `force=true` on forced-wipe. Covered by a fail-closed test.

### Stream files
The r1-server scans `datadir/streams/*.jsonl{,.enc}` and cross-references each with `r1.session.json` to get `ended_at`. If `ended_at + policy.StreamFiles < now()`, delete the file. Encrypted and plain variants share the same rule. Runs only in the sweep.

### Ledger nodes (chain tier)
Never wiped. Not touched by either path.

### Ledger content (content tier)
On session-end **only**, if `policy.LedgerContent == WipeAfterSession` OR (for backward-compat) `policy.PromptsAndResponses == WipeAfterSession`:
```go
signer, _ := retention.GetSigner()
for _, nodeID := range ledger.ListSensitiveNodesForSession(sessionID) {
    _ = store.Redact(ctx, nodeID,
        "retention_policy:prompts_and_responses:wipe_after_session", signer)
}
```
Sensitive node types: `PromptNode`, `ResponseNode`, `ToolInputNode`, `ToolOutputNode`. The chain tier entry stays; the content-tier blob is crypto-shredded per `specs/ledger-redaction.md`.

### Checkpoint files
Sweep opens `checkpoints/timeline.jsonl`, streams entries to a temp file keeping only those with `timestamp >= now() - policy.CheckpointFiles`, then atomic-renames over the original. Zero-entry case: truncate to empty file (don't delete — downstream readers assume existence).

## 6. Enforcement ordering

**Session-end (`EnforceOnSessionEnd`)**, strictly in this order:

1. Stamp `ended_at` on the `sessions` row (idempotent — caller may already have done this).
2. Wipe ephemeral memories for this session.
3. Set `expires_at` on session memories for this session per policy.
4. If `LedgerContent == WipeAfterSession` (or `PromptsAndResponses == WipeAfterSession`): redact content tier for every sensitive-type node owned by this session.
5. Mark stream file for rotation by recording `stream_rotate_after_at = ended_at + policy.StreamFiles` in the session row (the sweep reads this field).

**Hourly sweep (`EnforceSweep`)**:

1. Delete `stoke_memory_bus` rows with `expires_at IS NOT NULL AND expires_at < now()`.
2. Delete stream files whose session has `stream_rotate_after_at < now()` (fall back to `ended_at + policy.StreamFiles` if that field is null — pre-existing sessions).
3. Truncate `checkpoints/timeline.jsonl` entries older than `policy.CheckpointFiles`.
4. **Do not** redact ledger content in the sweep. Session-end is the only automatic trigger — retroactive redactions require explicit operator intent (the forced-wipe path in §8).

Rationale: the sweep is idempotent and additive (only deletes things already past their TTL). Ledger redaction is irreversible and content-destructive, so it must be tied to a specific operator decision (session-end with `wipe_after_session`, or the forced-wipe button).

## 7. Redaction signer contract

`retention.GetSigner()` tries, in order:

1. `crypto.GetRedactionSigner()` from `specs/encryption-at-rest.md`. If it returns a non-nil `ed25519.PrivateKey`, use it.
2. Fallback: read `~/.r1/retention-signer.pem`. If absent, generate a new Ed25519 key, write it with `0o600` permissions, and use it.
3. If both fail (e.g., read-only home), return an error and **skip all redactions** this pass (log loudly — the operator needs to see this).

**Security note in the fallback:** a file-based key at `~/.r1/retention-signer.pem` is only as secure as the filesystem. An attacker with read access to that file can forge redaction records. Operators in regulated environments **must** enable `specs/encryption-at-rest.md` so the signer lives in the OS keyring / HSM. The spec documents this trade-off in the rollout runbook (§12) and the CLI prints a warning on first fallback use.

## 8. r1-server UI for retention

The UI shell is owned by `specs/r1-server-ui-v2.md`. This spec owns:

- **`GET /api/settings/retention`** — returns the merged effective policy as JSON with a `source` field per key (`default`/`global`/`repo`) so the UI can show provenance.
- **`POST /api/settings/retention`** — accepts a JSON body with any subset of the nine keys; validates each against the Duration enum; writes to `~/.r1/config.yaml` (preserving surrounding YAML comments via round-trip parse); returns the new effective policy.
- **`POST /api/settings/retention/wipe-now`** — forced wipe. Body: `{"confirm_phrase": "WIPE ALL DATA", "force": true}`. Calls `retention.EnforceSweep(ctx)` with a `ForceIgnorePolicy` option that sets `expires_at = now()` on every non-permanent memory, redacts every sensitive-type ledger content node regardless of session age, deletes every stream file regardless of age, and truncates every checkpoint timeline to empty. Never touches permanent memories or ledger chain tier. Emits an audit event `retention.forced_wipe` with actor, timestamp, and counts.

Storage of the retention-policy state is the operator's on-disk YAML — no separate DB table. The HTTP handler is the single writer; concurrent POSTs serialize on a `sync.Mutex` in `retention.ConfigWriter`.

## 9. Implementation checklist

Each item is self-contained and independently mergeable.

- [ ] 1. Create `internal/retention/` package with `doc.go` and package declaration.
- [ ] 2. Define `Duration` string enum with the five constants in `policy.go`.
- [ ] 3. Implement `Duration.Validate() error` rejecting unknown strings with the full valid-list error message.
- [ ] 4. Implement `Duration.AsTime() (time.Duration, bool)` returning `(0, false)` for `WipeAfterSession` and `RetainForever`.
- [ ] 5. Define `Policy` struct with the nine fields matching §2 keys; YAML tags for each.
- [ ] 6. Implement `Defaults() Policy` returning the §2 defaults.
- [ ] 7. Implement `Policy.Validate() error` — per-key Duration validation plus immutability rules for `PermanentMemories` and `LedgerNodes`.
- [ ] 8. Implement `Merge(global, repo Policy) Policy` — per-key override (zero value = inherit).
- [ ] 9. Implement `Load(globalPath, repoPath string) (Policy, error)` — reads both files, unmarshals, merges, validates, falls back to `Defaults()` on parse error with a logged warning.
- [ ] 10. Unit test: `TestDurationValidate` covers all five valid + three invalid inputs.
- [ ] 11. Unit test: `TestPolicyValidateImmutable` — setting `PermanentMemories = Retain7Days` returns the expected error.
- [ ] 12. Unit test: `TestMergeRepoWins` — repo value overrides global; unset repo keys inherit global.
- [ ] 13. Unit test: `TestLoadFallsBackOnParseError` — malformed YAML returns defaults, not an error.
- [ ] 14. Create `enforce.go` with `EnforceOnSessionEnd(ctx, sessionID)` and `EnforceSweep(ctx, opts...)` signatures.
- [ ] 15. Implement `wipeEphemeralMemories(ctx, db, sessionID)` running the DELETE in §5.
- [ ] 16. Implement `stampSessionMemoryTTL(ctx, db, sessionID, policy)` — UPDATE with computed `expires_at`.
- [ ] 17. Implement `redactSessionLedgerContent(ctx, store, signer, sessionID)` iterating sensitive-type nodes.
- [ ] 18. Implement `markStreamForRotation(ctx, db, sessionID, rotateAt)` — UPDATE on sessions row.
- [ ] 19. Wire `EnforceOnSessionEnd` to call steps 1-5 in the order from §6, logging per-step counts.
- [ ] 20. Implement `sweepExpiredMemories(ctx, db)` — DELETE WHERE expires_at < now().
- [ ] 21. Implement `sweepStreamFiles(ctx, db, streamsDir, policy)` — scan, cross-ref sessions, unlink.
- [ ] 22. Implement `sweepCheckpointTimelines(ctx, checkpointsDir, policy)` — stream-filter + atomic rename.
- [ ] 23. Wire `EnforceSweep` to call steps 1-3 from §6, honoring `ForceIgnorePolicy` when set.
- [ ] 24. Implement `SweepLoop(ctx)` — hourly ticker, graceful shutdown on ctx.Done, one structured log per iteration.
- [ ] 25. Create `signer.go` with `GetSigner() (ed25519.PrivateKey, error)`.
- [ ] 26. Implement crypto-signer path (calls `crypto.GetRedactionSigner()` when present).
- [ ] 27. Implement fallback path: read `~/.r1/retention-signer.pem` with 0o600 check; generate + write if absent.
- [ ] 28. Print a one-time warning when the fallback path is used.
- [ ] 29. Unit test: `TestGetSignerPrefsKeyring` — mock `crypto.GetRedactionSigner` returning a key.
- [ ] 30. Unit test: `TestGetSignerFallbackGenerates` — tempdir without keyring produces a valid PEM.
- [ ] 31. Unit test: `TestGetSignerFallbackRejectsLoosePerms` — refuses to read a 0o644 PEM.
- [ ] 32. Integrate `EnforceOnSessionEnd` into `cmd/r1/sow_native.go` at the session-end path (gated on `STOKE_RETENTION=1`).
- [ ] 33. Integrate `SweepLoop` into `cmd/r1-server/main.go` startup (gated on `STOKE_RETENTION=1`).
- [ ] 34. Add `GET /api/settings/retention` handler returning merged policy + source provenance.
- [ ] 35. Add `POST /api/settings/retention` handler with YAML round-trip write preserving comments.
- [ ] 36. Add `POST /api/settings/retention/wipe-now` handler validating `confirm_phrase == "WIPE ALL DATA"`.
- [ ] 37. Emit `retention.forced_wipe` audit event from the wipe-now handler.
- [ ] 38. HTTP test: GET returns defaults when no config files present.
- [ ] 39. HTTP test: POST persists and subsequent GET reflects the new value.
- [ ] 40. HTTP test: POST with invalid duration returns 400 with the valid-list error message.
- [ ] 41. HTTP test: wipe-now without correct confirm phrase returns 400.
- [ ] 42. Integration test: seed 10 ephemeral + 5 session memories → `EnforceOnSessionEnd` → assert 0 ephemeral + 5 session rows with `expires_at` set.
- [ ] 43. Integration test: seeded DB with memories whose `expires_at` is 31 days ago → `EnforceSweep` → 0 rows.
- [ ] 44. Integration test: chain verifies before AND after `redactSessionLedgerContent` using a ledger fixture.
- [ ] 45. Integration test: permanent memories survive `EnforceSweep` with `ForceIgnorePolicy=true`.
- [ ] 46. Concurrency test: `EnforceSweep` running while the memory-bus writer-goroutine inserts — no lost writes, no duplicate deletes.
- [ ] 47. Doc update: mention `STOKE_RETENTION=1` flag and link to this spec from `CLAUDE.md` design-decisions list.
- [ ] 48. Operator runbook: `docs/retention-runbook.md` with the regulated-env checklist from §12.

## 10. Acceptance criteria

- `go build ./... && go vet ./... && go test ./...` clean.
- **Scenario A (session-end):** seed a session with 10 ephemeral + 5 session memories; end the session; observe 0 ephemeral rows + 5 session rows each with `expires_at = ended_at + 30d`.
- **Scenario B (sweep):** advance clock 31 days; run `EnforceSweep`; observe 0 memory rows for that session.
- **Scenario C (ledger integrity):** with `ledger_content: wipe_after_session`, seed a session with a `PromptNode` and `ResponseNode`, end the session, then run `ledger.VerifyChain(ctx)`. Chain verifies. Content-tier blobs for both nodes return `ErrRedacted`.
- **Scenario D (forced wipe):** call `POST /api/settings/retention/wipe-now` with the correct phrase; verify (i) every non-permanent memory gone, (ii) every sensitive-type ledger content blob redacted, (iii) chain still verifies, (iv) permanent memories untouched, (v) one `retention.forced_wipe` audit event emitted with the correct actor + counts.
- **Scenario E (default off):** with `STOKE_RETENTION` unset, run end-to-end workflow and assert no memories deleted, no content redacted, no sweep log lines.

## 11. Testing

- **Unit (`policy_test.go`):** Duration validation, policy validation, merge semantics, load fallback. ~12 tests.
- **Unit (`enforce_test.go`):** each per-surface helper in isolation with an in-memory SQLite DB and a mock ledger store. ~15 tests.
- **Unit (`signer_test.go`):** keyring-present path, fallback-generate path, loose-perms rejection. 3 tests.
- **Integration (`integration_test.go`):** Scenarios A-E from §10 wired against a real temp SQLite DB + real filesystem + real ledger fixture.
- **Concurrency (`concurrency_test.go`):** spawn the memory-bus writer goroutine from `specs/memory-bus.md`, insert 10k rows while `EnforceSweep` runs in a tight loop; assert (i) the writer never observes `database is locked`, (ii) sweep never deletes a row whose `expires_at` is in the future.
- **Chain verify (`ledger_integrity_test.go`):** borrowed harness from `specs/ledger-redaction.md` — seed chain, redact N sensitive nodes, re-verify. Matching test exists in both specs to catch regressions on either side.

All tests MUST run under `go test ./internal/retention/... -race`.

## 12. Rollout

**Flag gate.** The entire engine is gated on the environment variable `STOKE_RETENTION=1`. When unset (the default) both `EnforceOnSessionEnd` and `SweepLoop` are no-ops that log a single DEBUG line and return. This preserves today's retain-forever behavior for existing operators who haven't opted in.

**Default-on profile.** When `STOKE_RETENTION=1` is set and no `retention:` block exists in any config file, the defaults from §2 apply. This is safe for the majority case — ephemeral wipe on session-end + 30-day session-memory TTL + 90-day stream-file TTL — without touching any ledger content or persistent memories.

**Regulated-environment runbook** (shipped as `docs/retention-runbook.md`):

1. **Enable encryption-at-rest first** per `specs/encryption-at-rest.md`. The retention signer must live in the OS keyring, not the PEM fallback.
2. **Edit `~/.r1/config.yaml`:**
   ```yaml
   retention:
     ledger_content: wipe_after_session
     prompts_and_responses: wipe_after_session
     stream_files: retain_7_days
     session_memories: retain_7_days
     checkpoint_files: retain_7_days
   ```
3. **Test with a fixture session:** run a scripted session that emits a known prompt, end it, confirm the content-tier blob is redacted and the chain still verifies (Scenario C).
4. **Enable `STOKE_RETENTION=1`** in the service unit / launchd plist.
5. **Restart r1-server** to pick up the sweep goroutine.
6. **Verify the forced-wipe path works** against a staging environment before relying on it in production.
7. **Subscribe to the `retention.forced_wipe` audit event** in the operator's SIEM — surprise wipes should page on-call.

**Rollback.** Unset `STOKE_RETENTION`; restart. Already-redacted content stays redacted (crypto-shred is irreversible), but no new redactions or deletes occur. The chain tier and all non-redacted data remain intact. Operators should plan the initial rollout carefully for this reason.
