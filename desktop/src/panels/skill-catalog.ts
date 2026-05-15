// SPDX-License-Identifier: MIT
//
// Skill catalog browser panel (R1D-4).
//
// Composes the real skill-catalog surfaces into a single panel:
//
//   R1D-4.1 Catalog browser   faceted filters + search over SkillSummary
//   R1D-4.2 Manifest drawer   side-drawer rendering the 7 required fields
//
// The Rust host currently exposes only `skill_list` and `skill_get`.
// Marketplace actions, bundled-pack install, and test-skill invocation
// were previously rendered even though there were no matching host
// handlers. That was a false capability claim, so this build keeps the
// real read-only catalog surfaces and removes the fake mutation/test UI.
//
// All IPC goes through the R1D-2 `invokeStub` shim. Real dispatch lands
// when R1D-1.2 / R1D-1.3 wire Tauri. In this build the catalog
// hydrates from `skill_list`, and `skill_get` backs the manifest drawer.

import { invokeStub } from "../ipc-stub";
import type {
  SkillExample,
  SkillJsonSchema,
  SkillListResult,
  SkillManifest,
  SkillSummary,
} from "../types/ipc";

type Tab = "available" | "installed";

interface PanelState {
  skills: SkillSummary[];
  tab: Tab;
  search: string;
  filters: {
    category: string;
    pack: string;
    author: string;
    tag: string;
  };
}

const DRAWER_ID = "r1-skill-manifest-drawer";
const DRAWER_BACKDROP_ID = "r1-skill-manifest-backdrop";

