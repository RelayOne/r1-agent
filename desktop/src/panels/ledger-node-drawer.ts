// SPDX-License-Identifier: MIT
//
// Ledger node-detail drawer (R1D-5.2 + R1D-5.4).
//
// Shared overlay that slides in from the right when the user clicks a
// timeline row in the ledger browser. Renders node kind, timestamp,
// content hash, parent hash, a kind-specific payload view, and a
// crypto-shred action guarded by a double-confirm modal.
//
// The ledger-viewer panel owns the click wiring; this module only
// renders, controls visibility, and invokes the shred stub. On a
// successful shred the caller is notified via `onShredded(nodeId)` so
// it can update the timeline in place.

import { invokeStub } from "../ipc-stub";
import type { LedgerNode, LedgerShredResult } from "../types/ipc";

const DRAWER_ID = "r1-ledger-node-drawer";
const BACKDROP_ID = "r1-ledger-node-backdrop";
const CONFIRM_ID = "r1-ledger-shred-confirm";

type ShredCallback = (nodeId: string) => void;

interface DrawerHandlers {
  onShredded?: ShredCallback;
}

let drawerRoot: HTMLElement | null = null;
let backdropRoot: HTMLElement | null = null;
let confirmRoot: HTMLElement | null = null;
let lastFocus: HTMLElement | null = null;
let currentNode: LedgerNode | null = null;
let currentHandlers: DrawerHandlers = {};
let confirmStage: 0 | 1 | 2 = 0;

export function mountNodeDrawer(parent: HTMLElement): void {
  if (document.getElementById(DRAWER_ID)) return;

  const backdrop = document.createElement("div");
  backdrop.id = BACKDROP_ID;
  backdrop.className = "r1-drawer-backdrop";
  backdrop.hidden = true;
  backdrop.addEventListener("click", () => closeNodeDrawer());

  const drawer = document.createElement("aside");
  drawer.id = DRAWER_ID;
  drawer.className = "r1-drawer r1-ledger-node-drawer";
  drawer.setAttribute("role", "dialog");
  drawer.setAttribute("aria-modal", "true");
  drawer.setAttribute("aria-labelledby", `${DRAWER_ID}-title`);
  drawer.hidden = true;
  drawer.tabIndex = -1;
  drawer.innerHTML = `
    <header class="r1-drawer-header">
      <h2 id="${DRAWER_ID}-title" class="r1-drawer-title">Ledger Node</h2>
      <button
        type="button"
        class="r1-btn r1-drawer-close"
        data-role="drawer-close"
        aria-label="Close node detail drawer"
      >Close</button>
    </header>
    <div class="r1-drawer-body" data-role="drawer-body">
      <p class="r1-empty">Select a node to inspect.</p>
    </div>
    <footer class="r1-drawer-footer" data-role="drawer-footer" hidden>
      <button
        type="button"
        class="r1-btn r1-btn-danger"
        data-role="shred-btn"
      >Crypto-shred</button>
    </footer>
  `;
  drawer
    .querySelector<HTMLButtonElement>('[data-role="drawer-close"]')
    ?.addEventListener("click", () => closeNodeDrawer());
  drawer
    .querySelector<HTMLButtonElement>('[data-role="shred-btn"]')
    ?.addEventListener("click", () => openConfirm());

  const confirm = document.createElement("div");
  confirm.id = CONFIRM_ID;
  confirm.className = "r1-modal r1-ledger-shred-confirm";
  confirm.setAttribute("role", "alertdialog");
  confirm.setAttribute("aria-modal", "true");
  confirm.setAttribute("aria-labelledby", `${CONFIRM_ID}-title`);
  confirm.hidden = true;
  confirm.innerHTML = `
    <div class="r1-modal-panel">
      <h3 id="${CONFIRM_ID}-title" class="r1-modal-title">Crypto-shred node?</h3>
      <p class="r1-modal-body" data-role="confirm-body">
        This action drops the payload bytes and marks the node shredded
        in the meta-ledger. The content hash stays on the chain so
        verify-chain still passes, but the payload is gone forever.
      </p>
      <div class="r1-modal-actions">
        <button
          type="button"
          class="r1-btn"
          data-role="confirm-cancel"
        >Cancel</button>
        <button
          type="button"
          class="r1-btn r1-btn-danger"
          data-role="confirm-advance"
        >Yes, shred</button>
      </div>
    </div>
  `;
  confirm
    .querySelector<HTMLButtonElement>('[data-role="confirm-cancel"]')
    ?.addEventListener("click", () => closeConfirm());
  confirm
    .querySelector<HTMLButtonElement>('[data-role="confirm-advance"]')
    ?.addEventListener("click", () => advanceConfirm());

  parent.appendChild(backdrop);
  parent.appendChild(drawer);
  parent.appendChild(confirm);

  backdropRoot = backdrop;
  drawerRoot = drawer;
  confirmRoot = confirm;

  document.addEventListener("keydown", handleKeydown);
}

