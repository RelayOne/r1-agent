// SPDX-License-Identifier: MIT
// HTTP fetch wrapper. Throws typed R1dError on non-2xx responses or
// zod schema mismatch. Used by every method in r1d.ts.
//
// Keeps native `fetch` (per spec §Library Preferences "no axios").
import { z } from "zod";
import { ErrorResponseSchema, type R1dErrorCode } from "@/lib/api/types";

export interface R1dErrorOptions {
  code: R1dErrorCode | "TRANSPORT" | "SCHEMA";
  message: string;
  status?: number;
  retryable?: boolean;
  cause?: unknown;
  details?: Record<string, unknown>;
}

/** Typed error surface for everything thrown out of api/. */
export class R1dError extends Error {
  readonly code: R1dErrorOptions["code"];
  readonly status?: number;
  readonly retryable: boolean;
  readonly details?: Record<string, unknown>;

  constructor(opts: R1dErrorOptions) {
    super(opts.message, { cause: opts.cause });
    this.name = "R1dError";
    this.code = opts.code;
    if (opts.status !== undefined) this.status = opts.status;
    this.retryable = opts.retryable ?? false;
    if (opts.details) this.details = opts.details;
  }
}

export interface HttpClientOptions {
  baseUrl: string;
  /** Optional bearer for HTTP-tier auth (separate from WS subprotocol ticket). */
  bearerToken?: string;
  /** Per-request timeout in ms; default 15s for slow filesystem ops. */
  timeoutMs?: number;
  /** Custom fetch (test injection). Defaults to global fetch. */
  fetchImpl?: typeof fetch;
}

export interface RequestOptions<TReq> {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  path: string;
  body?: TReq;
  bodySchema?: z.ZodType<TReq>;
  signal?: AbortSignal;
}

export class HttpClient {
  private readonly baseUrl: string;
  private bearerToken?: string;
  private readonly timeoutMs: number;
  private readonly fetchImpl: typeof fetch;

  constructor(opts: HttpClientOptions) {
    // Normalise baseUrl: strip trailing slash so path joins are clean.
    this.baseUrl = opts.baseUrl.replace(/\/+$/, "");
    if (opts.bearerToken !== undefined) this.bearerToken = opts.bearerToken;
    this.timeoutMs = opts.timeoutMs ?? 15_000;
    this.fetchImpl = opts.fetchImpl ?? globalThis.fetch.bind(globalThis);
  }

  setBearerToken(token: string | undefined): void {
    this.bearerToken = token;
  }

  /**
   * Issue a request and validate the response against `responseSchema`.
   * - Throws R1dError(code: "TRANSPORT") on network failure / abort / timeout.
   * - Throws R1dError(code: <server>) on non-2xx with parseable error body.
   * - Throws R1dError(code: "SCHEMA") on response body that fails zod parse.
   */
  async request<TReq, TRes>(
    opts: RequestOptions<TReq>,
    responseSchema: z.ZodType<TRes>,
  ): Promise<TRes> {
    const url = `${this.baseUrl}${opts.path}`;
    const headers: Record<string, string> = {
      Accept: "application/json",
    };
    if (this.bearerToken) {
      headers.Authorization = `Bearer ${this.bearerToken}`;
    }

    let serializedBody: string | undefined;
    if (opts.body !== undefined) {
      // Validate the request body against its schema if supplied — fail
      // fast in dev rather than have the server reject malformed input.
      if (opts.bodySchema) {
        const parsed = opts.bodySchema.safeParse(opts.body);
        if (!parsed.success) {
          throw new R1dError({
            code: "VALIDATION_FAILED",
            message: `Request body failed schema validation for ${opts.path}`,
            details: { issues: parsed.error.issues },
          });
        }
        serializedBody = JSON.stringify(parsed.data);
      } else {
        serializedBody = JSON.stringify(opts.body);
      }
      headers["Content-Type"] = "application/json";
    }

    // Compose abort: caller signal + per-request timeout.
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(new Error("timeout")), this.timeoutMs);
    const onAbort = () => controller.abort(opts.signal?.reason ?? new Error("aborted"));
    if (opts.signal) {
      if (opts.signal.aborted) onAbort();
      else opts.signal.addEventListener("abort", onAbort, { once: true });
    }

    let response: Response;
    try {
      response = await this.fetchImpl(url, {
        method: opts.method ?? "GET",
        headers,
        body: serializedBody,
        signal: controller.signal,
        // Spec: SPA + same-origin (or daemon allowlist in dev). Send
        // cookies so loopback session-bound state works after refresh.
        credentials: "same-origin",
        // No cache for API responses — Last-Event-ID handles staleness.
        cache: "no-store",
      });
    } catch (err) {
      const aborted = (err as Error)?.name === "AbortError";
      throw new R1dError({
        code: "TRANSPORT",
        message: aborted ? `Request aborted: ${opts.path}` : `Network error: ${opts.path}`,
        retryable: !aborted,
        cause: err,
      });
    } finally {
      clearTimeout(timeoutId);
      if (opts.signal) opts.signal.removeEventListener("abort", onAbort);
    }

    // Decode body once. May be empty for 204s.
    const rawText = await response.text();
    const parsedJson = rawText.length > 0 ? safeJsonParse(rawText) : undefined;

    if (!response.ok) {
      const errBody = parsedJson !== undefined ? ErrorResponseSchema.safeParse(parsedJson) : undefined;
      if (errBody && errBody.success) {
        throw new R1dError({
          code: errBody.data.code,
          status: response.status,
          message: errBody.data.message,
          retryable: errBody.data.retryable ?? false,
          details: errBody.data.details,
        });
      }
      throw new R1dError({
        code: response.status === 401 ? "UNAUTHORIZED"
            : response.status === 403 ? "FORBIDDEN"
            : response.status === 404 ? "NOT_FOUND"
            : response.status === 409 ? "CONFLICT"
            : response.status === 429 ? "RATE_LIMITED"
            : response.status >= 500 ? "INTERNAL"
            : "TRANSPORT",
        status: response.status,
        message: `HTTP ${response.status} ${response.statusText} on ${opts.path}`,
        retryable: response.status >= 500 || response.status === 429,
        details: parsedJson !== undefined ? { rawBody: parsedJson } : undefined,
      });
    }

    // 204 / empty-body success: only allow if schema accepts undefined.
    const candidate = parsedJson !== undefined ? parsedJson : null;
    const validation = responseSchema.safeParse(candidate);
    if (!validation.success) {
      throw new R1dError({
        code: "SCHEMA",
        status: response.status,
        message: `Response body for ${opts.path} failed schema validation`,
        details: { issues: validation.error.issues, body: candidate },
      });
    }
    return validation.data;
  }
}

function safeJsonParse(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}
