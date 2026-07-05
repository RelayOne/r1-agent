// SPDX-License-Identifier: MIT
//
// Discovery-wizard reconnect-feedback test (audit gap: "'Reconnect'
// failure produces no user feedback"). The reconnect flow previously
// only console.error'd on failure, leaving the button to flicker back
// to "Reconnect" with zero on-screen explanation. These tests pin the
// fixed behaviour: a rejected onReconnect renders an inline alert and
// keeps the wizard open; describeIpcError shapes structured host errors.

import * as React from "react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup, waitFor } from "@testing-library/react";

// Avoid pulling the real @tauri-apps/api/core (invoke) transitively via the
// component under test — discovery-wizard imports it for resolveInstallCommand
// but our tests drive the component directly with explicit props.
vi.mock("@tauri-apps/api/core", () => ({ invoke: vi.fn() }));

import { DiscoveryWizard } from "./discovery-wizard";
import { describeIpcError } from "./discovery-wizard-mount";

afterEach(() => cleanup());

function baseProps(overrides: Partial<React.ComponentProps<typeof DiscoveryWizard>> = {}) {
  return {
    installCommand: "r1 serve --install --systemd-user",
    sidecarActive: false,
    onAcceptSidecar: vi.fn(),
    onReconnect: vi.fn().mockResolvedValue(undefined),
    onDismiss: vi.fn(),
    ...overrides,
  };
}

describe("DiscoveryWizard — reconnect feedback", () => {
  it("shows an inline alert when reconnect fails and keeps the wizard open", async () => {
    const onReconnect = vi.fn().mockRejectedValue(new Error("daemon not found"));
    render(React.createElement(DiscoveryWizard, baseProps({ onReconnect })));

    // No alert before the user tries.
    expect(screen.queryByRole("alert")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: /reconnect/i }));

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("daemon not found");
    // Wizard stays mounted (dialog still present) so the user can retry.
    expect(screen.getByRole("dialog")).not.toBeNull();
    // The button returns to its idle label rather than staying stuck.
    expect(
      screen.getByRole("button", { name: /^reconnect$/i }),
    ).not.toBeNull();
  });

  it("falls back to a generic message when the error carries none", async () => {
    const onReconnect = vi.fn().mockRejectedValue({});
    render(React.createElement(DiscoveryWizard, baseProps({ onReconnect })));

    fireEvent.click(screen.getByRole("button", { name: /reconnect/i }));

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/make sure r1 is running/i);
  });

  it("clears a prior error on a subsequent successful reconnect", async () => {
    const onReconnect = vi
      .fn()
      .mockRejectedValueOnce(new Error("transient drop"))
      .mockResolvedValueOnce(undefined);
    render(React.createElement(DiscoveryWizard, baseProps({ onReconnect })));

    fireEvent.click(screen.getByRole("button", { name: /reconnect/i }));
    await screen.findByRole("alert");

    fireEvent.click(screen.getByRole("button", { name: /reconnect/i }));
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
  });
});

describe("describeIpcError", () => {
  it("prefers an Error message", () => {
    expect(describeIpcError(new Error("boom"))).toBe("boom");
  });

  it("reads a structured IpcError message field", () => {
    expect(
      describeIpcError({ stoke_code: "not_found", message: "no daemon socket" }),
    ).toBe("no daemon socket");
  });

  it("falls back to stoke_code when no message", () => {
    expect(describeIpcError({ stoke_code: "spawn_failed" })).toBe("spawn_failed");
  });

  it("uses a plain string reason", () => {
    expect(describeIpcError("channel closed")).toBe("channel closed");
  });

  it("falls back to a generic hint for empty input", () => {
    expect(describeIpcError(null)).toMatch(/make sure r1 is running/i);
    expect(describeIpcError({})).toMatch(/make sure r1 is running/i);
  });
});
