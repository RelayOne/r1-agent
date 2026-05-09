// SPDX-License-Identifier: MIT
// Daemon home page — three-column shell with the session list on the
// left, an empty-state center, and the lanes sidebar on the right.
// Spec item 41/55 (DaemonHome slot in buildRouter).
//
// Audit ref: closes the audit finding that <ThreeColumnShell> +
// <SessionList> + <ConnectionLostBanner> + <StatusBar> never mounted
// because nothing routed to them.
//
// The page hydrates the session list on mount via R1dClient.listSessions
// and opens the WS via useDaemonSocket. It does not mount any
// session-scoped state — that lives on /d/:daemonId/sessions/:sessionId.
import { useEffect, useState } from "react";
import type { ReactElement } from "react";
import { useNavigate } from "react-router-dom";
import { ThreeColumnShell } from "@/components/layout/ThreeColumnShell";
import { SessionList } from "@/components/session/SessionList";
import { ConnectionLostBanner } from "@/components/ConnectionLostBanner";
import { StatusBar } from "@/components/StatusBar";
import { NewSessionDialog } from "@/components/session/NewSessionDialog";
import { Button } from "@/components/ui/button";
import { useDaemonSocket } from "@/hooks/useDaemonSocket";
import { useDaemonStore } from "@/app/DaemonStoreProvider";
import { useR1dClient, useR1dClientInfo } from "@/app/R1dClientProvider";
import { NotFound } from "@/routes/pages/NotFound";
import type { CreateSessionRequest, SessionId } from "@/lib/api/types";

const AVAILABLE_MODELS: ReadonlyArray<{ value: string; label: string }> = [
  { value: "claude-opus-4-7", label: "Opus 4.7" },
  { value: "claude-sonnet-4-6", label: "Sonnet 4.6" },
];

export interface DaemonHomeProps {
  daemonId: string;
}

export function DaemonHome({ daemonId }: DaemonHomeProps): ReactElement {
  const info = useR1dClientInfo(daemonId);
  if (!info) return <NotFound />;
  return <DaemonHomeContent daemonId={daemonId} />;
}

function DaemonHomeContent({ daemonId }: DaemonHomeProps): ReactElement {
  const navigate = useNavigate();
  const store = useDaemonStore(daemonId);
  const client = useR1dClient(daemonId);
  const socket = useDaemonSocket({ client, store });
  const [dialogOpen, setDialogOpen] = useState<boolean>(false);
  const [hydrateError, setHydrateError] = useState<string | null>(null);

  // Initial hydration: pull the session list once on mount. Updates
  // arrive via session.updated envelopes routed by the socket.
  useEffect(() => {
    let cancelled = false;
    client
      .listSessions()
      .then((rows) => {
        if (cancelled) return;
        store.getState().hydrateSessions(rows);
      })
      .catch((e: unknown) => {
        if (cancelled) return;
        setHydrateError(
          e instanceof Error ? e.message : "Failed to load sessions",
        );
      });
    return () => {
      cancelled = true;
    };
  }, [client, store]);

  const onSelectSession = (id: SessionId): void => {
    navigate(`/d/${encodeURIComponent(daemonId)}/sessions/${encodeURIComponent(id)}`);
  };

  const onCreate = async (req: CreateSessionRequest): Promise<void> => {
    const meta = await client.createSession(req);
    store.getState().hydrateSessions([meta]);
    onSelectSession(meta.id);
  };

  const onReconnect = async (): Promise<void> => {
    try {
      await socket.connect();
    } catch {
      // Connect errors surface via onStateChange / onHardCap into the
      // store; the user can press Reconnect again if it stays down.
    }
  };

  const center = (
    <div className="flex flex-col h-full">
      <ConnectionLostBanner store={store} onReconnect={onReconnect} />
      <div
        data-testid="daemon-home-empty-state"
        role="status"
        className="flex-1 flex items-center justify-center p-6 text-sm text-muted-foreground text-center"
      >
        <div className="space-y-3">
          <h2 className="text-lg font-medium text-foreground">
            Pick or start a session
          </h2>
          <p>Choose a session from the left rail, or create a new one.</p>
          {hydrateError ? (
            <p
              role="alert"
              data-testid="daemon-home-hydrate-error"
              className="text-destructive"
            >
              {hydrateError}
            </p>
          ) : null}
          <Button
            type="button"
            onClick={() => setDialogOpen(true)}
            data-testid="daemon-home-new-session"
          >
            New session
          </Button>
        </div>
      </div>
      <StatusBar store={store} sessionId={null} />
    </div>
  );

  return (
    <div
      data-testid="route-daemon-home"
      data-daemon-id={daemonId}
      className="h-screen w-screen overflow-hidden flex flex-col"
    >
      <ThreeColumnShell
        store={store}
        left={
          <SessionList
            store={store}
            activeSessionId={null}
            onSelect={onSelectSession}
          />
        }
        center={center}
        right={
          <div className="p-3 text-sm text-muted-foreground">
            Open a session to see its lanes.
          </div>
        }
      />
      <NewSessionDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        onCreate={onCreate}
        models={AVAILABLE_MODELS}
      />
    </div>
  );
}
