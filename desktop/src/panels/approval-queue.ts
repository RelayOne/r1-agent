// SPDX-License-Identifier: MIT
//
// Approval queue panel (R1D-10.2 — UI side).
//
// Lists pending operator-approval gates raised by autonomous sessions.
// Each row shows the requesting session, kind of action, summary, and
// approve / reject buttons with optional comment. Auto-refreshes on a
// 5s interval until the panel is detached.

import { invokeStub } from "../ipc-stub";
import type {
  ApprovalDecision,
  ApprovalOkResult,
  ApprovalRequest,
} from "../types/ipc";

interface PanelState {
  requests: ApprovalRequest[];
  refreshHandle: number | null;
}

const REFRESH_INTERVAL_MS = 5000;

export function renderPanel(root: HTMLElement): void {
  root.classList.add("r1-panel", "r1-panel-approval-queue");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>Approvals</h2>
      <span class="r1-panel-subtitle">human-in-the-loop gates</span>
      <span class="r1-approval-badge" data-role="approval-badge" hidden>0</span>
    </header>
    <div class="r1-panel-body r1-approval-body">
      <ul class="r1-approval-list" data-role="approval-list" aria-live="polite">
        <li class="r1-empty">Loading approvals&hellip;</li>
      </ul>
    </div>
  `;

  const list = root.querySelector<HTMLUListElement>('[data-role="approval-list"]');
  if (!list) return;

  const state: PanelState = { requests: [], refreshHandle: null };

  list.addEventListener("click", (ev) => {
    const target = ev.target;
    if (!(target instanceof HTMLElement)) return;
    const row = target.closest<HTMLElement>(".r1-approval-row");
    if (!row) return;
    const id = row.dataset.approvalId;
    if (!id) return;
    const role = target.dataset.role;
    if (role === "approval-approve" || role === "approval-reject") {
      const decision = role === "approval-approve" ? "approve" : "reject";
      const commentInput = row.querySelector<HTMLInputElement>('[data-role="approval-comment"]');
      const comment = commentInput?.value.trim() || undefined;
      void handleDecide(root, state, { id, decision, comment });
    }
  });

  void refresh(root, state);
  state.refreshHandle = window.setInterval(() => void refresh(root, state), REFRESH_INTERVAL_MS);
}

async function refresh(root: HTMLElement, state: PanelState): Promise<void> {
  const requests = await invokeStub<ApprovalRequest[]>("approval_list", "R1D-10", []);
  state.requests = requests;
  renderList(root, state);
}

function renderList(root: HTMLElement, state: PanelState): void {
  const list = root.querySelector<HTMLUListElement>('[data-role="approval-list"]');
  const badge = root.querySelector<HTMLElement>('[data-role="approval-badge"]');
  if (!list) return;
  if (badge) {
    if (state.requests.length === 0) {
      badge.hidden = true;
    } else {
      badge.hidden = false;
      badge.textContent = String(state.requests.length);
    }
  }
  if (state.requests.length === 0) {
    list.innerHTML = `<li class="r1-empty">No pending approvals.</li>`;
    return;
  }
  list.innerHTML = state.requests.map(renderRow).join("");
}

function renderRow(req: ApprovalRequest): string {
  const detail = req.detail ? `<pre class="r1-approval-detail">${escapeHtml(req.detail)}</pre>` : "";
  const expires = req.expires_at ? `<span class="r1-approval-expires">expires ${escapeHtml(req.expires_at)}</span>` : "";
  return `
    <li class="r1-approval-row" data-approval-id="${escapeHtml(req.id)}">
      <header class="r1-approval-row-head">
        <span class="r1-approval-kind r1-approval-kind-${req.kind}">${escapeHtml(req.kind)}</span>
        <span class="r1-approval-session">${escapeHtml(req.session_title)}</span>
        <span class="r1-approval-at">${escapeHtml(req.requested_at)}</span>
      </header>
      <p class="r1-approval-summary">${escapeHtml(req.summary)}</p>
      ${detail}
      <footer class="r1-approval-row-foot">
        <input type="text" class="r1-approval-comment-input" data-role="approval-comment" placeholder="Optional comment">
        <button type="button" class="r1-btn" data-role="approval-reject">Reject</button>
        <button type="button" class="r1-btn r1-btn-primary" data-role="approval-approve">Approve</button>
        ${expires}
      </footer>
    </li>
  `;
}

async function handleDecide(root: HTMLElement, state: PanelState, decision: ApprovalDecision): Promise<void> {
  const result = await invokeStub<ApprovalOkResult>(
    "approval_decide",
    "R1D-10",
    { ok: true },
    decision as unknown as Record<string, unknown>,
  );
  if (!result.ok) {
    alert(`Decision failed for ${decision.id}`);
    return;
  }
  await refresh(root, state);
}

function escapeHtml(raw: string): string {
  return raw
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}
