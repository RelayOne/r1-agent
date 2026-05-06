// SPDX-License-Identifier: MIT
import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { ConnectionLostBanner } from "@/components/ConnectionLostBanner";
import {
  _resetDaemonRegistryForTests,
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";

const NOOP = { schedule: () => 0, cancel: () => {} };

describe("<ConnectionLostBanner>", () => {
  let store: DaemonStore;
  beforeEach(() => {
    _resetDaemonRegistryForTests();
    store = createDaemonStore("d1", NOOP);
  });

  it("renders nothing when hardCapped is false", () => {
    render(<ConnectionLostBanner store={store} onReconnect={() => {}} />);
    expect(screen.queryByTestId("connection-lost-banner")).toBeNull();
  });

  it("renders the destructive banner when hardCapped flips true", () => {
    store.getState().setHardCapped(true);
    render(<ConnectionLostBanner store={store} onReconnect={() => {}} />);
    const banner = screen.getByTestId("connection-lost-banner");
    expect(banner.getAttribute("role")).toBe("alert");
    expect(banner.getAttribute("aria-live")).toBe("assertive");
  });

  it("invokes onReconnect when the Reconnect button is clicked", () => {
    store.getState().setHardCapped(true);
    const onReconnect = vi.fn();
    render(
      <ConnectionLostBanner store={store} onReconnect={onReconnect} />,
    );
    fireEvent.click(screen.getByTestId("connection-lost-banner-reconnect"));
    expect(onReconnect).toHaveBeenCalledTimes(1);
  });

  it("renders a custom message when provided", () => {
    store.getState().setHardCapped(true);
    render(
      <ConnectionLostBanner
        store={store}
        onReconnect={() => {}}
        message="The daemon crashed; restart r1 serve to continue."
      />,
    );
    expect(
      screen.getByTestId("connection-lost-banner-message").textContent,
    ).toContain("daemon crashed");
  });

  it("re-renders / disappears when hardCapped flips back to false", () => {
    store.getState().setHardCapped(true);
    const { rerender } = render(
      <ConnectionLostBanner store={store} onReconnect={() => {}} />,
    );
    expect(screen.getByTestId("connection-lost-banner")).toBeTruthy();
    store.getState().setHardCapped(false);
    rerender(<ConnectionLostBanner store={store} onReconnect={() => {}} />);
    expect(screen.queryByTestId("connection-lost-banner")).toBeNull();
  });
});