export async function openNodeDrawer(
  node: LedgerNode,
  handlers: DrawerHandlers = {},
): Promise<void> {
  if (!drawerRoot || !backdropRoot) return;

  lastFocus = document.activeElement instanceof HTMLElement
    ? document.activeElement
    : null;

  currentNode = node;
  currentHandlers = handlers;

  renderDrawerBody(node);

  backdropRoot.hidden = false;
  drawerRoot.hidden = false;
  drawerRoot.classList.add("is-open");
  drawerRoot.focus();
}

export function closeNodeDrawer(): void {
  closeConfirm();
  if (!drawerRoot || !backdropRoot) return;
  drawerRoot.classList.remove("is-open");
  drawerRoot.hidden = true;
  backdropRoot.hidden = true;
  currentNode = null;
  currentHandlers = {};
  if (lastFocus && document.body.contains(lastFocus)) {
    lastFocus.focus();
  }
  lastFocus = null;
}

function renderDrawerBody(node: LedgerNode): void {
  if (!drawerRoot) return;

  const title = drawerRoot.querySelector<HTMLHeadingElement>(
    `#${DRAWER_ID}-title`,
  );
  if (title) title.textContent = `Node - ${node.kind}`;

  const body = drawerRoot.querySelector<HTMLDivElement>(
    '[data-role="drawer-body"]',
  );
  if (body) body.innerHTML = renderNodeMarkup(node);

  const footer = drawerRoot.querySelector<HTMLDivElement>(
    '[data-role="drawer-footer"]',
  );
  const shredBtn = drawerRoot.querySelector<HTMLButtonElement>(
    '[data-role="shred-btn"]',
  );
  if (footer) footer.hidden = false;
  if (shredBtn) {
    shredBtn.disabled = node.shredded;
    shredBtn.textContent = node.shredded ? "Already shredded" : "Crypto-shred";
  }
}

function renderNodeMarkup(node: LedgerNode): string {
  const parent = node.parent_hash
    ? `<code>${escapeHtml(node.parent_hash)}</code>`
    : `<span class="r1-muted">(genesis)</span>`;
  const shredRow = node.shredded
    ? `<dt>Status</dt><dd><span class="r1-ledger-shredded-pill">SHREDDED</span></dd>`
    : "";
  return `
    <dl class="r1-ledger-node-meta">
      <dt>ID</dt><dd><code>${escapeHtml(node.id)}</code></dd>
      <dt>Kind</dt><dd>${escapeHtml(node.kind)}</dd>
      <dt>Timestamp</dt><dd><time datetime="${escapeHtml(node.timestamp)}">${escapeHtml(node.timestamp)}</time></dd>
      <dt>Content hash</dt><dd><code>${escapeHtml(node.content_hash)}</code></dd>
      <dt>Parent hash</dt><dd>${parent}</dd>
      ${shredRow}
    </dl>
    <section class="r1-ledger-node-payload" aria-label="Payload">
      <h3 class="r1-ledger-node-payload-title">Payload</h3>
      ${renderPayload(node)}
    </section>
  `;
}

function renderPayload(node: LedgerNode): string {
  if (node.shredded) {
    return `<p class="r1-empty">Payload shredded. Content hash is retained on chain.</p>`;
  }
  const renderer = PAYLOAD_RENDERERS[node.kind];
  if (renderer) return renderer(node.payload);
  return renderGenericJson(node.payload);
}

type PayloadRenderer = (payload: Record<string, unknown>) => string;

const PAYLOAD_RENDERERS: Record<string, PayloadRenderer> = {
  session_started: (p) => renderFields(p, [
    ["session_id", "Session ID"],
    ["prompt", "Prompt"],
    ["skill_pack", "Skill pack"],
    ["provider", "Provider"],
    ["budget_usd", "Budget (USD)"],
  ]),
  session_ended: (p) => renderFields(p, [
    ["session_id", "Session ID"],
    ["reason", "Reason"],
    ["at", "Ended at"],
  ]),
  task: (p) => renderFields(p, [
    ["task_id", "Task ID"],
    ["title", "Title"],
    ["status", "Status"],
    ["owner", "Owner"],
  ]),
  task_dispatched: (p) => renderFields(p, [
    ["task_id", "Task ID"],
    ["dispatched_to", "Dispatched to"],
    ["at", "At"],
  ]),
  verification_evidence: (p) => renderFields(p, [
    ["tier", "Tier"],
    ["kind", "Evidence kind"],
    ["summary", "Summary"],
    ["artifact_ref", "Artifact ref"],
  ]),
  memory_stored: (p) => renderFields(p, [
    ["scope", "Scope"],
    ["key", "Key"],
    ["value", "Value"],
    ["updated_at", "Updated at"],
  ]),
  memory_recalled: (p) => renderFields(p, [
    ["scope", "Scope"],
    ["key", "Key"],
    ["recall_count", "Recall count"],
  ]),
  skill_applied: (p) => renderFields(p, [
    ["skill_name", "Skill"],
    ["version", "Version"],
    ["task_id", "Task ID"],
    ["outcome", "Outcome"],
  ]),
  decision_internal: (p) => renderFields(p, [
    ["topic", "Topic"],
    ["decision", "Decision"],
    ["rationale", "Rationale"],
  ]),
  escalation: (p) => renderFields(p, [
    ["reason", "Reason"],
    ["from", "From"],
    ["to", "To"],
    ["severity", "Severity"],
  ]),
};

