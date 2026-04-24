// SPDX-License-Identifier: MIT
//
// Ledger-viewer panel (R1D-2).
//
// Renders the append-only event stream for the current session. At
// scaffold time the list is empty; the `ledger.list_events` call is
// issued through `invokeStub` with a TODO R1D-5 tag. A small
// "verify-chain" button is present but disabled — the real wiring
// lands in R1D-5.3.

import { invokeStub } from "../ipc-stub";
import type { LedgerEventSummary, LedgerListEventsResult } from "../types/ipc";

export function renderPanel(root: HTMLElement): void {
  root.classList.add("r1-panel", "r1-panel-ledger-viewer");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>Ledger Viewer</h2>
      <span class="r1-panel-subtitle">append-only event stream</span>
      <button
        type="button"
        class="r1-btn"
        data-role="verify-chain"
        disabled
        title="Available in R1D-5.3"
      >
        Verify chain
      </button>
    </header>
    <div class="r1-panel-body">
      <ul class="r1-ledger-events" data-role="ledger-events" aria-live="polite">
        <li class="r1-placeholder">Loading events&hellip;</li>
      </ul>
    </div>
  `;

  const events = root.querySelector<HTMLUListElement>(
    '[data-role="ledger-events"]',
  );
  if (!events) return;

  void loadEvents(events);
}

async function loadEvents(events: HTMLUListElement): Promise<void> {
  const result = await invokeStub<LedgerListEventsResult>(
    "ledger_list_events",
    "R1D-5",
    { events: [], next_cursor: undefined },
    { limit: 100 },
  );
  renderEvents(events, result.events);
}

function renderEvents(
  list: HTMLUListElement,
  events: LedgerEventSummary[],
): void {
  if (events.length === 0) {
    list.innerHTML = `
      <li class="r1-placeholder">
        No events yet. Start a session to populate the ledger.
      </li>
    `;
    return;
  }

  list.innerHTML = events
    .map(
      (e) => `
        <li class="r1-ledger-event" data-hash="${e.hash}">
          <code class="r1-ledger-hash">${e.hash.slice(0, 12)}</code>
          <span class="r1-ledger-type">${escapeHtml(e.type)}</span>
          <time class="r1-ledger-at" datetime="${e.at}">${e.at}</time>
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
