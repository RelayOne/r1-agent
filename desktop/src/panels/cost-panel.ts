// SPDX-License-Identifier: MIT
//
// Cost-summary panel (R1D-2).
//
// Renders a single current cost snapshot with USD spend + in/out token
// counts. When a session id is present on the panel root, the host
// request is scoped to that session; otherwise the desktop asks for the
// current aggregate snapshot.
//
// Real per-provider latency histogram + time-range picker lands in
// R1D-9.1 / R1D-9.3.

import { classifyIpcError, invokeStub } from "../ipc-stub";
import type { CostHistoryResult, CostSnapshot } from "../types/ipc";

const EMPTY_SNAPSHOT: CostSnapshot = {
  usd: 0,
  tokens_in: 0,
  tokens_out: 0,
  as_of: "",
};

const EMPTY_HISTORY: CostHistoryResult = { buckets: [] };

export function renderPanel(root: HTMLElement): void {
  root.classList.add("r1-panel", "r1-panel-cost");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>Cost</h2>
      <span class="r1-panel-subtitle">current cost snapshot</span>
    </header>
    <div class="r1-panel-body">
      <dl class="r1-cost-summary">
        <div class="r1-cost-row">
          <dt>Spend</dt>
          <dd data-role="cost-usd">$0.00</dd>
        </div>
        <div class="r1-cost-row">
          <dt>Tokens in</dt>
          <dd data-role="cost-tokens-in">0</dd>
        </div>
        <div class="r1-cost-row">
          <dt>Tokens out</dt>
          <dd data-role="cost-tokens-out">0</dd>
        </div>
        <div class="r1-cost-row">
          <dt>As of</dt>
          <dd data-role="cost-as-of">&mdash;</dd>
        </div>
      </dl>
      <section class="r1-cost-history" data-role="cost-history">
        <h3 class="r1-cost-history-title">History</h3>
        <p class="r1-empty" data-role="cost-history-empty">Loading…</p>
        <ul class="r1-cost-history-list" data-role="cost-history-list" hidden></ul>
      </section>
    </div>
  `;

  void loadCost(root);
  void loadCostHistory(root);
}

/**
 * Fetch bucketed spend history via `cost_get_history` (audit A095 —
 * the verb was registered host-side and typed in ipc.d.ts but had no
 * WebView caller). Renders newest-last rows; a not_implemented
 * rejection from an older subprocess renders a truthful note.
 */
async function loadCostHistory(root: HTMLElement): Promise<void> {
  const sessionId = root.dataset.sessionId?.trim();
  try {
    const history = await invokeStub<CostHistoryResult>(
      "cost_get_history",
      "R1D-9",
      EMPTY_HISTORY,
      sessionId ? { session_id: sessionId } : undefined,
    );
    applyHistory(root, history);
  } catch (err) {
    renderHistoryUnavailable(root, err);
  }
}

function applyHistory(root: HTMLElement, history: CostHistoryResult): void {
  const empty = root.querySelector<HTMLElement>(
    '[data-role="cost-history-empty"]',
  );
  const list = root.querySelector<HTMLUListElement>(
    '[data-role="cost-history-list"]',
  );
  if (!empty || !list) return;
  const buckets = Array.isArray(history?.buckets) ? history.buckets : [];
  if (buckets.length === 0) {
    empty.textContent = "No cost history yet.";
    list.hidden = true;
    return;
  }
  empty.hidden = true;
  list.hidden = false;
  list.innerHTML = buckets
    .map(
      (b) => `
      <li class="r1-cost-history-row">
        <span class="r1-cost-history-at">${escapeHtml(b.at || "—")}</span>
        <span class="r1-cost-history-usd">${formatUsd(b.usd ?? 0)}</span>
        <span class="r1-cost-history-tokens">${formatInt(b.tokens ?? 0)} tok</span>
      </li>`,
    )
    .join("");
}

function renderHistoryUnavailable(root: HTMLElement, err: unknown): void {
  const empty = root.querySelector<HTMLElement>(
    '[data-role="cost-history-empty"]',
  );
  if (!empty) return;
  const failure = classifyIpcError(err);
  empty.dataset.role = "cost-history-unavailable";
  empty.textContent = failure.notImplemented
    ? "Cost history is not available yet — the host verb cost.get_history is unimplemented."
    : `Couldn't load cost history: ${failure.message}`;
}

function escapeHtml(raw: string): string {
  return raw
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

async function loadCost(root: HTMLElement): Promise<void> {
  const sessionId = root.dataset.sessionId?.trim();
  try {
    const snapshot = await invokeStub<CostSnapshot>(
      "cost_get_current",
      "R1D-9",
      EMPTY_SNAPSHOT,
      sessionId ? { session_id: sessionId } : undefined,
    );
    applySnapshot(root, snapshot);
  } catch (err) {
    renderCostUnavailable(root, err);
  }
}

/**
 * Replace the seeded $0.00 summary with a truthful unavailable / error
 * note when the host RPC rejects (audit A034). A not_implemented
 * rejection means the Go subprocess hasn't wired the verb yet; other
 * errors are genuine failures and render as such.
 */
function renderCostUnavailable(root: HTMLElement, err: unknown): void {
  const body = root.querySelector<HTMLElement>(".r1-panel-body");
  if (!body) return;
  const failure = classifyIpcError(err);
  const note = document.createElement("p");
  note.className = "r1-empty";
  note.dataset.role = "cost-unavailable";
  note.textContent = failure.notImplemented
    ? "Cost data is not available yet — the host verb cost.get_current is unimplemented."
    : `Couldn't load cost data: ${failure.message}`;
  body.replaceChildren(note);
}

function applySnapshot(root: HTMLElement, snapshot: CostSnapshot): void {
  setText(root, "cost-usd", formatUsd(snapshot.usd));
  setText(root, "cost-tokens-in", formatInt(snapshot.tokens_in));
  setText(root, "cost-tokens-out", formatInt(snapshot.tokens_out));
  setText(root, "cost-as-of", snapshot.as_of || "—");
}

function setText(root: HTMLElement, role: string, text: string): void {
  const el = root.querySelector<HTMLElement>(`[data-role="${role}"]`);
  if (el) el.textContent = text;
}

function formatUsd(n: number): string {
  return `$${n.toFixed(2)}`;
}

function formatInt(n: number): string {
  return n.toLocaleString("en-US");
}
