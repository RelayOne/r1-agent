// PoppedLaneApp — root mount used inside the pop-out WebviewWindow.
//
// Subscribes to a single lane's stream via the supplied subscribe()
// callback and renders <LaneDetail> full-window. The desktop entry
// (`desktop/src/popout.tsx`) reads URL params, threads them in, and
// passes a Tauri-backed subscribe; web/ surfaces can pass a WS-backed
// subscribe of the same shape.

import * as React from "react";
import type { LaneEvent, LaneStatus } from "../types/LaneEvent";
import { isStatus, isSpawned, isKilled } from "../types/LaneEvent";
import { LaneDetail } from "./LaneDetail";

export type LaneSubscribeFn = (
  sessionId: string,
  laneId: string,
  onEvent: (ev: LaneEvent) => void,
) => Promise<() => Promise<void>>;

export interface PoppedLaneAppProps {
  sessionId: string;
  laneId: string;
  initialTitle: string;
  initialStatus?: LaneStatus;
  subscribe: LaneSubscribeFn;
  onCopyLink?: (deeplink: string) => void;
  onKill?: (laneId: string) => void;
  maxEvents?: number;
}

interface LaneRuntimeState {
  title: string;
  status: LaneStatus;
  events: LaneEvent[];
}

export const PoppedLaneApp: React.FC<PoppedLaneAppProps> =
  function PoppedLaneApp(props: PoppedLaneAppProps) {
    const {
      sessionId,
      laneId,
      initialTitle,
      initialStatus = "running",
      subscribe,
      onCopyLink,
      onKill,
      maxEvents = 1024,
    } = props;

    const [state, setState] = React.useState<LaneRuntimeState>({
      title: initialTitle,
      status: initialStatus,
      events: [],
    });

    React.useEffect(() => {
      let unsub: (() => Promise<void>) | null = null;
      let cancelled = false;

      const handleEvent = (ev: LaneEvent) => {
        if (cancelled) return;
        setState((prev) => {
          let nextStatus = prev.status;
          let nextTitle = prev.title;
          if (isStatus(ev)) {
            nextStatus = ev.to;
          } else if (isSpawned(ev)) {
            nextTitle = ev.title;
          } else if (isKilled(ev)) {
            nextStatus = "cancelled";
          }
          const nextEvents = prev.events.length >= maxEvents
            ? prev.events.slice(prev.events.length - maxEvents + 1).concat(ev)
            : prev.events.concat(ev);
          return {
            title: nextTitle,
            status: nextStatus,
            events: nextEvents,
          };
        });
      };

      subscribe(sessionId, laneId, handleEvent)
        .then((teardown) => {
          if (cancelled) {
            void teardown();
            return;
          }
          unsub = teardown;
        })
        .catch((err: unknown) => {
          // Surface subscription failures via a synthetic killed event so
          // the timeline shows what happened rather than silently freezing.
          if (cancelled) return;
          const reason =
            err instanceof Error ? err.message : "subscription failed";
          handleEvent({
            kind: "killed",
            session_id: sessionId,
            lane_id: laneId,
            reason,
            at: new Date().toISOString(),
          });
        });

      return () => {
        cancelled = true;
        if (unsub) {
          void unsub();
        }
      };
    }, [sessionId, laneId, subscribe, maxEvents]);

    return (
      <div className="popped-lane-app" data-session-id={sessionId} data-lane-id={laneId}>
        <LaneDetail
          sessionId={sessionId}
          laneId={laneId}
          title={state.title}
          status={state.status}
          events={state.events}
          onCopyLink={onCopyLink}
          onKill={onKill}
        />
      </div>
    );
  };