export function renderPanel(root: HTMLElement): void {
  const state: PanelState = {
    skills: [],
    tab: "available",
    search: "",
    filters: { category: "", pack: "", author: "", tag: "" },
  };

  root.classList.add("r1-panel", "r1-panel-skill-catalog");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>Skill Catalog</h2>
      <span class="r1-panel-subtitle">read-only host catalog</span>
    </header>
    <div class="r1-skill-readonly-note" data-role="skill-readonly-note">
      This build exposes the host-cached catalog and manifest drawer only.
      Install, uninstall, bundled-pack, and test-skill actions are not wired in the desktop host.
    </div>
    <div class="r1-skill-tabs" role="tablist" aria-label="Skill marketplace tabs">
      <button
        type="button"
        class="r1-skill-tab"
        role="tab"
        data-tab="available"
        aria-selected="true"
        tabindex="0"
      >Available <span class="r1-skill-tab-count" data-role="count-available">0</span></button>
      <button
        type="button"
        class="r1-skill-tab"
        role="tab"
        data-tab="installed"
        aria-selected="false"
        tabindex="-1"
      >Installed <span class="r1-skill-tab-count" data-role="count-installed">0</span></button>
    </div>
    <div class="r1-skill-toolbar">
      <label class="r1-skill-search-label">
        <span class="r1-skill-search-hint">Search</span>
        <input
          type="search"
          class="r1-skill-search"
          data-role="search"
          aria-label="Search skills (name, description, tag)"
        />
      </label>
      <select class="r1-skill-filter" data-role="filter-category" aria-label="Filter by category">
        <option value="">All categories</option>
      </select>
      <select class="r1-skill-filter" data-role="filter-pack" aria-label="Filter by pack">
        <option value="">All packs</option>
      </select>
      <select class="r1-skill-filter" data-role="filter-author" aria-label="Filter by author">
        <option value="">All authors</option>
      </select>
      <select class="r1-skill-filter" data-role="filter-tag" aria-label="Filter by tag">
        <option value="">All tags</option>
      </select>
    </div>
    <div class="r1-panel-body r1-skill-body" role="tabpanel">
      <ul class="r1-skill-grid" data-role="grid"></ul>
    </div>
  `;

  mountManifestDrawer(document.body);
  wireTabs(root, state);
  wireToolbar(root, state);
  void loadCatalog(root, state);
}

async function loadCatalog(root: HTMLElement, state: PanelState): Promise<void> {
  const result = await invokeStub<SkillListResult>(
    "skill_list",
    "R1D-4",
    { skills: [] },
  );
  state.skills = result.skills;
  refreshFilters(root, state);
  refreshCounts(root, state);
  renderGrid(root, state);
}

function wireTabs(root: HTMLElement, state: PanelState): void {
  const tabs = root.querySelectorAll<HTMLButtonElement>(".r1-skill-tab");
  tabs.forEach((tab) => {
    tab.addEventListener("click", () => {
      const next = tab.dataset.tab as Tab | undefined;
      if (!next || next === state.tab) return;
      state.tab = next;
      tabs.forEach((t) => {
        const match = t.dataset.tab === next;
        t.setAttribute("aria-selected", match ? "true" : "false");
        t.tabIndex = match ? 0 : -1;
      });
      renderGrid(root, state);
    });
  });
}

function wireToolbar(root: HTMLElement, state: PanelState): void {
  const search = root.querySelector<HTMLInputElement>('[data-role="search"]');
  search?.addEventListener("input", () => {
    state.search = search.value;
    renderGrid(root, state);
  });
  const bind = (role: string, key: keyof PanelState["filters"]): void => {
    const el = root.querySelector<HTMLSelectElement>(`[data-role="${role}"]`);
    el?.addEventListener("change", () => {
      state.filters[key] = el.value;
      renderGrid(root, state);
    });
  };
  bind("filter-category", "category");
  bind("filter-pack", "pack");
  bind("filter-author", "author");
  bind("filter-tag", "tag");
}

function refreshFilters(root: HTMLElement, state: PanelState): void {
  const categories = new Set<string>();
  const packs = new Set<string>();
  const authors = new Set<string>();
  const tags = new Set<string>();
  for (const s of state.skills) {
    if (s.category) categories.add(s.category);
    if (s.pack) packs.add(s.pack);
    if (s.author) authors.add(s.author);
    for (const t of s.tags ?? []) tags.add(t);
  }
  fillSelect(root, "filter-category", "All categories", categories);
  fillSelect(root, "filter-pack", "All packs", packs);
  fillSelect(root, "filter-author", "All authors", authors);
  fillSelect(root, "filter-tag", "All tags", tags);
}

function fillSelect(
  root: HTMLElement,
  role: string,
  anyLabel: string,
  values: Set<string>,
): void {
  const el = root.querySelector<HTMLSelectElement>(`[data-role="${role}"]`);
  if (!el) return;
  const current = el.value;
  const sorted = Array.from(values).sort((a, b) => a.localeCompare(b));
  el.innerHTML = [
    `<option value="">${escapeHtml(anyLabel)}</option>`,
    ...sorted.map(
      (v) => `<option value="${escapeHtml(v)}">${escapeHtml(v)}</option>`,
    ),
  ].join("");
  if (current && sorted.includes(current)) el.value = current;
}

function refreshCounts(root: HTMLElement, state: PanelState): void {
  const available = state.skills.filter((s) => !s.installed).length;
  const installed = state.skills.filter((s) => s.installed).length;
  const avail = root.querySelector<HTMLSpanElement>('[data-role="count-available"]');
  const inst = root.querySelector<HTMLSpanElement>('[data-role="count-installed"]');
  if (avail) avail.textContent = String(available);
  if (inst) inst.textContent = String(installed);
}

function renderGrid(root: HTMLElement, state: PanelState): void {
  const grid = root.querySelector<HTMLUListElement>('[data-role="grid"]');
  if (!grid) return;
  const filtered = applyFilters(state);
  if (filtered.length === 0) {
    grid.innerHTML = `<li class="r1-skill-empty">No skills match the current filters.</li>`;
    return;
  }
  grid.innerHTML = filtered.map(renderCard).join("");
  for (const card of grid.querySelectorAll<HTMLLIElement>("li[data-skill-id]")) {
    const id = card.dataset.skillId;
    if (!id) continue;
    card.addEventListener("click", () => {
      void openManifestDrawer(id);
    });
  }
}

function applyFilters(state: PanelState): SkillSummary[] {
  const needle = state.search.trim().toLowerCase();
  return state.skills.filter((s) => {
    if (state.tab === "available" && s.installed) return false;
    if (state.tab === "installed" && !s.installed) return false;
    if (state.filters.category && s.category !== state.filters.category) return false;
    if (state.filters.pack && s.pack !== state.filters.pack) return false;
    if (state.filters.author && s.author !== state.filters.author) return false;
    if (
      state.filters.tag &&
      !(s.tags ?? []).includes(state.filters.tag)
    )
      return false;
    if (!needle) return true;
    const hay = [
      s.name,
      s.description,
      s.author,
      s.category,
      s.pack,
      ...(s.tags ?? []),
    ]
      .join(" ")
      .toLowerCase();
    return hay.includes(needle);
  });
}

function renderCard(skill: SkillSummary): string {
  const tagChips = (skill.tags ?? [])
    .slice(0, 6)
    .map((t) => `<span class="r1-skill-tag">${escapeHtml(t)}</span>`)
    .join("");
  const availability = skill.installed ? "Installed in host cache" : "Available in host cache";
  return `
    <li
      class="r1-skill-card"
      data-skill-id="${escapeHtml(skill.id)}"
      tabindex="0"
      role="button"
      aria-label="Inspect ${escapeHtml(skill.name)}"
    >
      <div class="r1-skill-card-head">
        <span class="r1-skill-card-name">${escapeHtml(skill.name)}</span>
        <span class="r1-skill-card-version">v${escapeHtml(skill.version)}</span>
      </div>
      <p class="r1-skill-card-desc">${escapeHtml(skill.description)}</p>
      <div class="r1-skill-card-meta">
        <span class="r1-skill-card-pack">${escapeHtml(skill.pack)}</span>
        <span class="r1-skill-card-author">${escapeHtml(skill.author)}</span>
        <span class="r1-skill-card-category">${escapeHtml(skill.category)}</span>
      </div>
      <div class="r1-skill-card-tags">${tagChips}</div>
      <div class="r1-skill-card-actions" data-role="skill-readonly-state">
        <span class="r1-skill-readonly-pill">${escapeHtml(availability)}</span>
        <span class="r1-skill-readonly-copy">Read-only in this desktop build</span>
      </div>
    </li>
  `;
}

// ---------------------------------------------------------------------
// Manifest drawer (R1D-4.2)
// ---------------------------------------------------------------------

let manifestDrawer: HTMLElement | null = null;
let manifestBackdrop: HTMLElement | null = null;
let manifestLastFocus: HTMLElement | null = null;
let manifestActiveId: string | null = null;

function mountManifestDrawer(parent: HTMLElement): void {
  if (document.getElementById(DRAWER_ID)) {
    manifestDrawer = document.getElementById(DRAWER_ID);
    manifestBackdrop = document.getElementById(DRAWER_BACKDROP_ID);
    return;
  }

  const backdrop = document.createElement("div");
  backdrop.id = DRAWER_BACKDROP_ID;
  backdrop.className = "r1-drawer-backdrop";
  backdrop.hidden = true;
  backdrop.addEventListener("click", closeManifestDrawer);

  const drawer = document.createElement("aside");
  drawer.id = DRAWER_ID;
  drawer.className = "r1-drawer r1-skill-manifest-drawer";
  drawer.setAttribute("role", "dialog");
  drawer.setAttribute("aria-modal", "true");
  drawer.setAttribute("aria-labelledby", `${DRAWER_ID}-title`);
  drawer.hidden = true;
  drawer.tabIndex = -1;
  drawer.innerHTML = `
    <header class="r1-drawer-header">
      <h2 id="${DRAWER_ID}-title" class="r1-drawer-title">Skill manifest</h2>
      <button
        type="button"
        class="r1-btn r1-drawer-close"
        data-role="manifest-close"
        aria-label="Close skill manifest drawer"
      >Close</button>
    </header>
    <div class="r1-drawer-body" data-role="manifest-body">
      <p class="r1-empty">Loading manifest&hellip;</p>
    </div>
  `;
  drawer
    .querySelector<HTMLButtonElement>('[data-role="manifest-close"]')
    ?.addEventListener("click", closeManifestDrawer);

  parent.appendChild(backdrop);
  parent.appendChild(drawer);

  manifestDrawer = drawer;
  manifestBackdrop = backdrop;

  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape") return;
    if (!manifestDrawer || manifestDrawer.hidden) return;
    event.preventDefault();
    closeManifestDrawer();
  });
}

async function openManifestDrawer(id: string): Promise<void> {
  if (!manifestDrawer || !manifestBackdrop) return;
  manifestLastFocus = document.activeElement instanceof HTMLElement
    ? document.activeElement
    : null;
  manifestActiveId = id;

  const body = manifestDrawer.querySelector<HTMLDivElement>(
    '[data-role="manifest-body"]',
  );
  if (body) body.innerHTML = `<p class="r1-empty">Loading manifest&hellip;</p>`;

  manifestBackdrop.hidden = false;
  manifestDrawer.hidden = false;
  manifestDrawer.classList.add("is-open");
  manifestDrawer.focus();

  const empty = emptyManifest(id);
  const manifest = await invokeStub<SkillManifest>(
    "skill_get",
    "R1D-4",
    empty,
    { id },
  );

  if (manifestActiveId === id && body) {
    const title = manifestDrawer.querySelector<HTMLHeadingElement>(
      `#${DRAWER_ID}-title`,
    );
    if (title) title.textContent = manifest.name || id;
    body.innerHTML = renderManifestBody(manifest);
  }
}

