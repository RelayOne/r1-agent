// LaneDetail — focus view for a single lane.
//
// Shows the full event timeline (most-recent first), kill button, and
// a copy-link button that emits a deeplink string callers can stuff
// into the clipboard. Pure presentational — parent owns subscription.

import * as React from "react";
import type { LaneEvent, LaneStatus } from "../types/LaneEvent";
import { isDelta, isStatus, isSpawned, isKilled, isDeltaGap } from "../types/LaneEvent";
import { statusGlyph, statusColorToken } from "./statusTokens";

export interface LaneDetailProps {
  sessionId: string;
  laneId: string;
  title: string;
  status: LaneStatus;
  events: LaneEvent[];
  onKill?: (laneId: string) => void;
  onCopyLink?: (deeplink: string) => void;
}

function describeEvent(ev: LaneEvent): string {
  if (isDelta(ev)) {
    const kind = String(ev.payload.kind ?? "delta");
    return `[seq ${ev.seq}] ${kind}`;
  }
  if (isStatus(ev)) {
    return `status: ${ev.from} → ${ev.to}`;
  }
  if (isSpawned(ev)) {
    return `spawned: ${ev.title}`;
  }
  if (isKilled(ev)) {
    return `killed: ${ev.reason}`;
  }
  if (isDeltaGap(ev)) {
    return `gap after seq ${ev.last_seen_seq}`;
  }
  return "unknown event";
}

function eventTimestamp(ev: LaneEvent): string {
  if (isStatus(ev) || isSpawned(ev) || isKilled(ev) || isDeltaGap(ev)) {
    return ev.at;
  }
  return "";
}

export const LaneDetail: React.FC<LaneDetailProps> = function LaneDetail(
  props: LaneDetailProps,
) {
  const { sessionId, laneId, title, status, events, onKill, onCopyLink } =
    props;

  // Reverse for chronological-newest-first display, but don't mutate input.
  const ordered = React.useMemo(() => events.slice().reverse(), [events]);

  const handleKill = React.useCallback(() => {
    if (onKill) onKill(laneId);
  }, [laneId, onKill]);

  const handleCopy = React.useCallback(() => {
    const deeplink = `r1://session/${sessionId}/lane/${laneId}`;
    if (onCopyLink) {
      onCopyLink(deeplink);
    }
  }, [sessionId, laneId, onCopyLink]);

  const colorClass = statusColorToken(status);
  const glyph = statusGlyph(status);

  return (
    <section
      className="lane-detail"
      data-session-id={sessionId}
      data-lane-id={laneId}
      aria-label={`lane detail: ${title}`}
    >
      <header className="lane-detail__header">
        <span
          className={`lane-detail__glyph text-${colorClass}`}
          aria-label={`status: ${status}`}
        >
          {glyph}
        </span>
        <h2 className="lane-detail__title">{title}</h2>
        <div className="lane-detail__actions">
          <button
            type="button"
            className="lane-detail__copy"
            onClick={handleCopy}
            aria-label="copy lane link"
          >
            Copy link
          </button>
          {onKill && status !== "done" && status !== "cancelled" ? (
            <button
              type="button"
              className="lane-detail__kill"
              onClick={handleKill}
              aria-label="kill lane"
            >
              Kill
            </button>
          ) : null}
        </div>
      </header>
      {ordered.length === 0 ? (
        <div className="lane-detail__empty">No events yet.</div>
      ) : (
        <ol className="lane-detail__timeline" aria-label="event timeline">
          {ordered.map((ev, idx) => {
            const ts = eventTimestamp(ev);
            return (
              <li
                key={`${ev.kind}-${idx}-${"seq" in ev ? ev.seq : ts}`}
                className={`lane-detail__event lane-detail__event--${ev.kind}`}
              >
                {ts ? <time dateTime={ts}>{ts}</time> : null}
                <span className="lane-detail__event-text">
                  {describeEvent(ev)}
                </span>
              </li>
            );
          })}
        </ol>
      )}
    </section>
  );
};
