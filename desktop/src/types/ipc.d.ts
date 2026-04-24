// SPDX-License-Identifier: MIT
//
// R1 Desktop IPC type definitions for the WebView tier.
//
// Source of truth: `desktop/IPC-CONTRACT.md` §2. These TypeScript types
// mirror 1:1 with the Rust structs in `src-tauri/src/ipc.rs` and the Go
// structs in `internal/desktopapi/desktopapi.go`. Any schema change
// must land in all three files in the same commit.
//
// This file is declaration-only (.d.ts) — types, interfaces, and
// type aliases. Runtime constants that pair with these types
// (canonical scope/tier arrays, etc.) live in `ipc.ts` alongside.
//
// Scaffold status (R1D-2): panel skeletons consume these types. The
// invoke stubs log "TODO <phase>" and return empty values. Real
// dispatch lands with R1D-1.2 / R1D-1.3 once the Tauri runtime is
// wired; real data-fetching lands panel-by-panel across R1D-3
// through R1D-9.

// ---------------------------------------------------------------------
// Shared envelope + error (§1, §3)
// ---------------------------------------------------------------------

/** ISO-8601 timestamp (e.g., "2026-04-23T15:04:05Z"). */
export type Iso8601 = string;

/** R1 taxonomy codes. Mirrors `internal/stokerr` and §3.2 of the contract. */
export type StokeCode =
  | "validation"
  | "not_found"
  | "conflict"
  | "append_only_violation"
  | "permission_denied"
  | "budget_exceeded"
  | "timeout"
  | "crash_recovery"
  | "schema_version"
  | "not_implemented"
  | "internal";

/** Error shape propagated through Tauri `invoke`. */
export interface IpcError {
  /** Numeric JSON-RPC code (e.g., -32010 for not_implemented). */
  code: number;
  /** R1 taxonomy string; clients should pattern-match on this. */
  stoke_code: StokeCode;
  /** Human-readable message. */
  message: string;
}

// ---------------------------------------------------------------------
// Session control (§2.1)
// ---------------------------------------------------------------------

export interface SessionStartParams {
  prompt: string;
  skill_pack?: string;
  provider?: string;
  budget_usd?: number;
}

export interface SessionStartResult {
  session_id: string;
  started_at: Iso8601;
}

export interface SessionIdParams {
  session_id: string;
}

export interface SessionPauseResult {
  paused_at: Iso8601;
}

export interface SessionResumeResult {
  resumed_at: Iso8601;
}

/** Minimal session summary used by the SOW-tree sidebar scaffold. */
export interface SessionSummary {
  session_id: string;
  title: string;
  started_at: Iso8601;
  status: "running" | "paused" | "ended";
}

/** Kind tag for the SOW tree drill-down (§R1D-3.1). */
export type SessionTreeNodeKind = "session" | "ac" | "task";

/** Lifecycle state for a SOW tree node. Superset of `SessionSummary.status`. */
export type SessionTreeNodeStatus =
  | "pending"
  | "running"
  | "passed"
  | "failed"
  | "paused"
  | "ended";

/**
 * Single node in the SOW drill-down tree. Sessions expand to their
 * acceptance criteria, which expand to their tasks. `children` is
 * empty at stub time; the real `session_tree` RPC body lands later.
 */
export interface SessionTreeNode {
  id: string;
  label: string;
  kind: SessionTreeNodeKind;
  status: SessionTreeNodeStatus;
  children: SessionTreeNode[];
}

export interface SessionTreeParams {
  session_id: string;
}

export interface SessionTreeResult {
  nodes: SessionTreeNode[];
}

// ---------------------------------------------------------------------
// Ledger query (§2.2)
// ---------------------------------------------------------------------

export interface LedgerEdge {
  to: string;
  kind: string;
}

export interface LedgerGetNodeParams {
  hash: string;
}

