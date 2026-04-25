// SPDX-License-Identifier: MIT
//
// R1D-1 tests — IPC contract surface.
//
// Validates that the WebView-tier IPC types and the ipc-stub module
// conform to the wire shapes documented in desktop/IPC-CONTRACT.md §1-3.
// Real Tauri runtime is not required; tests exercise the TS-side contract.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { invokeStub } from "./ipc-stub";
import type { InvokeMethod } from "./types/ipc";

// -----------------------------------------------------------------------
// §1 Envelope — invokeStub returns the empty value for any method
// -----------------------------------------------------------------------

describe("invokeStub — R1D-1 scaffold contract", () => {
  beforeEach(() => {
    vi.spyOn(console, "info").mockImplementation(() => {});
  });

  it("resolves to the empty value passed by the caller", async () => {
    const empty = { session_id: "", started_at: "" };
    const result = await invokeStub<typeof empty>("session_start" as InvokeMethod, "R1D-3", empty);
    expect(result).toEqual({ session_id: "", started_at: "" });
  });

  it("resolves an empty array for list-returning methods", async () => {
    const result = await invokeStub<string[]>("session_list" as InvokeMethod, "R1D-3", []);
    expect(result).toEqual([]);
  });

  it("resolves false as the empty value for boolean stub", async () => {
    const result = await invokeStub<boolean>("session_cancel" as InvokeMethod, "R1D-3", false);
    expect(result).toEqual(false);
  });

  it("logs a structured console.info with the method name and phase tag", async () => {
    const spy = vi.spyOn(console, "info");
    await invokeStub<string[]>("skill_list" as InvokeMethod, "R1D-4", []);
    const calls = spy.mock.calls;
    expect(calls.length).toBeGreaterThanOrEqual(1);
    const lastCall = calls[calls.length - 1];
    expect(lastCall[0]).toContain("skill_list");
    expect(lastCall[0]).toContain("R1D-4");
  });

  it("passes args through to the log output", async () => {
    const spy = vi.spyOn(console, "info");
    const args = { session_id: "abc-123" };
    await invokeStub<object>("memory_scan" as InvokeMethod, "R1D-6", {}, args);
    const lastCall = spy.mock.calls[spy.mock.calls.length - 1];
    // args must be passed through as second log param
    expect(lastCall[1]).toEqual(args);
  });

  it("does not throw when args are omitted", async () => {
    await expect(
      invokeStub<null>("config_get" as InvokeMethod, "R1D-7", null),
    ).resolves.toEqual(null);
  });
});

// -----------------------------------------------------------------------
// §2 IPC method names are strings (not symbols, not numbers)
// -----------------------------------------------------------------------

describe("InvokeMethod type contract", () => {
  it("session_start is a valid method string", () => {
    const method: InvokeMethod = "session_start";
    expect(typeof method).toBe("string");
  });

  it("skill_list is a valid method string", () => {
    const method: InvokeMethod = "skill_list";
    expect(typeof method).toBe("string");
  });
});
