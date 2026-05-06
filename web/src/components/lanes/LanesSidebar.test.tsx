// SPDX-License-Identifier: MIT
import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { LanesSidebar, LaneRow } from "@/components/lanes/LanesSidebar";
import {
  _resetDaemonRegistryForTests,
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";
import type { LaneSnapshot } from "@/lib/api/types";

const NOOP = { schedule: () => 0, cancel: () => {} };
const NOW = "2026-05-04T12:30:00.000Z";

function mkLane(over: Partial<LaneSnapshot> & { id: string }): LaneSnapshot {
  const { id, ...rest } = over;
  return {
    id,
    sessionId: "s-1",
    label: `lane ${id}`,
    state: "running",
    createdAt: NOW,
    updatedAt: NOW,
    progress: null,
    lastRender: null,
    lastSeq: 0,
    ...rest,
  } as LaneSnapshot;
}

describe("<LanesSidebar>", () => {
  let store: DaemonStore;
  beforeEach(() => {
    _resetDaemonRegistryForTests();
    store = createDaemonStore("d1", NOOP);
  });

  it("shows the empty-state notice when no lanes are registered", () => {
    render(<LanesSidebar store={store} sessionId="s-1" onKill={() => {}} />);
    expect(screen.getByTestId("lanes-sidebar-empty")).toBeTruthy();
  });

  it("renders one row per lane in store order", () => {
    store.getState().hydrateLanes("s-1", [
      mkLane({ id: "l1", label: "alpha" }),
      mkLane({ id: "l2", label: "beta" }),
      mkLane({ id: "l3", label: "gamma" }),
    ]);
    render(<LanesSidebar store={store} sessionId="s-1" onKill={() => {}} />);
    expect(screen.getByTestId("lane-row-l1")).toBeTruthy();
    expect(screen.getByTestId("lane-row-l2")).toBeTruthy();
    expect(screen.getByTestId("lane-row-l3")).toBeTruthy();
  });

  it("toggles pin state via the store actions", () => {
    store.getState().hydrateLanes("s-1", [mkLane({ id: "l1" })]);
    render(<LanesSidebar store={store} sessionId="s-1" onKill={() => {}} />);
    const pinBtn = screen.getByTestId("lane-row-l1-pin");
    expect(pinBtn.getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(pinBtn);
    expect(
      screen.getByTestId("lane-row-l1-pin").getAttribute("aria-pressed"),
    ).toBe("true");
    fireEvent.click(screen.getByTestId("lane-row-l1-pin"));
    expect(
      screen.getByTestId("lane-row-l1-pin").getAttribute("aria-pressed"),
    ).toBe("false");
  });

  it("calls onKill with the lane id when Kill is clicked", () => {
    store.getState().hydrateLanes("s-1", [mkLane({ id: "l1" })]);
    const onKill = vi.fn();
    render(<LanesSidebar store={store} sessionId="s-1" onKill={onKill} />);
    fireEvent.click(screen.getByTestId("lane-row-l1-kill"));
    expect(onKill).toHaveBeenCalledWith("l1");
  });

  it("calls onFocus with the lane id when the label is clicked", () => {
    store.getState().hydrateLanes("s-1", [mkLane({ id: "l1" })]);
    const onFocus = vi.fn();
    render(
      <LanesSidebar
        store={store}
        sessionId="s-1"
        onKill={() => {}}
        onFocus={onFocus}
      />,
    );
    fireEvent.click(screen.getByTestId("lane-row-l1-label"));
    expect(onFocus).toHaveBeenCalledWith("l1");
  });

  it("isolates lanes per session", () => {
    store.getState().hydrateLanes("s-1", [mkLane({ id: "l1" })]);
    render(<LanesSidebar store={store} sessionId="s-other" onKill={() => {}} />);
    expect(screen.getByTestId("lanes-sidebar-empty")).toBeTruthy();
  });
});

describe("<LaneRow>", () => {
  it("renders progress when the snapshot includes a [0,1] number", () => {
    render(
      <ol>
        <LaneRow
          lane={mkLane({ id: "l1", progress: 0.42 })}
          pinned={false}
          onTogglePin={() => {}}
          onKill={() => {}}
        />
      </ol>,
    );
    expect(screen.getByTestId("lane-row-l1-progress").textContent).toBe("42%");
  });

  it("hides progress when the snapshot's progress is null", () => {
    render(
      <ol>
        <LaneRow
          lane={mkLane({ id: "l1", progress: null })}
          pinned={false}
          onTogglePin={() => {}}
          onKill={() => {}}
        />
      </ol>,
    );
    expect(screen.queryByTestId("lane-row-l1-progress")).toBeNull();
  });

  it("aria-label on label button reports lane label + state", () => {
    render(
      <ol>
        <LaneRow
          lane={mkLane({ id: "l1", label: "research", state: "waiting-tool" })}
          pinned={false}
          onTogglePin={() => {}}
          onKill={() => {}}
        />
      </ol>,
    );
    expect(
      screen.getByLabelText(/Open lane research, waiting on tool/),
    ).toBeTruthy();
  });
});
