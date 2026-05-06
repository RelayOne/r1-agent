// SPDX-License-Identifier: MIT
// ResilientSocket — hand-rolled WebSocket wrapper with state machine,
// exponential backoff + jitter, heartbeat watchdog, Last-Event-ID
// replay, and ticket refresh on close 4401.
//
// Design contract (specs/web-chat-ui.md §WebSocket Reconnect Strategy):
//   1. Backoff: min(8000, 250 * 2^n) ± rand(0, 250) ms; reset on
//      successful open + first envelope.
//   2. State machine: idle → connecting → open → reconnecting → closed.
//      onStateChange fires on every transition.
//   3. Last-Event-ID replay: the caller stores last-seen `seq` per
//      session in zustand; ResilientSocket re-issues `subscribe`
//      frames on each (re)connect using that seq.
//   4. Token refresh: close 4401 → AuthClient.refresh() → reconnect.
//      `auth.expiring_soon` → AuthClient.refresh() preemptively.
//   5. Heartbeat: 30s no-traffic → send {type:"ping"}; 30s no pong →
//      force close + reconnect.
//   6. Hard cap: 10 reconnect attempts before transitioning to
//      `closed` and surfacing onHardCap to the UI.
//   7. Re-render coalescing is the responsibility of the store — this
//      class fires onEnvelope synchronously per message; the store
//      middleware batches at 5–10 Hz (D-S2).
import { z } from "zod";
import { AuthClient } from "@/lib/api/auth";
import {
  WsServerEnvelopeSchema,
  type WsClientFrame,
  type WsServerEnvelope,
  type SessionId,
} from "@/lib/api/types";

export type ResilientSocketState =
  | "idle"
  | "connecting"
  | "open"
  | "reconnecting"
  | "closed";

export interface ResilientSocketOptions {
  wsUrl: string;
  auth: AuthClient;
  /** Map of sessionId → last seen seq for Last-Event-ID replay. */
  getLastEventId?: (sessionId: SessionId) => number | undefined;
  /** Currently subscribed sessions; replayed on every (re)connect. */
  getSubscribedSessions?: () => SessionId[];
  /** Called on every successfully-validated envelope. */
  onEnvelope: (env: WsServerEnvelope) => void;
  /** Called on every state transition. */
  onStateChange?: (state: ResilientSocketState, reason?: string) => void;
  /** Called when reconnect cap (10) is exhausted; UI shows banner. */
  onHardCap?: (lastError: string | undefined) => void;
  /** Called when a server frame fails zod validation. */
  onSchemaError?: (err: z.ZodError, raw: unknown) => void;
  /** Called when a non-JSON server frame arrives. */
  onParseError?: (raw: unknown) => void;

  // Knobs (defaults below match the spec verbatim).
  baseBackoffMs?: number;       // 250
  maxBackoffMs?: number;        // 8000
  jitterMs?: number;            // 250
  hardCapAttempts?: number;     // 10
  heartbeatIntervalMs?: number; // 30_000
  pongTimeoutMs?: number;       // 30_000

  /** Test injection. Default: globalThis.WebSocket. */
  webSocketImpl?: typeof WebSocket;
  /** Test injection. Default: Math.random. */
  random?: () => number;
}

const DEFAULTS = {
  baseBackoffMs: 250,
  maxBackoffMs: 8_000,
  jitterMs: 250,
  hardCapAttempts: 10,
  heartbeatIntervalMs: 30_000,
  pongTimeoutMs: 30_000,
} as const;

const SUBPROTOCOL_PREFIX = "r1.bearer";
const CLOSE_AUTH_EXPIRED = 4401;
const CLOSE_NORMAL = 1000;

export class ResilientSocket {
  private readonly opts: Required<Omit<ResilientSocketOptions,
    "getLastEventId" | "getSubscribedSessions" | "onStateChange" | "onHardCap" |
    "onSchemaError" | "onParseError" | "webSocketImpl" | "random">> &
    Pick<ResilientSocketOptions, "getLastEventId" | "getSubscribedSessions" | "onStateChange" |
    "onHardCap" | "onSchemaError" | "onParseError">;

  private readonly webSocketImpl: typeof WebSocket;
  private readonly random: () => number;

  private socket: WebSocket | undefined;
  private currentState: ResilientSocketState = "idle";
  private attempt = 0;
  /** Number of envelopes received during the current `open` lifetime;
   *  used to know whether to reset the backoff counter on close. */
  private receivedSinceOpen = 0;
  private lastError: string | undefined;

  // Heartbeat
  private heartbeatTimer: ReturnType<typeof setTimeout> | undefined;
  private pongTimer: ReturnType<typeof setTimeout> | undefined;
  // Reconnect scheduling
  private reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  // Manual close flag prevents auto-reconnect after .close().
  private deliberatelyClosed = false;