function closeManifestDrawer(): void {
  if (!manifestDrawer || !manifestBackdrop) return;
  manifestDrawer.classList.remove("is-open");
  manifestDrawer.hidden = true;
  manifestBackdrop.hidden = true;
  manifestActiveId = null;
  if (manifestLastFocus && document.body.contains(manifestLastFocus)) {
    manifestLastFocus.focus();
  }
  manifestLastFocus = null;
}

function emptyManifest(id: string): SkillManifest {
  return {
    id,
    name: id,
    description: "",
    author: "",
    version: "",
    category: "",
    tags: [],
    pack: "",
    installed: false,
    inputs: { type: "object", properties: {} },
    outputs: { type: "object", properties: {} },
    examples: [],
  };
}

function renderManifestBody(m: SkillManifest): string {
  return `
    <dl class="r1-skill-manifest-meta">
      <dt>Name</dt><dd>${escapeHtml(m.name)}</dd>
      <dt>Description</dt><dd>${escapeHtml(m.description || "-")}</dd>
      <dt>Author</dt><dd>${escapeHtml(m.author || "-")}</dd>
      <dt>Version</dt><dd>${escapeHtml(m.version || "-")}</dd>
    </dl>
    <section class="r1-skill-manifest-section">
      <h3>Inputs</h3>
      ${renderSchemaSummary(m.inputs)}
    </section>
    <section class="r1-skill-manifest-section">
      <h3>Outputs</h3>
      ${renderSchemaSummary(m.outputs)}
    </section>
    <section class="r1-skill-manifest-section">
      <h3>Examples</h3>
      ${renderExamples(m.examples)}
    </section>
  `;
}

