// SPDX-License-Identifier: MIT
// Single-lane focus view. Spec item 41/55 (LaneFocus slot).
//
// Renders one <LaneTile> at full width with a Back button that
// navigates back to the parent SessionView. The LaneTile already
// reads its render-string + state from the store, so this page is a
// thin shell.
//
// Audit ref: closes the audit finding that <LaneTile>'s focus mode
// (Maximize2) had no destination route.
import { useEffect } from "react";
import type { ReactElement } from "react";
import { useNavigate } from "react-router-dom";
import { ArrowLeft } from "lucide-react";
import { Button } from "@/components/ui/button";
import { LaneTile } from "@/components/lanes/LaneTile";
import { ConnectionLostBanner } from "@/components/ConnectionLostBanner";
import { StatusBar } from "@/components/StatusBar";
import { useDaemonSocket } from "@/hooks/useDaemonSocket";
import { useDaemonStore } from "@/app/DaemonStoreProvider";
import { useR1dClient, useR1dClientInfo } from "@/app/R1dClientProvider";
import { NotFound } from "@/routes/pages/NotFound";
import type { LaneId } from "@/lib/api/types";

export interface LaneFocusProps {
  daemonId: string;
  sessionId: string;
  laneId: string;
}

export function LaneFocus(props: LaneFocusProps): ReactElement {
  const info = useR1dClientInfo(props.daemonId);
  if (!info) return <NotFound />;
  return <LaneFocusContent {...props} />;
}

function LaneFocusContent({
  daemonId,
  sessionId,
  laneId,
}: LaneFocusProps): ReactElement {
  const navigate = useNavigate();
  const store = useDaemonStore(daemonId);
  const client = useR1dClient(daemonId);
  const socket = useDaemonSocket({ client, store });

  useEffect(() => {
    socket.subscribe(sessionId);
    return () => {
      socket.unsubscribe(sessionId);
    };
  }, [sessionId, socket]);

  const onBack = (): void => {
    navigate(`/d/${encodeURIComponent(daemonId)}/sessions/${encodeURIComponent(sessionId)}`);
  };

  const onKill = (lid: LaneId): void => {
    void client.killLane(sessionId, lid).catch(() => undefined);
  };

  const onUnpin = (lid: LaneId): void => {
    store.getState().unpinLane(sessionId, lid);
  };

  const onReconnect = async (): Promise<void> => {
    try {
      await socket.connect();
    } catch {
      // ResilientSocket handles retry; surface via store flags.
    }
  };

  return (
    <div
      data-testid="route-lane-focus"
      data-daemon-id={daemonId}
      data-session-id={sessionId}
      data-lane-id={laneId}
      className="h-screen w-screen overflow-hidden flex flex-col bg-background text-foreground"
    >
      <ConnectionLostBanner store={store} onReconnect={onReconnect} />
      <header className="flex items-center gap-2 p-2 border-b border-border">
        <Button
          type="button"
          size="sm"
          variant="ghost"
          onClick={onBack}
          data-testid="lane-focus-back"
          aria-label="Back to session"
        >
          <ArrowLeft className="w-3 h-3 mr-1" aria-hidden="true" />
          Back
        </Button>
        <h1 className="text-sm font-mono">{laneId}</h1>
      </header>
      <div className="flex-1 min-h-0 p-2">
        <LaneTile
          store={store}
          sessionId={sessionId}
          laneId={laneId}
          onUnpin={onUnpin}
          onKill={onKill}
        />
      </div>
      <StatusBar store={store} sessionId={sessionId} />
    </div>
  );
}
