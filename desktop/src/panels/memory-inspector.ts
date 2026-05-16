// SPDX-License-Identifier: MIT
//
// Memory-bus inspector panel (R1D-6).
//
// The desktop host currently exposes only the read-only memory verbs
// `memory_list_scopes` and `memory_query`. Older UI scaffolding
// claimed row history, import, delete, and full export support, and it
// also rendered fields the host does not provide. This panel now keeps
// the truthful subset: scope tabs, filtering, sorting, and a read-only
// table of `key` / `value` / `updated_at`.

import { invokeStub } from "../ipc-stub";
import { ALL_MEMORY_SCOPES } from "../types/ipc-const";
import type {
  MemoryEntry,
  MemoryListScopesResult,
  MemoryQueryResult,
  MemoryScope,
} from "../types/ipc";

type SortKey = "key" | "updated_at";
type SortDir = "asc" | "desc";

interface ScopeState {
  entries: MemoryEntry[];
  sortKey: SortKey;
  sortDir: SortDir;
  filter: string;
  truncated: boolean;
}

interface PanelState {
  active: MemoryScope;
  scopes: MemoryScope[];
  byScope: Map<MemoryScope, ScopeState>;
}

export function renderPanel(root: HTMLElement): void {
  const state: PanelState = {
    active: ALL_MEMORY_SCOPES[0],
    scopes: ALL_MEMORY_SCOPES.slice(),
    byScope: new Map(),
  };
  for (const scope of ALL_MEMORY_SCOPES) {
    state.byScope.set(scope, newScopeState());
  }

  root.classList.add("r1-panel", "r1-panel-memory-inspector");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>Memory Bus</h2>
      <span class="r1-panel-subtitle">read-only host-backed key/value browser</span>
    </header>
    <div class="r1-memory-readonly-note" data-role="memory-readonly-note">
      This desktop host currently exposes read-only memory browsing only.
      History, import, delete, and write controls are not wired in the host.
    </div>
    <div class="r1-memory-tabs" role="tablist" aria-label="Memory scopes" data-role="scope-tabs"></div>
    <div class="r1-memory-toolbar">
      <label class="r1-memory-filter-label">
        <span class="r1-memory-filter-hint">Filter</span>
        <input
          type="search"
          class="r1-memory-filter"
          data-role="filter"
          aria-label="Filter keys"
        />
      </label>
    </div>
    <div class="r1-panel-body r1-memory-body" data-role="tabpanel" role="tabpanel">
      <div class="r1-memory-truncated" data-role="memory-truncated" hidden></div>
      <table class="r1-memory-table" data-role="memory-table">
        <thead>
          <tr>
            <th data-sort-key="key" data-sort="asc">Key</th>
            <th>Value</th>
            <th data-sort-key="updated_at">Updated</th>
          </tr>
        </thead>
        <tbody data-role="rows">
          <tr>
            <td colspan="3" class="r1-memory-empty">Loading scopes&hellip;</td>
          </tr>
        </tbody>
      </table>
    </div>
  `;

  wireToolbar(root, state);
  wireSort(root, state);
  void loadScopes(root, state);
}

function newScopeState(): ScopeState {
  return {
    entries: [],
    sortKey: "key",
    sortDir: "asc",
    filter: "",
    truncated: false,
  };
}

async function loadScopes(root: HTMLElement, state: PanelState): Promise<void> {
  const result = await invokeStub<MemoryListScopesResult>(
    "memory_list_scopes",
    "R1D-6",
    { scopes: ALL_MEMORY_SCOPES.slice() },
  );
  if (result.scopes.length > 0) {
    state.scopes = result.scopes;
    if (!state.byScope.has(state.active) && result.scopes[0]) {
      state.active = result.scopes[0];
    }
    for (const scope of result.scopes) {
      if (!state.byScope.has(scope)) {
        state.byScope.set(scope, newScopeState());
      }
    }
  }
  renderTabs(root, state);
  void loadScope(root, state, state.active);
}

function renderTabs(root: HTMLElement, state: PanelState): void {
  const tablist = root.querySelector<HTMLDivElement>('[data-role="scope-tabs"]');
  if (!tablist) return;
  tablist.innerHTML = state.scopes.map((scope) => renderTab(state, scope)).join("");
  const tabs = tablist.querySelectorAll<HTMLButtonElement>(".r1-memory-tab");
  tabs.forEach((tab) => {
    tab.addEventListener("click", () => {
      const scope = tab.dataset.scope as MemoryScope | undefined;
      if (!scope) return;
      activateScope(root, state, scope);
    });
    tab.addEventListener("keydown", (event) => {
      if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
      event.preventDefault();
      const idx = state.scopes.indexOf(state.active);
      const delta = event.key === "ArrowRight" ? 1 : -1;
      const next = state.scopes[
        (idx + delta + state.scopes.length) % state.scopes.length
      ];
      activateScope(root, state, next);
      root
        .querySelector<HTMLButtonElement>(`#r1-memory-tab-${next}`)
        ?.focus();
    });
  });
}

function renderTab(state: PanelState, scope: MemoryScope): string {
  const selected = scope === state.active ? "true" : "false";
  const tabindex = scope === state.active ? "0" : "-1";
  const count = state.byScope.get(scope)?.entries.length ?? 0;
  return `
    <button
      type="button"
      class="r1-memory-tab"
      role="tab"
      data-scope="${scope}"
      aria-selected="${selected}"
      tabindex="${tabindex}"
      id="r1-memory-tab-${scope}"
    >
      <span class="r1-memory-tab-name">${scope}</span>
      <span class="r1-memory-tab-count" data-role="count" aria-label="entry count">${count}</span>
    </button>
  `;
}

