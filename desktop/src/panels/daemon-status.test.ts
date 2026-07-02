// SPDX-License-Identifier: MIT
//
// DaemonStatus pill tests (audit/complete-systems-2026-07-01.md A053).
//
// The pill previously listened for daemon.up/daemon.down that no host
// code emitted and ignored the app_discovery_status verb built for it,
// so it rendered "Offline (starting)" forever. These tests pin the
// fixed behaviour: one snapshot pull at mount (startup-race closer)
// plus live daemon.up/daemon.down transitions.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

type ListenerCb = (ev: { payload: unknown }) => void;

const listeners = new Map<string, ListenerCb>();
const invokeMock = vi.fn();

vi.mock("@tauri-apps/api/event", () => ({
  listen: vi.fn(async (event: string, cb: ListenerCb) => {
    listeners.set(event, cb);
    return () => {
      listeners.delete(event);
    };
  }),
}));

vi.mock("@tauri-apps/api/core", () => ({
  invoke: (...args: unknown[]) => invokeMock(...args),
  Channel: class MockChannel {
    onmessage: ((v: unknown) => void) | undefined;
  },
}));

import {
  mountDaemonStatus,
  stateFromSnapshot,
  type DaemonDiscoverySnapshot,
} from "./daemon-status";

function snapshot(
  overrides: Partial<DaemonDiscoverySnapshot>,
): DaemonDiscoverySnapshot {
  return {
    connected: false,
    pending: false,
    url: null,
    mode: null,
    error: null,
    sidecar_accepted: false,
    ...overrides,
  };
}

function makeHost(): HTMLElement {
  const el = document.createElement("div");
  document.body.appendChild(el);
  return el;
}

describe("daemon-status pill — discovery snapshot + events (A053)", () => {
  beforeEach(() => {
    listeners.clear();
    invokeMock.mockReset();
  });

  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("adopts the connected external state from app_discovery_status", async () => {
    invokeMock.mockResolvedValue(
      snapshot({ connected: true, mode: "external", url: "ws://127.0.0.1:7777" }),
    );
    const host = makeHost();
    const handle = await mountDaemonStatus(host);

    expect(invokeMock).toHaveBeenCalledWith("app_discovery_status");
    expect(host.textContent).toContain("Connected (external)");
    expect(handle.current().kind).toBe("external");
    handle.dispose();
  });

  it("renders offline with the discovery error as reason", async () => {
    invokeMock.mockResolvedValue(snapshot({ error: "daemon refused connection" }));
    const host = makeHost();
    const handle = await mountDaemonStatus(host);

    expect(handle.current()).toEqual({
      kind: "offline",
      url: "",
      reason: "daemon refused connection",
    });
    handle.dispose();
  });

  it("keeps the default starting state when the snapshot verb rejects (non-Tauri build)", async () => {
    invokeMock.mockRejectedValue(new Error("not in tauri"));
    const host = makeHost();
    const handle = await mountDaemonStatus(host);

    expect(handle.current()).toEqual({
      kind: "offline",
      url: "",
      reason: "starting",
    });
    handle.dispose();
  });

  it("keeps waiting while discovery is still pending, then flips on daemon.up", async () => {
    invokeMock.mockResolvedValue(snapshot({ pending: true }));
    const host = makeHost();
    const handle = await mountDaemonStatus(host);
    expect(handle.current().kind).toBe("offline"); // still starting

    const up = listeners.get("daemon.up");
    expect(up).toBeDefined();
    up!({ payload: { url: "ws://127.0.0.1:9001", mode: "sidecar" } });
    expect(handle.current()).toEqual({
      kind: "sidecar",
      url: "ws://127.0.0.1:9001",
    });
    expect(host.textContent).toContain("Bundled daemon");
    handle.dispose();
  });

  it("daemon.down without will_retry flips the pill to offline", async () => {
    invokeMock.mockResolvedValue(
      snapshot({ connected: true, mode: "external", url: "ws://127.0.0.1:7777" }),
    );
    const host = makeHost();
    const handle = await mountDaemonStatus(host);

    listeners.get("daemon.down")!({
      payload: { reason: "sidecar exited", will_retry: false },
    });
    expect(handle.current()).toEqual({
      kind: "offline",
      url: "ws://127.0.0.1:7777",
      reason: "sidecar exited",
    });
    handle.dispose();
  });
});

describe("daemon-status pill — reconnect status channel (A095)", () => {
  beforeEach(() => {
    listeners.clear();
    invokeMock.mockReset();
  });

  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("subscribes to transport_reconnect_status and renders derived frames", async () => {
    let channel: { onmessage?: (v: unknown) => void } | null = null;
    invokeMock.mockImplementation(
      async (cmd: string, args?: Record<string, unknown>) => {
        if (cmd === "app_discovery_status") return snapshot({ pending: true });
        if (cmd === "transport_reconnect_status") {
          channel = (args?.on_status ?? null) as never;
          return null;
        }
        throw new Error(`unexpected invoke: ${cmd}`);
      },
    );
    const host = makeHost();
    const handle = await mountDaemonStatus(host);

    expect(invokeMock).toHaveBeenCalledWith(
      "transport_reconnect_status",
      expect.objectContaining({ on_status: expect.anything() }),
    );
    expect(channel).not.toBeNull();

    channel!.onmessage?.({
      state: "reconnecting",
      attempt: 3,
      next_in_ms: 2000,
    });
    expect(handle.current()).toEqual({
      kind: "reconnecting",
      url: "",
      attempt: 3,
      nextInMs: 2000,
    });
    expect(host.textContent).toContain("Reconnecting");

    channel!.onmessage?.({ state: "offline", reason: "probe refused" });
    expect(handle.current()).toEqual({
      kind: "offline",
      url: "",
      reason: "probe refused",
    });
    handle.dispose();
  });

  it("connected frames trigger a snapshot refresh for url/mode", async () => {
    let channel: { onmessage?: (v: unknown) => void } | null = null;
    let snapshotCalls = 0;
    invokeMock.mockImplementation(
      async (cmd: string, args?: Record<string, unknown>) => {
        if (cmd === "app_discovery_status") {
          snapshotCalls += 1;
          // First call (mount): still pending. Later calls: connected.
          return snapshotCalls === 1
            ? snapshot({ pending: true })
            : snapshot({
                connected: true,
                mode: "external",
                url: "ws://127.0.0.1:7777",
              });
        }
        if (cmd === "transport_reconnect_status") {
          channel = (args?.on_status ?? null) as never;
          return null;
        }
        throw new Error(`unexpected invoke: ${cmd}`);
      },
    );
    const host = makeHost();
    const handle = await mountDaemonStatus(host);
    expect(handle.current().kind).toBe("offline"); // pending at mount

    channel!.onmessage?.({ state: "connected" });
    await vi.waitFor(() => {
      expect(handle.current()).toEqual({
        kind: "external",
        url: "ws://127.0.0.1:7777",
      });
    });
    handle.dispose();
  });
});

describe("stateFromSnapshot projection", () => {
  it("maps connected sidecar snapshots", () => {
    const s = stateFromSnapshot(
      snapshot({ connected: true, mode: "sidecar", url: "ws://1.2.3.4:1" }),
    );
    expect(s).toEqual({ kind: "sidecar", url: "ws://1.2.3.4:1" });
  });

  it("returns null while pending so the caller keeps its state", () => {
    expect(stateFromSnapshot(snapshot({ pending: true }))).toBeNull();
    expect(stateFromSnapshot(snapshot({}))).toBeNull();
  });
});
