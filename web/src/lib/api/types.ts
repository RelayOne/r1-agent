// SPDX-License-Identifier: MIT
// Zod schemas + inferred TS types for every envelope and HTTP body
// in §API Client Wrapper of specs/web-chat-ui.md.
//
// Every WS envelope carries a per-session monotonic `seq` field used
// by Last-Event-ID replay (spec §WebSocket Reconnect Strategy). The
// server enforces the invariant; the client trusts it for ordering.
import { z } from "zod";

// ===========================================================================
// Common scalar shapes
// ===========================================================================

export const IsoTimestampSchema = z.string().datetime({ offset: true });
export type IsoTimestamp = z.infer<typeof IsoTimestampSchema>;

export const SessionIdSchema = z.string().min(1).max(128);
export const LaneIdSchema = z.string().min(1).max(128);
export const DaemonIdSchema = z.string().min(1).max(128);
export const MessageIdSchema = z.string().min(1).max(128);

export type SessionId = z.infer<typeof SessionIdSchema>;
export type LaneId = z.infer<typeof LaneIdSchema>;
export type DaemonId = z.infer<typeof DaemonIdSchema>;
export type MessageId = z.infer<typeof MessageIdSchema>;

// Mirrors internal/stokerr taxonomy. Server always sends one of these.
export const R1dErrorCodeSchema = z.enum([
  "INVALID_INPUT",
  "UNAUTHORIZED",
  "FORBIDDEN",
  "NOT_FOUND",
  "CONFLICT",
  "RATE_LIMITED",
  "INTERNAL",
  "UNAVAILABLE",
  "TIMEOUT",
  "VALIDATION_FAILED",
]);
export type R1dErrorCode = z.infer<typeof R1dErrorCodeSchema>;

// ===========================================================================
// HTTP — request / response bodies
// ===========================================================================

export const DaemonInfoSchema = z.object({
  id: DaemonIdSchema,
  name: z.string(),
  baseUrl: z.string().url(),
  wsUrl: z.string().url(),
  status: z.enum(["online", "offline", "degraded"]),
  version: z.string().optional(),
});
export type DaemonInfo = z.infer<typeof DaemonInfoSchema>;

export const ListDaemonsResponseSchema = z.object({
  daemons: z.array(DaemonInfoSchema),
});
export type ListDaemonsResponse = z.infer<typeof ListDaemonsResponseSchema>;

export const SessionStatusSchema = z.enum([
  "idle",
  "thinking",
  "running",
  "waiting",
  "error",
  "completed",
]);
export type SessionStatus = z.infer<typeof SessionStatusSchema>;

export const SessionMetadataSchema = z.object({
  id: SessionIdSchema,
  title: z.string().nullable(),
  workdir: z.string(),
  model: z.string(),
  status: SessionStatusSchema,
  createdAt: IsoTimestampSchema,
  updatedAt: IsoTimestampSchema,
  lastActivityAt: IsoTimestampSchema.nullable(),
  costUsd: z.number().nonnegative(),
  laneCount: z.number().int().nonnegative(),
  systemPromptPreset: z.string().nullable(),
});
export type SessionMetadata = z.infer<typeof SessionMetadataSchema>;

export const ListSessionsResponseSchema = z.object({
  sessions: z.array(SessionMetadataSchema),
});
export type ListSessionsResponse = z.infer<typeof ListSessionsResponseSchema>;

export const CreateSessionRequestSchema = z.object({
  model: z.string().min(1),
  workdir: z.string().min(1),
  systemPromptPreset: z.string().min(1).optional(),
});
export type CreateSessionRequest = z.infer<typeof CreateSessionRequestSchema>;

export const PatchSessionRequestSchema = z.object({
  workdir: z.string().min(1).optional(),
  model: z.string().min(1).optional(),
  title: z.string().nullable().optional(),
});
export type PatchSessionRequest = z.infer<typeof PatchSessionRequestSchema>;

export const LaneStateSchema = z.enum([
  "queued",
  "running",
  "waiting-tool",
  "completed",
  "failed",
  "killed",
]);
export type LaneState = z.infer<typeof LaneStateSchema>;

export const LaneSnapshotSchema = z.object({
  id: LaneIdSchema,
  sessionId: SessionIdSchema,
  label: z.string(),
  state: LaneStateSchema,
  createdAt: IsoTimestampSchema,
  updatedAt: IsoTimestampSchema,
  progress: z.number().min(0).max(1).nullable(),
  // Last cached render-string for live tool-use output (LaneTile).
  lastRender: z.string().nullable(),
  lastSeq: z.number().int().nonnegative(),
});
export type LaneSnapshot = z.infer<typeof LaneSnapshotSchema>;

