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

export interface LedgerNode {
  hash: string;
  /** 22 ledger node types; see `ledger/nodes/`. */
  type: string;
  payload: Record<string, unknown>;
  edges: LedgerEdge[];
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

// ---------------------------------------------------------------------
// Memory inspection (§2.3)
// ---------------------------------------------------------------------

/** The five canonical memory-bus scopes; see `memory/` package. */
export type MemoryScope =
  | "Session"
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

// ---------------------------------------------------------------------
// Tauri-only verbs (§5)
// ---------------------------------------------------------------------

export interface SessionSendParams {
  session_id: string;
  prompt: string;
}

export interface SkillSummary {
  name: string;
  version: string;
  description: string;
}

export interface SkillGetParams {
  name: string;
}

export interface SkillManifest {
  name: string;
  version: string;
  description: string;
  input_schema: Record<string, unknown>;
  output_schema: Record<string, unknown>;
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
  // Memory
  | "memory_list_scopes"
  | "memory_query"
  // Cost
  | "cost_get_current"
  | "cost_get_history"
  // Descent
  | "descent_current_tier"
  | "descent_tier_history"
  // Tauri-only
  | "session_send"
  | "session_cancel"
  | "skill_list"
  | "skill_get"
  // WebView convenience (cached in Rust host; not a JSON-RPC verb)
  | "session_list";