/**
 * Rich ledger node shape used by the R1D-5 browser. Consumed by the
 * node-detail drawer and the session-timeline renderers. `id` doubles
 * as the user-visible identifier; `content_hash` is the canonical
 * content-addressed hash; `parent_hash` is the prior node in the
 * per-session chain (empty on the first node). `shredded` flips true
 * after a crypto-shred so the UI can render a tombstone.
 */
export interface LedgerNode {
  id: string;
  /** One of ~30 node kinds registered in `internal/ledger/nodes/`. */
  kind: string;
  timestamp: Iso8601;
  content_hash: string;
  parent_hash: string;
  payload: Record<string, unknown>;
  shredded: boolean;
  /** Legacy alias for `kind` retained for `ledger_get_node` consumers. */
  type?: string;
  edges?: LedgerEdge[];
}

export interface LedgerListEventsParams {
  session_id?: string;
  since?: Iso8601;
  /** Default 100, max 1000. */
  limit?: number;
}

export interface LedgerEventSummary {
  hash: string;
  type: string;
  at: Iso8601;
}

export interface LedgerListEventsResult {
  events: LedgerEventSummary[];
  next_cursor?: string;
}

/** Summary row returned by `ledger_sessions`, shown in the left pane. */
export interface LedgerSessionSummary {
  session_id: string;
  started_at: Iso8601;
  node_count: number;
}

export interface LedgerSessionsResult {
  sessions: LedgerSessionSummary[];
}

export interface LedgerTimelineParams {
  session_id: string;
}

export interface LedgerTimelineResult {
  nodes: LedgerNode[];
}

export interface LedgerVerifyParams {
  session_id: string;
}

export interface LedgerVerifyResult {
  passed: boolean;
  first_bad_offset: number | null;
  message?: string;
}

export interface LedgerShredParams {
  session_id: string;
  node_id: string;
}

export interface LedgerShredResult {
  ok: boolean;
}

export interface LedgerExportParams {
  session_id: string;
}

export interface LedgerExportResult {
  ndjson: string;
}

// ---------------------------------------------------------------------
// Memory inspection (§2.3)
// ---------------------------------------------------------------------

/**
 * Canonical memory-bus scopes. Mirrors the six `Scope*` constants in
 * `internal/memory/membus/bus.go`. The earlier R1D-2 scaffold listed
 * five; `ScopeSessionStep` was added as scope coverage widened to the
 * per-step view.
 */
export type MemoryScope =
  | "Session"
  | "SessionStep"
  | "Worker"
  | "AllSessions"
  | "Global"
  | "Always";

export interface MemoryListScopesResult {
  scopes: MemoryScope[];
}

export interface MemoryQueryParams {
  scope: MemoryScope;
  key_prefix?: string;
  /** Default 100. */
  limit?: number;
}

export interface MemoryEntry {
  key: string;
  value: string;
  updated_at: Iso8601;
}

export interface MemoryQueryResult {
  entries: MemoryEntry[];
  truncated: boolean;
}

/**
 * Row shape surfaced in the memory-bus viewer table (§R1D-6.2). Richer
 * than `MemoryEntry` — it carries author + counters used by the
 * sortable key/value table and drill-down drawer.
 */
export interface MemoryRow {
  scope: MemoryScope;
  key: string;
  value: unknown;
  author: string;
  last_updated_at: Iso8601;
  read_count: number;
  write_count: number;
}

/** Single history entry rendered inside the memory drill-down drawer. */
export interface MemoryHistoryEntry {
  kind: "write" | "read";
  who: string;
  when: Iso8601;
  detail?: string;
}

export interface MemoryHistoryParams {
  scope: MemoryScope;
  key: string;
}

export interface MemoryHistoryResult {
  scope: MemoryScope;
  key: string;
  entries: MemoryHistoryEntry[];
}

/**
 * Conflict descriptor returned by the `memory_import` stub when an
 * incoming row collides with an existing row. The UI surfaces each
 * conflict with overwrite / skip / cancel actions.
 */
export interface MemoryImportConflict {
  key: string;
  existing: unknown;
  incoming: unknown;
}

