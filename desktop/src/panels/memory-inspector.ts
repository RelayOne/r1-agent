// SPDX-License-Identifier: MIT
//
// Memory-bus inspector panel (R1D-6).
//
// Scope tabs on top (R1D-6.1), per-scope sortable key/value table
// below (R1D-6.2). Clicking a row opens the shared drill-down drawer
// with write/read history (R1D-6.3). Per-scope export/import lives in
// the panel header (R1D-6.4); per-row delete sits in the table
// (R1D-6.5). All IPC goes through the R1D-2 stub shim — Tauri runtime
// wiring lands with the rest of R1D-1.2 / R1D-1.3.
//
// IPC verbs touched: memory_query, memory_history, memory_import,
// memory_delete. Schemas live in `types/ipc.d.ts`.

import { invokeStub } from "../ipc-stub";
import { ALL_MEMORY_SCOPES } from "../types/ipc-const";
import type {
  MemoryDeleteResult,
  MemoryHistoryEntry,
  MemoryHistoryResult,
  MemoryImportConflict,
  MemoryImportResult,
  MemoryRow,
  MemoryScope,
} from "../types/ipc";

type SortKey = "key" | "last_updated_at";
type SortDir = "asc" | "desc";

interface ScopeState {
  rows: MemoryRow[];
  sortKey: SortKey;
  sortDir: SortDir;
  filter: string;
}

interface PanelState {
  active: MemoryScope;
  byScope: Map<MemoryScope, ScopeState>;
}

const HISTORY_DRAWER_ID = "r1-memory-history-drawer";
const HISTORY_BACKDROP_ID = "r1-memory-history-backdrop";

export function renderPanel(root: HTMLElement): void {
  const state: PanelState = {
    active: ALL_MEMORY_SCOPES[0],
    byScope: new Map(),
  };
  for (const scope of ALL_MEMORY_SCOPES) {
    state.byScope.set(scope, {
      rows: [],
      sortKey: "key",
      sortDir: "asc",
      filter: "",
    });
  }

  root.classList.add("r1-panel", "r1-panel-memory-inspector");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>Memory Bus</h2>
      <span class="r1-panel-subtitle">6-scope key/value inspector</span>
    </header>
    <div class="r1-memory-tabs" role="tablist" aria-label="Memory scopes" data-role="scope-tabs">
      ${ALL_MEMORY_SCOPES.map(renderTab).join("")}
    </div>
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
      <button type="button" class="r1-btn" data-role="export">Export</button>
      <label class="r1-btn r1-memory-import-label">
        Import
        <input type="file" accept=".json,application/json" data-role="import" hidden />
      </label>
    </div>
    <div class="r1-panel-body r1-memory-body" data-role="tabpanel" role="tabpanel">
      <table class="r1-memory-table" data-role="memory-table">
        <thead>
          <tr>
            <th data-sort-key="key" data-sort="asc">Key</th>
            <th>Value</th>
            <th>Author</th>
            <th data-sort-key="last_updated_at">Updated</th>
            <th class="r1-memory-th-numeric">Reads</th>
            <th class="r1-memory-th-numeric">Writes</th>
            <th></th>
          </tr>
        </thead>
        <tbody data-role="rows"></tbody>
      </table>
    </div>
  `;

  mountHistoryDrawer(document.body);
  wireTabs(root, state);
  wireToolbar(root, state);
  wireSort(root, state);
  void loadScope(root, state, state.active);
}

function renderTab(scope: MemoryScope): string {
  const selected = scope === ALL_MEMORY_SCOPES[0] ? "true" : "false";
  const tabindex = scope === ALL_MEMORY_SCOPES[0] ? "0" : "-1";
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
      <span class="r1-memory-tab-count" data-role="count" aria-label="entry count">0</span>
    </button>
  `;
}

function wireTabs(root: HTMLElement, state: PanelState): void {
  const tablist = root.querySelector<HTMLDivElement>('[data-role="scope-tabs"]');
  if (!tablist) return;
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
      const idx = ALL_MEMORY_SCOPES.indexOf(state.active);
      const delta = event.key === "ArrowRight" ? 1 : -1;
      const next = ALL_MEMORY_SCOPES[
        (idx + delta + ALL_MEMORY_SCOPES.length) % ALL_MEMORY_SCOPES.length
      ];
      activateScope(root, state, next);
      root
        .querySelector<HTMLButtonElement>(`#r1-memory-tab-${next}`)
        ?.focus();
    });
  });
}

