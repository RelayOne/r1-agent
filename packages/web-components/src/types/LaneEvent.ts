// LaneEvent — TS mirror of the lanes-protocol envelope.
//
// Ground truth: specs/lanes-protocol.md (lane.delta, lane.status_changed,
// lane.spawned, lane.killed, lane.delta.gap). The matching Rust type
// lives in desktop/src-tauri/src/lanes.rs LaneEvent enum.
//
// LaneStatus comes from docs/decisions/index.md D-S1 — six visible states
// each paired with a glyph + colour token from the shared Tailwind preset.

export type LaneStatus =
  | "pending"
  | "running"
  | "blocked"
  | "done"
  | "errored"
  | "cancelled";

export interface LaneSummary {
  lane_id: string;
  session_id: string;
  title: string;
  status: LaneStatus;
  created_at: string; // ISO 8601
}

export type LaneDeltaPayloadKind =
  | "token"
  | "tool_use"
  | "tool_result"
  | "thought"
  | "status";

export interface LaneDeltaPayload {
  kind: LaneDeltaPayloadKind;
  // Free-form per kind. Keep `unknown` so call-sites narrow explicitly.
  [field: string]: unknown;
}

export interface LaneDeltaEvent {
  kind: "delta";
  session_id: string;
  lane_id: string;
  seq: number;
  payload: LaneDeltaPayload;
}

export interface LaneStatusEvent {
  kind: "status";
  session_id: string;
  lane_id: string;
  from: LaneStatus;
  to: LaneStatus;
  at: string;
}

export interface LaneSpawnedEvent {
  kind: "spawned";
  session_id: string;
  lane_id: string;
  title: string;
  at: string;
}

export interface LaneKilledEvent {
  kind: "killed";
  session_id: string;
  lane_id: string;
  reason: string;
  at: string;
}

export interface LaneDeltaGapEvent {
  kind: "delta_gap";
  session_id: string;
  lane_id: string;
  last_seen_seq: number;
  at: string;
}

export type LaneEvent =
  | LaneDeltaEvent
  | LaneStatusEvent
  | LaneSpawnedEvent
  | LaneKilledEvent
  | LaneDeltaGapEvent;

// Type guards for narrowing on the discriminator.
export function isDelta(ev: LaneEvent): ev is LaneDeltaEvent {
  return ev.kind === "delta";
}
export function isStatus(ev: LaneEvent): ev is LaneStatusEvent {
  return ev.kind === "status";
}
export function isSpawned(ev: LaneEvent): ev is LaneSpawnedEvent {
  return ev.kind === "spawned";
}
export function isKilled(ev: LaneEvent): ev is LaneKilledEvent {
  return ev.kind === "killed";
}
export function isDeltaGap(ev: LaneEvent): ev is LaneDeltaGapEvent {
  return ev.kind === "delta_gap";
}

// All six statuses in the order the sidebar should sort by when status
// is the chosen secondary key (creation-time stable order is primary).
export const LANE_STATUSES: readonly LaneStatus[] = Object.freeze([
  "pending",
  "running",
  "blocked",
  "done",
  "errored",
  "cancelled",
]);