export interface MemoryImportParams {
  scope: MemoryScope;
  rows: MemoryRow[];
  resolution?: "overwrite" | "skip";
}

export interface MemoryImportResult {
  imported: number;
  conflicts: MemoryImportConflict[];
}

export interface MemoryDeleteParams {
  scope: MemoryScope;
  key: string;
}

export interface MemoryDeleteResult {
  ok: boolean;
}

// ---------------------------------------------------------------------
// Cost (§2.4)
// ---------------------------------------------------------------------

export interface CostGetCurrentParams {
  session_id?: string;
}

export interface CostSnapshot {
  usd: number;
  tokens_in: number;
  tokens_out: number;
  as_of: Iso8601;
}

export interface CostGetHistoryParams {
  session_id?: string;
  since?: Iso8601;
  /** One of "minute", "hour", "day". Default "hour". */
  bucket?: "minute" | "hour" | "day";
}

export interface CostBucket {
  at: Iso8601;
  usd: number;
  tokens: number;
}

export interface CostHistoryResult {
  buckets: CostBucket[];
}

// ---------------------------------------------------------------------
// Descent state (§2.5)
// ---------------------------------------------------------------------

/** Eight verification tiers T1..T8. */
export type DescentTier =
  | "T1"
  | "T2"
  | "T3"
  | "T4"
  | "T5"
  | "T6"
  | "T7"
  | "T8";

export type DescentStatus = "pending" | "running" | "passed" | "failed";

export interface DescentCurrentTierParams {
  session_id: string;
  ac_id?: string;
}

export interface DescentTierRow {
  ac_id: string;
  tier: DescentTier;
  status: DescentStatus;
  evidence_ref?: string;
}

export interface DescentTierHistoryParams {
  session_id: string;
  ac_id: string;
}

export interface DescentAttempt {
  tier: DescentTier;
  status: DescentStatus;
  at: Iso8601;
  evidence_ref?: string;
  failure_class?: string;
}

export interface DescentTierHistoryResult {
  ac_id: string;
  attempts: DescentAttempt[];
}

/** Evidence drill-down row (§R1D-3.4). Shown in the descent drawer. */
export type DescentEvidenceKind =
  | "build_log"
  | "test_log"
  | "lint_log"
  | "verify_log"
  | "ledger_node"
  | "failure_report"
  | "other";

export interface DescentEvidence {
  tier: DescentTier;
  kind: DescentEvidenceKind;
  summary: string;
  artifact_ref?: string;
  at?: Iso8601;
}

export interface DescentEvidenceParams {
  session_id: string;
  ac_id?: string;
  tier: DescentTier;
}

export interface DescentEvidenceResult {
  tier: DescentTier;
  items: DescentEvidence[];
}

// ---------------------------------------------------------------------
// Tauri-only verbs (§5)
// ---------------------------------------------------------------------

export interface SessionSendParams {
  session_id: string;
  prompt: string;
}

/**
 * Summary row rendered in the R1D-4 skill catalog grid. Indexes the
 * bundled skill packs (e.g. actium-studio) and any user-installed
 * packs discovered by the host. `installed` flips true once the
 * marketplace install stub (§R1D-4.3) succeeds.
 */
export interface SkillSummary {
  id: string;
  name: string;
  description: string;
  author: string;
  version: string;
  category: string;
  tags: string[];
  pack: string;
  installed: boolean;
}

export interface SkillGetParams {
  id: string;
}

/**
 * JSON-schema-shaped skill input / output spec. The R1D-4.2 manifest
 * drawer renders this as a field list; the R1D-4.5 test modal walks
 * it to auto-generate an HTML form. Shape mirrors the subset of
 * JSON-Schema draft-07 that the Studio manifests actually use (type,
 * properties, required, enum, minLength, maxLength, minimum, format,
 * items, description).
 */
