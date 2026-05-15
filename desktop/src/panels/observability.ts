// SPDX-License-Identifier: MIT
//
// Observability panel (R1D-9).
//
// This desktop build does not expose the `obs_*` IPC verbs that a live
// dashboard would require. Earlier versions of this panel rendered KPI,
// latency, skill-count, error-timeline, CSV-export, and RelayGate
// reconciliation controls even though the Rust host had no matching
// invoke handlers. That was a false capability claim and produced
// missing-handler failures in real Tauri runs.
//
// Until the host implements the observability RPC surface, render an
// explicit unavailable state instead of loading placeholders.

const UNAVAILABLE_REASONS = [
  "No KPI or latency IPC handlers are registered in the desktop host.",
  "CSV export and RelayGate reconciliation are not available in this build.",
  "Use the hosted admin or service-side telemetry surfaces for live observability until R1D-9 host wiring lands.",
];

export function renderPanel(root: HTMLElement): void {
  root.classList.add("r1-panel", "r1-panel-observability");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>Observability</h2>
      <span class="r1-panel-subtitle">host support pending</span>
    </header>
    <div class="r1-panel-body r1-obs-body">
      <section class="r1-obs-unavailable" data-role="obs-unavailable" aria-live="polite">
        <p class="r1-empty">
          The desktop host in this build does not implement the Observability IPC surface.
        </p>
        <ul class="r1-obs-unavailable-list">
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
