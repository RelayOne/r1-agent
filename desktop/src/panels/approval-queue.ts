// SPDX-License-Identifier: MIT
//
// Approval queue panel (R1D-10.2 — UI side).
//
// Approval review requires host verbs that are not present in this
// desktop build. Earlier versions of this panel mounted a live queue
// and approve/reject controls even though the Rust host exposed no
// `approval_*` handlers, which produced missing-handler failures in
// real Tauri runs.

export function renderPanel(root: HTMLElement): void {
  root.classList.add("r1-panel", "r1-panel-approval-queue");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>Approvals</h2>
      <span class="r1-panel-subtitle">host support pending</span>
    </header>
    <div class="r1-panel-body r1-approval-body">
      <section class="r1-approval-unavailable" data-role="approval-unavailable" aria-live="polite">
        <p class="r1-empty">
          The desktop host in this build does not implement approval queue IPC.
        </p>
        <ul class="r1-approval-unavailable-list">
          <li>Pending approval rows cannot be listed here.</li>
          <li>Approve and reject actions are unavailable.</li>
          <li>Use a host surface with real approval handlers until R1D-10 host wiring lands.</li>
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