function renderFields(
  payload: Record<string, unknown>,
  fields: Array<[string, string]>,
): string {
  const rows: string[] = [];
  for (const [key, label] of fields) {
    if (!(key in payload)) continue;
    const value = formatScalar(payload[key]);
    if (value === null) continue;
    rows.push(`<dt>${escapeHtml(label)}</dt><dd>${value}</dd>`);
  }
  if (rows.length === 0) return renderGenericJson(payload);
  return `<dl class="r1-ledger-node-fields">${rows.join("")}</dl>`;
}

function formatScalar(value: unknown): string | null {
  if (value === null || value === undefined) return null;
  if (typeof value === "string") {
    if (value === "") return `<span class="r1-muted">(empty)</span>`;
    return escapeHtml(value);
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return escapeHtml(String(value));
  }
  return `<code>${escapeHtml(JSON.stringify(value))}</code>`;
}

function renderGenericJson(payload: Record<string, unknown>): string {
  const keys = Object.keys(payload);
  if (keys.length === 0) {
    return `<p class="r1-empty">Empty payload.</p>`;
  }
  const pretty = JSON.stringify(payload, null, 2);
  return `<pre class="r1-ledger-node-json"><code>${escapeHtml(pretty)}</code></pre>`;
}

function openConfirm(): void {
  if (!confirmRoot || !currentNode) return;
  if (currentNode.shredded) return;
  confirmStage = 1;
  const body = confirmRoot.querySelector<HTMLParagraphElement>(
    '[data-role="confirm-body"]',
  );
  const advance = confirmRoot.querySelector<HTMLButtonElement>(
    '[data-role="confirm-advance"]',
  );
  if (body) {
    body.textContent =
      "This action drops the payload bytes and marks the node shredded in the meta-ledger. The content hash stays on the chain so verify-chain still passes, but the payload is gone forever.";
  }
  if (advance) {
    advance.textContent = "Yes, shred";
    advance.disabled = false;
  }
  confirmRoot.hidden = false;
  confirmRoot.classList.add("is-open");
}

function closeConfirm(): void {
  if (!confirmRoot) return;
  confirmRoot.classList.remove("is-open");
  confirmRoot.hidden = true;
  confirmStage = 0;
}

async function advanceConfirm(): Promise<void> {
  if (!confirmRoot || !currentNode) return;

  if (confirmStage === 1) {
    confirmStage = 2;
    const body = confirmRoot.querySelector<HTMLParagraphElement>(
      '[data-role="confirm-body"]',
    );
    const advance = confirmRoot.querySelector<HTMLButtonElement>(
      '[data-role="confirm-advance"]',
    );
    if (body) {
      body.textContent = `Final confirmation: shred node ${currentNode.id}? This cannot be undone.`;
    }
    if (advance) advance.textContent = "Confirm shred";
    return;
  }

  const node = currentNode;
  const advance = confirmRoot.querySelector<HTMLButtonElement>(
    '[data-role="confirm-advance"]',
  );
  if (advance) {
    advance.disabled = true;
    advance.textContent = "Shredding…";
  }

  const result = await invokeStub<LedgerShredResult>(
    "ledger_shred",
    "R1D-5",
    { ok: true },
    { session_id: "", node_id: node.id },
  );

  closeConfirm();
  if (!result.ok) return;

  node.shredded = true;
  renderDrawerBody(node);
  currentHandlers.onShredded?.(node.id);
}

function handleKeydown(event: KeyboardEvent): void {
  if (event.key !== "Escape") return;
  if (confirmRoot && !confirmRoot.hidden) {
    event.preventDefault();
    closeConfirm();
    return;
  }
  if (!drawerRoot || drawerRoot.hidden) return;
  event.preventDefault();
  closeNodeDrawer();
}

function escapeHtml(raw: string): string {
  return raw
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}
