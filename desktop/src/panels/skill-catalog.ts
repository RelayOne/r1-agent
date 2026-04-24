// SPDX-License-Identifier: MIT
//
// Skill catalog browser panel (R1D-4).
//
// Composes five sub-surfaces into a single panel:
//
//   R1D-4.1 Catalog browser   faceted filters + search over SkillSummary
//   R1D-4.2 Manifest drawer   side-drawer rendering the 7 required fields
//   R1D-4.3 Marketplace       Installed / Available tabs, install/uninstall
//   R1D-4.4 Bundled pack CTA  single-click install of actium-studio (56)
//   R1D-4.5 Test-skill modal  JSON-schema-driven form + invoke stub
//
// All IPC goes through the R1D-2 `invokeStub` shim. Real dispatch lands
// when R1D-1.2 / R1D-1.3 wire Tauri. The catalog hydrates from
// `skill_list`; `skill_get` backs the drawer; install / uninstall /
// install_pack / invoke return the shapes described in
// `types/ipc.d.ts`.

import { invokeStub } from "../ipc-stub";
import type {
  SkillExample,
  SkillInstallResult,
  SkillInvokeResult,
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
  packInstalled: boolean;
}

const ACTIUM_PACK = "actium-studio";
const ACTIUM_SKILL_COUNT = 56;
const DRAWER_ID = "r1-skill-manifest-drawer";
const DRAWER_BACKDROP_ID = "r1-skill-manifest-backdrop";
const TEST_MODAL_ID = "r1-skill-test-modal";

export function renderPanel(root: HTMLElement): void {
  const state: PanelState = {
    skills: [],
    tab: "available",
    search: "",
    filters: { category: "", pack: "", author: "", tag: "" },
    packInstalled: false,
  };

  root.classList.add("r1-panel", "r1-panel-skill-catalog");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>Skill Catalog</h2>
      <span class="r1-panel-subtitle">Browse, install, and test skills</span>
    </header>
    <div class="r1-skill-pack-cta" data-role="pack-cta" hidden>
      <div class="r1-skill-pack-cta-body">
        <strong>Actium Studio pack</strong>
        <span>${ACTIUM_SKILL_COUNT} skills covering sites, pages, posts, SEO, redirects, analytics, and billing.</span>
      </div>
      <button
        type="button"
        class="r1-btn r1-btn-primary"
        data-role="install-pack"
      >Install Actium Studio pack (${ACTIUM_SKILL_COUNT} skills)</button>
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
  mountTestModal(document.body);
  wireTabs(root, state);
  wireToolbar(root, state);
  wirePackCta(root, state);
  void loadCatalog(root, state);
}

async function loadCatalog(root: HTMLElement, state: PanelState): Promise<void> {
  const result = await invokeStub<SkillListResult>(
    "skill_list",
    "R1D-4",
    { skills: [] },
  );
  state.skills = result.skills;
  state.packInstalled = computePackInstalled(state.skills);
  refreshFilters(root, state);
  refreshCounts(root, state);
  refreshPackCta(root, state);
  renderGrid(root, state);
}