function activateScope(
  root: HTMLElement,
  state: PanelState,
  scope: MemoryScope,
): void {
  if (state.active === scope && state.byScope.get(scope)) {
    return;
  }
  state.active = scope;
  const filter = root.querySelector<HTMLInputElement>('[data-role="filter"]');
  if (filter) filter.value = state.byScope.get(scope)?.filter ?? "";
  renderTabs(root, state);
  void loadScope(root, state, scope);
}

function wireToolbar(root: HTMLElement, state: PanelState): void {
  const filter = root.querySelector<HTMLInputElement>('[data-role="filter"]');
  filter?.addEventListener("input", () => {
    const scopeState = state.byScope.get(state.active);
    if (!scopeState) return;
    scopeState.filter = filter.value;
    renderRows(root, state);
  });
}

function wireSort(root: HTMLElement, state: PanelState): void {
  const ths = root.querySelectorAll<HTMLTableCellElement>("th[data-sort-key]");
  ths.forEach((th) => {
    th.addEventListener("click", () => {
      const key = th.dataset.sortKey as SortKey | undefined;
      if (!key) return;
      const scopeState = state.byScope.get(state.active);
      if (!scopeState) return;
      if (scopeState.sortKey === key) {
        scopeState.sortDir = scopeState.sortDir === "asc" ? "desc" : "asc";
      } else {
        scopeState.sortKey = key;
        scopeState.sortDir = "asc";
      }
      updateSortIndicators(root, scopeState);
      renderRows(root, state);
    });
  });
}

function updateSortIndicators(root: HTMLElement, scopeState: ScopeState): void {
  const ths = root.querySelectorAll<HTMLTableCellElement>("th[data-sort-key]");
  ths.forEach((th) => {
    if (th.dataset.sortKey === scopeState.sortKey) {
      th.dataset.sort = scopeState.sortDir;
    } else {
      delete th.dataset.sort;
    }
  });
}

async function loadScope(
  root: HTMLElement,
  state: PanelState,
  scope: MemoryScope,
): Promise<void> {
  const scopeState = state.byScope.get(scope);
  if (!scopeState) return;

  const tbody = root.querySelector<HTMLTableSectionElement>('[data-role="rows"]');
  if (tbody) {
    tbody.innerHTML = `
      <tr>
        <td colspan="3" class="r1-memory-empty">Loading entries&hellip;</td>
      </tr>
    `;
  }

  const result = await invokeStub<MemoryQueryResult>(
    "memory_query",
    "R1D-6",
    { entries: [], truncated: false },
    { scope },
  );
  scopeState.entries = result.entries;
  scopeState.truncated = result.truncated;
  updateSortIndicators(root, scopeState);
  renderTabs(root, state);
  renderRows(root, state);
}

function renderRows(root: HTMLElement, state: PanelState): void {
  const tbody = root.querySelector<HTMLTableSectionElement>('[data-role="rows"]');
  if (!tbody) return;
  const scopeState = state.byScope.get(state.active);
  if (!scopeState) return;

  renderTruncation(root, state.active, scopeState.truncated);

  const visible = applyFilter(scopeState.entries, scopeState.filter);
  const sorted = sortRows(visible, scopeState.sortKey, scopeState.sortDir);
  if (sorted.length === 0) {
    tbody.innerHTML = `
      <tr>
        <td colspan="3" class="r1-memory-empty">No entries in ${escapeHtml(state.active)} yet.</td>
      </tr>
    `;
    return;
  }

  tbody.innerHTML = sorted.map(renderRow).join("");
}

function renderTruncation(root: HTMLElement, scope: MemoryScope, truncated: boolean): void {
  const banner = root.querySelector<HTMLDivElement>('[data-role="memory-truncated"]');
  if (!banner) return;
  if (!truncated) {
    banner.hidden = true;
    banner.textContent = "";
    return;
  }
  banner.hidden = false;
  banner.textContent = `Showing a partial result set for ${scope}. The host reported truncation for this query.`;
}

function applyFilter(entries: MemoryEntry[], filter: string): MemoryEntry[] {
  const needle = filter.trim().toLowerCase();
  if (!needle) return entries;
  return entries.filter((entry) => entry.key.toLowerCase().includes(needle));
}

function sortRows(entries: MemoryEntry[], key: SortKey, dir: SortDir): MemoryEntry[] {
  const copy = entries.slice();
  copy.sort((a, b) => {
    const av = key === "key" ? a.key : a.updated_at;
    const bv = key === "key" ? b.key : b.updated_at;
    if (av === bv) return 0;
    const order = av < bv ? -1 : 1;
    return dir === "asc" ? order : -order;
  });
  return copy;
}

function renderRow(entry: MemoryEntry): string {
  return `
    <tr data-key="${escapeHtml(entry.key)}">
      <td class="r1-memory-key"><code>${escapeHtml(entry.key)}</code></td>
      <td class="r1-memory-value" title="${escapeHtml(entry.value)}">${escapeHtml(truncate(entry.value, 80))}</td>
      <td class="r1-memory-at">
        <time datetime="${escapeHtml(entry.updated_at)}">${escapeHtml(entry.updated_at)}</time>
      </td>
    </tr>
  `;
}

function truncate(raw: string, max: number): string {
  if (raw.length <= max) return raw;
  return `${raw.slice(0, max - 1)}…`;
}

function escapeHtml(raw: string): string {
  return raw
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}