export const ListLanesResponseSchema = z.object({
  lanes: z.array(LaneSnapshotSchema),
});
export type ListLanesResponse = z.infer<typeof ListLanesResponseSchema>;

export const KillLaneResponseSchema = z.object({
  laneId: LaneIdSchema,
  state: LaneStateSchema,
});
export type KillLaneResponse = z.infer<typeof KillLaneResponseSchema>;

export const SettingsSchema = z.object({
  defaultModel: z.string(),
  laneFilters: z.array(z.string()),
  theme: z.enum(["light", "dark", "system"]),
  highContrast: z.boolean(),
  reducedMotion: z.boolean(),
  keybindings: z.record(z.string(), z.string()),
});
export type Settings = z.infer<typeof SettingsSchema>;

export const WsTicketSchema = z.object({
  token: z.string().min(1),
  expiresAt: IsoTimestampSchema,
});
export type WsTicket = z.infer<typeof WsTicketSchema>;

export const ListAllowedRootsResponseSchema = z.object({
  roots: z.array(z.string()),
});
export type ListAllowedRootsResponse = z.infer<typeof ListAllowedRootsResponseSchema>;

// Generic server-side error body (for non-2xx responses).
export const ErrorResponseSchema = z.object({
  code: R1dErrorCodeSchema,
  message: z.string(),
  retryable: z.boolean().optional(),
  details: z.record(z.string(), z.unknown()).optional(),
});
export type ErrorResponse = z.infer<typeof ErrorResponseSchema>;

// ===========================================================================
// WebSocket — client -> server frames
// ===========================================================================

export const WsClientSubscribeSchema = z.object({
  type: z.literal("subscribe"),
  sessionId: SessionIdSchema,
  lastEventId: z.number().int().nonnegative().optional(),
});
export type WsClientSubscribe = z.infer<typeof WsClientSubscribeSchema>;

export const WsClientUnsubscribeSchema = z.object({
  type: z.literal("unsubscribe"),
  sessionId: SessionIdSchema,
});
export type WsClientUnsubscribe = z.infer<typeof WsClientUnsubscribeSchema>;

export const WsClientChatSchema = z.object({
  type: z.literal("chat"),
  sessionId: SessionIdSchema,
  content: z.string().min(1),
});
export type WsClientChat = z.infer<typeof WsClientChatSchema>;

export const WsClientInterruptSchema = z.object({
  type: z.literal("interrupt"),
  sessionId: SessionIdSchema,
});
export type WsClientInterrupt = z.infer<typeof WsClientInterruptSchema>;

export const WsClientPingSchema = z.object({
  type: z.literal("ping"),
});
export type WsClientPing = z.infer<typeof WsClientPingSchema>;

export const WsClientFrameSchema = z.discriminatedUnion("type", [
  WsClientSubscribeSchema,
  WsClientUnsubscribeSchema,
  WsClientChatSchema,
  WsClientInterruptSchema,
  WsClientPingSchema,
]);
export type WsClientFrame = z.infer<typeof WsClientFrameSchema>;

// ===========================================================================
// WebSocket — server -> client envelopes (every one carries `seq`)
// ===========================================================================

const baseEnvelopeShape = {
  seq: z.number().int().nonnegative(),
  ts: IsoTimestampSchema,
};

// `lane.delta` — incremental render-string update.
export const LaneDeltaEnvelopeSchema = z.object({
  ...baseEnvelopeShape,
  type: z.literal("lane.delta"),
  sessionId: SessionIdSchema,
  laneId: LaneIdSchema,
  data: z.string(),
});
export type LaneDeltaEnvelope = z.infer<typeof LaneDeltaEnvelopeSchema>;

// `lane.status` — status transition.
export const LaneStatusEnvelopeSchema = z.object({
  ...baseEnvelopeShape,
  type: z.literal("lane.status"),
  sessionId: SessionIdSchema,
  laneId: LaneIdSchema,
  state: LaneStateSchema,
  progress: z.number().min(0).max(1).nullable().optional(),
});
export type LaneStatusEnvelope = z.infer<typeof LaneStatusEnvelopeSchema>;

// `lane.created` — lifecycle.
export const LaneCreatedEnvelopeSchema = z.object({
  ...baseEnvelopeShape,
  type: z.literal("lane.created"),
  sessionId: SessionIdSchema,
  lane: LaneSnapshotSchema,
});
export type LaneCreatedEnvelope = z.infer<typeof LaneCreatedEnvelopeSchema>;

