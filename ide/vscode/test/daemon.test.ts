// daemon.test.ts — unit tests for the daemon HTTP client. Boots a
// real http.createServer locally so we exercise the wire format end
// to end without bringing in @vscode/test-electron.

import * as assert from "assert";
import * as http from "http";
import { DaemonClient, DaemonError, resolveApiKey, BEARER_HEADER } from "../src/daemon";

interface MockServer {
  url: string;
  close: () => Promise<void>;
  lastHeaders: () => http.IncomingHttpHeaders;
  lastBody: () => string;
  lastPath: () => string;
}

async function startMockServer(routes: Record<string, (body: string) => { status: number; body: string }>): Promise<MockServer> {
  let lastHeaders: http.IncomingHttpHeaders = {};
  let lastBody = "";
  let lastPath = "";
  const server = http.createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", (c: Buffer) => chunks.push(c));
    req.on("end", () => {
      lastHeaders = req.headers;
      lastBody = Buffer.concat(chunks).toString("utf8");
      lastPath = req.url || "";
      const key = `${req.method} ${req.url}`;
      // exact-match first, then prefix-match for /api/task/*
      let handler = routes[key];
      if (!handler) {
        for (const k of Object.keys(routes)) {
          const sep = k.indexOf(" ");
          const m = sep > 0 ? k.slice(0, sep) : "";
          const p = sep > 0 ? k.slice(sep + 1) : "";
          if (m === req.method && p && p.endsWith("*") && req.url && req.url.startsWith(p.slice(0, -1))) {
            handler = routes[k];
            break;
          }
        }
      }
      if (!handler) {
        res.statusCode = 404;
        res.setHeader("content-type", "application/json");
        res.end(JSON.stringify({ error: "no route" }));
        return;
      }
      const out = handler(lastBody);
      res.statusCode = out.status;
      res.setHeader("content-type", "application/json");
      res.end(out.body);
    });
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", () => resolve()));
  const addr = server.address();
  if (!addr || typeof addr === "string") {
    throw new Error("could not resolve mock server address");
  }
  return {
    url: `http://127.0.0.1:${addr.port}`,
    close: () => new Promise<void>((resolve) => server.close(() => resolve())),
    lastHeaders: () => lastHeaders,
    lastBody: () => lastBody,
    lastPath: () => lastPath
  };
}

