// SPDX-License-Identifier: MIT
//
// Cost-summary panel (R1D-2).
//
// Renders a single card with USD spend + in/out token counts. At
// scaffold time all three read $0 / 0. The `cost.get_current` call is
// issued through `invokeStub` with a TODO R1D-9 tag.
//
// Real per-provider latency histogram + time-range picker lands in
// R1D-9.1 / R1D-9.3.

import { invokeStub } from "../ipc-stub";
import type { CostSnapshot } from "../types/ipc";

const EMPTY_SNAPSHOT: CostSnapshot = {
  usd: 0,
  tokens_in: 0,
  tokens_out: 0,
  as_of: "",
};

export function renderPanel(root: HTMLElement): void {
  root.classList.add("r1-panel", "r1-panel-cost");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>Cost</h2>
      <span class="r1-panel-subtitle">session + all-time spend</span>
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
    </div>
  `;

  void loadCost(root);
}

async function loadCost(root: HTMLElement): Promise<void> {
  const snapshot = await invokeStub<CostSnapshot>(
    "cost_get_current",
    "R1D-9",
    EMPTY_SNAPSHOT,
  );
  applySnapshot(root, snapshot);
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
