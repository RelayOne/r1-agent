// SPDX-License-Identifier: MIT
//
// SOW-tree panel (R1D-2).
//
// Renders a sidebar widget listing the active R1 sessions and, for
// each, the corresponding SOW tree. At scaffold time the tree is an
// empty list rendered behind a "no sessions yet" placeholder; the
// underlying `session_list` call is issued through `invokeStub` and
// logs "TODO R1D-3".
//
// Real data-fetching + dependency-graph render lands in R1D-3.1 /
// R1D-3.2 per `desktop/PLAN.md`.

import { invokeStub } from "../ipc-stub";
import type { SessionSummary } from "../types/ipc";

/**
 * renderPanel populates `root` with the SOW-tree panel markup and
 * kicks off the async load. Returns synchronously; the panel updates
 * itself once the (stub) IPC call resolves.
 */
export function renderPanel(root: HTMLElement): void {
  root.classList.add("r1-panel", "r1-panel-sow-tree");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>SOW Tree</h2>
      <span class="r1-panel-subtitle">sessions &rarr; acceptance criteria &rarr; tasks</span>
    </header>
    <div class="r1-panel-body">
      <ul class="r1-sow-tree" data-role="sow-tree" aria-live="polite">
        <li class="r1-placeholder">Loading sessions&hellip;</li>
      </ul>
    </div>
  `;

  const list = root.querySelector<HTMLUListElement>('[data-role="sow-tree"]');
  if (!list) return;

  void loadSessions(list);
}

async function loadSessions(list: HTMLUListElement): Promise<void> {
  const sessions = await invokeStub<SessionSummary[]>(
    "session_list",
    "R1D-3",
    [],
  );
  renderTree(list, sessions);
}

function renderTree(list: HTMLUListElement, sessions: SessionSummary[]): void {
  if (sessions.length === 0) {
    list.innerHTML = `
      <li class="r1-placeholder">
        No sessions yet. Start one from the composer (R1D-2.5).
      </li>
    `;
    return;
  }

  list.innerHTML = sessions
    .map(
      (s) => `
        <li class="r1-sow-node" data-session-id="${s.session_id}">
          <span class="r1-sow-status r1-status-${s.status}" aria-hidden="true"></span>
          <span class="r1-sow-title">${escapeHtml(s.title)}</span>
          <span class="r1-sow-meta">${s.started_at}</span>
        </li>
      `,
    )
    .join("");
}

function escapeHtml(raw: string): string {
  return raw
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}