function activateScope(
  root: HTMLElement,
  state: PanelState,
  scope: MemoryScope,
): void {
  if (state.active === scope && state.byScope.get(scope)?.rows.length) {
    return;
  }
  state.active = scope;
  const tabs = root.querySelectorAll<HTMLButtonElement>(".r1-memory-tab");
  tabs.forEach((tab) => {
    const match = tab.dataset.scope === scope;
    tab.setAttribute("aria-selected", match ? "true" : "false");
    tab.tabIndex = match ? 0 : -1;
  });
  const filter = root.querySelector<HTMLInputElement>('[data-role="filter"]');
  if (filter) filter.value = state.byScope.get(scope)?.filter ?? "";
  void loadScope(root, state, scope);
}

function wireToolbar(root: HTMLElement, state: PanelState): void {
  const filter = root.querySelector<HTMLInputElement>('[data-role="filter"]');
  filter?.addEventListener("input", () => {
    const s = state.byScope.get(state.active);
    if (!s) return;
    s.filter = filter.value;
    renderRows(root, state);
  });

  const exportBtn = root.querySelector<HTMLButtonElement>('[data-role="export"]');
  exportBtn?.addEventListener("click", () => exportScope(state));

  const importInput = root.querySelector<HTMLInputElement>('[data-role="import"]');
  importInput?.addEventListener("change", () => {
    void handleImport(root, state, importInput);
  });
}

function wireSort(root: HTMLElement, state: PanelState): void {
  const ths = root.querySelectorAll<HTMLTableCellElement>("th[data-sort-key]");
  ths.forEach((th) => {
    th.addEventListener("click", () => {
      const key = th.dataset.sortKey as SortKey | undefined;
      if (!key) return;
      const s = state.byScope.get(state.active);
      if (!s) return;
      if (s.sortKey === key) {
        s.sortDir = s.sortDir === "asc" ? "desc" : "asc";
      } else {
        s.sortKey = key;
        s.sortDir = "asc";
      }
      updateSortIndicators(root, s);
      renderRows(root, state);
    });
  });
}