describe("DaemonClient", () => {
  it("rejects baseUrl that lacks a scheme", () => {
    let threw = false;
    try {
      new DaemonClient({ baseUrl: "127.0.0.1:7777" });
    } catch (err) {
      threw = true;
      assert.strictEqual((err as Error).message.includes("http://"), true);
    }
    assert.strictEqual(threw, true);
  });

  it("submitTask sends task_type and bearer header", async () => {
    const mock = await startMockServer({
      "POST /api/task": () => ({
        status: 200,
        body: JSON.stringify({
          id: "t-abc",
          status: "completed",
          task_type: "explain",
          created_at: "2026-04-26T00:00:00Z",
          summary: "ok"
        })
      })
    });
    try {
      const client = new DaemonClient({ baseUrl: mock.url, apiKey: "secret-token-123" });
      const state = await client.submitTask({
        task_type: "explain",
        description: "hello"
      });
      assert.strictEqual(state.id, "t-abc");
      assert.strictEqual(state.status, "completed");
      const headers = mock.lastHeaders();
      assert.strictEqual(headers[BEARER_HEADER.toLowerCase()], "secret-token-123");
      const sentBody = JSON.parse(mock.lastBody());
      assert.strictEqual(sentBody.task_type, "explain");
      assert.strictEqual(sentBody.description, "hello");
    } finally {
      await mock.close();
    }
  });

  it("submitTask rejects when description and query are both empty", async () => {
    const client = new DaemonClient({ baseUrl: "http://127.0.0.1:1" });
    let threw = false;
    try {
      await client.submitTask({ task_type: "explain" });
    } catch (err) {
      threw = true;
      assert.strictEqual((err as Error).message.includes("description or query"), true);
    }
    assert.strictEqual(threw, true);
  });

  it("getCapabilities parses the JSON envelope", async () => {
    const mock = await startMockServer({
      "GET /api/capabilities": () => ({
        status: 200,
        body: JSON.stringify({
          version: "0.1.0",
          task_types: ["explain", "research"],
          budget_usd: 0,
          requires_auth: false
        })
      })
    });
    try {
      const client = new DaemonClient({ baseUrl: mock.url });
      const caps = await client.getCapabilities();
      assert.strictEqual(caps.version, "0.1.0");
      assert.deepStrictEqual(caps.task_types, ["explain", "research"]);
      assert.strictEqual(caps.requires_auth, false);
    } finally {
      await mock.close();
    }
  });

  it("non-2xx responses surface the daemon error message", async () => {
    const mock = await startMockServer({
      "POST /api/task": () => ({
        status: 400,
        body: JSON.stringify({ error: "task_type required" })
      })
    });
    try {
      const client = new DaemonClient({ baseUrl: mock.url });
      let caught: unknown;
      try {
        await client.submitTask({ task_type: "explain", description: "x" });
      } catch (err) {
        caught = err;
      }
      assert.ok(caught instanceof DaemonError, "expected DaemonError");
      const dErr = caught as DaemonError;
      assert.strictEqual(dErr.status, 400);
      assert.strictEqual(dErr.message.includes("task_type required"), true);
    } finally {
      await mock.close();
    }
  });

  it("getTask hits /api/task/{id} and returns the parsed state", async () => {
    const mock = await startMockServer({
      "GET /api/task/t-poll": () => ({
        status: 200,
        body: JSON.stringify({
          id: "t-poll",
          status: "running",
          task_type: "explain",
          created_at: "2026-04-26T00:00:00Z"
        })
      })
    });
    try {
      const client = new DaemonClient({ baseUrl: mock.url });
      const state = await client.getTask("t-poll");
      assert.strictEqual(state.id, "t-poll");
      assert.strictEqual(state.status, "running");
      assert.strictEqual(mock.lastPath(), "/api/task/t-poll");
    } finally {
      await mock.close();
    }
  });

  it("cancelTask sends POST to /cancel sub-route", async () => {
    const mock = await startMockServer({
      "POST /api/task/t-x/cancel": () => ({
        status: 200,
        body: JSON.stringify({
          id: "t-x",
          status: "cancelled",
          task_type: "explain",
          created_at: "2026-04-26T00:00:00Z"
        })
      })
    });
    try {
      const client = new DaemonClient({ baseUrl: mock.url });
      const state = await client.cancelTask("t-x");
      assert.strictEqual(state.status, "cancelled");
    } finally {
      await mock.close();
    }
  });

  it("resolveApiKey prefers explicit value over env", () => {
    const prev = process.env.R1_API_KEY;
    process.env.R1_API_KEY = "from-env";
    try {
      assert.strictEqual(resolveApiKey("from-arg"), "from-arg");
      assert.strictEqual(resolveApiKey(""), "from-env");
      assert.strictEqual(resolveApiKey(undefined), "from-env");
    } finally {
      if (prev === undefined) {
        delete process.env.R1_API_KEY;
      } else {
        process.env.R1_API_KEY = prev;
      }
    }
  });

  it("resolveApiKey returns undefined when no source is set", () => {
    const prev = process.env.R1_API_KEY;
    delete process.env.R1_API_KEY;
    try {
      assert.strictEqual(resolveApiKey(undefined), undefined);
      assert.strictEqual(resolveApiKey(""), undefined);
      assert.strictEqual(resolveApiKey("   "), undefined);
    } finally {
      if (prev !== undefined) {
        process.env.R1_API_KEY = prev;
      }
    }
  });
});
