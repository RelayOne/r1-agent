# S1-3 — NATS `stoke.*` → `r1.*` Dual-Publish (N/A for Stoke core repo)

**Status:** N/A — verified 2026-04-23 on branch `rename/s1-3-nats-r1-dual-publish`.

**Source spec:** `/home/eric/repos/plans/work-orders/work-r1-rename.md` §S1-3.

## Finding

The Stoke core Go repo does **not** publish to NATS. A full grep of the
source tree (`cmd/`, `internal/`) on 2026-04-23 returned:

- Zero `nats-io/nats.go` imports (`go.mod` has no NATS dependency).
- Zero `nats.Conn.Publish` / `jetstream.Publish` / `js.Publish` call sites.
- Zero `NATS_URL`, `NATS_SUBJECT`, or `nats://` connection config.

The `stoke.*` strings listed in the S1-3 canonical inventory
(`stoke.session.*`, `stoke.task.*`, `stoke.descent.*`, `stoke.cost`,
`stoke.cost.update`, `stoke.ac.result`, `stoke.delegation.{verify,settle,dispute}`)
are **NDJSON event `type` fields** emitted to stdout via
`internal/streamjson/emitter.go` — specifically `EmitSystem` / `EmitStoke` /
`EmitSharedAudit`. These are in-process stream events, not NATS messages.

## Where the NATS bridge lives

Downstream consumer services own the streamjson → NATS adapter:

- **RelayGate control-plane** (router-core):
  `apps/control-plane/src/modules/audit-ingest/nats-audit-ingest.service.ts`
  subscribes to `r1.agent.*` + `stoke.agent.*` (SHIPPED under S4-5 with the
  `source_product` enum dual-accept).
- **CloudSwarm temporal workflows** (pending S4-1):
  `temporal/workflows/audit_replay.py` reads `r1_session_id` canonical with
  `stoke_session_id` fallback per S1-6.

Stoke stdout NDJSON is consumed by those bridges, which are responsible for
the `stoke.*` → `r1.*` subject translation on publish.

## Precedent

This finding mirrors two prior in-repo N/A annotations already in the work-order:

- **Truecom S4-3 (line 272):** "NATS subjects: `stoke.descent.*` →
  `r1.descent.*` dual-publish per S1-3; 60d. **(N/A for Truecom — no such
  subject is published from this repo.)**"
- **Veritize S4-4 (line 296):** "git-verity does not expose any `STOKE_*`
  env vars, `X-Stoke-*` headers, `stoke.*` NATS subjects, `stoke_*` MCP
  tools, or `stoke_session_id` audit keys."

## Verification

- `go build ./...` — green.
- `go vet ./...` — green.
- `go test -count=1 -timeout=300s ./...` — 177 packages, all `ok`, zero `FAIL`.

## No action required

- No legacy `stoke.*` publish to keep; no canonical `r1.*` publish to add.
- No `internal/r1nats/` helper needed.
- `R1_NATS_LEGACY_DROP=true` flag referenced in the spec is a no-op for
  this repo; it lives in the downstream bridge services.

Work-order §S1-3 in `/home/eric/repos/plans/work-orders/work-r1-rename.md`
has been annotated with a `STATUS: N/A for Stoke core repo` note referencing
this file.