function updateSortIndicators(root: HTMLElement, s: ScopeState): void {
  const ths = root.querySelectorAll<HTMLTableCellElement>("th[data-sort-key]");
  ths.forEach((th) => {
    if (th.dataset.sortKey === s.sortKey) {
      th.dataset.sort = s.sortDir;
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
  const s = state.byScope.get(scope);
  if (!s) return;
  const result = await invokeStub<{ rows: MemoryRow[] }>(
    "memory_query",
    "R1D-6",
    { rows: [] },
    { scope },
  );
  s.rows = result.rows;
  updateCountBadges(root, state);
  updateSortIndicators(root, s);
  renderRows(root, state);
}

function updateCountBadges(root: HTMLElement, state: PanelState): void {
  for (const scope of ALL_MEMORY_SCOPES) {
    const count = state.byScope.get(scope)?.rows.length ?? 0;
    const badge = root.querySelector<HTMLSpanElement>(
      `#r1-memory-tab-${scope} [data-role="count"]`,
    );
    if (badge) badge.textContent = String(count);
  }
}

function renderRows(root: HTMLElement, state: PanelState): void {
  const tbody = root.querySelector<HTMLTableSectionElement>('[data-role="rows"]');
  if (!tbody) return;
  const s = state.byScope.get(state.active);
  if (!s) return;
  const visible = applyFilter(s.rows, s.filter);
  const sorted = sortRows(visible, s.sortKey, s.sortDir);
  if (sorted.length === 0) {
    tbody.innerHTML = `
      <tr>
        <td colspan="7" class="r1-memory-empty">No entries in ${escapeHtml(state.active)} yet.</td>
      </tr>
    `;
    return;
  }
  tbody.innerHTML = sorted.map(renderRow).join("");
  tbody.querySelectorAll<HTMLTableRowElement>("tr[data-key]").forEach((tr) => {
    const key = tr.dataset.key;
    if (!key) return;
    tr.addEventListener("click", (event) => {
      const target = event.target as HTMLElement | null;
      if (target?.closest('[data-role="delete"]')) return;
      void openHistoryDrawer(state.active, key);
    });
    tr.querySelector<HTMLButtonElement>('[data-role="delete"]')
      ?.addEventListener("click", (event) => {
        event.stopPropagation();
        void handleDelete(root, state, key);
      });
  });
}

function applyFilter(rows: MemoryRow[], filter: string): MemoryRow[] {
  const needle = filter.trim().toLowerCase();
  if (!needle) return rows;
  return rows.filter((row) => row.key.toLowerCase().includes(needle));
}

function sortRows(rows: MemoryRow[], key: SortKey, dir: SortDir): MemoryRow[] {
  const copy = rows.slice();
  copy.sort((a, b) => {
    const av = key === "key" ? a.key : a.last_updated_at;
    const bv = key === "key" ? b.key : b.last_updated_at;
    if (av === bv) return 0;
    const order = av < bv ? -1 : 1;
    return dir === "asc" ? order : -order;
  });
  return copy;
}

function renderRow(row: MemoryRow): string {
  const value = formatValue(row.value);
  return `
    <tr data-key="${escapeHtml(row.key)}" tabindex="0">
      <td class="r1-memory-key"><code>${escapeHtml(row.key)}</code></td>
      <td class="r1-memory-value" title="${escapeHtml(value)}">${escapeHtml(truncate(value, 80))}</td>
      <td class="r1-memory-author">${escapeHtml(row.author)}</td>
      <td class="r1-memory-at">
        <time datetime="${escapeHtml(row.last_updated_at)}">${escapeHtml(row.last_updated_at)}</time>
      </td>
      <td class="r1-memory-th-numeric">${row.read_count}</td>
      <td class="r1-memory-th-numeric">${row.write_count}</td>
      <td class="r1-memory-actions">
        <button
          type="button"
          class="r1-btn r1-memory-delete"
          data-role="delete"
          aria-label="Delete ${escapeHtml(row.key)}"
        >Delete</button>
      </td>
    </tr>
  `;
}

function formatValue(value: unknown): string {
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

function truncate(raw: string, max: number): string {
  if (raw.length <= max) return raw;
  return `${raw.slice(0, max - 1)}…`;
}

function exportScope(state: PanelState): void {
  const s = state.byScope.get(state.active);
  if (!s) return;
  const payload = {
    scope: state.active,
    rows: s.rows,
  };
  const blob = new Blob([JSON.stringify(payload, null, 2)], {
    type: "application/json",
  });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = `r1-memory-${state.active}.json`;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}

async function handleImport(
  root: HTMLElement,
  state: PanelState,
  input: HTMLInputElement,
): Promise<void> {
  const file = input.files?.[0];
  input.value = "";
  if (!file) return;
  let rows: MemoryRow[] = [];
  try {
    const text = await file.text();
    const parsed = JSON.parse(text);
    rows = Array.isArray(parsed) ? parsed : (parsed?.rows ?? []);
  } catch {
    alert("Import failed: invalid JSON");
    return;
  }
  if (!Array.isArray(rows) || rows.length === 0) {
    alert("Import file has no rows");
    return;
  }
  const result = await invokeStub<MemoryImportResult>(
    "memory_import",
    "R1D-6",
    { imported: 0, conflicts: [] },
    { scope: state.active, rows },
  );
  if (result.conflicts.length === 0) {
    mergeImported(state, rows);
    updateCountBadges(root, state);
    renderRows(root, state);
    return;
  }
  showConflictDialog(root, state, rows, result.conflicts);
}

function mergeImported(state: PanelState, rows: MemoryRow[]): void {
  const s = state.byScope.get(state.active);
  if (!s) return;
  const byKey = new Map(s.rows.map((r) => [r.key, r]));
  for (const row of rows) byKey.set(row.key, { ...row, scope: state.active });
  s.rows = Array.from(byKey.values());
}

function showConflictDialog(
  root: HTMLElement,
  state: PanelState,
  rows: MemoryRow[],
  conflicts: MemoryImportConflict[],
): void {
  const existing = document.getElementById("r1-memory-conflict-dialog");
  existing?.remove();

  const dialog = document.createElement("div");
  dialog.id = "r1-memory-conflict-dialog";
  dialog.className = "r1-drawer-backdrop r1-memory-conflict-backdrop";
  dialog.setAttribute("role", "alertdialog");
  dialog.setAttribute("aria-modal", "true");
  dialog.innerHTML = `
    <div class="r1-memory-conflict" role="document">
      <header class="r1-drawer-header">
        <h2 class="r1-drawer-title">Import conflicts (${conflicts.length})</h2>
      </header>
      <div class="r1-drawer-body">
        <ul class="r1-memory-conflict-list">
          ${conflicts.map(renderConflict).join("")}
        </ul>
        <p class="r1-memory-conflict-hint">
          Overwrite replaces all conflicting rows. Skip keeps existing rows
          and discards incoming ones. Cancel aborts the import.
        </p>
      </div>
      <footer class="r1-memory-conflict-actions">
        <button type="button" class="r1-btn" data-resolve="cancel">Cancel</button>
        <button type="button" class="r1-btn" data-resolve="skip">Skip</button>
        <button type="button" class="r1-btn r1-btn-primary" data-resolve="overwrite">Overwrite</button>
      </footer>
    </div>
  `;
  document.body.appendChild(dialog);

  const close = () => dialog.remove();
  dialog.addEventListener("click", (event) => {
    if (event.target === dialog) close();
  });
  dialog
    .querySelector<HTMLButtonElement>('[data-resolve="cancel"]')
    ?.addEventListener("click", close);
  dialog
    .querySelector<HTMLButtonElement>('[data-resolve="skip"]')
    ?.addEventListener("click", () => {
      const conflictKeys = new Set(conflicts.map((c) => c.key));
      const nonConflicting = rows.filter((r) => !conflictKeys.has(r.key));
      mergeImported(state, nonConflicting);
      updateCountBadges(root, state);
      renderRows(root, state);
      close();
    });
  dialog
    .querySelector<HTMLButtonElement>('[data-resolve="overwrite"]')
    ?.addEventListener("click", () => {
      mergeImported(state, rows);
      updateCountBadges(root, state);
      renderRows(root, state);
      close();
    });
}

function renderConflict(conflict: MemoryImportConflict): string {
  const existing = truncate(formatValue(conflict.existing), 60);
  const incoming = truncate(formatValue(conflict.incoming), 60);
  return `
    <li class="r1-memory-conflict-row">
      <code class="r1-memory-conflict-key">${escapeHtml(conflict.key)}</code>
      <div class="r1-memory-conflict-existing">
        <span class="r1-memory-conflict-label">existing</span>
        <span>${escapeHtml(existing)}</span>
      </div>
      <div class="r1-memory-conflict-incoming">
        <span class="r1-memory-conflict-label">incoming</span>
        <span>${escapeHtml(incoming)}</span>
      </div>
    </li>
  `;
}

async function handleDelete(
  root: HTMLElement,
  state: PanelState,
  key: string,
): Promise<void> {
  const confirmed = confirm(`Delete key "${key}" from ${state.active}?`);
  if (!confirmed) return;
  const result = await invokeStub<MemoryDeleteResult>(
    "memory_delete",
    "R1D-6",
    { ok: true },
    { scope: state.active, key },
  );
  if (!result.ok) {
    alert(`Delete failed for ${key}`);
    return;
  }
  const s = state.byScope.get(state.active);
  if (!s) return;
  s.rows = s.rows.filter((r) => r.key !== key);
  updateCountBadges(root, state);
  renderRows(root, state);
}

// ---------------------------------------------------------------------
// History drawer (R1D-6.3)
// ---------------------------------------------------------------------

let historyDrawer: HTMLElement | null = null;
let historyBackdrop: HTMLElement | null = null;
let historyLastFocus: HTMLElement | null = null;
let historyActiveKey: { scope: MemoryScope; key: string } | null = null;

function mountHistoryDrawer(parent: HTMLElement): void {
  if (document.getElementById(HISTORY_DRAWER_ID)) {
    historyDrawer = document.getElementById(HISTORY_DRAWER_ID);
    historyBackdrop = document.getElementById(HISTORY_BACKDROP_ID);
    return;
  }

  const backdrop = document.createElement("div");
  backdrop.id = HISTORY_BACKDROP_ID;
  backdrop.className = "r1-drawer-backdrop";
  backdrop.hidden = true;
  backdrop.addEventListener("click", closeHistoryDrawer);

  const drawer = document.createElement("aside");
  drawer.id = HISTORY_DRAWER_ID;
  drawer.className = "r1-drawer r1-memory-history-drawer";
  drawer.setAttribute("role", "dialog");
  drawer.setAttribute("aria-modal", "true");
  drawer.setAttribute("aria-labelledby", `${HISTORY_DRAWER_ID}-title`);
  drawer.hidden = true;
  drawer.tabIndex = -1;
  drawer.innerHTML = `
    <header class="r1-drawer-header">
      <h2 id="${HISTORY_DRAWER_ID}-title" class="r1-drawer-title">History</h2>
      <button
        type="button"
        class="r1-btn r1-drawer-close"
        data-role="history-close"
        aria-label="Close history drawer"
      >Close</button>
    </header>
    <div class="r1-drawer-body" data-role="history-body">
      <p class="r1-empty">Loading history&hellip;</p>
    </div>
  `;
  drawer
    .querySelector<HTMLButtonElement>('[data-role="history-close"]')
    ?.addEventListener("click", closeHistoryDrawer);

  parent.appendChild(backdrop);
  parent.appendChild(drawer);

  historyDrawer = drawer;
  historyBackdrop = backdrop;

  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape") return;
    if (!historyDrawer || historyDrawer.hidden) return;
    event.preventDefault();
    closeHistoryDrawer();
  });
}

async function openHistoryDrawer(
  scope: MemoryScope,
  key: string,
): Promise<void> {
  if (!historyDrawer || !historyBackdrop) return;
  historyLastFocus = document.activeElement instanceof HTMLElement
    ? document.activeElement
    : null;
  historyActiveKey = { scope, key };

  const title = historyDrawer.querySelector<HTMLHeadingElement>(
    `#${HISTORY_DRAWER_ID}-title`,
  );
  if (title) title.textContent = `${scope} / ${key}`;

  const body = historyDrawer.querySelector<HTMLDivElement>(
    '[data-role="history-body"]',
  );
  if (body) body.innerHTML = `<p class="r1-empty">Loading history&hellip;</p>`;

  historyBackdrop.hidden = false;
  historyDrawer.hidden = false;
  historyDrawer.classList.add("is-open");
  historyDrawer.focus();

  const result = await invokeStub<MemoryHistoryResult>(
    "memory_history",
    "R1D-6",
    { scope, key, entries: [] },
    { scope, key },
  );

  if (
    historyActiveKey?.scope === scope &&
    historyActiveKey?.key === key &&
    body
  ) {
    renderHistory(body, result.entries);
  }
}

function closeHistoryDrawer(): void {
  if (!historyDrawer || !historyBackdrop) return;
  historyDrawer.classList.remove("is-open");
  historyDrawer.hidden = true;
  historyBackdrop.hidden = true;
  historyActiveKey = null;
  if (historyLastFocus && document.body.contains(historyLastFocus)) {
    historyLastFocus.focus();
  }
  historyLastFocus = null;
}

function renderHistory(
  body: HTMLDivElement,
  entries: MemoryHistoryEntry[],
): void {
  if (entries.length === 0) {
    body.innerHTML = `
      <p class="r1-empty">No history yet &mdash; writes + reads will stream in once R1D-6 IPC lands.</p>
    `;
    return;
  }
  body.innerHTML = `
    <ul class="r1-memory-history-list">
      ${entries.map(renderHistoryEntry).join("")}
    </ul>
  `;
}

function renderHistoryEntry(entry: MemoryHistoryEntry): string {
  const detail = entry.detail
    ? `<span class="r1-memory-history-detail">${escapeHtml(entry.detail)}</span>`
    : "";
  return `
    <li class="r1-memory-history-item" data-kind="${entry.kind}">
      <span class="r1-memory-history-kind">${entry.kind}</span>
      <span class="r1-memory-history-who">${escapeHtml(entry.who)}</span>
      <time class="r1-memory-history-when" datetime="${escapeHtml(entry.when)}">${escapeHtml(entry.when)}</time>
      ${detail}
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
