// SPDX-License-Identifier: MIT
//
// Ledger-viewer panel (R1D-5).
//
// The desktop host currently exposes only the read-only ledger verbs
// `ledger_list_events` and `ledger_get_node`. Older UI scaffolding
// claimed session grouping, verify-chain, export, and crypto-shred
// actions that had no matching host handlers. This panel now renders
// the truthful subset: a global recent-events browser with node
// drill-down.

import { classifyIpcError, invokeStub } from "../ipc-stub";
import type {
  LedgerEventSummary,
  LedgerListEventsResult,
  LedgerNode,
} from "../types/ipc";
import { openNodeDrawer } from "./ledger-node-drawer";

interface LedgerViewState {
  events: LedgerEventSummary[];
}

const STATE: LedgerViewState = {
  events: [],
};

export function renderPanel(root: HTMLElement): void {
  root.classList.add("r1-panel", "r1-panel-ledger-viewer");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>Ledger Browser</h2>
      <span class="r1-panel-subtitle">recent host-backed event stream</span>
    </header>
    <div class="r1-ledger-readonly-note" data-role="ledger-readonly-note">
      This desktop host currently exposes read-only ledger browsing only.
      Session grouping, verify-chain, export, and crypto-shred are not wired in the host.
    </div>
    <div class="r1-panel-body r1-ledger-browser">
      <section class="r1-ledger-timeline" aria-label="Recent ledger events">
        <ul
          class="r1-ledger-events"
          data-role="ledger-events"
          aria-live="polite"
        >
          <li class="r1-empty">Loading recent events&hellip;</li>
        </ul>
      </section>
    </div>
  `;

  void loadEvents(root);
}

async function loadEvents(root: HTMLElement): Promise<void> {
  try {
    const result = await invokeStub<LedgerListEventsResult>(
      "ledger_list_events",
      "R1D-5",
      { events: [] },
      { limit: 100 },
    );
    STATE.events = result.events;
    renderEvents(root);
  } catch (err) {
    renderEventsUnavailable(root, err);
  }
}

/**
 * Replace the loading row with a truthful unavailable / error row when
 * the host RPC rejects (audit A034).
 */
function renderEventsUnavailable(root: HTMLElement, err: unknown): void {
  const events = root.querySelector<HTMLUListElement>('[data-role="ledger-events"]');
  if (!events) return;
  const failure = classifyIpcError(err);
  const row = document.createElement("li");
  row.className = "r1-empty";
  row.dataset.role = "ledger-unavailable";
  row.textContent = failure.notImplemented
    ? "Ledger events are not available yet — the host verb ledger.list_events is unimplemented."
    : `Couldn't load ledger events: ${failure.message}`;
  events.replaceChildren(row);
}

function renderEvents(root: HTMLElement): void {
  const events = root.querySelector<HTMLUListElement>('[data-role="ledger-events"]');
  if (!events) return;

  if (STATE.events.length === 0) {
    events.innerHTML = `
      <li class="r1-empty">
        No ledger events are available from the host yet.
      </li>
    `;
    return;
  }

  events.innerHTML = STATE.events.map(renderEventRow).join("");
  for (const row of events.querySelectorAll<HTMLLIElement>(".r1-ledger-event")) {
    const hash = row.dataset.hash;
    if (!hash) continue;
    row.addEventListener("click", () => {
      void openEventNode(hash);
    });
    row.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      void openEventNode(hash);
    });
  }
}

function renderEventRow(event: LedgerEventSummary): string {
  const hashPrefix = event.hash.slice(0, 12);
  return `
    <li
      class="r1-ledger-event"
      data-hash="${escapeHtml(event.hash)}"
      tabindex="0"
      role="button"
      aria-label="Inspect node ${escapeHtml(event.hash)}"
    >
      <code class="r1-ledger-hash">${escapeHtml(hashPrefix)}</code>
      <span class="r1-ledger-type">${escapeHtml(event.type)}</span>
      <time class="r1-ledger-at" datetime="${escapeHtml(event.at)}">${escapeHtml(event.at)}</time>
    </li>
  `;
}

async function openEventNode(hash: string): Promise<void> {
  try {
    const node = await invokeStub<LedgerNode>(
      "ledger_get_node",
      "R1D-5",
      emptyNode(hash),
      { hash },
    );
    await openNodeDrawer(node);
  } catch (err) {
    // Drawer never opened; log the classified failure instead of
    // leaving an unhandled rejection (audit A034).
    console.error(
      "[r1-desktop] ledger.get_node failed:",
      classifyIpcError(err).message,
    );
  }
}

function emptyNode(hash: string): LedgerNode {
  return {
    id: hash,
    kind: "unknown",
    timestamp: "",
    content_hash: hash,
    parent_hash: "",
    payload: {},
    shredded: false,
  };
}

function escapeHtml(raw: string): string {
  return raw
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}