  constructor(opts: ResilientSocketOptions) {
    this.opts = {
      wsUrl: opts.wsUrl,
      auth: opts.auth,
      onEnvelope: opts.onEnvelope,
      ...(opts.getLastEventId !== undefined && { getLastEventId: opts.getLastEventId }),
      ...(opts.getSubscribedSessions !== undefined && { getSubscribedSessions: opts.getSubscribedSessions }),
      ...(opts.onStateChange !== undefined && { onStateChange: opts.onStateChange }),
      ...(opts.onHardCap !== undefined && { onHardCap: opts.onHardCap }),
      ...(opts.onSchemaError !== undefined && { onSchemaError: opts.onSchemaError }),
      ...(opts.onParseError !== undefined && { onParseError: opts.onParseError }),
      baseBackoffMs: opts.baseBackoffMs ?? DEFAULTS.baseBackoffMs,
      maxBackoffMs: opts.maxBackoffMs ?? DEFAULTS.maxBackoffMs,
      jitterMs: opts.jitterMs ?? DEFAULTS.jitterMs,
      hardCapAttempts: opts.hardCapAttempts ?? DEFAULTS.hardCapAttempts,
      heartbeatIntervalMs: opts.heartbeatIntervalMs ?? DEFAULTS.heartbeatIntervalMs,
      pongTimeoutMs: opts.pongTimeoutMs ?? DEFAULTS.pongTimeoutMs,
    };
    this.webSocketImpl = opts.webSocketImpl ?? (globalThis.WebSocket as typeof WebSocket);
    this.random = opts.random ?? Math.random;
  }

  // -------------------------------------------------------------------------
  // Public API
  // -------------------------------------------------------------------------

  get state(): ResilientSocketState {
    return this.currentState;
  }

  /**
   * Open the socket. Resolves on first `open`. On failure, transitions
   * to `reconnecting` and schedules a backoff retry; the original
   * `connect()` promise rejects so the caller can show a transient
   * error, but reconnect proceeds in the background.
   */
  async connect(): Promise<void> {
    if (this.currentState === "open" || this.currentState === "connecting") {
      return;
    }
    this.deliberatelyClosed = false;
    this.attempt = 0;
    return this.openOnce();
  }

  /** Send a typed client frame. Throws if the socket isn't open. */
  send(frame: WsClientFrame): void {
    if (this.currentState !== "open" || !this.socket || this.socket.readyState !== this.webSocketImpl.OPEN) {
      throw new Error(`ResilientSocket.send called in state=${this.currentState}`);
    }
    this.socket.send(JSON.stringify(frame));
  }

  /** Clean close. Disables auto-reconnect. */
  close(code: number = CLOSE_NORMAL, reason: string = "client-close"): void {
    this.deliberatelyClosed = true;
    this.clearReconnectTimer();
    this.clearHeartbeat();
    if (this.socket) {
      try { this.socket.close(code, reason); } catch { /* already closed */ }
      this.socket = undefined;
    }
    this.transition("closed", reason);
  }

  // -------------------------------------------------------------------------
  // Internals
  // -------------------------------------------------------------------------

  private async openOnce(): Promise<void> {
    this.transition("connecting");
    let token: string;
    try {
      const ticket = await this.opts.auth.mintWsTicket();
      token = ticket.token;
    } catch (err) {
      this.lastError = `mint failed: ${(err as Error)?.message ?? String(err)}`;
      this.scheduleReconnect();
      throw err;
    }

    return new Promise((resolve, reject) => {
      let resolved = false;
      let socket: WebSocket;
      try {
        socket = new this.webSocketImpl(this.opts.wsUrl, [SUBPROTOCOL_PREFIX, token]);
      } catch (err) {
        this.lastError = `WebSocket ctor: ${(err as Error)?.message ?? String(err)}`;
        this.scheduleReconnect();
        reject(err);
        return;
      }
      this.socket = socket;
      this.receivedSinceOpen = 0;

      socket.onopen = () => {
        this.transition("open");
        // Replay subscriptions with Last-Event-ID per session.
        const sessions = this.opts.getSubscribedSessions?.() ?? [];
        for (const sessionId of sessions) {
          const lastEventId = this.opts.getLastEventId?.(sessionId);
          this.safeSend({
            type: "subscribe",
            sessionId,
            ...(lastEventId !== undefined && { lastEventId }),
          });
        }
        this.startHeartbeat();
        if (!resolved) {
          resolved = true;
          resolve();
        }
      };

      socket.onmessage = (ev: MessageEvent) => {
        this.handleMessage(ev.data);
      };

      socket.onerror = () => {
        this.lastError = "websocket error event";
        // No state change here — `onclose` always fires after `onerror`,
        // and that's where we drive the reconnect.
      };

      socket.onclose = (ev: CloseEvent) => {
        this.clearHeartbeat();
        this.socket = undefined;
        const code = ev.code;
        const reason = ev.reason || `close-${code}`;
        this.lastError = reason;

        if (this.deliberatelyClosed) {
          this.transition("closed", reason);
          return;
        }

        // Reset attempt counter if we successfully received traffic
        // during this open lifetime — this socket worked, so the next
        // failure starts a fresh backoff window.
        if (this.receivedSinceOpen > 0) {
          this.attempt = 0;
        }

        if (code === CLOSE_AUTH_EXPIRED) {
          // Force re-mint, then immediate reconnect (no backoff for
          // auth expiry — server told us exactly what to do).
          this.opts.auth.refresh().finally(() => this.scheduleReconnect(0));
        } else {
          this.scheduleReconnect();
        }

        if (!resolved) {
          resolved = true;
          reject(new Error(`socket closed before open: code=${code} reason=${reason}`));
        }
      };
    });
  }

