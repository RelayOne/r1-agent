// SPDX-License-Identifier: MIT
//
// SOW-tree panel (R1D-3.1 / R1D-3.2).
//
// Renders each active R1 session as an ARIA tree with expandable
// children down two levels: session -> acceptance criteria -> tasks.
// The top-level list comes from the `session_list` stub; children are
// fetched lazily through the `session_tree` stub on first expand.
// Panels render meaningful empty-state rows when the stubs return no
// data.
//
// Keyboard: Enter or Space toggles the focused session row. The
// `aria-expanded` / `role="tree"` / `role="treeitem"` / `role="group"`
// attributes keep screen readers in sync with the visual state.

import { classifyIpcError, invokeStub } from "../ipc-stub";
import type {
  SessionSummary,
  SessionTreeNode,
  SessionTreeResult,
} from "../types/ipc";

interface SessionRowState {
  session: SessionSummary;
  expanded: boolean;
  loaded: boolean;
  children: SessionTreeNode[];
}

export function renderPanel(root: HTMLElement): void {
  root.classList.add("r1-panel", "r1-panel-sow-tree");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>SOW Tree</h2>
      <span class="r1-panel-subtitle">sessions &rarr; acceptance criteria &rarr; tasks</span>
    </header>
    <div class="r1-panel-body">
      <ul
        class="r1-sow-tree"
        data-role="sow-tree"
        role="tree"
        aria-label="SOW tree"
        aria-live="polite"
      >
        <li class="r1-empty" role="none">Loading sessions&hellip;</li>
      </ul>
    </div>
  `;

  const list = root.querySelector<HTMLUListElement>('[data-role="sow-tree"]');
  if (!list) return;

  void loadSessions(list);
}

async function loadSessions(list: HTMLUListElement): Promise<void> {
  let sessions: SessionSummary[];
  try {
    sessions = await invokeStub<SessionSummary[]>(
      "session_list",
      "R1D-3",
      [],
    );
  } catch (err) {
    // In a real Tauri WebView this rejects today: session_list is not
    // registered in the Rust host's generate_handler!. Render the
    // truthful state instead of hanging on the loading row or showing
    // a misleading "No sessions yet" (audit A035).
    renderTreeUnavailable(list, "session_list", err);
    return;
  }
  renderTree(list, sessions);
}

/**
 * Truthful unavailable / error row for the tree root or a children
 * group. Mirrors the pattern used by mcp-servers.ts / observability.ts.
 */
function renderTreeUnavailable(
  container: HTMLUListElement,
  verb: string,
  err: unknown,
): void {
  const failure = classifyIpcError(err);
  const row = document.createElement("li");
  row.className = "r1-empty";
  row.dataset.role = "sow-unavailable";
  row.setAttribute("role", "none");
  row.textContent = failure.notImplemented
    ? `Session tree IPC is not wired yet — the ${verb} verb is unimplemented on the host.`
    : `Session tree IPC is not wired yet — the ${verb} host command is not registered (${failure.message}).`;
  container.replaceChildren(row);
}

function renderTree(list: HTMLUListElement, sessions: SessionSummary[]): void {
  if (sessions.length === 0) {
    list.innerHTML = `
      <li class="r1-empty" role="none">
        No sessions yet. Start one from the composer (R1D-2.5).
      </li>
    `;
    return;
  }

  list.innerHTML = "";
  sessions.forEach((session) => {
    const state: SessionRowState = {
      session,
      expanded: false,
      loaded: false,
      children: [],
    };
    list.appendChild(renderSessionRow(state));
  });
}

function renderSessionRow(state: SessionRowState): HTMLLIElement {
  const { session } = state;
  const li = document.createElement("li");
  li.className = "r1-sow-node r1-sow-session";
  li.dataset.sessionId = session.session_id;
  li.setAttribute("role", "treeitem");
  li.setAttribute("aria-expanded", "false");
  li.tabIndex = 0;

  li.innerHTML = `
    <div class="r1-sow-row" data-role="session-row">
      <span class="r1-sow-twisty" aria-hidden="true">&#9656;</span>
      <span class="r1-sow-status r1-status-${session.status}" aria-hidden="true"></span>
      <span class="r1-sow-title">${escapeHtml(session.title)}</span>
      <span class="r1-sow-meta">${escapeHtml(session.started_at)}</span>
    </div>
    <ul
      class="r1-sow-children"
      data-role="session-children"
      role="group"
      hidden
    ></ul>
  `;

  const toggle = () => toggleRow(li, state);
  li.addEventListener("click", (event) => {
    const target = event.target as HTMLElement | null;
    if (target && target.closest('[data-role="session-children"]')) return;
    toggle();
  });
  li.addEventListener("keydown", (event) => {
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      toggle();
    }
  });

  return li;
}

function toggleRow(li: HTMLLIElement, state: SessionRowState): void {
  state.expanded = !state.expanded;
  li.setAttribute("aria-expanded", String(state.expanded));
  li.classList.toggle("is-expanded", state.expanded);

  const children = li.querySelector<HTMLUListElement>(
    '[data-role="session-children"]',
  );
  if (!children) return;

  if (!state.expanded) {
    children.hidden = true;
    return;
  }

  children.hidden = false;
  if (!state.loaded) {
    void loadChildren(state, children);
  }
}

async function loadChildren(
  state: SessionRowState,
  container: HTMLUListElement,
): Promise<void> {
  container.innerHTML = `<li class="r1-empty" role="none">Loading&hellip;</li>`;

  let result: SessionTreeResult;
  try {
    result = await invokeStub<SessionTreeResult>(
      "session_tree",
      "R1D-3",
      { nodes: [] },
      { session_id: state.session.session_id },
    );
  } catch (err) {
    // session_tree is likewise unregistered in the Rust host; render
    // the truthful state in the children group (audit A035).
    renderTreeUnavailable(container, "session_tree", err);
    return;
  }
  state.children = result.nodes;
  state.loaded = true;
  renderChildren(container, result.nodes);
}

function renderChildren(
  container: HTMLUListElement,
  nodes: SessionTreeNode[],
): void {
  if (nodes.length === 0) {
    container.innerHTML = `
      <li class="r1-empty" role="none">
        No children yet &mdash; R1D-3.4 will wire this.
      </li>
    `;
    return;
  }

  container.innerHTML = nodes.map((n) => renderTreeNodeMarkup(n)).join("");
}

function renderTreeNodeMarkup(node: SessionTreeNode): string {
  const hasChildren = node.children.length > 0;
  const childMarkup = hasChildren
    ? `<ul class="r1-sow-children" role="group">${node.children
        .map(renderTreeNodeMarkup)
        .join("")}</ul>`
    : "";
  return `
    <li
      class="r1-sow-node r1-sow-${node.kind}"
      role="treeitem"
      aria-expanded="${hasChildren ? "true" : "false"}"
      data-node-id="${escapeHtml(node.id)}"
      data-status="${node.status}"
    >
      <div class="r1-sow-row">
        <span class="r1-sow-kind">${node.kind}</span>
        <span class="r1-sow-title">${escapeHtml(node.label)}</span>
        <span class="r1-status-pill r1-status-${node.status}">${node.status}</span>
      </div>
      ${childMarkup}
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
