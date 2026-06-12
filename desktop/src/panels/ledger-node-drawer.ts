// SPDX-License-Identifier: MIT
//
// Ledger node-detail drawer (R1D-5.2).
//
// Shared overlay that slides in from the right when the user clicks a
// event row in the ledger browser. Renders node kind, timestamp,
// content hash, parent hash, and a kind-specific payload view.
//
// The desktop host does not implement ledger mutation verbs yet, so
// the drawer is read-only and does not claim crypto-shred support.

import type { LedgerNode } from "../types/ipc";

const DRAWER_ID = "r1-ledger-node-drawer";
const BACKDROP_ID = "r1-ledger-node-backdrop";

let drawerRoot: HTMLElement | null = null;
let backdropRoot: HTMLElement | null = null;
let lastFocus: HTMLElement | null = null;
let currentNode: LedgerNode | null = null;

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
  `;
  drawer
    .querySelector<HTMLButtonElement>('[data-role="drawer-close"]')
    ?.addEventListener("click", () => closeNodeDrawer());

  parent.appendChild(backdrop);
  parent.appendChild(drawer);

  backdropRoot = backdrop;
  drawerRoot = drawer;

  document.addEventListener("keydown", handleKeydown);
}

export async function openNodeDrawer(node: LedgerNode): Promise<void> {
  if (!drawerRoot || !backdropRoot) return;

  lastFocus = document.activeElement instanceof HTMLElement
    ? document.activeElement
    : null;

  currentNode = node;
  renderDrawerBody(node);

  backdropRoot.hidden = false;
  drawerRoot.hidden = false;
  drawerRoot.classList.add("is-open");
  drawerRoot.focus();
}

export function closeNodeDrawer(): void {
  if (!drawerRoot || !backdropRoot) return;
  drawerRoot.classList.remove("is-open");
  drawerRoot.hidden = true;
  backdropRoot.hidden = true;
  currentNode = null;
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
      <p class="r1-ledger-node-readonly-note" data-role="ledger-drawer-readonly">
        Read-only node detail. This desktop host does not expose verify or shred controls.
      </p>
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

function handleKeydown(event: KeyboardEvent): void {
  if (event.key !== "Escape") return;
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