export interface SkillJsonSchema {
  type?: string;
  description?: string;
  properties?: Record<string, SkillJsonSchema>;
  required?: string[];
  enum?: Array<string | number>;
  minLength?: number;
  maxLength?: number;
  minimum?: number;
  maximum?: number;
  format?: string;
  items?: SkillJsonSchema;
  default?: unknown;
}

/** One worked example pair shipped inside a skill manifest. */
export interface SkillExample {
  title: string;
  input: Record<string, unknown>;
  output?: Record<string, unknown>;
}

/**
 * Full skill manifest payload returned by `skill_get` — extends the
 * catalog summary with the 7 R1D-4.2 required fields (inputs, outputs,
 * examples in addition to name/description/author/version already on
 * SkillSummary).
 */
export interface SkillManifest extends SkillSummary {
  inputs: SkillJsonSchema;
  outputs: SkillJsonSchema;
  examples: SkillExample[];
}

export interface SkillListResult {
  skills: SkillSummary[];
}

export interface SkillInstallParams {
  id: string;
}

export interface SkillInstallPackParams {
  pack: string;
}

/** Shared result shape for single-skill + pack install / uninstall. */
export interface SkillInstallResult {
  ok: boolean;
  installed: number;
}

export interface SkillInvokeParams {
  id: string;
  input: Record<string, unknown>;
}

export interface SkillInvokeResult {
  output: string;
  duration_ms: number;
}

// ---------------------------------------------------------------------
// Server-pushed events (§4)
// ---------------------------------------------------------------------

export interface SessionStartedEvent {
  event: "session.started";
  session_id: string;
  at: Iso8601;
}

export interface SessionDeltaEvent {
  event: "session.delta";
  session_id: string;
  payload: Record<string, unknown>;
}

export interface SessionEndedEvent {
  event: "session.ended";
  session_id: string;
  reason: "ok" | "cancelled" | "error";
  at: Iso8601;
}

export interface LedgerAppendedEvent {
  event: "ledger.appended";
  session_id: string;
  hash: string;
  type: string;
}

export interface CostTickEvent {
  event: "cost.tick";
  session_id: string;
  usd_delta: number;
  tokens_delta: number;
}

export interface DescentTierChangedEvent {
  event: "descent.tier_changed";
  session_id: string;
  ac_id: string;
  from: DescentTier | "";
  to: DescentTier;
  status: DescentStatus;
}

export type ServerEvent =
  | SessionStartedEvent
  | SessionDeltaEvent
  | SessionEndedEvent
  | LedgerAppendedEvent
  | CostTickEvent
  | DescentTierChangedEvent;

// ---------------------------------------------------------------------
// Invoke method-name union (the full Tauri `invoke_handler` surface)
// ---------------------------------------------------------------------

/**
 * Every Tauri command name exposed by the Rust host. 11 round-trip to
 * the Go subprocess; 4 are Tauri-only (§5). The `session_list` verb is
 * a WebView-level convenience that maps onto cached session summaries
 * once multi-session lands in R1D-2.4.
 */
export type InvokeMethod =
  // Session control
  | "session_start"
  | "session_pause"
  | "session_resume"
  // Ledger
  | "ledger_get_node"
  | "ledger_list_events"
  | "ledger_sessions"
  | "ledger_timeline"
  | "ledger_verify"
  | "ledger_shred"
  | "ledger_export"
  // Memory
  | "memory_list_scopes"
  | "memory_query"
  | "memory_history"
  | "memory_import"
  | "memory_delete"
  // Cost
  | "cost_get_current"
  | "cost_get_history"
  // Descent
  | "descent_current_tier"
  | "descent_tier_history"
  | "descent_evidence"
  // SOW drill-down (R1D-3.1 / R1D-3.2)
  | "session_tree"
  // Tauri-only
  | "session_send"
  | "session_cancel"
  | "skill_list"
  | "skill_get"
  | "skill_install"
  | "skill_uninstall"
  | "skill_install_pack"
  | "skill_invoke"
  // WebView convenience (cached in Rust host; not a JSON-RPC verb)
  | "session_list";
