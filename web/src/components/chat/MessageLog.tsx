// SPDX-License-Identifier: MIT
// <MessageLog> — virtualized scroller for one session's messages.
// Spec item 26/55.
//
// Behaviors per the spec:
//   - Uses `@tanstack/react-virtual` to mount only the visible rows; we
//     measure rows dynamically so multi-line messages don't cause
//     overscroll.
//   - Sticky-bottom: while the viewport is anchored at the bottom (within
//     a small threshold), new rows scroll the viewport so the latest
//     bubble stays in view. If the user scrolls up, the anchor releases
//     and the log stops auto-following.
//   - aria-live="polite" on the currently streaming bubble (the latest
//     message whose `streaming === true`) so SR users hear updates as
//     parts land, without interrupting reading.
//
// The component owns *only* virtualization + scroll behavior. The actual
// message rendering lives in <MessageBubble> (item 27); we accept it via
// a `renderMessage` callback to keep the unit test free of bubble HTML.
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { ReactElement, ReactNode } from "react";
import { useStore } from "zustand";
import { useVirtualizer } from "@tanstack/react-virtual";
import type { DaemonStore, ChatMessage } from "@/lib/store/daemonStore";
import type { SessionId } from "@/lib/api/types";

const STICK_THRESHOLD_PX = 64;
const ROW_ESTIMATE_PX = 96;

export interface MessageLogProps {
  store: DaemonStore;
  sessionId: SessionId;
  renderMessage: (message: ChatMessage, isStreaming: boolean) => ReactNode;
  /** Override scroll-parent height behavior in tests (skips virtualizer
   *  when no parent height is measurable in jsdom). */
  __testForceList?: boolean;
}

export function MessageLog({
  store,
  sessionId,
  renderMessage,
  __testForceList,
}: MessageLogProps): ReactElement {
  const order = useStore(
    store,
    (s) => s.messages.orderBySession[sessionId] ?? [],
  );
  const byKey = useStore(store, (s) => s.messages.byKey);

  const messages = useMemo(() => {
    const out: ChatMessage[] = [];
    for (const id of order) {
      const m = byKey[`${sessionId}:${id}`];
      if (m) out.push(m);
    }
    return out;
  }, [order, byKey, sessionId]);

  const parentRef = useRef<HTMLDivElement | null>(null);
  const [stuckBottom, setStuckBottom] = useState(true);

  const virtualizer = useVirtualizer({
    count: messages.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => ROW_ESTIMATE_PX,
    overscan: 6,
  });

  // Track whether the user is parked at the bottom of the log.
  useEffect(() => {
    const el = parentRef.current;
    if (!el) return;
    const onScroll = (): void => {
      const distanceFromBottom =
        el.scrollHeight - el.scrollTop - el.clientHeight;
      setStuckBottom(distanceFromBottom <= STICK_THRESHOLD_PX);
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => el.removeEventListener("scroll", onScroll);
  }, []);

  // Auto-scroll the log when new rows arrive *and* the user was already at
  // the bottom. We use useLayoutEffect so the scroll happens before the
  // browser paints the new row, avoiding a flash above the fold.
  const prevCount = useRef(messages.length);
  useLayoutEffect(() => {
    const grew = messages.length > prevCount.current;
    prevCount.current = messages.length;
    if (!grew) return;
    if (!stuckBottom) return;
    const el = parentRef.current;
    if (!el) return;
    el.scrollTop = el.scrollHeight;
  }, [messages.length, stuckBottom]);

  // Find the latest streaming message id; only its bubble gets aria-live.
  const streamingId = useMemo(() => {
    for (let i = messages.length - 1; i >= 0; i -= 1) {
      if (messages[i].streaming) return messages[i].id;
    }
    return null;
  }, [messages]);

  if (messages.length === 0) {
    return (
      <div
        ref={parentRef}
        data-testid="message-log"
        data-session-id={sessionId}
        data-stuck-bottom={stuckBottom ? "true" : "false"}
        className="flex-1 min-h-0 overflow-y-auto p-6 text-sm text-muted-foreground"
        role="log"
        aria-label="Conversation"
        aria-live="polite"
      >
        <p data-testid="message-log-empty">
          Send a message to start the conversation.
        </p>
      </div>
    );
  }

  // jsdom does not implement layout, so the virtualizer reports zero
  // measured rows and the scroller's children would be empty in tests.
  // Fall back to a non-virtualized render in that environment.
  if (__testForceList) {
    return (
      <div
        ref={parentRef}
        data-testid="message-log"
        data-session-id={sessionId}
        data-stuck-bottom={stuckBottom ? "true" : "false"}
        data-virtualized="false"
        className="flex-1 min-h-0 overflow-y-auto"
        role="log"
        aria-label="Conversation"
      >
        <ol className="m-0 p-0 list-none">
          {messages.map((m) => {
            const isStreaming = m.id === streamingId;
            return (
              <li
                key={m.id}
                data-testid={`message-row-${m.id}`}
                data-streaming={isStreaming ? "true" : "false"}
                aria-live={isStreaming ? "polite" : undefined}
                aria-busy={isStreaming ? "true" : undefined}
              >
                {renderMessage(m, isStreaming)}
              </li>
            );
          })}
        </ol>
      </div>
    );
  }

  return (
    <div
      ref={parentRef}
      data-testid="message-log"
      data-session-id={sessionId}
      data-stuck-bottom={stuckBottom ? "true" : "false"}
      data-virtualized="true"
      className="flex-1 min-h-0 overflow-y-auto"
      role="log"
      aria-label="Conversation"
    >
      <div
        style={{
          height: virtualizer.getTotalSize(),
          width: "100%",
          position: "relative",
        }}
      >
        {virtualizer.getVirtualItems().map((row) => {
          const m = messages[row.index];
          if (!m) return null;
          const isStreaming = m.id === streamingId;
          return (
            <div
              key={m.id}
              data-index={row.index}
              data-testid={`message-row-${m.id}`}
              data-streaming={isStreaming ? "true" : "false"}
              aria-live={isStreaming ? "polite" : undefined}
              aria-busy={isStreaming ? "true" : undefined}
              ref={virtualizer.measureElement}
              style={{
                position: "absolute",
                top: 0,
                left: 0,
                width: "100%",
                transform: `translateY(${row.start}px)`,
              }}
            >
              {renderMessage(m, isStreaming)}
            </div>
          );
        })}
      </div>
    </div>
  );
}
