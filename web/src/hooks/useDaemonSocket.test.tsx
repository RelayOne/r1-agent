// SPDX-License-Identifier: MIT
// Tests for useDaemonSocket. We mock R1dClient and verify that
// envelope routing, state-change forwarding, hard-cap surfacing,
// and the subscribe/send/interrupt action wrappers do what the
// spec requires.
import React from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, act } from "@testing-library/react";
import {
  _resetDaemonRegistryForTests,
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";
import { useDaemonSocket } from "@/hooks/useDaemonSocket";
import type { ConnectOptions, R1dClient } from "@/lib/api/r1d";
import type { ResilientSocketState } from "@/lib/api/ws";
import type { WsServerEnvelope } from "@/lib/api/types";

const NOW = "2026-05-04T12:00:00.000Z";

// Hand-rolled fake client that captures the connect callbacks so the
// test can drive envelopes / state changes synchronously.
function makeFakeClient(): {
  client: R1dClient;
  fire: (env: WsServerEnvelope) => void;
  fireState: (state: ResilientSocketState) => void;
  fireHardCap: () => void;
  subscribeCalls: Array<{ sid: string; lastEventId?: number }>;
  unsubscribeCalls: string[];
  sendCalls: Array<{ sid: string; content: string }>;
  interruptCalls: string[];
  closeCalls: number;
  connectCalls: number;
} {
  let storedOpts: ConnectOptions | undefined;
  const subscribeCalls: Array<{ sid: string; lastEventId?: number }> = [];
  const unsubscribeCalls: string[] = [];
  const sendCalls: Array<{ sid: string; content: string }> = [];
  const interruptCalls: string[] = [];
  let closeCalls = 0;
  let connectCalls = 0;

  const client = {
    async connect(opts: ConnectOptions): Promise<void> {
      connectCalls += 1;
      storedOpts = opts;
    },
    subscribe(sid: string, lastEventId?: number): void {
      const entry: { sid: string; lastEventId?: number } = { sid };
      if (lastEventId !== undefined) entry.lastEventId = lastEventId;
      subscribeCalls.push(entry);
    },
    unsubscribe(sid: string): void {
      unsubscribeCalls.push(sid);
    },
    sendMessage(sid: string, content: string): void {
      sendCalls.push({ sid, content });
    },
    interrupt(sid: string): void {
      interruptCalls.push(sid);
    },
    close(): void {
      closeCalls += 1;
    },
  } as unknown as R1dClient;

  return {
    client,
    fire(env: WsServerEnvelope) {
      storedOpts?.onEnvelope(env);
    },
    fireState(state) {
      storedOpts?.onStateChange?.(state, "test");
    },
    fireHardCap() {
      storedOpts?.onHardCap?.("hard-cap");
    },
    subscribeCalls,
    unsubscribeCalls,
    sendCalls,
    interruptCalls,
    get closeCalls() { return closeCalls; },
    get connectCalls() { return connectCalls; },
  };
}

interface HostProps {
  client: R1dClient;
  store: DaemonStore;
  api: { current: ReturnType<typeof useDaemonSocket> | null };
}
function Host({ client, store, api }: HostProps): React.ReactElement {
  const r = useDaemonSocket({ client, store });
  api.current = r;
  return <div />;
}

describe("useDaemonSocket", () => {
  beforeEach(() => _resetDaemonRegistryForTests());

  it("connects on mount and routes envelopes into the store", async () => {
    const fake = makeFakeClient();
    const store = createDaemonStore("d1", { schedule: () => 0, cancel: () => {} });
    const api: HostProps["api"] = { current: null };
    await act(async () => {
      render(<Host client={fake.client} store={store} api={api} />);
    });
    expect(fake.connectCalls).toBe(1);
    // Drive a lane.created envelope; store should pick it up.
    await act(async () => {
      fake.fire({
        type: "lane.created",
        seq: 1,
        ts: NOW,
        sessionId: "s1",
        lane: {
          id: "L1",
          sessionId: "s1",
          label: "lane",
          state: "queued",
          createdAt: NOW,
          updatedAt: NOW,
          progress: null,
          lastRender: null,
          lastSeq: 0,
        },
      });
    });
    expect(store.getState().lanes.byKey["s1:L1"]?.id).toBe("L1");
  });

  it("forwards onStateChange into store.setConnectionState", async () => {
    const fake = makeFakeClient();
    const store = createDaemonStore("d2", { schedule: () => 0, cancel: () => {} });
    const api: HostProps["api"] = { current: null };
    await act(async () => {
      render(<Host client={fake.client} store={store} api={api} />);
    });
    await act(async () => fake.fireState("connecting"));
    expect(store.getState().ui.connectionState).toBe("connecting");
    await act(async () => fake.fireState("open"));
    expect(store.getState().ui.connectionState).toBe("open");
  });

  it("opening clears any prior hard-cap flag", async () => {
    const fake = makeFakeClient();
    const store = createDaemonStore("d3", { schedule: () => 0, cancel: () => {} });
    store.getState().setHardCapped(true);
    const api: HostProps["api"] = { current: null };
    await act(async () => {
      render(<Host client={fake.client} store={store} api={api} />);
    });
    await act(async () => fake.fireState("open"));
    expect(store.getState().ui.hardCapped).toStrictEqual(false);
  });

  it("hard-cap callback flips store.ui.hardCapped to true", async () => {
    const fake = makeFakeClient();
    const store = createDaemonStore("d4", { schedule: () => 0, cancel: () => {} });
    const api: HostProps["api"] = { current: null };
    await act(async () => {
      render(<Host client={fake.client} store={store} api={api} />);
    });
    expect(store.getState().ui.hardCapped).toStrictEqual(false);
    await act(async () => fake.fireHardCap());
    expect(store.getState().ui.hardCapped).toStrictEqual(true);
  });

  it("subscribe marks store and forwards lastEventId from the store", async () => {
    const fake = makeFakeClient();
    const store = createDaemonStore("d5", { schedule: () => 0, cancel: () => {} });
    // Seed a lastSeq so subscribe replays.
    store.getState().applyEnvelope({
      type: "session.updated",
      seq: 42,
      ts: NOW,
      sessionId: "s1",
      patch: {},
    });
    const api: HostProps["api"] = { current: null };
    await act(async () => {
      render(<Host client={fake.client} store={store} api={api} />);
    });
    act(() => api.current!.subscribe("s1"));
    expect(store.getState().sessions.subscribed.has("s1")).toStrictEqual(true);
    expect(fake.subscribeCalls).toEqual([{ sid: "s1", lastEventId: 42 }]);
  });

  it("unsubscribe clears the subscribed flag and tells the client", async () => {
    const fake = makeFakeClient();
    const store = createDaemonStore("d6", { schedule: () => 0, cancel: () => {} });
    store.getState().markSubscribed("s1");
    const api: HostProps["api"] = { current: null };
    await act(async () => {
      render(<Host client={fake.client} store={store} api={api} />);
    });
    act(() => api.current!.unsubscribe("s1"));
    expect(store.getState().sessions.subscribed.has("s1")).toStrictEqual(false);
    expect(fake.unsubscribeCalls).toEqual(["s1"]);
  });

  it("sendMessage and interrupt forward to the client", async () => {
    const fake = makeFakeClient();
    const store = createDaemonStore("d7", { schedule: () => 0, cancel: () => {} });
    const api: HostProps["api"] = { current: null };
    await act(async () => {
      render(<Host client={fake.client} store={store} api={api} />);
    });
    act(() => api.current!.sendMessage("s1", "hello"));
    act(() => api.current!.interrupt("s1"));
    expect(fake.sendCalls).toEqual([{ sid: "s1", content: "hello" }]);
    expect(fake.interruptCalls).toEqual(["s1"]);
  });

  it("error envelopes invoke the onError callback exactly once each", async () => {
    const errors: WsServerEnvelope[] = [];
    expect(errors).toHaveLength(0);
    const fake = makeFakeClient();
    const store = createDaemonStore("d8", { schedule: () => 0, cancel: () => {} });
    const HostE = (): React.ReactElement => {
      useDaemonSocket({ client: fake.client, store, onError: (e) => errors.push(e) });
      return <div />;
    };
    await act(async () => { render(<HostE />); });
    await act(async () => fake.fire({
      type: "error",
      seq: 1,
      ts: NOW,
      code: "INTERNAL",
      message: "kaboom",
      retryable: false,
    }));
    expect(errors).toHaveLength(1);
  });

  it("autoConnect=false skips connect() until the consumer triggers it", async () => {
    const fake = makeFakeClient();
    const store = createDaemonStore("d9", { schedule: () => 0, cancel: () => {} });
    function HostManual(): React.ReactElement {
      useDaemonSocket({ client: fake.client, store, autoConnect: false });
      return <div />;
    }
    await act(async () => {
      render(<HostManual />);
    });
    expect(fake.connectCalls).toBe(0);
  });

  it("unmount triggers a deliberate close on the client", async () => {
    const fake = makeFakeClient();
    const store = createDaemonStore("d10", { schedule: () => 0, cancel: () => {} });
    const api: HostProps["api"] = { current: null };
    let unmount: () => void = () => {};
    await act(async () => {
      const result = render(<Host client={fake.client} store={store} api={api} />);
      unmount = result.unmount;
    });
    const before = fake.closeCalls;
    act(() => unmount());
    expect(fake.closeCalls - before).toBeGreaterThanOrEqual(1);
    // Silence unused warning.
    void vi;
  });
});
