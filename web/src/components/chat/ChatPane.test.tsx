// SPDX-License-Identifier: MIT
import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, act } from "@testing-library/react";
import { ChatPane } from "@/components/chat/ChatPane";
import {
  _resetDaemonRegistryForTests,
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";

const NOOP = { schedule: () => 0, cancel: () => {} };

describe("<ChatPane>", () => {
  let store: DaemonStore;
  beforeEach(() => {
    _resetDaemonRegistryForTests();
    store = createDaemonStore("d1", NOOP);
  });

  it("renders the message column when no lanes are pinned", () => {
    const renderCol = vi.fn(() => <div data-testid="msg-col">msgs</div>);
    const renderGrid = vi.fn(() => <div>tiles</div>);
    render(
      <ChatPane
        store={store}
        sessionId="s-1"
        renderMessageColumn={renderCol}
        renderTileGrid={renderGrid}
      />,
    );
    expect(screen.getByTestId("chat-pane-message-region")).toBeTruthy();
    expect(screen.getByTestId("msg-col")).toBeTruthy();
    expect(screen.queryByTestId("chat-pane-tile-region")).toBeNull();
    expect(renderCol).toHaveBeenCalledWith("s-1");
    expect(renderGrid).not.toHaveBeenCalled();
    expect(
      screen.getByTestId("chat-pane").getAttribute("data-tile-mode"),
    ).toBe("false");
  });

  it("renders the tile grid when at least one lane is pinned", () => {
    store.getState().pinLane("s-1", "lane-a");
    const renderCol = vi.fn(() => <div>msgs</div>);
    const renderGrid = vi.fn((_sid: string, laneIds: ReadonlyArray<string>) => (
      <div data-testid="tile-grid">{laneIds.join(",")}</div>
    ));
    render(
      <ChatPane
        store={store}
        sessionId="s-1"
        renderMessageColumn={renderCol}
        renderTileGrid={renderGrid}
      />,
    );
    expect(screen.getByTestId("chat-pane-tile-region")).toBeTruthy();
    expect(screen.getByTestId("tile-grid").textContent).toBe("lane-a");
    expect(renderGrid).toHaveBeenCalledWith("s-1", ["lane-a"]);
    expect(renderCol).not.toHaveBeenCalled();
    expect(
      screen.getByTestId("chat-pane").getAttribute("data-tile-mode"),
    ).toBe("true");
  });

  it("swaps regions when the user pins a lane after mount", () => {
    const renderCol = vi.fn(() => <div data-testid="msg-col">msgs</div>);
    const renderGrid = vi.fn((_sid: string, ids: ReadonlyArray<string>) => (
      <div data-testid="tile-grid">{ids.join(",")}</div>
    ));
    render(
      <ChatPane
        store={store}
        sessionId="s-1"
        renderMessageColumn={renderCol}
        renderTileGrid={renderGrid}
      />,
    );
    expect(screen.getByTestId("msg-col")).toBeTruthy();
    act(() => {
      store.getState().pinLane("s-1", "lane-a");
    });
    expect(screen.queryByTestId("msg-col")).toBeNull();
    expect(screen.getByTestId("tile-grid").textContent).toBe("lane-a");
  });

  it("isolates pinning state per session", () => {
    store.getState().pinLane("s-1", "lane-a");
    const renderCol = vi.fn(() => <div data-testid="msg-col">msgs</div>);
    const renderGrid = vi.fn(() => <div data-testid="tile-grid">tiles</div>);
    render(
      <ChatPane
        store={store}
        sessionId="s-2"
        renderMessageColumn={renderCol}
        renderTileGrid={renderGrid}
      />,
    );
    expect(screen.getByTestId("msg-col")).toBeTruthy();
    expect(screen.queryByTestId("tile-grid")).toBeNull();
  });

  it("forwards the active session id via data-session-id", () => {
    render(
      <ChatPane
        store={store}
        sessionId="s-9"
        renderMessageColumn={() => null}
        renderTileGrid={() => null}
      />,
    );
    expect(
      screen.getByTestId("chat-pane").getAttribute("data-session-id"),
    ).toBe("s-9");
  });
});
