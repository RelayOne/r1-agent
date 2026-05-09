// SPDX-License-Identifier: MIT
// Messages slice — per spec §Directory Layout (item 16).
//
// Owns the `messages` state record (chat-message snapshots keyed by
// `${sessionId}:${messageId}` plus per-session message order) and the
// streaming-state types used by the chat surface.  Cross-slice
// envelope dispatch (`applyEnvelope`) lives at the composition root.
import type { StateCreator } from "zustand";
import type { MessagePart, SessionId } from "@/lib/api/types";
import type { DaemonState } from "@/lib/store/daemonStore";

/** A single chat message accumulated from `message.part` envelopes. */
export interface ChatMessage {
  id: string;
  sessionId: SessionId;
  role: "assistant" | "user" | "system" | "tool";
  parts: MessagePart[];
  /** True until the server emits message.complete. */
  streaming: boolean;
  createdAt: string;
  updatedAt: string;
  costUsd?: number;
  durationMs?: number;
}

/** Internal shape of the `messages` sub-state. */
export interface MessagesState {
  /** Messages keyed by `${sessionId}:${messageId}`. */
  byKey: Record<string, ChatMessage>;
  /** Per-session message order. */
  orderBySession: Record<SessionId, string[]>;
}

export interface MessagesSlice {
  messages: MessagesState;
}

/** Internal helper used by the cross-slice envelope dispatcher. */
export const messageKey = (sid: SessionId, mid: string): string => `${sid}:${mid}`;

export function emptyMessagesState(): MessagesState {
  return { byKey: {}, orderBySession: {} };
}

/** Append/merge a streamed message part. Text parts coalesce (the
 *  same kind streamed multiple times accumulates text); other kinds
 *  replace by `toolCallId` / index. Exported so the cross-slice
 *  envelope dispatcher in daemonStore.ts can reuse the logic. */
export function appendPart(existing: MessagePart[], next: MessagePart): MessagePart[] {
  if (next.kind === "text") {
    const last = existing[existing.length - 1];
    if (last && last.kind === "text") {
      const merged: MessagePart = { kind: "text", text: last.text + next.text };
      return [...existing.slice(0, -1), merged];
    }
    return [...existing, next];
  }
  if (next.kind === "tool") {
    const idx = existing.findIndex(
      (p) => p.kind === "tool" && p.toolCallId === next.toolCallId,
    );
    if (idx >= 0) {
      const copy = existing.slice();
      copy[idx] = next;
      return copy;
    }
    return [...existing, next];
  }
  if (next.kind === "reasoning") {
    const idx = existing.findIndex((p) => p.kind === "reasoning");
    if (idx >= 0) {
      const copy = existing.slice();
      copy[idx] = next;
      return copy;
    }
    return [...existing, next];
  }
  if (next.kind === "plan") {
    const idx = existing.findIndex((p) => p.kind === "plan");
    if (idx >= 0) {
      const copy = existing.slice();
      copy[idx] = next;
      return copy;
    }
    return [...existing, next];
  }
  return [...existing, next];
}

// The MessagesSlice currently exposes only state + part-merging logic;
// the dispatcher that mutates messages (`applyEnvelope`) is defined at
// the composition root (`daemonStore.ts`) because it routes envelopes
// across all three slices in one transactional `set`. The factory is
// still defined here to honour the spec layout — an empty action map
// keeps the slice file ready for future message-only mutators.
export const createMessagesSlice: StateCreator<
  DaemonState,
  [],
  [],
  MessagesSlice
> = () => ({
  messages: emptyMessagesState(),
});