  private handleMessage(data: unknown): void {
    let payload: unknown;
    if (typeof data === "string") {
      try { payload = JSON.parse(data); }
      catch {
        this.opts.onParseError?.(data);
        return;
      }
    } else {
      // We only accept text frames. Binary frames are protocol violation.
      this.opts.onParseError?.(data);
      return;
    }
    const parsed = WsServerEnvelopeSchema.safeParse(payload);
    if (!parsed.success) {
      this.opts.onSchemaError?.(parsed.error, payload);
      return;
    }
    const env = parsed.data;
    this.receivedSinceOpen++;
    this.resetHeartbeat();

    // Side-effects on specific envelopes BEFORE handing to caller:
    //   - pong cancels the watchdog (already covered by resetHeartbeat).
    //   - auth.expiring_soon triggers preemptive ticket refresh; we
    //     don't reconnect — the next connect will use the fresh ticket.
    if (env.type === "auth.expiring_soon") {
      this.opts.auth.refresh().catch(() => { /* surfaced via next connect */ });
    }

    this.opts.onEnvelope(env);
  }

  // -------------------------------------------------------------------------
  // Heartbeat (30s ping / 30s pong watchdog)
  // -------------------------------------------------------------------------

  private startHeartbeat(): void {
    this.clearHeartbeat();
    this.scheduleNextPing();
  }

  private resetHeartbeat(): void {
    if (this.currentState !== "open") return;
    this.clearHeartbeat();
    this.scheduleNextPing();
  }

  private scheduleNextPing(): void {
    this.heartbeatTimer = setTimeout(() => {
      if (this.currentState !== "open" || !this.socket) return;
      this.safeSend({ type: "ping" });
      // Watchdog: server has pongTimeoutMs to reply.
      this.pongTimer = setTimeout(() => {
        // No traffic → force close. onclose handler will reconnect.
        this.lastError = "pong-watchdog-timeout";
        try { this.socket?.close(4000, "pong-timeout"); } catch { /* noop */ }
      }, this.opts.pongTimeoutMs);
    }, this.opts.heartbeatIntervalMs);
  }

  private clearHeartbeat(): void {
    if (this.heartbeatTimer) {
      clearTimeout(this.heartbeatTimer);
      this.heartbeatTimer = undefined;
    }
    if (this.pongTimer) {
      clearTimeout(this.pongTimer);
      this.pongTimer = undefined;
    }
  }

  // -------------------------------------------------------------------------
  // Reconnect scheduling
  // -------------------------------------------------------------------------

  private scheduleReconnect(overrideDelayMs?: number): void {
    if (this.deliberatelyClosed) return;
    if (this.attempt >= this.opts.hardCapAttempts) {
      this.transition("closed", "hard-cap");
      this.opts.onHardCap?.(this.lastError);
      return;
    }
    this.attempt++;
    const delay = overrideDelayMs ?? this.computeBackoff(this.attempt);
    this.transition("reconnecting", `attempt=${this.attempt} delay=${delay}ms`);
    this.clearReconnectTimer();
    this.reconnectTimer = setTimeout(() => {
      // Fire-and-forget: errors propagate via state callbacks.
      this.openOnce().catch(() => { /* scheduleReconnect already invoked from onclose */ });
    }, delay);
  }

  private clearReconnectTimer(): void {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = undefined;
    }
  }

  /**
   * Exponential with jitter: `min(maxBackoffMs, base * 2^(n-1)) ±
   * rand(0, jitterMs)`. n=1 → ~250ms, n=2 → ~500ms, ..., capped at
   * maxBackoffMs. Spec verbatim.
   */
  private computeBackoff(n: number): number {
    const exp = Math.min(this.opts.maxBackoffMs, this.opts.baseBackoffMs * 2 ** (n - 1));
    const jitter = this.random() * this.opts.jitterMs;
    return Math.max(0, Math.floor(exp + jitter));
  }

  // -------------------------------------------------------------------------
  // State machine
  // -------------------------------------------------------------------------

  private transition(next: ResilientSocketState, reason?: string): void {
    if (this.currentState === next) return;
    this.currentState = next;
    this.opts.onStateChange?.(next, reason);
  }

  private safeSend(frame: WsClientFrame): void {
    if (!this.socket || this.socket.readyState !== this.webSocketImpl.OPEN) return;
    this.socket.send(JSON.stringify(frame));
  }
}
