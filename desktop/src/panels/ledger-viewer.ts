// SPDX-License-Identifier: MIT
//
// Ledger-viewer panel (R1D-5).
//
// Two-pane browser: session list on the left, node timeline for the
// selected session on the right. Clicking a timeline row opens the
// shared ledger-node drawer (R1D-5.2). A verify-chain button (R1D-5.3)
// lives in the header; NDJSON export (R1D-5.5) lives on each session
// row as a per-row action.

import { invokeStub } from "../ipc-stub";
import type {
  LedgerExportResult,
  LedgerNode,
  LedgerSessionSummary,
  LedgerSessionsResult,
  LedgerTimelineResult,
  LedgerVerifyResult,
} from "../types/ipc";
import { openNodeDrawer } from "./ledger-node-drawer";

interface LedgerViewState {
  sessions: LedgerSessionSummary[];
  selectedSessionId: string | null;
  timeline: LedgerNode[];
  verify: LedgerVerifyResult | null;
}

const STATE: LedgerViewState = {
  sessions: [],
  selectedSessionId: null,
  timeline: [],
  verify: null,
};

export function renderPanel(root: HTMLElement): void {
  root.classList.add("r1-panel", "r1-panel-ledger-viewer");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>Ledger Browser</h2>
      <span class="r1-panel-subtitle">sessions &rarr; node timeline</span>
      <button
        type="button"
        class="r1-btn"
        data-role="verify-chain"
        disabled
        title="Select a session to enable"
      >
        Verify chain
      </button>
    </header>
    <div class="r1-panel-body r1-ledger-browser">
      <aside class="r1-ledger-sessions" aria-label="Ledger sessions">
        <ul
          class="r1-ledger-session-list"
          data-role="session-list"
          aria-live="polite"
        >
          <li class="r1-empty">Loading sessions&hellip;</li>
        </ul>
      </aside>
      <section class="r1-ledger-timeline" aria-label="Node timeline">
        <div
          class="r1-ledger-verify-banner"
          data-role="verify-banner"
          hidden
        ></div>
        <ul
          class="r1-ledger-events"
          data-role="ledger-events"
          aria-live="polite"
        >
          <li class="r1-empty">Select a session to view nodes.</li>
        </ul>
      </section>
    </div>
  `;

  const verifyBtn = root.querySelector<HTMLButtonElement>(
    '[data-role="verify-chain"]',
  );
  verifyBtn?.addEventListener("click", () => {
    void runVerify(root);
  });

  void loadSessions(root);
}

async function loadSessions(root: HTMLElement): Promise<void> {
  const result = await invokeStub<LedgerSessionsResult>(
    "ledger_sessions",
    "R1D-5",
    { sessions: [] },
  );
  STATE.sessions = result.sessions;
  renderSessionList(root);
}

function renderSessionList(root: HTMLElement): void {
  const list = root.querySelector<HTMLUListElement>(
    '[data-role="session-list"]',
  );
  if (!list) return;

  if (STATE.sessions.length === 0) {
    list.innerHTML = `
      <li class="r1-empty">
        No sessions on ledger. Start one from the composer.
      </li>
    `;
    return;
  }

  list.innerHTML = STATE.sessions
    .map(
      (s) => `
        <li
          class="r1-ledger-session-row"
          data-session-id="${escapeHtml(s.session_id)}"
          data-selected="${s.session_id === STATE.selectedSessionId}"
          tabindex="0"
          role="button"
          aria-pressed="${s.session_id === STATE.selectedSessionId}"
        >
          <div class="r1-ledger-session-main">
            <code class="r1-ledger-session-id">${escapeHtml(s.session_id)}</code>
            <time class="r1-ledger-session-at" datetime="${escapeHtml(s.started_at)}">${escapeHtml(s.started_at)}</time>
          </div>
          <div class="r1-ledger-session-meta">
            <span class="r1-ledger-session-count">${s.node_count} node${s.node_count === 1 ? "" : "s"}</span>
            <button
              type="button"
              class="r1-btn r1-ledger-session-export"
              data-role="session-export"
              data-session-id="${escapeHtml(s.session_id)}"
              aria-label="Export ${escapeHtml(s.session_id)} as NDJSON"
            >Export</button>
          </div>
        </li>
      `,
    )
    .join("");

  list.querySelectorAll<HTMLLIElement>(".r1-ledger-session-row").forEach((li) => {
    const sessionId = li.dataset.sessionId ?? "";
    li.addEventListener("click", (event) => {
      const target = event.target as HTMLElement | null;
      if (target?.closest('[data-role="session-export"]')) return;
      void selectSession(root, sessionId);
    });
    li.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      void selectSession(root, sessionId);
    });
  });

  list.querySelectorAll<HTMLButtonElement>(
    '[data-role="session-export"]',
  ).forEach((btn) => {
    btn.addEventListener("click", (event) => {
      event.stopPropagation();
      const sessionId = btn.dataset.sessionId ?? "";
      void exportSession(sessionId);
    });
  });
}

async function selectSession(root: HTMLElement, sessionId: string): Promise<void> {
  STATE.selectedSessionId = sessionId;
  STATE.timeline = [];
  STATE.verify = null;
  renderSessionList(root);
  renderVerifyBanner(root);

  const verifyBtn = root.querySelector<HTMLButtonElement>(
    '[data-role="verify-chain"]',
  );
  if (verifyBtn) {
    verifyBtn.disabled = false;
    verifyBtn.title = "Verify this session's ledger chain";
  }

  const events = root.querySelector<HTMLUListElement>(
    '[data-role="ledger-events"]',
  );
  if (events) {
    events.innerHTML = `<li class="r1-empty">Loading nodes&hellip;</li>`;
  }

  const result = await invokeStub<LedgerTimelineResult>(
    "ledger_timeline",
    "R1D-5",
    { nodes: [] },
    { session_id: sessionId },
  );

  if (STATE.selectedSessionId !== sessionId) return;
  STATE.timeline = result.nodes;
  renderTimeline(root);
}

function renderTimeline(root: HTMLElement): void {
  const events = root.querySelector<HTMLUListElement>(
    '[data-role="ledger-events"]',
  );
  if (!events) return;

  if (STATE.timeline.length === 0) {
    events.innerHTML = `
      <li class="r1-empty">
        No nodes yet for this session. The ledger starts empty.
      </li>
    `;
    return;
  }

  events.innerHTML = STATE.timeline.map(renderTimelineRow).join("");

  events.querySelectorAll<HTMLLIElement>(".r1-ledger-event").forEach((li) => {
    const nodeId = li.dataset.nodeId ?? "";
    const node = STATE.timeline.find((n) => n.id === nodeId);
    if (!node) return;
    const open = () => {
      void openNodeDrawer(node, {
        onShredded: (shreddedId) => {
          const target = STATE.timeline.find((n) => n.id === shreddedId);
          if (target) target.shredded = true;
          renderTimeline(root);
        },
      });
    };
    li.addEventListener("click", open);
    li.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      open();
    });
  });
}

function renderTimelineRow(node: LedgerNode): string {
  const shredClass = node.shredded ? " is-shredded" : "";
  const shredPill = node.shredded
    ? `<span class="r1-ledger-shredded-pill" aria-label="Shredded">SHRED</span>`
    : "";
  const hashPrefix = node.content_hash
    ? node.content_hash.slice(0, 12)
    : node.id.slice(0, 12);
  return `
    <li
      class="r1-ledger-event${shredClass}"
      data-node-id="${escapeHtml(node.id)}"
      data-kind="${escapeHtml(node.kind)}"
      tabindex="0"
      role="button"
      aria-label="Inspect node ${escapeHtml(node.id)}"
    >
      <code class="r1-ledger-hash">${escapeHtml(hashPrefix)}</code>
      <span class="r1-ledger-type">${escapeHtml(node.kind)}</span>
      ${shredPill}
      <time class="r1-ledger-at" datetime="${escapeHtml(node.timestamp)}">${escapeHtml(node.timestamp)}</time>
    </li>
  `;
}

async function runVerify(root: HTMLElement): Promise<void> {
  const sessionId = STATE.selectedSessionId;
  if (!sessionId) return;

  const banner = root.querySelector<HTMLDivElement>(
    '[data-role="verify-banner"]',
  );
  if (banner) {
    banner.hidden = false;
    banner.className = "r1-ledger-verify-banner is-running";
    banner.textContent = "Verifying chain…";
  }

  const result = await invokeStub<LedgerVerifyResult>(
    "ledger_verify",
    "R1D-5",
    { passed: true, first_bad_offset: null },
    { session_id: sessionId },
  );

  if (STATE.selectedSessionId !== sessionId) return;
  STATE.verify = result;
  renderVerifyBanner(root);
}

function renderVerifyBanner(root: HTMLElement): void {
  const banner = root.querySelector<HTMLDivElement>(
    '[data-role="verify-banner"]',
  );
  if (!banner) return;

  const result = STATE.verify;
  if (!result) {
    banner.hidden = true;
    banner.className = "r1-ledger-verify-banner";
    banner.textContent = "";
    return;
  }

  banner.hidden = false;
  if (result.passed) {
    banner.className = "r1-ledger-verify-banner is-pass";
    banner.textContent = result.message ?? "Chain verified. All nodes pass.";
    return;
  }

  const offset = result.first_bad_offset;
  const where = offset === null ? "unknown offset" : `offset ${offset}`;
  const detail = result.message ? ` ${result.message}` : "";
  banner.className = "r1-ledger-verify-banner is-fail";
  banner.textContent = `Chain verification FAILED at ${where}.${detail}`;
}

async function exportSession(sessionId: string): Promise<void> {
  if (!sessionId) return;
  const result = await invokeStub<LedgerExportResult>(
    "ledger_export",
    "R1D-5",
    { ndjson: "" },
    { session_id: sessionId },
  );
  triggerNdjsonDownload(sessionId, result.ndjson);
}

function triggerNdjsonDownload(sessionId: string, ndjson: string): void {
  const blob = new Blob([ndjson], { type: "application/x-ndjson" });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = `${safeFilenamePart(sessionId)}.ndjson`;
  anchor.rel = "noopener";
  document.body.appendChild(anchor);
  anchor.click();
  document.body.removeChild(anchor);
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

function safeFilenamePart(raw: string): string {
  const cleaned = raw.replace(/[^a-zA-Z0-9._-]+/g, "_").replace(/^_+|_+$/g, "");
  return cleaned.length > 0 ? cleaned : "ledger-session";
}

function escapeHtml(raw: string): string {
  return raw
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}
