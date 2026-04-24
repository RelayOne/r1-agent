// Package r1rename centralises the Stoke -> R1 rename compatibility
// surface for the stoke (R1) Go module per work-r1-rename.md Phase S1.
//
// The S1 dual-accept windows live here as a single, audited
// compatibility layer rather than scattered string literals across the
// tree. Each surface ships with a window-close date pinned in code so
// the S6 deprecation phase has a deterministic flip target:
//
//   - Env vars (S1-1, 90 days, ends 2026-07-23) -- env.go.
//     LookupEnv(canonical, legacy) reads R1_* first, falls back to the
//     legacy STOKE_* var, and rate-limits a single-shot deprecation
//     WARN per (canonical, legacy) pair. EnvLegacyDropEnabled() reads
//     R1_ENV_LEGACY_DROP for the post-window canonical-only mode.
//
//   - HTTP headers (S1-2, 30 days, ends 2026-05-23) -- headers.go.
//     DualHeader(h, pair, value) stamps both X-R1-* and X-Stoke-* on
//     egress; AcceptHeader(r, pair) reads X-R1-* canonical first on
//     ingress with X-Stoke-* fallback. Four pairs are exposed as
//     package vars: SessionHeaderPair, AgentHeaderPair, TaskHeaderPair,
//     BearerHeaderPair.
//
//   - MCP tool names (S1-4, until v2.0.0) -- mcp.go.
//     CanonicalToolName / LegacyToolName perform the stoke_ <-> r1_
//     prefix swap. MCPLegacyDropEnabled() reads R1_MCP_LEGACY_DROP for
//     the v2.0.0 cutover; until then both names are registered and
//     dispatch through the same handler.
//
//   - Data dirs (S1-5, indefinite) -- dirs.go. Re-exports the .r1 /
//     .stoke constants and a one-shot MigrateStokeDir(repo) helper
//     that copies a legacy .stoke/ tree to .r1/ while leaving the
//     legacy tree intact for rollback. Bulk read/write helpers live in
//     the lower-level internal/r1dir package and are used by the
//     migration helper here.
//
//   - Audit metadata (S1-6, 60 days, ends 2026-06-22) -- audit.go.
//     DualAuditMeta(meta, canonical, legacy, value) writes both keys
//     into a SharedAuditEvent metadata map with the identical value.
//
// # NATS subjects (S1-3) -- N/A for the R1 core repo
//
// The stoke binary has no NATS client dependency. The stoke.* strings
// in the spec inventory (stoke.session.*, stoke.task.*,
// stoke.descent.*, stoke.cost, stoke.cost.update, stoke.ac.result,
// stoke.delegation.{verify,settle,dispute}) are NDJSON event "type"
// fields emitted to stdout via internal/streamjson/emitter.go. NATS
// publication is handled downstream (RelayGate audit-ingest,
// CloudSwarm temporal). Per the work-order's wire-format invariant,
// those NDJSON event-type values continue to emit stoke.* for the 60d
// window. Canonical r1.* event-type aliases are NOT introduced in S1
// because downstream consumers have not migrated yet; that is a
// future-phase change scoped to land alongside the S6 NATS legacy
// drop.
//
// # Why a single package for five surfaces
//
// The five S1 surfaces share an audit-trail invariant: every legacy
// emission has to land in the same code path that emits the canonical
// counterpart, or the dual-accept guarantee silently weakens. Pulling
// the helpers into one package (modeled on Veritize's
// internal/verityrename) makes the compatibility contract a single
// review surface. When S6 fires, deletions happen here and propagate
// to call sites mechanically.
//
// The lower-level packages internal/r1env (env-var resolver) and
// internal/r1dir (data-dir resolver) ship the per-surface
// implementations; this package wraps them in the single API surface
// the work-order specifies (LookupEnv, DualHeader, AcceptHeader,
// MigrateStokeDir, DualAuditMeta).
package r1rename
