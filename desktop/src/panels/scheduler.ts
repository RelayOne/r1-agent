// SPDX-License-Identifier: MIT
//
// Scheduler panel (R1D-10.3 — UI side).
//
// Scheduler CRUD requires host verbs that are not present in this
// desktop build. Earlier versions of this panel exposed create/edit/
// toggle/run-now/delete controls even though the Rust host had no
// `schedule_*` handlers, which produced missing-handler failures in
// real Tauri runs.

export function renderPanel(root: HTMLElement): void {
  root.classList.add("r1-panel", "r1-panel-scheduler");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>Scheduler</h2>
      <span class="r1-panel-subtitle">host support pending</span>
    </header>
    <div class="r1-panel-body r1-scheduler-body">
      <section class="r1-scheduler-unavailable" data-role="scheduler-unavailable" aria-live="polite">
        <p class="r1-empty">
          The desktop host in this build does not implement scheduler IPC.
        </p>
        <ul class="r1-scheduler-unavailable-list">
          <li>Schedules cannot be listed or edited here.</li>
          <li>Create, update, delete, toggle, and run-now actions are unavailable.</li>
          <li>Use a host surface with real scheduler handlers until R1D-10 host wiring lands.</li>
        </ul>
      </section>
    </div>
  `;
}
