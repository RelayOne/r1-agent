// SPDX-License-Identifier: MIT
// R1dClient — public API surface for daemon I/O. Exposes every HTTP
// and WS method listed in spec §API Client Wrapper.
//
// Usage:
//   const client = new R1dClient({
//     baseUrl: 'http://127.0.0.1:7777',
//     wsUrl:   'ws://127.0.0.1:7777/ws',
//   });
//   await client.connect({
//     onEnvelope: (env) => store.dispatch(env),
//     getLastEventId: (id) => store.getLastSeq(id),
//     getSubscribedSessions: () => store.getSubscribedSessions(),
//   });
//   const sessions = await client.listSessions();
//   client.subscribe('session-abc');
//   client.sendMessage('session-abc', 'hello');
//
// Construction is sync; the WS connection opens lazily via connect().
import { z } from "zod";
import { HttpClient } from "@/lib/api/http";
import { AuthClient } from "@/lib/api/auth";
import { ResilientSocket, type ResilientSocketState } from "@/lib/api/ws";
import {
  CreateSessionRequestSchema,
  KillLaneResponseSchema,
  ListAllowedRootsResponseSchema,
  ListDaemonsResponseSchema,
  ListLanesResponseSchema,
  ListSessionsResponseSchema,
  PatchSessionRequestSchema,
  SessionMetadataSchema,
  SettingsSchema,
  type CreateSessionRequest,
  type DaemonInfo,
  type KillLaneResponse,
  type LaneId,
  type LaneSnapshot,
  type PatchSessionRequest,
  type SessionId,
  type SessionMetadata,
  type Settings,
  type WsServerEnvelope,
  type WsTicket,
} from "@/lib/api/types";

export interface R1dClientOptions {
  baseUrl: string;
  wsUrl: string;
  /** Optional pre-minted bearer for HTTP. (Loopback usually relies on
   *  same-origin cookies; bearer is for reverse-proxy scenarios.) */
  bearerToken?: string;
  /** Test injection. */
  fetchImpl?: typeof fetch;
  /** Test injection. */
  webSocketImpl?: typeof WebSocket;
  /** Per-request HTTP timeout. */
  httpTimeoutMs?: number;
}

export interface ConnectOptions {
  onEnvelope: (env: WsServerEnvelope) => void;
  getLastEventId?: (sessionId: SessionId) => number | undefined;
  getSubscribedSessions?: () => SessionId[];
  onStateChange?: (state: ResilientSocketState, reason?: string) => void;
  onHardCap?: (lastError: string | undefined) => void;
  onSchemaError?: (err: z.ZodError, raw: unknown) => void;
}

export class R1dClient {
  private readonly http: HttpClient;
  private readonly auth: AuthClient;
  private readonly opts: R1dClientOptions;
  private socket: ResilientSocket | undefined;

  constructor(opts: R1dClientOptions) {
    this.opts = opts;
    this.http = new HttpClient({
      baseUrl: opts.baseUrl,
      ...(opts.bearerToken !== undefined && { bearerToken: opts.bearerToken }),
      ...(opts.fetchImpl !== undefined && { fetchImpl: opts.fetchImpl }),
      ...(opts.httpTimeoutMs !== undefined && { timeoutMs: opts.httpTimeoutMs }),
    });
    this.auth = new AuthClient({ http: this.http });
  }

  // ---------------------------------------------------------------------------
  // HTTP methods (verbatim from §API Client Wrapper "HTTP Methods" table)
  // ---------------------------------------------------------------------------

  /** GET /api/daemons — all known r1d daemons reachable from this origin. */
  async listDaemons(): Promise<DaemonInfo[]> {
    const res = await this.http.request({ path: "/api/daemons" }, ListDaemonsResponseSchema);
    return res.daemons;
  }

  /** GET /api/sessions — all sessions on the connected daemon. */
  async listSessions(): Promise<SessionMetadata[]> {
    const res = await this.http.request({ path: "/api/sessions" }, ListSessionsResponseSchema);
    return res.sessions;
  }

  /** GET /api/sessions/:id — session metadata. */
  async getSession(id: SessionId): Promise<SessionMetadata> {
    return this.http.request(
      { path: `/api/sessions/${encodeURIComponent(id)}` },
      SessionMetadataSchema,
    );
  }

  /** POST /api/sessions — create a new session. */
  async createSession(req: CreateSessionRequest): Promise<SessionMetadata> {
    return this.http.request(
      {
        method: "POST",
        path: "/api/sessions",
        body: req,
        bodySchema: CreateSessionRequestSchema,
      },
      SessionMetadataSchema,
    );
  }

  /** PATCH /api/sessions/:id — change workdir / model / title. */
  async setSessionWorkdir(id: SessionId, workdir: string): Promise<SessionMetadata> {
    return this.patchSession(id, { workdir });
  }

  /** PATCH /api/sessions/:id — generic patch (workdir, model, title). */
  async patchSession(id: SessionId, patch: PatchSessionRequest): Promise<SessionMetadata> {
    return this.http.request(
      {
        method: "PATCH",
        path: `/api/sessions/${encodeURIComponent(id)}`,
        body: patch,
        bodySchema: PatchSessionRequestSchema,
      },
      SessionMetadataSchema,
    );
  }

