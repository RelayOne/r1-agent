// SPDX-License-Identifier: MIT
// Subprotocol-token mint + cached refresh.
//
// Tickets are short-lived (~30s per spec §API Client Wrapper) so the
// cache window is naturally narrow. Refresh is triggered:
//   1. Pre-emptively by the WS layer on `auth.expiring_soon` (~60s
//      before expiry).
//   2. Reactively by the WS layer on close code 4401 (auth expired).
// In either case, the WS layer calls `refresh()` which forces a new
// network mint and busts the in-memory cache.
//
// Mints are deduplicated: concurrent callers share an in-flight
// promise. This avoids hammering /auth/ws-ticket if multiple sockets
// reconnect in the same tick.
import { HttpClient } from "@/lib/api/http";
import { WsTicketSchema, type WsTicket } from "@/lib/api/types";

export interface AuthClientOptions {
  http: HttpClient;
  /** ms safety window before `expiresAt` — beyond this we re-mint. */
  refreshSkewMs?: number;
}

export class AuthClient {
  private readonly http: HttpClient;
  private readonly refreshSkewMs: number;
  private cached: WsTicket | undefined;
  private inflight: Promise<WsTicket> | undefined;

  constructor(opts: AuthClientOptions) {
    this.http = opts.http;
    // Default 5s skew: re-mint when the cached ticket has <5s left.
    this.refreshSkewMs = opts.refreshSkewMs ?? 5_000;
  }

  /**
   * Returns a valid (un-expired) WS ticket. Mints if missing or near
   * expiry. Concurrent callers share one in-flight POST.
   */
  async mintWsTicket(): Promise<WsTicket> {
    if (this.cached && !this.isNearExpiry(this.cached)) {
      return this.cached;
    }
    if (this.inflight) {
      return this.inflight;
    }
    this.inflight = this.fetchTicket()
      .then((tk) => {
        this.cached = tk;
        return tk;
      })
      .finally(() => {
        this.inflight = undefined;
      });
    return this.inflight;
  }

  /**
   * Force a re-mint (called on `auth.expiring_soon` or close 4401).
   * Bypasses the cache; concurrent callers still share the in-flight
   * request so we don't issue duplicate mints during a reconnect storm.
   */
  async refresh(): Promise<WsTicket> {
    this.cached = undefined;
    return this.mintWsTicket();
  }

  /** Test / logout helper. */
  clear(): void {
    this.cached = undefined;
    this.inflight = undefined;
  }

  /** Return cached ticket without minting. Used by tests + diagnostics. */
  peek(): WsTicket | undefined {
    return this.cached;
  }

  private isNearExpiry(ticket: WsTicket): boolean {
    const expiresAt = Date.parse(ticket.expiresAt);
    if (Number.isNaN(expiresAt)) return true;
    return expiresAt - Date.now() <= this.refreshSkewMs;
  }

  private async fetchTicket(): Promise<WsTicket> {
    return this.http.request(
      {
        method: "POST",
        path: "/auth/ws-ticket",
      },
      WsTicketSchema,
    );
  }
}

/**
 * Convenience factory for callers that already have an HttpClient.
 * Most code paths construct AuthClient directly via R1dClient.
 */
export function createAuthClient(http: HttpClient): AuthClient {
  return new AuthClient({ http });
}
