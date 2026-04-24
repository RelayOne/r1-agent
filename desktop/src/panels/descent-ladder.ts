// SPDX-License-Identifier: MIT
//
// Descent-ladder panel (R1D-2).
//
// Renders the eight verification tiers T1..T8 as a vertical ladder.
// Each tier row shows its name, status pill, and an evidence-link slot
// (empty at scaffold time). The `descent.current_tier` call is issued
// through `invokeStub` with a TODO R1D-3 tag and resolves to an empty
// array; the panel falls back to rendering every tier as `pending`.
//
// Real data-fetching + evidence drill-down lands in R1D-3.3 / R1D-3.4.

import { invokeStub } from "../ipc-stub";
import { ALL_DESCENT_TIERS } from "../types/ipc-const";
import type {
  DescentStatus,
  DescentTier,
  DescentTierRow,
} from "../types/ipc";

export function renderPanel(root: HTMLElement): void {
  root.classList.add("r1-panel", "r1-panel-descent-ladder");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>Descent Ladder</h2>
      <span class="r1-panel-subtitle">T1 &rarr; T8 verification tiers</span>
    </header>
    <div class="r1-panel-body">
      <ol class="r1-descent-ladder" data-role="descent-ladder">
        ${ALL_DESCENT_TIERS.map(renderTierRow).join("")}
      </ol>
    </div>
  `;

  const ladder = root.querySelector<HTMLOListElement>(
    '[data-role="descent-ladder"]',
  );
  if (!ladder) return;

  void loadTiers(ladder);
}

async function loadTiers(ladder: HTMLOListElement): Promise<void> {
  const rows = await invokeStub<DescentTierRow[]>(
    "descent_current_tier",
    "R1D-3",
    [],
    { session_id: "" },
  );
  applyStatuses(ladder, rows);
}

function applyStatuses(
  ladder: HTMLOListElement,
  rows: DescentTierRow[],
): void {
  const byTier = new Map<DescentTier, DescentStatus>();
  for (const row of rows) byTier.set(row.tier, row.status);

  for (const tier of ALL_DESCENT_TIERS) {
    const li = ladder.querySelector<HTMLLIElement>(`[data-tier="${tier}"]`);
    if (!li) continue;
    const status: DescentStatus = byTier.get(tier) ?? "pending";
    li.dataset.status = status;
    const pill = li.querySelector<HTMLSpanElement>(".r1-status-pill");
    if (pill) pill.textContent = status;
  }
}

function renderTierRow(tier: DescentTier): string {
  return `
    <li class="r1-descent-tier" data-tier="${tier}" data-status="pending">
      <span class="r1-descent-tier-name">${tier}</span>
      <span class="r1-status-pill r1-status-pending">pending</span>
      <span class="r1-descent-evidence" data-role="evidence"></span>
    </li>
  `;
}
