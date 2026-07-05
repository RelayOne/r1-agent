// SPDX-License-Identifier: MIT
// Tests for ResilientSocket heartbeat round-trip latency measurement.
//
// Regression for gap audit 2026-07-05: the StatusBar advertised a live
// latency segment that was never wired — the socket sent ping/pong but
// never computed an RTT. These tests drive the heartbeat with a fake
// WebSocket + injected clock and assert onLatency reports the real RTT.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { ResilientSocket } from "@/lib/api/ws";
import type { AuthClient } from "@/lib/api/auth";

const TS = "2026-05-04T12:00:00.000Z";

// Minimal fake WebSocket. Fires onopen on the next microtask so
// `await connect()` resolves; records every frame the socket sends.
class FakeWebSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;

  readyState = FakeWebSocket.OPEN;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  sent: string[] = [];

  static last: FakeWebSocket | undefined;

  constructor(
    public url: string,
    public protocols?: string | string[],
  ) {
    FakeWebSocket.last = this;
    queueMicrotask(() => this.onopen?.());
  }

  send(data: string): void {
    this.sent.push(data);
  }

  close(code = 1000, reason = "closed"): void {
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.({ code, reason });
  }

  // Test helper: deliver a server frame.
  deliver(obj: unknown): void {
    this.onmessage?.({ data: JSON.stringify(obj) });
  }
}

const fakeAuth = {
  mintWsTicket: async () => ({ token: "test-token", expiresAt: TS }),
  refresh: async () => ({ token: "test-token", expiresAt: TS }),
} as unknown as AuthClient;

function makeSocket(opts: {
  now: () => number;
  onLatency: (ms: number) => void;
  heartbeatIntervalMs?: number;
}): ResilientSocket {
  return new ResilientSocket({
    wsUrl: "ws://test/socket",
    auth: fakeAuth,
    onEnvelope: () => {},
    webSocketImpl: FakeWebSocket as unknown as typeof WebSocket,
    now: opts.now,
    onLatency: opts.onLatency,
    heartbeatIntervalMs: opts.heartbeatIntervalMs ?? 1_000,
    pongTimeoutMs: 5_000,
  });
}

describe("ResilientSocket heartbeat latency", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    FakeWebSocket.last = undefined;
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("reports the ping→pong round-trip time via onLatency", async () => {
    let clock = 1_000;
    const latencies: number[] = [];
    const sock = makeSocket({
      now: () => clock,
      onLatency: (ms) => latencies.push(ms),
      heartbeatIntervalMs: 1_000,
    });
    await sock.connect();
    const ws = FakeWebSocket.last!;
    expect(ws).toBeDefined();

    // Fire the heartbeat: the ping stamps lastPingSentAt = now() = 1000.
    clock = 1_000;
    await vi.advanceTimersByTimeAsync(1_000);
    expect(ws.sent.some((f) => f.includes('"ping"'))).toBe(true);

    // 42 ms later the server pong comes back → RTT = 42.
    clock = 1_042;
    ws.deliver({ type: "pong", seq: 1, ts: TS });

    expect(latencies).toEqual([42]);
    sock.close();
  });

  it("does not report a latency sample without an outstanding ping", async () => {
    let clock = 1_000;
    const latencies: number[] = [];
    const sock = makeSocket({
      now: () => clock,
      onLatency: (ms) => latencies.push(ms),
    });
    await sock.connect();
    const ws = FakeWebSocket.last!;

    // A stray pong with no ping in flight must NOT synthesize an RTT.
    clock = 9_999;
    ws.deliver({ type: "pong", seq: 1, ts: TS });
    expect(latencies).toEqual([]);
    sock.close();
  });

  it("measures each heartbeat cycle independently", async () => {
    let clock = 0;
    const latencies: number[] = [];
    const sock = makeSocket({
      now: () => clock,
      onLatency: (ms) => latencies.push(ms),
      heartbeatIntervalMs: 1_000,
    });
    await sock.connect();
    const ws = FakeWebSocket.last!;

    // Cycle 1: ping at t=1000, pong at t=1010 → 10.
    clock = 1_000;
    await vi.advanceTimersByTimeAsync(1_000);
    clock = 1_010;
    ws.deliver({ type: "pong", seq: 1, ts: TS });

    // The pong reset the heartbeat; next ping fires 1000ms after the pong.
    // Cycle 2: ping at t=2010, pong at t=2075 → 65.
    clock = 2_010;
    await vi.advanceTimersByTimeAsync(1_000);
    clock = 2_075;
    ws.deliver({ type: "pong", seq: 2, ts: TS });

    expect(latencies).toEqual([10, 65]);
    sock.close();
  });
});
