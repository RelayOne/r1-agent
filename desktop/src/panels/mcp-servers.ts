// SPDX-License-Identifier: MIT
//
// MCP servers panel (R1D-8).
//
// This desktop build does not expose the `mcp_*` IPC verbs that a live
// MCP management panel would require. Earlier versions rendered server
// listings, add/remove flows, test controls, and tool invocation forms
// even though the Rust host had no matching invoke handlers. That was a
// false capability claim and would fail in real Tauri runs.
//
// Until the host implements the MCP RPC surface, render an explicit
// unavailable state instead of interactive placeholders.

const UNAVAILABLE_REASONS = [
  "No MCP server-management IPC handlers are registered in the desktop host.",
  "Add, remove, test, and tool invocation actions are not available in this build.",
  "Use hosted or service-side MCP surfaces until the desktop host wiring lands.",
];

export function renderPanel(root: HTMLElement): void {
  root.classList.add("r1-panel", "r1-panel-mcp");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>MCP Servers</h2>
      <span class="r1-panel-subtitle">host support pending</span>
    </header>
    <div class="r1-panel-body r1-mcp-body">
      <section class="r1-mcp-unavailable" data-role="mcp-unavailable" aria-live="polite">
        <p class="r1-empty">
          The desktop host in this build does not implement the MCP IPC surface.
        </p>
        <ul class="r1-mcp-unavailable-list">
          ${UNAVAILABLE_REASONS.map((reason) => `<li>${escapeHtml(reason)}</li>`).join("")}
        </ul>
      </section>
    </div>
  `;
}

function escapeHtml(raw: string): string {
  return raw
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}