  /** GET /api/sessions/:id/lanes — snapshot (also streamed via WS). */
  async listLanes(sessionId: SessionId): Promise<LaneSnapshot[]> {
    const res = await this.http.request(
      { path: `/api/sessions/${encodeURIComponent(sessionId)}/lanes` },
      ListLanesResponseSchema,
    );
    return res.lanes;
  }

  /** POST /api/sessions/:id/lanes/:lane_id/kill — cancel a lane. */
  async killLane(sessionId: SessionId, laneId: LaneId): Promise<KillLaneResponse> {
    return this.http.request(
      {
        method: "POST",
        path: `/api/sessions/${encodeURIComponent(sessionId)}/lanes/${encodeURIComponent(laneId)}/kill`,
      },
      KillLaneResponseSchema,
    );
  }

  /** GET /api/settings — server-persisted user settings. */
  async getSettings(): Promise<Settings> {
    return this.http.request({ path: "/api/settings" }, SettingsSchema);
  }

  /** PUT /api/settings — persist settings. */
  async putSettings(settings: Settings): Promise<Settings> {
    return this.http.request(
      {
        method: "PUT",
        path: "/api/settings",
        body: settings,
        bodySchema: SettingsSchema,
      },
      SettingsSchema,
    );
  }

  /** POST /auth/ws-ticket — short-lived ticket for WS subprotocol. */
  async mintWsTicket(): Promise<WsTicket> {
    return this.auth.mintWsTicket();
  }

  /** GET /api/allowed-roots — for workdir picker autocomplete. */
  async listAllowedRoots(): Promise<string[]> {
    const res = await this.http.request(
      { path: "/api/allowed-roots" },
      ListAllowedRootsResponseSchema,
    );
    return res.roots;
  }

  // ---------------------------------------------------------------------------
  // WebSocket methods
  // ---------------------------------------------------------------------------

  /** Open the WS with the subprotocol ticket. Idempotent. */
  async connect(opts: ConnectOptions): Promise<void> {
    if (this.socket && this.socket.state === "open") return;
    this.socket = new ResilientSocket({
      wsUrl: this.opts.wsUrl,
      auth: this.auth,
      onEnvelope: opts.onEnvelope,
      ...(opts.getLastEventId !== undefined && { getLastEventId: opts.getLastEventId }),
      ...(opts.getSubscribedSessions !== undefined && { getSubscribedSessions: opts.getSubscribedSessions }),
      ...(opts.onStateChange !== undefined && { onStateChange: opts.onStateChange }),
      ...(opts.onHardCap !== undefined && { onHardCap: opts.onHardCap }),
      ...(opts.onSchemaError !== undefined && { onSchemaError: opts.onSchemaError }),
      ...(this.opts.webSocketImpl !== undefined && { webSocketImpl: this.opts.webSocketImpl }),
    });
    await this.socket.connect();
  }

  /** Subscribe to a session's event stream. Replays from lastEventId if given. */
  subscribe(sessionId: SessionId, lastEventId?: number): void {
    this.requireSocket();
    this.socket!.send({
      type: "subscribe",
      sessionId,
      ...(lastEventId !== undefined && { lastEventId }),
    });
  }

  /** Unsubscribe from a session. */
  unsubscribe(sessionId: SessionId): void {
    this.requireSocket();
    this.socket!.send({ type: "unsubscribe", sessionId });
  }

  /** Send a chat message on a session. */
  sendMessage(sessionId: SessionId, content: string): void {
    this.requireSocket();
    this.socket!.send({ type: "chat", sessionId, content });
  }

  /** Cancel the current turn on a session (drops partial assistant message). */
  interrupt(sessionId: SessionId): void {
    this.requireSocket();
    this.socket!.send({ type: "interrupt", sessionId });
  }

  /**
   * Backwards-compatible single-handler envelope hook. Re-uses the
   * onEnvelope callback supplied to connect(); this method exists so
   * the spec's `onEnvelope(handler)` signature is satisfied verbatim
   * for callers that prefer to wire the handler post-connect.
   */
  onEnvelope(handler: (env: WsServerEnvelope) => void): () => void {
    if (!this.envelopeHandlers) this.envelopeHandlers = new Set();
    this.envelopeHandlers.add(handler);
    return () => this.envelopeHandlers?.delete(handler);
  }

  /** Clean close of the WS (code 1000). */
  close(): void {
    if (this.socket) {
      this.socket.close();
      this.socket = undefined;
    }
  }

  /** Current socket state (idle / connecting / open / reconnecting / closed). */
  get state(): ResilientSocketState {
    return this.socket?.state ?? "idle";
  }

  // ---------------------------------------------------------------------------
  // Internals
  // ---------------------------------------------------------------------------

  private envelopeHandlers: Set<(env: WsServerEnvelope) => void> | undefined;

  private requireSocket(): void {
    if (!this.socket) {
      throw new Error("R1dClient: call connect() before WS methods");
    }
  }
}