// `lane.killed` — lifecycle.
export const LaneKilledEnvelopeSchema = z.object({
  ...baseEnvelopeShape,
  type: z.literal("lane.killed"),
  sessionId: SessionIdSchema,
  laneId: LaneIdSchema,
  reason: z.string().nullable(),
});
export type LaneKilledEnvelope = z.infer<typeof LaneKilledEnvelopeSchema>;

// Message-part variants. Each maps to an @ai-sdk/elements card.
export const MessagePartTextSchema = z.object({
  kind: z.literal("text"),
  text: z.string(),
});

export const MessagePartToolSchema = z.object({
  kind: z.literal("tool"),
  toolCallId: z.string(),
  toolName: z.string(),
  input: z.unknown(),
  output: z.unknown().optional(),
  state: z.enum(["input-streaming", "input-available", "output-streaming", "output-available", "error"]),
  errorText: z.string().optional(),
});

export const MessagePartReasoningSchema = z.object({
  kind: z.literal("reasoning"),
  text: z.string(),
  state: z.enum(["streaming", "complete"]),
});

export const PlanItemSchema = z.object({
  id: z.string(),
  text: z.string(),
  status: z.enum(["pending", "in-progress", "completed", "blocked", "skipped"]),
});

export const MessagePartPlanSchema = z.object({
  kind: z.literal("plan"),
  items: z.array(PlanItemSchema),
});

export const MessagePartSchema = z.discriminatedUnion("kind", [
  MessagePartTextSchema,
  MessagePartToolSchema,
  MessagePartReasoningSchema,
  MessagePartPlanSchema,
]);
export type MessagePart = z.infer<typeof MessagePartSchema>;

// `message.part` — streamed part.
export const MessagePartEnvelopeSchema = z.object({
  ...baseEnvelopeShape,
  type: z.literal("message.part"),
  sessionId: SessionIdSchema,
  messageId: MessageIdSchema,
  role: z.enum(["assistant", "user", "system", "tool"]),
  part: MessagePartSchema,
});
export type MessagePartEnvelope = z.infer<typeof MessagePartEnvelopeSchema>;

// `message.complete` — terminal.
export const MessageCompleteEnvelopeSchema = z.object({
  ...baseEnvelopeShape,
  type: z.literal("message.complete"),
  sessionId: SessionIdSchema,
  messageId: MessageIdSchema,
  costUsd: z.number().nonnegative().optional(),
  durationMs: z.number().int().nonnegative().optional(),
});
export type MessageCompleteEnvelope = z.infer<typeof MessageCompleteEnvelopeSchema>;

// `session.updated` — workdir / model / cost changes.
export const SessionUpdatedEnvelopeSchema = z.object({
  ...baseEnvelopeShape,
  type: z.literal("session.updated"),
  sessionId: SessionIdSchema,
  patch: SessionMetadataSchema.partial(),
});
export type SessionUpdatedEnvelope = z.infer<typeof SessionUpdatedEnvelopeSchema>;

// `auth.expiring_soon` — pre-emptive ticket refresh (~60s before expiry).
export const AuthExpiringSoonEnvelopeSchema = z.object({
  ...baseEnvelopeShape,
  type: z.literal("auth.expiring_soon"),
  expiresAt: IsoTimestampSchema,
});
export type AuthExpiringSoonEnvelope = z.infer<typeof AuthExpiringSoonEnvelopeSchema>;

// `pong` — heartbeat reply.
export const PongEnvelopeSchema = z.object({
  ...baseEnvelopeShape,
  type: z.literal("pong"),
});
export type PongEnvelope = z.infer<typeof PongEnvelopeSchema>;

// `error` — server-side error.
export const ErrorEnvelopeSchema = z.object({
  ...baseEnvelopeShape,
  type: z.literal("error"),
  sessionId: SessionIdSchema.optional(),
  code: R1dErrorCodeSchema,
  message: z.string(),
  retryable: z.boolean(),
});
export type ErrorEnvelope = z.infer<typeof ErrorEnvelopeSchema>;

export const WsServerEnvelopeSchema = z.discriminatedUnion("type", [
  LaneDeltaEnvelopeSchema,
  LaneStatusEnvelopeSchema,
  LaneCreatedEnvelopeSchema,
  LaneKilledEnvelopeSchema,
  MessagePartEnvelopeSchema,
  MessageCompleteEnvelopeSchema,
  SessionUpdatedEnvelopeSchema,
  AuthExpiringSoonEnvelopeSchema,
  PongEnvelopeSchema,
  ErrorEnvelopeSchema,
]);
export type WsServerEnvelope = z.infer<typeof WsServerEnvelopeSchema>;

export type WsServerEnvelopeType = WsServerEnvelope["type"];
