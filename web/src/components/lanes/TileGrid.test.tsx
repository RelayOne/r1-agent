// SPDX-License-Identifier: MIT
import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { TileGrid } from "@/components/lanes/TileGrid";
import {
  _resetDaemonRegistryForTests,
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";
import type { LaneSnapshot } from "@/lib/api/types";

const NOOP = { schedule: () => 0, cancel: () => {} };
const NOW = "2026-05-04T12:30:00.000Z";

function mkLane(id: string): LaneSnapshot {
  return {
    id,
    sessionId: "s-1",
    label: id,
    state: "running",
    createdAt: NOW,
    updatedAt: NOW,
    progress: null,
    lastRender: `frame for ${id}`,
    lastSeq: 0,
  };
}

function seed(store: DaemonStore, ids: string[]): void {
  store.getState().hydrateLanes("s-1", ids.map(mkLane));
  for (const id of ids) store.getState().pinLane("s-1", id);
}

describe("<TileGrid>", () => {
  let store: DaemonStore;
  beforeEach(() => {
    _resetDaemonRegistryForTests();
    store = createDaemonStore("d1", NOOP);
  });

  it("renders the empty notice when no tiles are pinned", () => {
    render(<TileGrid store={store} sessionId="s-1" onKill={() => {}} />);
    expect(screen.getByTestId("tile-grid-empty")).toBeTruthy();
  });

  it("renders one tile per pinned lane in store order", () => {
    seed(store, ["a", "b", "c"]);
    render(<TileGrid store={store} sessionId="s-1" onKill={() => {}} />);
    expect(screen.getByTestId("tile-grid-tile-a")).toBeTruthy();
    expect(screen.getByTestId("tile-grid-tile-b")).toBeTruthy();
    expect(screen.getByTestId("tile-grid-tile-c")).toBeTruthy();
    expect(
      screen.getByTestId("tile-grid").getAttribute("data-tile-count"),
    ).toBe("3");
  });

  it("collapse toggle sets data-collapsed and aria-expanded", () => {
    seed(store, ["a"]);
    render(<TileGrid store={store} sessionId="s-1" onKill={() => {}} />);
    const tile = screen.getByTestId("tile-grid-tile-a");
    const toggle = screen.getByTestId("tile-grid-tile-a-collapse");
    expect(tile.getAttribute("data-collapsed")).toBe("false");
    fireEvent.click(toggle);
    expect(tile.getAttribute("data-collapsed")).toBe("true");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByTestId("tile-grid-tile-a-body")).toBeNull();
  });

  it("unpin removes the tile from the grid", () => {
    seed(store, ["a", "b"]);
    render(<TileGrid store={store} sessionId="s-1" onKill={() => {}} />);
    fireEvent.click(screen.getByTestId("tile-grid-tile-a-unpin"));
    expect(screen.queryByTestId("tile-grid-tile-a")).toBeNull();
    expect(screen.getByTestId("tile-grid-tile-b")).toBeTruthy();
  });

  it("Cmd+Shift+ArrowRight reorders the tile to the right", () => {
    seed(store, ["a", "b", "c"]);
    render(<TileGrid store={store} sessionId="s-1" onKill={() => {}} />);
    const header = screen.getByTestId("tile-grid-tile-a-header");
    fireEvent.keyDown(header, { key: "ArrowRight", metaKey: true, shiftKey: true });
    const order = store.getState().ui.tilePinnedBySession["s-1"] ?? [];
    expect(order).toEqual(["b", "a", "c"]);
  });

  it("Ctrl+Shift+ArrowLeft reorders the tile to the left", () => {
    seed(store, ["a", "b", "c"]);
    render(<TileGrid store={store} sessionId="s-1" onKill={() => {}} />);
    const header = screen.getByTestId("tile-grid-tile-c-header");
    fireEvent.keyDown(header, { key: "ArrowLeft", ctrlKey: true, shiftKey: true });
    const order = store.getState().ui.tilePinnedBySession["s-1"] ?? [];
    expect(order).toEqual(["a", "c", "b"]);
  });

  it("Enter on a tile header invokes onFocusLane", () => {
    seed(store, ["a"]);
    const onFocus = vi.fn();
    render(
      <TileGrid
        store={store}
        sessionId="s-1"
        onKill={() => {}}
        onFocusLane={onFocus}
      />,
    );
    fireEvent.keyDown(screen.getByTestId("tile-grid-tile-a-header"), {
      key: "Enter",
    });
    expect(onFocus).toHaveBeenCalledWith("a");
  });

  it("double-click on a tile header invokes onFocusLane", () => {
    seed(store, ["a"]);
    const onFocus = vi.fn();
    render(
      <TileGrid
        store={store}
        sessionId="s-1"
        onKill={() => {}}
        onFocusLane={onFocus}
      />,
    );
    fireEvent.doubleClick(screen.getByTestId("tile-grid-tile-a-header"));
    expect(onFocus).toHaveBeenCalledWith("a");
  });

  it("aria-grabbed flips on the dragged source during drag", () => {
    seed(store, ["a", "b"]);
    render(<TileGrid store={store} sessionId="s-1" onKill={() => {}} />);
    const headerA = screen.getByTestId("tile-grid-tile-a-header");
    const dt = {
      effectAllowed: "",
      setData: vi.fn(),
      getData: vi.fn().mockReturnValue("a"),
      dropEffect: "",
    };
    fireEvent.dragStart(headerA, { dataTransfer: dt });
    expect(headerA.getAttribute("aria-grabbed")).toBe("true");
    fireEvent.dragEnd(headerA, { dataTransfer: dt });
    expect(headerA.getAttribute("aria-grabbed")).toBe("false");
  });
});
