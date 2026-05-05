// SPDX-License-Identifier: MIT
import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent, screen } from "@testing-library/react";
import { SessionList, SessionItem } from "@/components/session/SessionList";
import {
  _resetDaemonRegistryForTests,
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";
import type { SessionMetadata, SessionStatus } from "@/lib/api/types";

const NOOP = { schedule: () => 0, cancel: () => {} };
const NOW = new Date("2026-05-04T12:30:00.000Z");

function mkSession(over: Partial<SessionMetadata> = {}): SessionMetadata {
  return {
    id: "s1",
    title: "scaffold web ui",
    workdir: "/repo/web",
    model: "claude",
    status: "idle",
    createdAt: "2026-05-04T12:00:00.000Z",
    updatedAt: "2026-05-04T12:25:00.000Z",
    lastActivityAt: "2026-05-04T12:25:00.000Z",
    costUsd: 0,
    laneCount: 0,
    systemPromptPreset: null,
    ...over,
  };
}

describe("<SessionList>", () => {
  let store: DaemonStore;
  beforeEach(() => {
    _resetDaemonRegistryForTests();
    store = createDaemonStore("d1", NOOP);
  });

  it("renders the empty-state notice when no sessions exist", () => {
    render(
      <SessionList store={store} activeSessionId={null} onSelect={() => {}} />,
    );
    expect(screen.getByTestId("session-list-empty")).toBeTruthy();
    expect(screen.queryByTestId("session-list")).toBeNull();
  });

  it("renders one row per session preserving store order", () => {
    store.getState().hydrateSessions([
      mkSession({ id: "a", title: "alpha" }),
      mkSession({ id: "b", title: "bravo" }),
      mkSession({ id: "c", title: "charlie" }),
    ]);
    render(
      <SessionList
        store={store}
        activeSessionId={null}
        onSelect={() => {}}
        now={NOW}
      />,
    );
    const rows = screen.getAllByRole("link");
    expect(rows.length).toBe(3);
    expect(rows[0].getAttribute("data-testid")).toBe("session-list-item-a");
    expect(rows[2].getAttribute("data-testid")).toBe("session-list-item-c");
  });

  it("marks the active session via aria-current=page", () => {
    store.getState().hydrateSessions([
      mkSession({ id: "a" }),
      mkSession({ id: "b" }),
    ]);
    render(
      <SessionList
        store={store}
        activeSessionId="b"
        onSelect={() => {}}
        now={NOW}
      />,
    );
    const aRow = screen.getByTestId("session-list-item-a");
    const bRow = screen.getByTestId("session-list-item-b");
    expect(aRow.getAttribute("aria-current")).toBeNull();
    expect(bRow.getAttribute("aria-current")).toBe("page");
  });

  it("invokes onSelect with the session id on click", () => {
    store.getState().hydrateSessions([mkSession({ id: "a" })]);
    const onSelect = vi.fn();
    render(
      <SessionList
        store={store}
        activeSessionId={null}
        onSelect={onSelect}
        now={NOW}
      />,
    );
    fireEvent.click(screen.getByTestId("session-list-item-a"));
    expect(onSelect).toHaveBeenCalledWith("a");
  });

  it("renders relative last-activity text", () => {
    store.getState().hydrateSessions([
      mkSession({
        id: "a",
        title: "alpha",
        lastActivityAt: "2026-05-04T12:25:00.000Z",
      }),
    ]);
    render(
      <SessionList
        store={store}
        activeSessionId={null}
        onSelect={() => {}}
        now={NOW}
      />,
    );
    const rel = screen.getByTestId("session-list-item-a-relative");
    expect(rel.textContent).toMatch(/ago$/);
    expect(rel.textContent).toMatch(/5 minutes ago/);
  });

  it("falls back to workdir basename when title is null", () => {
    store.getState().hydrateSessions([
      mkSession({ id: "a", title: null, workdir: "/home/eric/code/r1-agent/" }),
    ]);
    render(
      <SessionList
        store={store}
        activeSessionId={null}
        onSelect={() => {}}
        now={NOW}
      />,
    );
    const row = screen.getByTestId("session-list-item-a");
    expect(row.textContent).toContain("r1-agent");
  });
});

describe("<SessionItem>", () => {
  it("renders a status dot test-id reflecting the session status", () => {
    const statuses: SessionStatus[] = [
      "idle",
      "thinking",
      "running",
      "waiting",
      "error",
      "completed",
    ];
    for (const status of statuses) {
      const { unmount } = render(
        <ul>
          <SessionItem
            session={mkSession({ id: `s-${status}`, status })}
            active={false}
            onSelect={() => {}}
            now={NOW}
          />
        </ul>,
      );
      expect(
        screen.getByTestId(`session-list-item-s-${status}-status-${status}`),
      ).toBeTruthy();
      unmount();
    }
  });

  it("shows 'no activity' when lastActivityAt is null", () => {
    render(
      <ul>
        <SessionItem
          session={mkSession({ id: "n", lastActivityAt: null })}
          active={false}
          onSelect={() => {}}
          now={NOW}
        />
      </ul>,
    );
    expect(screen.getByTestId("session-list-item-n-relative").textContent).toBe(
      "no activity",
    );
  });
});