function computePackInstalled(skills: SkillSummary[]): boolean {
  const packRows = skills.filter((s) => s.pack === ACTIUM_PACK);
  if (packRows.length === 0) return false;
  return packRows.every((s) => s.installed);
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

function wirePackCta(root: HTMLElement, state: PanelState): void {
  const btn = root.querySelector<HTMLButtonElement>('[data-role="install-pack"]');
  btn?.addEventListener("click", () => {
    void handleInstallPack(root, state, btn);
  });
}

async function handleInstallPack(
  root: HTMLElement,
  state: PanelState,
  btn: HTMLButtonElement,
): Promise<void> {
  btn.disabled = true;
  btn.textContent = "Installing...";
  const result = await invokeStub<SkillInstallResult>(
    "skill_install_pack",
    "R1D-4",
    { ok: true, installed: ACTIUM_SKILL_COUNT },
    { pack: ACTIUM_PACK },
  );
  btn.disabled = false;
  if (!result.ok) {
    btn.textContent = `Install Actium Studio pack (${ACTIUM_SKILL_COUNT} skills)`;
    alert("Pack install failed");
    return;
  }
  for (const skill of state.skills) {
    if (skill.pack === ACTIUM_PACK) skill.installed = true;
  }
  state.packInstalled = true;
  refreshCounts(root, state);
  refreshPackCta(root, state);
  renderGrid(root, state);
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

function refreshPackCta(root: HTMLElement, state: PanelState): void {
  const cta = root.querySelector<HTMLDivElement>('[data-role="pack-cta"]');
  if (!cta) return;
  cta.hidden = state.packInstalled;
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
    card.addEventListener("click", (event) => {
      const target = event.target as HTMLElement | null;
      if (target?.closest('[data-role="skill-action"]')) return;
      void openManifestDrawer(id);
    });
    card
      .querySelector<HTMLButtonElement>('[data-role="skill-install"]')
      ?.addEventListener("click", (event) => {
        event.stopPropagation();
        void handleInstall(root, state, id);
      });
    card
      .querySelector<HTMLButtonElement>('[data-role="skill-uninstall"]')
      ?.addEventListener("click", (event) => {
        event.stopPropagation();
        void handleUninstall(root, state, id);
      });
    card
      .querySelector<HTMLButtonElement>('[data-role="skill-test"]')
      ?.addEventListener("click", (event) => {
        event.stopPropagation();
        void openTestModal(id);
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
  const action = skill.installed
    ? `<button type="button" class="r1-btn r1-skill-uninstall r1-skill-click-stop" data-role="skill-uninstall">Uninstall</button>`
    : `<button type="button" class="r1-btn r1-btn-primary r1-skill-click-stop" data-role="skill-install">Install</button>`;
  const testBtn = skill.installed
    ? `<button type="button" class="r1-btn r1-skill-click-stop" data-role="skill-test">Test</button>`
    : "";
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
      <div class="r1-skill-card-actions">
        ${testBtn}
        ${action}
      </div>
    </li>
  `;
}

async function handleInstall(
  root: HTMLElement,
  state: PanelState,
  id: string,
): Promise<void> {
  const result = await invokeStub<SkillInstallResult>(
    "skill_install",
    "R1D-4",
    { ok: true, installed: 1 },
    { id },
  );
  if (!result.ok) {
    alert(`Install failed for ${id}`);
    return;
  }
  const skill = state.skills.find((s) => s.id === id);
  if (skill) skill.installed = true;
  state.packInstalled = computePackInstalled(state.skills);
  refreshCounts(root, state);
  refreshPackCta(root, state);
  renderGrid(root, state);
}

async function handleUninstall(
  root: HTMLElement,
  state: PanelState,
  id: string,
): Promise<void> {
  const result = await invokeStub<SkillInstallResult>(
    "skill_uninstall",
    "R1D-4",
    { ok: true, installed: 0 },
    { id },
  );
  if (!result.ok) {
    alert(`Uninstall failed for ${id}`);
    return;
  }
  const skill = state.skills.find((s) => s.id === id);
  if (skill) skill.installed = false;
  state.packInstalled = computePackInstalled(state.skills);
  refreshCounts(root, state);
  refreshPackCta(root, state);
  renderGrid(root, state);
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
    const modal = document.getElementById(TEST_MODAL_ID);
    if (modal && !modal.hidden) return;
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

// ---------------------------------------------------------------------
// Test modal (R1D-4.5)
// ---------------------------------------------------------------------

let testModal: HTMLElement | null = null;
let testActiveId: string | null = null;
let testActiveSchema: SkillJsonSchema | null = null;
let testLastFocus: HTMLElement | null = null;

function mountTestModal(parent: HTMLElement): void {
  if (document.getElementById(TEST_MODAL_ID)) {
    testModal = document.getElementById(TEST_MODAL_ID);
    return;
  }

  const modal = document.createElement("div");
  modal.id = TEST_MODAL_ID;
  modal.className = "r1-modal r1-skill-test-modal";
  modal.setAttribute("role", "dialog");
  modal.setAttribute("aria-modal", "true");
  modal.setAttribute("aria-labelledby", `${TEST_MODAL_ID}-title`);
  modal.hidden = true;
  modal.innerHTML = `
    <div class="r1-modal-panel r1-skill-test-panel" role="document">
      <header class="r1-skill-test-header">
        <h3 id="${TEST_MODAL_ID}-title" class="r1-modal-title">Test skill</h3>
        <button
          type="button"
          class="r1-btn r1-drawer-close"
          data-role="test-close"
          aria-label="Close test modal"
        >Close</button>
      </header>
      <form class="r1-skill-test-form" data-role="test-form" autocomplete="off"></form>
      <div class="r1-skill-test-actions">
        <button type="button" class="r1-btn" data-role="test-cancel">Cancel</button>
        <button type="button" class="r1-btn r1-btn-primary" data-role="test-submit">Run</button>
      </div>
      <section class="r1-skill-test-output" data-role="test-output" hidden>
        <h4>Output</h4>
        <pre class="r1-skill-test-output-pre"><code data-role="test-output-code"></code></pre>
        <div class="r1-skill-test-duration" data-role="test-duration"></div>
      </section>
    </div>
  `;
  modal.addEventListener("click", (event) => {
    if (event.target === modal) closeTestModal();
  });
  modal
    .querySelector<HTMLButtonElement>('[data-role="test-close"]')
    ?.addEventListener("click", closeTestModal);
  modal
    .querySelector<HTMLButtonElement>('[data-role="test-cancel"]')
    ?.addEventListener("click", closeTestModal);
  modal
    .querySelector<HTMLButtonElement>('[data-role="test-submit"]')
    ?.addEventListener("click", () => {
      void submitTestModal();
    });

  parent.appendChild(modal);
  testModal = modal;

  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape") return;
    if (!testModal || testModal.hidden) return;
    event.preventDefault();
    closeTestModal();
  });
}

async function openTestModal(id: string): Promise<void> {
  if (!testModal) return;
  testLastFocus = document.activeElement instanceof HTMLElement
    ? document.activeElement
    : null;
  testActiveId = id;

  const form = testModal.querySelector<HTMLFormElement>('[data-role="test-form"]');
  const outputBox = testModal.querySelector<HTMLElement>('[data-role="test-output"]');
  if (form) form.innerHTML = `<p class="r1-empty">Loading schema&hellip;</p>`;
  if (outputBox) outputBox.hidden = true;

  testModal.hidden = false;
  testModal.classList.add("is-open");

  const manifest = await invokeStub<SkillManifest>(
    "skill_get",
    "R1D-4",
    emptyManifest(id),
    { id },
  );

  if (testActiveId !== id || !form) return;

  const title = testModal.querySelector<HTMLHeadingElement>(
    `#${TEST_MODAL_ID}-title`,
  );
  if (title) title.textContent = `Test ${manifest.name || id}`;

  testActiveSchema = manifest.inputs ?? { type: "object", properties: {} };
  form.innerHTML = renderSchemaForm(testActiveSchema, "");
  const firstInput = form.querySelector<HTMLElement>("input, select, textarea");
  firstInput?.focus();
}

function closeTestModal(): void {
  if (!testModal) return;
  testModal.classList.remove("is-open");
  testModal.hidden = true;
  testActiveId = null;
  testActiveSchema = null;
  if (testLastFocus && document.body.contains(testLastFocus)) {
    testLastFocus.focus();
  }
  testLastFocus = null;
}

async function submitTestModal(): Promise<void> {
  if (!testModal || !testActiveId || !testActiveSchema) return;
  const form = testModal.querySelector<HTMLFormElement>('[data-role="test-form"]');
  if (!form) return;
  const input = collectFormValue(form, testActiveSchema, "");
  const submitBtn = testModal.querySelector<HTMLButtonElement>(
    '[data-role="test-submit"]',
  );
  if (submitBtn) {
    submitBtn.disabled = true;
    submitBtn.textContent = "Running...";
  }

  const result = await invokeStub<SkillInvokeResult>(
    "skill_invoke",
    "R1D-4",
    { output: "", duration_ms: 0 },
    { id: testActiveId, input },
  );

  if (submitBtn) {
    submitBtn.disabled = false;
    submitBtn.textContent = "Run";
  }

  const outputBox = testModal.querySelector<HTMLElement>('[data-role="test-output"]');
  const outputCode = testModal.querySelector<HTMLElement>(
    '[data-role="test-output-code"]',
  );
  const durationEl = testModal.querySelector<HTMLElement>(
    '[data-role="test-duration"]',
  );
  if (outputBox) outputBox.hidden = false;
  if (outputCode) {
    outputCode.textContent = result.output === ""
      ? "(empty stub output)"
      : result.output;
  }
  if (durationEl) {
    durationEl.textContent = `duration: ${result.duration_ms} ms`;
  }
}

// ---------------------------------------------------------------------
// JSON-schema form generator (R1D-4.5)
// ---------------------------------------------------------------------

function renderSchemaForm(schema: SkillJsonSchema, path: string): string {
  if (!schema || !schema.properties || Object.keys(schema.properties).length === 0) {
    return `<p class="r1-empty">No input fields declared for this skill.</p>`;
  }
  const required = new Set(schema.required ?? []);
  return Object.entries(schema.properties)
    .map(([key, child]) =>
      renderField(key, child, required.has(key), joinPath(path, key)),
    )
    .join("");
}

function renderField(
  key: string,
  schema: SkillJsonSchema,
  required: boolean,
  path: string,
): string {
  const label = `${escapeHtml(key)}${required ? '<span class="r1-skill-required">*</span>' : ""}`;
  const desc = schema.description
    ? `<span class="r1-skill-field-desc">${escapeHtml(schema.description)}</span>`
    : "";

  if (schema.enum && schema.enum.length > 0) {
    const options = schema.enum
      .map(
        (opt) =>
          `<option value="${escapeHtml(String(opt))}">${escapeHtml(String(opt))}</option>`,
      )
      .join("");
    return `
      <label class="r1-skill-form-field">
        <span class="r1-skill-form-label">${label}</span>
        ${desc}
        <select name="${escapeHtml(path)}" data-field-path="${escapeHtml(path)}" data-field-kind="enum">
          <option value="">-- select --</option>
          ${options}
        </select>
      </label>
    `;
  }

  if (schema.type === "object") {
    const inner = schema.properties
      ? renderSchemaForm(schema, path)
      : `<p class="r1-empty">No nested fields.</p>`;
    return `
      <fieldset class="r1-skill-form-fieldset">
        <legend>${label}</legend>
        ${desc}
        ${inner}
      </fieldset>
    `;
  }

  if (schema.type === "boolean") {
    return `
      <label class="r1-skill-form-field r1-skill-form-bool">
        <input
          type="checkbox"
          name="${escapeHtml(path)}"
          data-field-path="${escapeHtml(path)}"
          data-field-kind="boolean"
        />
        <span class="r1-skill-form-label">${label}</span>
        ${desc}
      </label>
    `;
  }

  if (schema.type === "number" || schema.type === "integer") {
    const step = schema.type === "integer" ? "1" : "any";
    const min =
      typeof schema.minimum === "number" ? `min="${schema.minimum}"` : "";
    const max =
      typeof schema.maximum === "number" ? `max="${schema.maximum}"` : "";
    return `
      <label class="r1-skill-form-field">
        <span class="r1-skill-form-label">${label}</span>
        ${desc}
        <input
          type="number"
          step="${step}"
          ${min}
          ${max}
          name="${escapeHtml(path)}"
          data-field-path="${escapeHtml(path)}"
          data-field-kind="${schema.type}"
        />
      </label>
    `;
  }

  if (schema.type === "array") {
    return `
      <label class="r1-skill-form-field">
        <span class="r1-skill-form-label">${label} (comma-separated)</span>
        ${desc}
        <input
          type="text"
          name="${escapeHtml(path)}"
          data-field-path="${escapeHtml(path)}"
          data-field-kind="array"
        />
      </label>
    `;
  }

  const maxAttr =
    typeof schema.maxLength === "number" ? `maxlength="${schema.maxLength}"` : "";
  const minAttr =
    typeof schema.minLength === "number" ? `minlength="${schema.minLength}"` : "";
  const long =
    typeof schema.maxLength === "number" && schema.maxLength > 200;
  if (long) {
    return `
      <label class="r1-skill-form-field">
        <span class="r1-skill-form-label">${label}</span>
        ${desc}
        <textarea
          rows="3"
          ${maxAttr}
          ${minAttr}
          name="${escapeHtml(path)}"
          data-field-path="${escapeHtml(path)}"
          data-field-kind="string"
        ></textarea>
      </label>
    `;
  }
  return `
    <label class="r1-skill-form-field">
      <span class="r1-skill-form-label">${label}</span>
      ${desc}
      <input
        type="text"
        ${maxAttr}
        ${minAttr}
        name="${escapeHtml(path)}"
        data-field-path="${escapeHtml(path)}"
        data-field-kind="string"
      />
    </label>
  `;
}

function collectFormValue(
  form: HTMLFormElement,
  schema: SkillJsonSchema,
  path: string,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  if (!schema.properties) return out;
  for (const [key, child] of Object.entries(schema.properties)) {
    const childPath = joinPath(path, key);
    if (child.type === "object" && child.properties) {
      out[key] = collectFormValue(form, child, childPath);
      continue;
    }
    const el = form.querySelector<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>(
      `[data-field-path="${cssEscape(childPath)}"]`,
    );
    if (!el) continue;
    const kind = el.dataset.fieldKind;
    if (kind === "boolean") {
      out[key] = (el as HTMLInputElement).checked;
    } else if (kind === "number" || kind === "integer") {
      const raw = el.value;
      if (raw === "") continue;
      const num = kind === "integer" ? parseInt(raw, 10) : Number(raw);
      if (!Number.isNaN(num)) out[key] = num;
    } else if (kind === "array") {
      const raw = el.value.trim();
      if (raw === "") continue;
      out[key] = raw
        .split(",")
        .map((v) => v.trim())
        .filter((v) => v.length > 0);
    } else {
      const raw = el.value;
      if (raw !== "") out[key] = raw;
    }
  }
  return out;
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

function joinPath(parent: string, child: string): string {
  return parent ? `${parent}.${child}` : child;
}

function cssEscape(value: string): string {
  return value.replace(/["\\]/g, "\\$&");
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
