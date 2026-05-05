// SPDX-License-Identifier: MIT
// <SessionList> + <SessionItem> — sessions rail. Spec item 23/55.
//
// Renders the per-daemon session order from the zustand store. Each
// row exposes:
//   - a status dot (idle / thinking / running / waiting / error / completed),
//   - the title (or workdir basename if title is null),
//   - last-activity-relative ("4 min ago"),
//   - data-testid="session-list-item-<id>",
//   - aria-current="page" when the row matches the active session.
//
// The component takes an `onSelect(id)` callback rather than calling
// react-router directly, so it stays pure and easy to test. The route
// wiring lives in `src/routes/` (item 41).
import { useStore } from "zustand";
import type { ReactElement } from "react";
import type { DaemonStore } from "@/lib/store/daemonStore";
import type { SessionId, SessionMetadata, SessionStatus } from "@/lib/api/types";
import { cn } from "@/lib/utils";

const STATUS_DOT_CLASS: Record<SessionStatus, string> = {
  idle: "bg-slate-400",
  thinking: "bg-amber-500 animate-pulse",
  running: "bg-emerald-500 animate-pulse",
  waiting: "bg-sky-500",
  error: "bg-rose-500",
  completed: "bg-violet-500",
};

const STATUS_LABEL: Record<SessionStatus, string> = {
  idle: "Idle",
  thinking: "Thinking",
  running: "Running",
  waiting: "Waiting for input",
  error: "Error",
  completed: "Completed",
};

function basename(path: string): string {
  const trimmed = path.replace(/\/+$/, "");
  const i = trimmed.lastIndexOf("/");
  return i >= 0 ? trimmed.slice(i + 1) : trimmed;
}

function relativeTime(ts: string | null, now?: Date): string {
  if (!ts) return "no activity";
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return "no activity";
  const diffMs = (now ? now.getTime() : Date.now()) - d.getTime();
  const seconds = Math.floor(Math.abs(diffMs) / 1000);
  if (diffMs < 0 || seconds === 0) return "just now";
  if (seconds < 60) return `${seconds} second${seconds === 1 ? "" : "s"} ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} minute${minutes === 1 ? "" : "s"} ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} hour${hours === 1 ? "" : "s"} ago`;
  const days = Math.floor(hours / 24);
  return `${days} day${days === 1 ? "" : "s"} ago`;
}

export interface SessionItemProps {
  session: SessionMetadata;
  active: boolean;
  onSelect: (id: SessionId) => void;
  /** Override clock for deterministic tests / stories. */
  now?: Date;
}

export function SessionItem({
  session,
  active,
  onSelect,
  now,
}: SessionItemProps): ReactElement {
  const display = session.title ?? basename(session.workdir);
  const statusKey = session.status;
  const statusLabel = STATUS_LABEL[statusKey];
  const dotClass = STATUS_DOT_CLASS[statusKey];

  return (
    <li role="none">
      <button
        type="button"
        role="link"
        onClick={() => onSelect(session.id)}
        data-testid={`session-list-item-${session.id}`}
        aria-label={`Open session ${display}, status ${statusLabel}`}
        aria-current={active ? "page" : undefined}
        className={cn(
          "w-full text-left flex items-center gap-2 px-3 py-2 rounded-md text-sm transition-colors",
          "hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
          active && "bg-muted",
        )}
      >
        <span
          aria-hidden="true"
          className={cn(
            "inline-block w-2 h-2 rounded-full shrink-0",
            dotClass,
          )}
          data-testid={`session-list-item-${session.id}-status-${statusKey}`}
        />
        <span className="flex-1 min-w-0 truncate">{display}</span>
        <span
          className="text-xs text-muted-foreground shrink-0"
          data-testid={`session-list-item-${session.id}-relative`}
        >
          {relativeTime(session.lastActivityAt, now)}
        </span>
      </button>
    </li>
  );
}

export interface SessionListProps {
  store: DaemonStore;
  activeSessionId: SessionId | null;
  onSelect: (id: SessionId) => void;
  /** Override clock for deterministic tests / stories. */
  now?: Date;
}

export function SessionList({
  store,
  activeSessionId,
  onSelect,
  now,
}: SessionListProps): ReactElement {
  const order = useStore(store, (s) => s.sessions.order);
  const byId = useStore(store, (s) => s.sessions.byId);

  if (order.length === 0) {
    return (
      <div
        className="p-4 text-sm text-muted-foreground"
        data-testid="session-list-empty"
        role="status"
      >
        No sessions yet.
      </div>
    );
  }

  return (
    <ul
      role="list"
      aria-label="Sessions"
      data-testid="session-list"
      className="p-1 space-y-0.5 overflow-y-auto h-full"
    >
      {order.map((id) => {
        const session = byId[id];
        if (!session) return null;
        return (
          <SessionItem
            key={id}
            session={session}
            active={id === activeSessionId}
            onSelect={onSelect}
            now={now}
          />
        );
      })}
    </ul>
  );
}
