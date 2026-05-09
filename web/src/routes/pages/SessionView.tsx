// SPDX-License-Identifier: MIT
// Per-session chat view. Spec item 41/55 (SessionView slot).
// Three-column shell: SessionList | ChatPane (or TileGrid via ChatPane
// router) | TileGrid+LanesSidebar.
//
// Audit ref: closes the audit finding that <ChatPane>, <Composer>,
// <MessageLog>, <MessageBubble>, <ToolCard>, <PlanCard>,
// <ReasoningCard>, <StopButton>, <TileGrid>, <LaneTile>, and
// <LanesSidebar> were never reachable from the SPA.
import { useEffect, useState } from "react";
import type { ReactElement, ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { ThreeColumnShell } from "@/components/layout/ThreeColumnShell";
import { SessionList } from "@/components/session/SessionList";
import { LanesSidebar } from "@/components/lanes/LanesSidebar";
import { TileGrid } from "@/components/lanes/TileGrid";
import { ChatPane } from "@/components/chat/ChatPane";
import { MessageLog } from "@/components/chat/MessageLog";
import { MessageBubble } from "@/components/chat/MessageBubble";
import { ToolCard } from "@/components/chat/ToolCard";
import { ReasoningCard } from "@/components/chat/ReasoningCard";
import { PlanCard } from "@/components/chat/PlanCard";
import { Composer } from "@/components/chat/Composer";
import { StopButton } from "@/components/chat/StopButton";
import { ConnectionLostBanner } from "@/components/ConnectionLostBanner";
import { StatusBar } from "@/components/StatusBar";
import { useDaemonSocket } from "@/hooks/useDaemonSocket";
import { useChat } from "@/hooks/useChat";
import { useDaemonStore } from "@/app/DaemonStoreProvider";
import { useR1dClient, useR1dClientInfo } from "@/app/R1dClientProvider";
import { NotFound } from "@/routes/pages/NotFound";
import type { LaneId, SessionId } from "@/lib/api/types";

export interface SessionViewProps {
  daemonId: string;
  sessionId: string;
}

export function SessionView(props: SessionViewProps): ReactElement {
  const info = useR1dClientInfo(props.daemonId);
  if (!info) return <NotFound />;
  return <SessionViewContent {...props} />;
}

function SessionViewContent({
  daemonId,
  sessionId,
}: SessionViewProps): ReactElement {
  const navigate = useNavigate();
  const store = useDaemonStore(daemonId);
  const client = useR1dClient(daemonId);
  const socket = useDaemonSocket({ client, store });
  const [composerValue, setComposerValue] = useState<string>("");

  // Subscribe to this session's WS stream on mount; unsubscribe on
  // unmount. Hydrate the lane list once via REST so the right rail
  // renders immediately even before the first lane.created envelope.
  useEffect(() => {
    socket.subscribe(sessionId);
    let cancelled = false;
    client
      .listLanes(sessionId)
      .then((lanes) => {
        if (cancelled) return;
        store.getState().hydrateLanes(sessionId, lanes);
      })
      .catch(() => {
        // Lanes also stream over WS; failure here is non-fatal.
      });
    return () => {
      cancelled = true;
      socket.unsubscribe(sessionId);
    };
    // socket is stable across renders; including it would refire the
    // subscribe each time the store reference changes.
  }, [sessionId, client, store, socket]);

  const chat = useChat({
    store,
    sessionId,
    sendChat: socket.sendMessage,
    sendInterrupt: socket.interrupt,
  });
  const streaming = chat.status === "streaming" || chat.status === "submitted";

  const onSelectSession = (id: SessionId): void => {
    navigate(`/d/${encodeURIComponent(daemonId)}/sessions/${encodeURIComponent(id)}`);
  };

  const onFocusLane = (laneId: LaneId): void => {
    navigate(
      `/d/${encodeURIComponent(daemonId)}/sessions/${encodeURIComponent(sessionId)}/lanes/${encodeURIComponent(laneId)}`,
    );
  };

  const onKillLane = (laneId: LaneId): void => {
    void client.killLane(sessionId, laneId).catch(() => {
      // Errors surface via session.error envelopes; nothing to do here.
    });
  };

  const onSendMessage = (text: string): void => {
    chat.sendMessage(text);
    setComposerValue("");
  };

  const onReconnect = async (): Promise<void> => {
    try {
      await socket.connect();
    } catch {
      // Reconnect retries are managed by the ResilientSocket itself.
    }
  };

  const renderMessageColumn = (sid: SessionId): ReactNode => (
    <>
      <MessageLog
        store={store}
        sessionId={sid}
        renderMessage={(msg, isStreaming) => (
          <MessageBubble
            message={msg}
            streaming={isStreaming}
            renderTool={(p, st) => <ToolCard part={p} streaming={st} />}
            renderReasoning={(p) => <ReasoningCard part={p} />}
            renderPlan={(p) => <PlanCard part={p} />}
          />
        )}
      />
      <div className="flex items-end gap-2">
        <div className="flex-1">
          <Composer
            value={composerValue}
            onChange={setComposerValue}
            onSend={onSendMessage}
            streaming={streaming}
          />
        </div>
        {streaming ? (
          <div className="p-2">
            <StopButton streaming={streaming} onInterrupt={chat.stop} />
          </div>
        ) : null}
      </div>
    </>
  );

  const renderTileGridSlot = (sid: SessionId): ReactNode => (
    <TileGrid
      store={store}
      sessionId={sid}
      onFocusLane={onFocusLane}
      onKill={onKillLane}
    />
  );

  const center = (
    <div className="flex flex-col h-full">
      <ConnectionLostBanner store={store} onReconnect={onReconnect} />
      <div className="flex-1 min-h-0">
        <ChatPane
          store={store}
          sessionId={sessionId}
          renderMessageColumn={renderMessageColumn}
          renderTileGrid={renderTileGridSlot}
        />
      </div>
      <StatusBar store={store} sessionId={sessionId} />
    </div>
  );

  return (
    <div
      data-testid="route-session-view"
      data-daemon-id={daemonId}
      data-session-id={sessionId}
      className="h-screen w-screen overflow-hidden flex flex-col"
    >
      <ThreeColumnShell
        store={store}
        left={
          <SessionList
            store={store}
            activeSessionId={sessionId}
            onSelect={onSelectSession}
          />
        }
        center={center}
        right={
          <LanesSidebar
            store={store}
            sessionId={sessionId}
            onKill={onKillLane}
            onFocus={onFocusLane}
          />
        }
      />
    </div>
  );
}
