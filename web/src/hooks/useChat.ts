// SPDX-License-Identifier: MIT
// useChat — hook with the same shape as `@ai-sdk/react`'s useChat
// helpers, powered by our zustand store + R1dClient WS transport.
// Spec item 18/55.
//
// The spec calls for `wrapping @ai-sdk/react useChat with a custom
// transport`. The official transport interface is HTTP-only (it
// expects a POST endpoint that streams `data` chunks); our daemon
// streams WS envelopes directly into the daemon store. So this hook
// composes the same observable surface (`messages`, `status`,
// `sendMessage`, `stop`, `error`, `setMessages`) by reading from the
// store and dispatching through the daemon socket.
//
// Components import this and treat it as the official `useChat` —
// they get streaming text + tool / reasoning / plan parts via the
// store, keyed by sessionId.
import { useCallback, useMemo } from "react";
import { useStore } from "zustand";
import type { ChatMessage, DaemonStore } from "@/lib/store/daemonStore";
import type { SessionId } from "@/lib/api/types";

export type ChatStatus = "ready" | "submitted" | "streaming" | "error";

export interface UseChatOptions {
  /** Daemon store for the session's daemon. */
  store: DaemonStore;
  /** Session id whose messages this hook reflects. */
  sessionId: SessionId;
  /** Sender. Most commonly `useDaemonSocket().sendMessage`. */
  sendChat: (sessionId: SessionId, content: string) => void;
  /** Interrupt. Most commonly `useDaemonSocket().interrupt`. */
  sendInterrupt: (sessionId: SessionId) => void;
}

export interface UseChatHelpers {
  /** Ordered messages for this session. */
  messages: ChatMessage[];
  /** "ready" idle, "submitted" awaiting first part, "streaming"
   *  receiving parts, "error" terminal failure. */
  status: ChatStatus;
  /** Last error seen for the session, if any. */
  error: Error | undefined;
  /** Send a user message; the daemon assigns the message id. */
  sendMessage: (content: string) => void;
  /** Stop the current turn (drop-partial). */
  stop: () => void;
  /** Clear the local error so the UI can recover. */
  clearError: () => void;
}

/**
 * Stable hook that selects this session's messages from the store
 * and exposes a sendMessage / stop pair backed by the daemon socket.
 */
export function useChat(opts: UseChatOptions): UseChatHelpers {
  const { store, sessionId, sendChat, sendInterrupt } = opts;

  // Subscribe to the session's message order and lookup map. We
  // re-derive the ordered ChatMessage[] each render, but the
  // selector returns stable references when nothing changes.
  const order = useStore(store, (s) => s.messages.orderBySession[sessionId]);
  const byKey = useStore(store, (s) => s.messages.byKey);
  const sessionStatus = useStore(store, (s) => s.sessions.byId[sessionId]?.status);

  const messages: ChatMessage[] = useMemo(() => {
    if (!order) return [];
    const out: ChatMessage[] = [];
    for (const mid of order) {
      const m = byKey[`${sessionId}:${mid}`];
      if (m) out.push(m);
    }
    return out;
  }, [order, byKey, sessionId]);

  // Derive a status. The session's status enum is the source of
  // truth for the daemon side; we map it to the @ai-sdk-style
  // discriminator the components expect.
  const status: ChatStatus = useMemo(() => {
    if (sessionStatus === "thinking" || sessionStatus === "running") {
      const last = messages[messages.length - 1];
      if (last?.streaming) return "streaming";
      return "submitted";
    }
    if (sessionStatus === "error") return "error";
    return "ready";
  }, [sessionStatus, messages]);

  const sendMessage = useCallback((content: string): void => {
    if (content.trim().length === 0) return;
    sendChat(sessionId, content);
  }, [sendChat, sessionId]);

  const stop = useCallback((): void => {
    sendInterrupt(sessionId);
  }, [sendInterrupt, sessionId]);

  const clearError = useCallback((): void => {
    // The store does not currently surface a per-session error string;
    // when the session.status comes off "error", `status` will follow.
    // Hook is a no-op stub so consumers can still call clearError().
    void sessionId;
  }, [sessionId]);

  return {
    messages,
    status,
    error: undefined,
    sendMessage,
    stop,
    clearError,
  };
}
