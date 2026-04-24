// SPDX-License-Identifier: MIT
//
// Descent-evidence drawer (R1D-3.4).
//
// A single shared overlay panel that slides in from the right when the
// user clicks a tier's evidence slot in the descent ladder. The drawer
// reads through the `descent_evidence` stub, which returns an empty
// result today. Close via the explicit button, the backdrop, or Escape.
//
// The descent-ladder panel owns the click wiring; this module only
// renders and controls visibility. The ladder calls `openDrawer(tier)`
// to show and `closeDrawer()` to hide.

import { invokeStub } from "../ipc-stub";
import type {
  DescentEvidence,
  DescentEvidenceResult,
  DescentTier,
} from "../types/ipc";

const DRAWER_ID = "r1-descent-evidence-drawer";
const BACKDROP_ID = "r1-descent-evidence-backdrop";

let drawerRoot: HTMLElement | null = null;
let backdropRoot: HTMLElement | null = null;
let lastFocus: HTMLElement | null = null;

export function mountDrawer(parent: HTMLElement): void {
  if (document.getElementById(DRAWER_ID)) return;

  const backdrop = document.createElement("div");
  backdrop.id = BACKDROP_ID;
  backdrop.className = "r1-drawer-backdrop";
  backdrop.hidden = true;
  backdrop.addEventListener("click", () => closeDrawer());

  const drawer = document.createElement("aside");
  drawer.id = DRAWER_ID;
  drawer.className = "r1-drawer r1-descent-evidence-drawer";
  drawer.setAttribute("role", "dialog");
  drawer.setAttribute("aria-modal", "true");
  drawer.setAttribute("aria-labelledby", `${DRAWER_ID}-title`);
  drawer.hidden = true;
  drawer.tabIndex = -1;
  drawer.innerHTML = `
    <header class="r1-drawer-header">
      <h2 id="${DRAWER_ID}-title" class="r1-drawer-title">Evidence</h2>
      <button
        type="button"
        class="r1-btn r1-drawer-close"
        data-role="drawer-close"
        aria-label="Close evidence drawer"
      >Close</button>
    </header>
    <div class="r1-drawer-body" data-role="drawer-body">
      <p class="r1-empty">Loading evidence&hellip;</p>
    </div>
  `;
  drawer
    .querySelector<HTMLButtonElement>('[data-role="drawer-close"]')
    ?.addEventListener("click", () => closeDrawer());

  parent.appendChild(backdrop);
  parent.appendChild(drawer);

  backdropRoot = backdrop;
  drawerRoot = drawer;

  document.addEventListener("keydown", handleKeydown);
}

export async function openDrawer(
  tier: DescentTier,
  sessionId: string,
  acId?: string,
): Promise<void> {
  if (!drawerRoot || !backdropRoot) return;

  lastFocus = document.activeElement instanceof HTMLElement
    ? document.activeElement
    : null;

  const title = drawerRoot.querySelector<HTMLHeadingElement>(
    `#${DRAWER_ID}-title`,
  );
  if (title) title.textContent = `Evidence - ${tier}`;
  drawerRoot.dataset.tier = tier;

  const body = drawerRoot.querySelector<HTMLDivElement>(
    '[data-role="drawer-body"]',
  );
  if (body) body.innerHTML = `<p class="r1-empty">Loading evidence&hellip;</p>`;

  backdropRoot.hidden = false;
  drawerRoot.hidden = false;
  drawerRoot.classList.add("is-open");
  drawerRoot.focus();

  const result = await invokeStub<DescentEvidenceResult>(
    "descent_evidence",
    "R1D-3",
    { tier, items: [] },
    { session_id: sessionId, tier, ac_id: acId },
  );

  if (drawerRoot.dataset.tier === tier && body) {
    renderItems(body, result.items);
  }
}

export function closeDrawer(): void {
  if (!drawerRoot || !backdropRoot) return;
  drawerRoot.classList.remove("is-open");
  drawerRoot.hidden = true;
  backdropRoot.hidden = true;
  if (lastFocus && document.body.contains(lastFocus)) {
    lastFocus.focus();
  }
  lastFocus = null;
}

function handleKeydown(event: KeyboardEvent): void {
  if (event.key !== "Escape") return;
  if (!drawerRoot || drawerRoot.hidden) return;
  event.preventDefault();
  closeDrawer();
}

function renderItems(body: HTMLDivElement, items: DescentEvidence[]): void {
  if (items.length === 0) {
    body.innerHTML = `
      <p class="r1-empty">
        No evidence yet &mdash; R1D-3.4 will wire this.
      </p>
    `;
    return;
  }

  body.innerHTML = `
    <ul class="r1-descent-evidence-list">
      ${items.map(renderItem).join("")}
    </ul>
  `;
}

function renderItem(item: DescentEvidence): string {
  const ref = item.artifact_ref
    ? `<code class="r1-descent-evidence-ref">${escapeHtml(item.artifact_ref)}</code>`
    : "";
  const at = item.at
    ? `<time class="r1-descent-evidence-at" datetime="${escapeHtml(item.at)}">${escapeHtml(item.at)}</time>`
    : "";
  return `
    <li class="r1-descent-evidence-item" data-kind="${item.kind}">
      <span class="r1-descent-evidence-kind">${escapeHtml(item.kind)}</span>
      <span class="r1-descent-evidence-summary">${escapeHtml(item.summary)}</span>
      ${ref}
      ${at}
    </li>
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
