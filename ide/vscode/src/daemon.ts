// daemon.ts — pure (no vscode imports) HTTP client for the r1-agent
// agentserve API. Lives separately so unit tests can exercise it
// against a local http.createServer mock without booting the editor.
//
// Wire format: see ide/PROTOCOL.md.

import * as http from "http";
import * as https from "https";
import { URL } from "url";

export interface Capabilities {
  version: string;
  task_types: string[];
  budget_usd: number;
  requires_auth: boolean;
}

export interface TaskRequestBody {
  task_type: string;
  description?: string;
  query?: string;
  spec?: string;
  budget?: number;
  effort?: string;
  extra?: Record<string, unknown>;
}

export interface TaskState {
  id: string;
  status: string;
  task_type: string;
  created_at: string;
  started_at?: string;
  completed_at?: string;
  summary?: string;
  size?: number;
  error?: string;
}

export interface DaemonClientOptions {
  baseUrl: string;
  apiKey?: string;
  timeoutMs?: number;
}

// Header name kept in a constant so the rename window (X-Stoke-* →
// X-R1-*) is a one-line swap and easy to audit. Follows
// work-r1-rename.md guidance.
export const BEARER_HEADER = "X-Stoke-Bearer";

export class DaemonError extends Error {
  readonly status: number;
  readonly bodySnippet: string;
  constructor(message: string, status: number, bodySnippet: string) {
    super(message);
    this.name = "DaemonError";
    this.status = status;
    this.bodySnippet = bodySnippet;
  }
}

// Build the auth header, honoring (in order): explicit constructor
// option, R1_API_KEY env var. Returns undefined to signal "do not
// send a header at all" so the daemon's no-auth dev mode keeps
// working.
export function resolveApiKey(explicit?: string): string | undefined {
  if (explicit && explicit.trim().length > 0) {
    return explicit.trim();
  }
  const fromEnv = process.env.R1_API_KEY;
  if (fromEnv && fromEnv.trim().length > 0) {
    return fromEnv.trim();
  }
  return undefined;
}

export class DaemonClient {
  private readonly baseUrl: string;
  private readonly apiKey: string | undefined;
  private readonly timeoutMs: number;

  constructor(opts: DaemonClientOptions) {
    if (!opts.baseUrl || !/^https?:\/\//i.test(opts.baseUrl)) {
      throw new Error(
        `daemon baseUrl must start with http:// or https:// (got ${JSON.stringify(opts.baseUrl)})`
      );
    }
    // strip trailing slash so callers can join with a leading "/"
    this.baseUrl = opts.baseUrl.replace(/\/+$/, "");
    this.apiKey = resolveApiKey(opts.apiKey);
    this.timeoutMs = opts.timeoutMs ?? 120_000;
  }

  capabilitiesUrl(): string {
    return `${this.baseUrl}/api/capabilities`;
  }

  async getCapabilities(): Promise<Capabilities> {
    const res = await this.request<Capabilities>("GET", "/api/capabilities");
    return res;
  }

  async submitTask(body: TaskRequestBody): Promise<TaskState> {
    if (!body.task_type || body.task_type.trim().length === 0) {
      throw new Error("submitTask: task_type is required");
    }
    if (
      (!body.description || body.description.trim().length === 0) &&
      (!body.query || body.query.trim().length === 0)
    ) {
      throw new Error("submitTask: description or query is required");
    }
    return this.request<TaskState>("POST", "/api/task", body);
  }

  async getTask(id: string): Promise<TaskState> {
    if (!id) {
      throw new Error("getTask: id is required");
    }
    return this.request<TaskState>("GET", `/api/task/${encodeURIComponent(id)}`);
  }

  async cancelTask(id: string): Promise<TaskState> {
    if (!id) {
      throw new Error("cancelTask: id is required");
    }
    return this.request<TaskState>("POST", `/api/task/${encodeURIComponent(id)}/cancel`);
  }

  // request is a tiny HTTP helper that returns a typed JSON body or
  // throws DaemonError. We intentionally avoid `fetch` so the
  // extension also works in older Electron-bundled VS Code releases
  // that lack a global fetch on the extension-host runtime.
  private request<T>(method: string, path: string, body?: unknown): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      const url = new URL(this.baseUrl + path);
      const lib: typeof http | typeof https = url.protocol === "https:" ? https : http;
      const headers: Record<string, string> = {
        Accept: "application/json"
      };
      if (this.apiKey) {
        headers[BEARER_HEADER] = this.apiKey;
      }
      let payload: Buffer | undefined;
      if (body !== undefined) {
        payload = Buffer.from(JSON.stringify(body), "utf8");
        headers["Content-Type"] = "application/json";
        headers["Content-Length"] = String(payload.length);
      }
      const req = lib.request(
        {
          method,
          hostname: url.hostname,
          port: url.port || (url.protocol === "https:" ? 443 : 80),
          path: url.pathname + url.search,
          headers
        },
        (res) => {
          const chunks: Buffer[] = [];
          res.on("data", (c: Buffer) => chunks.push(c));
          res.on("end", () => {
            const raw = Buffer.concat(chunks).toString("utf8");
            const status = res.statusCode ?? 0;
            if (status < 200 || status >= 300) {
              let snippet = raw.length > 400 ? raw.slice(0, 400) + "..." : raw;
              let msg = `daemon ${method} ${path} returned HTTP ${status}`;
              try {
                const parsed = JSON.parse(raw) as { error?: string };
                if (parsed && parsed.error) {
                  msg += `: ${parsed.error}`;
                }
              } catch {
                // body wasn't JSON — fall through with the raw snippet
              }
              reject(new DaemonError(msg, status, snippet));
              return;
            }
            if (raw.length === 0) {
              reject(new DaemonError("daemon returned empty body", status, ""));
              return;
            }
            try {
              resolve(JSON.parse(raw) as T);
            } catch (err) {
              reject(
                new DaemonError(
                  `daemon returned non-JSON body: ${(err as Error).message}`,
                  status,
                  raw.slice(0, 400)
                )
              );
            }
          });
        }
      );
      req.setTimeout(this.timeoutMs, () => {
        req.destroy(new Error(`request timed out after ${this.timeoutMs}ms`));
      });
      req.on("error", (err) => reject(err));
      if (payload) {
        req.write(payload);
      }
      req.end();
    });
  }
}
