// SPDX-License-Identifier: MIT
//
// Descent-ladder panel (R1D-3.3 / R1D-3.4).
//
// Renders the eight verification tiers T1..T8 as a vertical ladder.
// Each tier row shows its name, status pill, and an evidence slot that
// opens the shared descent-evidence drawer on click or Enter/Space.
// Tier rows pick up per-tier color from CSS custom properties
// introduced by `TIER_COLORS` below.

import { classifyIpcError, invokeStub } from "../ipc-stub";
import { ALL_DESCENT_TIERS } from "../types/ipc-const";
import type {
  DescentStatus,
  DescentTier,
  DescentTierRow,
} from "../types/ipc";
import { openDrawer } from "./descent-evidence";

/**
 * Per-tier brand colors (T1..T8) injected as CSS custom properties on
 * the document root. The palette walks cold-to-warm so that higher
 * tiers read as higher-risk.
 */
export const TIER_COLORS: Record<DescentTier, string> = {
  T1: "#4ea1ff",
  T2: "#3ee0c8",
  T3: "#6ddf5b",
  T4: "#b7d84a",
  T5: "#f0c84a",
  T6: "#f39a3d",
  T7: "#ef6e4a",
  T8: "#d94a4a",
};

export function renderPanel(root: HTMLElement): void {
  applyTierPalette();
  const sessionId = root.dataset.sessionId?.trim() ?? "";
  const acId = root.dataset.acId?.trim() ?? "";

  root.classList.add("r1-panel", "r1-panel-descent-ladder");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>Descent Ladder</h2>
      <span class="r1-panel-subtitle">T1 &rarr; T8 verification tiers</span>
    </header>
    <div class="r1-panel-body">
      <p class="r1-empty" data-role="descent-current-tier-note"></p>
      <ol class="r1-descent-ladder" data-role="descent-ladder">
        ${ALL_DESCENT_TIERS.map(renderTierRow).join("")}
      </ol>
    </div>
  `;

  const ladder = root.querySelector<HTMLOListElement>(
    '[data-role="descent-ladder"]',
  );
  if (!ladder) return;
  if (sessionId) ladder.dataset.sessionId = sessionId;
  if (acId) ladder.dataset.acId = acId;

  wireEvidenceSlots(ladder);
  const note = root.querySelector<HTMLElement>('[data-role="descent-current-tier-note"]');
  if (!sessionId) {
    if (note) {
      note.textContent = "Select an active session to inspect the current descent tier.";
    }
    return;
  }
  if (note) note.hidden = true;
  void loadTiers(ladder, sessionId, acId || undefined);
}

function applyTierPalette(): void {
  const style = document.documentElement.style;
  for (const tier of ALL_DESCENT_TIERS) {
    style.setProperty(`--r1-tier-${tier.toLowerCase()}`, TIER_COLORS[tier]);
  }
}

function wireEvidenceSlots(ladder: HTMLOListElement): void {
  const slots = ladder.querySelectorAll<HTMLButtonElement>(
    '[data-role="evidence"]',
  );
  slots.forEach((slot) => {
    const tier = slot.dataset.tier as DescentTier | undefined;
    if (!tier) return;
    slot.addEventListener("click", () => {
      const sessionId = ladder.dataset.sessionId ?? "";
      const acId = ladder.dataset.acId || undefined;
      void openDrawer(tier, sessionId, acId);
    });
    slot.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      const sessionId = ladder.dataset.sessionId ?? "";
      const acId = ladder.dataset.acId || undefined;
      void openDrawer(tier, sessionId, acId);
    });
  });
}

async function loadTiers(
  ladder: HTMLOListElement,
  sessionId: string,
  acId?: string,
): Promise<void> {
  try {
    const rows = await invokeStub<DescentTierRow[]>(
      "descent_current_tier",
      "R1D-3",
      [],
      acId ? { session_id: sessionId, ac_id: acId } : { session_id: sessionId },
    );
    applyStatuses(ladder, rows);
  } catch (err) {
    renderTiersUnavailable(ladder, err);
  }
}

/**
 * Surface a truthful unavailable / error note above the ladder when
 * the host RPC rejects, instead of leaving every tier pinned at the
 * seeded "pending" state (audit A034).
 */
function renderTiersUnavailable(ladder: HTMLOListElement, err: unknown): void {
  const root = ladder.closest<HTMLElement>(".r1-panel-descent-ladder");
  const note = root?.querySelector<HTMLElement>(
    '[data-role="descent-current-tier-note"]',
  );
  if (!note) return;
  const failure = classifyIpcError(err);
  note.hidden = false;
  note.dataset.state = "unavailable";
  note.textContent = failure.notImplemented
    ? "Tier statuses are not available yet — the host verb descent.current_tier is unimplemented; the rows below are seeded placeholders."
    : `Couldn't load tier statuses: ${failure.message}`;
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
    <li
      class="r1-descent-tier"
      data-tier="${tier}"
      data-status="pending"
      style="--r1-tier-color: var(--r1-tier-${tier.toLowerCase()});"
    >
      <span class="r1-descent-tier-name">${tier}</span>
      <span class="r1-status-pill r1-status-pending">pending</span>
      <button
        type="button"
        class="r1-descent-evidence"
        data-role="evidence"
        data-tier="${tier}"
        aria-label="Show evidence for ${tier}"
      >Evidence</button>
    </li>
  `;
}