function renderSchemaSummary(schema: SkillJsonSchema | undefined): string {
  if (!schema || !schema.properties || Object.keys(schema.properties).length === 0) {
    return `<p class="r1-empty">No fields declared.</p>`;
  }
  const required = new Set(schema.required ?? []);
  const rows = Object.entries(schema.properties).map(([key, child]) => {
    const type = schemaTypeLabel(child);
    const req = required.has(key)
      ? `<span class="r1-skill-required">required</span>`
      : "";
    const desc = child.description
      ? `<span class="r1-skill-field-desc">${escapeHtml(child.description)}</span>`
      : "";
    return `
      <li class="r1-skill-field-row">
        <code class="r1-skill-field-key">${escapeHtml(key)}</code>
        <span class="r1-skill-field-type">${escapeHtml(type)}</span>
        ${req}
        ${desc}
      </li>
    `;
  });
  return `<ul class="r1-skill-field-list">${rows.join("")}</ul>`;
}

function schemaTypeLabel(schema: SkillJsonSchema): string {
  if (schema.enum && schema.enum.length > 0) {
    return `enum(${schema.enum.map(String).join(" | ")})`;
  }
  if (schema.type === "array") {
    const inner = schema.items ? schemaTypeLabel(schema.items) : "any";
    return `array<${inner}>`;
  }
  if (schema.type === "object" && schema.properties) {
    return `object{${Object.keys(schema.properties).join(", ")}}`;
  }
  return schema.type ?? "any";
}

function renderExamples(examples: SkillExample[] | undefined): string {
  if (!examples || examples.length === 0) {
    return `<p class="r1-empty">No examples provided.</p>`;
  }
  return `
    <ul class="r1-skill-example-list">
      ${examples
        .map(
          (ex) => `
        <li class="r1-skill-example">
          <div class="r1-skill-example-title">${escapeHtml(ex.title)}</div>
          <div class="r1-skill-example-pair">
            <div>
              <span class="r1-skill-example-label">input</span>
              <pre class="r1-skill-example-pre"><code>${escapeHtml(
                safeStringify(ex.input),
              )}</code></pre>
            </div>
            ${
              ex.output
                ? `<div>
              <span class="r1-skill-example-label">output</span>
              <pre class="r1-skill-example-pre"><code>${escapeHtml(
                safeStringify(ex.output),
              )}</code></pre>
            </div>`
                : ""
            }
          </div>
        </li>
      `,
        )
        .join("")}
    </ul>
  `;
}

function safeStringify(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

function escapeHtml(raw: string): string {
  return raw
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}
