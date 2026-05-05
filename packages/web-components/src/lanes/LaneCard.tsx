// LaneCard — single lane row in the LaneSidebar.
//
// Renders status glyph + colour token, lane title, and an optional
// last-event preview. Pure presentational component; the parent owns
// subscription state.

import * as React from "react";
import type { LaneStatus } from "../types/LaneEvent";
import { statusGlyph, statusColorToken } from "./statusTokens";

export interface LaneCardProps {
  laneId: string;
  title: string;
  status: LaneStatus;
  lastEventPreview?: string;
  selected?: boolean;
  onSelect?: (laneId: string) => void;
  onKill?: (laneId: string) => void;
}

export const LaneCard: React.FC<LaneCardProps> = React.memo(function LaneCard(
  props: LaneCardProps,
) {
  const {
    laneId,
    title,
    status,
    lastEventPreview,
    selected,
    onSelect,
    onKill,
  } = props;

  const handleClick = React.useCallback(() => {
    if (onSelect) onSelect(laneId);
  }, [laneId, onSelect]);

  const handleKill = React.useCallback(
    (ev: React.MouseEvent<HTMLButtonElement>) => {
      ev.stopPropagation();
      if (onKill) onKill(laneId);
    },
    [laneId, onKill],
  );

  const colorClass = statusColorToken(status);
  const glyph = statusGlyph(status);

  return (
    <div
      role="listitem"
      aria-selected={selected ? "true" : "false"}
      data-lane-id={laneId}
      data-status={status}
      className={[
        "lane-card",
        `lane-card--${colorClass}`,
        selected ? "lane-card--selected" : "",
      ]
        .filter(Boolean)
        .join(" ")}
      onClick={handleClick}
      onKeyDown={(ev) => {
        if (ev.key === "Enter" || ev.key === " ") {
          ev.preventDefault();
          handleClick();
        }
      }}
      tabIndex={0}
    >
      <span
        className={`lane-card__glyph text-${colorClass}`}
        aria-label={`status: ${status}`}
      >
        {glyph}
      </span>
      <div className="lane-card__body">
        <div className="lane-card__title">{title}</div>
        {lastEventPreview ? (
          <div className="lane-card__preview" title={lastEventPreview}>
            {lastEventPreview}
          </div>
        ) : null}
      </div>
      {onKill && status !== "done" && status !== "cancelled" ? (
        <button
          type="button"
          className="lane-card__kill"
          aria-label={`kill lane ${title}`}
          onClick={handleKill}
        >
          ×
        </button>
      ) : null}
    </div>
  );
});
