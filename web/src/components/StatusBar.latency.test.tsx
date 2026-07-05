// SPDX-License-Identifier: MIT
// Regression tests for gap audit 2026-07-05: the StatusBar latency
// segment was a dead field — its only source was a prop no caller ever
// supplied, so it rendered "— ms" forever. It must now read the live
// heartbeat RTT from the store (ui.latencyMs), with the prop as an
// optional override. Also asserts the store's setLatency dispatcher.
import { describe, it, expect, beforeEach } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { StatusBar } from "@/components/StatusBar";
import {
  _resetDaemonRegistryForTests,
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";

const NOOP = { schedule: () => 0, cancel: () => {} };

describe("StatusBar latency wiring", () => {
  let store: DaemonStore;
  beforeEach(() => {
    _resetDaemonRegistryForTests();
    store = createDaemonStore("d-lat", NOOP);
  });

  it("store.setLatency updates ui.latencyMs and dedupes no-op writes", () => {
    expect(store.getState().ui.latencyMs).toBeNull();
    store.getState().setLatency(21);
    expect(store.getState().ui.latencyMs).toBe(21);
    const before = store.getState().ui;
    store.getState().setLatency(21); // no-op: same value keeps identity
    expect(store.getState().ui).toBe(before);
    store.getState().setLatency(null);
    expect(store.getState().ui.latencyMs).toBeNull();
  });

  it("reads live heartbeat latency from the store when no prop is given", () => {
    store.getState().setLatency(17);
    render(<StatusBar store={store} />);
    expect(screen.getByTestId("status-bar-latency").textContent).toBe("17 ms");
  });

  it("updates the latency segment live as the store latency changes", () => {
    render(<StatusBar store={store} />);
    expect(screen.getByTestId("status-bar-latency").textContent).toBe("— ms");
    act(() => store.getState().setLatency(88));
    expect(screen.getByTestId("status-bar-latency").textContent).toBe("88 ms");
  });

  it("prefers an explicit latencyMs prop over the store value", () => {
    store.getState().setLatency(99);
    render(<StatusBar store={store} latencyMs={5} />);
    expect(screen.getByTestId("status-bar-latency").textContent).toBe("5 ms");
  });
});
