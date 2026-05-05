// SPDX-License-Identifier: MIT
//
// Settings overlay (R1D-7).
//
// Full-screen modal overlay with a left-nav + right-pane tabs layout.
// Seven sections are exposed; four land in R1D-7.1 through R1D-7.4, the
// remaining three render a short deferral stub that points the user at
// already-shipped panels or future phases.
//
// Sections:
//   - General     : empty-state copy; lands in a later phase
//   - Providers   : R1D-7.2 — provider rows + test-connection + default
//   - Vault       : R1D-7.3 — stored secrets with reveal / edit / delete
//   - Ledger      : pass-through link to the ledger browser panel
//   - Memory      : pass-through link to the memory inspector panel
//   - Governance  : R1D-7.4 — policy tier, HITL timeout, retention, shred
//   - Advanced    : empty-state copy; lands in R1D-7.4 / R1D-7.5
//
// IPC verbs touched: provider_test, vault_list, vault_set,
// vault_delete, gov_set. All go through the shared `invokeStub` shim
// under the R1D-7 phase tag.

import { invokeStub } from "../ipc-stub";
import type {
  GovSetResult,
  PolicyTier,
  ProviderRow,
  ProviderTestResult,
  RetentionPolicy,
  VaultDeleteResult,
  VaultEntry,
  VaultEntryKind,
  VaultSetResult,
} from "../types/ipc";

const OVERLAY_ID = "r1-settings-overlay";
const BACKDROP_ID = "r1-settings-backdrop";
const TRIGGER_ID = "r1-settings-trigger";

type SectionId =
  | "general"
  | "providers"
  | "vault"
  | "ledger"
  | "memory"
  | "governance"
  | "daemon"
  | "autostart"
  | "lanes"
  | "advanced";

interface SectionDef {
  id: SectionId;
  label: string;
  render: (body: HTMLElement, state: SettingsState) => void;
}

interface ProviderTestState {
  running: boolean;
  result?: ProviderTestResult;
}

interface VaultEditDraft {
  id: string;
  name: string;
  kind: VaultEntryKind;
  value: string;
}

interface GovernanceState {
  tier: PolicyTier;
  hitl_timeout_seconds: number;
  retention: RetentionPolicy;
  crypto_shred: boolean;
}

type LaneDensity = "verbose" | "normal" | "summary";

interface DaemonInfoState {
  url: string;
  mode: "external" | "sidecar" | "unknown";
  version: string;
  uptimeS: number;
}

interface AutostartState {
  enabled: boolean;
  loaded: boolean; // true once isEnabled() resolved on first visit
}

interface LanesPrefsState {
  density: LaneDensity;
}

interface SettingsState {
  active: SectionId;
  providers: ProviderRow[];
  providerTests: Map<string, ProviderTestState>;
  revealedProviderEndpoints: Set<string>;
  vault: VaultEntry[];
  revealedVaultIds: Set<string>;
  governance: GovernanceState;
  daemon: DaemonInfoState;
  autostart: AutostartState;
  lanes: LanesPrefsState;
}

const SEED_PROVIDERS: ProviderRow[] = [
  {
    id: "claude",
    name: "Claude (Anthropic)",
    endpoint: "https://api.anthropic.com",
    model: "claude-opus-4-7",
    is_default: true,
    status: "configured",
  },
  {
    id: "openai",
    name: "OpenAI",
    endpoint: "https://api.openai.com/v1",
    model: "gpt-4o",
    is_default: false,
    status: "needs_key",
  },
  {
    id: "gemini",
    name: "Google Gemini",
    endpoint: "https://generativelanguage.googleapis.com",
    model: "gemini-1.5-pro",
    is_default: false,
    status: "needs_key",
  },
  {
    id: "openrouter",
    name: "OpenRouter",
    endpoint: "https://openrouter.ai/api/v1",
    model: "auto",
    is_default: false,
    status: "needs_key",
  },
  {
    id: "ollama",
    name: "Ollama (local)",
    endpoint: "http://localhost:11434",
    model: "llama3.1:8b",
    is_default: false,
    status: "configured",
  },
];

const MODEL_OPTIONS: Record<string, string[]> = {
  claude: [
    "claude-opus-4-7",
    "claude-sonnet-4-7",
    "claude-haiku-4-6",
  ],
  openai: ["gpt-4o", "gpt-4o-mini", "o1-preview"],
  gemini: ["gemini-1.5-pro", "gemini-1.5-flash"],
  openrouter: ["auto", "openrouter/auto"],
  ollama: ["llama3.1:8b", "llama3.1:70b", "qwen2.5:14b"],
};

const DEFAULT_GOVERNANCE: GovernanceState = {
  tier: "community",
  hitl_timeout_seconds: 300,
  retention: "90d",
  crypto_shred: false,
};

const RETENTION_OPTIONS: Array<[RetentionPolicy, string]> = [
  ["ephemeral", "Ephemeral (clear on exit)"],
  ["30d", "30 days"],
  ["90d", "90 days"],
  ["1y", "1 year"],
  ["forever", "Keep forever"],
];

const SECTIONS: SectionDef[] = [
  { id: "general", label: "General", render: renderGeneral },
  { id: "providers", label: "Providers", render: renderProviders },
  { id: "vault", label: "Vault", render: renderVault },
  { id: "ledger", label: "Ledger", render: renderLedgerStub },
  { id: "memory", label: "Memory", render: renderMemoryStub },
  { id: "governance", label: "Governance", render: renderGovernance },
  // Spec desktop-cortex-augmentation §10 + checklist item 27 — three
  // new sub-sections augmenting the R1D-7 settings panel without
  // touching its existing tabs.
  { id: "daemon", label: "Daemon", render: renderDaemon },
  { id: "autostart", label: "Auto-start", render: renderAutostart },
  { id: "lanes", label: "Lanes", render: renderLanes },
  { id: "advanced", label: "Advanced", render: renderAdvanced },
];

let overlayRoot: HTMLElement | null = null;
let backdropRoot: HTMLElement | null = null;
let lastFocus: HTMLElement | null = null;
let state: SettingsState = newState();

function newState(): SettingsState {
  return {
    active: "providers",
    providers: SEED_PROVIDERS.map((p) => ({ ...p })),
    providerTests: new Map(),
    revealedProviderEndpoints: new Set(),
    vault: [],
    revealedVaultIds: new Set(),
    governance: { ...DEFAULT_GOVERNANCE },
    daemon: {
      url: "",
      mode: "unknown",
      version: "",
      uptimeS: 0,
    },
    autostart: { enabled: false, loaded: false },
    lanes: { density: "normal" },
  };
}

export function mountSettings(parent: HTMLElement): void {
  if (document.getElementById(OVERLAY_ID)) return;

  const backdrop = document.createElement("div");
  backdrop.id = BACKDROP_ID;
  backdrop.className = "r1-settings-backdrop";
  backdrop.hidden = true;
  backdrop.addEventListener("click", (event) => {
    if (event.target === backdrop) closeSettings();
  });

  const overlay = document.createElement("section");
  overlay.id = OVERLAY_ID;
  overlay.className = "r1-settings-overlay";
  overlay.setAttribute("role", "dialog");
  overlay.setAttribute("aria-modal", "true");
  overlay.setAttribute("aria-labelledby", `${OVERLAY_ID}-title`);
  overlay.hidden = true;
  overlay.tabIndex = -1;
  overlay.innerHTML = `
    <header class="r1-settings-header">
      <h2 id="${OVERLAY_ID}-title" class="r1-settings-title">Settings</h2>
      <button
        type="button"
        class="r1-btn r1-settings-close"
        data-role="settings-close"
        aria-label="Close settings"
      >Close</button>
    </header>
    <div class="r1-settings-body">
      <nav class="r1-settings-nav" role="navigation" aria-label="Settings sections">
        ${SECTIONS.map(renderNavButton).join("")}
      </nav>
      <div
        class="r1-settings-pane"
        data-role="settings-pane"
        role="tabpanel"
        tabindex="0"
      ></div>
    </div>
  `;
  overlay
    .querySelector<HTMLButtonElement>('[data-role="settings-close"]')
    ?.addEventListener("click", closeSettings);

  backdrop.appendChild(overlay);
  parent.appendChild(backdrop);

  overlayRoot = overlay;
  backdropRoot = backdrop;

  wireNav();
  document.addEventListener("keydown", handleKeydown);
}

/**
 * Adds a small "Settings" button inside `host`. Call once from
 * `main.ts` after mounting the panel grid.
 */
export function mountSettingsTrigger(host: HTMLElement): void {
  if (document.getElementById(TRIGGER_ID)) return;
  const btn = document.createElement("button");
  btn.id = TRIGGER_ID;
  btn.type = "button";
  btn.className = "r1-btn r1-settings-trigger";
  btn.textContent = "Settings";
  btn.setAttribute("aria-haspopup", "dialog");
  btn.addEventListener("click", () => openSettings());
  host.appendChild(btn);
}

export function openSettings(section: SectionId = "providers"): void {
  if (!overlayRoot || !backdropRoot) return;
  lastFocus = document.activeElement instanceof HTMLElement
    ? document.activeElement
    : null;

  backdropRoot.hidden = false;
  overlayRoot.hidden = false;
  overlayRoot.classList.add("is-open");
  activateSection(section);
  overlayRoot.focus();
  void loadInitial();
}

export function closeSettings(): void {
  if (!overlayRoot || !backdropRoot) return;
  overlayRoot.classList.remove("is-open");
  overlayRoot.hidden = true;
  backdropRoot.hidden = true;
  if (lastFocus && document.body.contains(lastFocus)) {
    lastFocus.focus();
  }
  lastFocus = null;
}

function handleKeydown(event: KeyboardEvent): void {
  if (event.key !== "Escape") return;
  if (!overlayRoot || overlayRoot.hidden) return;
  const modal = document.querySelector<HTMLElement>(".r1-settings-inner-modal");
  if (modal) {
    event.preventDefault();
    modal.remove();
    return;
  }
  event.preventDefault();
  closeSettings();
}

function renderNavButton(def: SectionDef): string {
  return `
    <button
      type="button"
      class="r1-settings-nav-btn"
      role="tab"
      data-section="${def.id}"
      id="r1-settings-nav-${def.id}"
      aria-selected="false"
      tabindex="-1"
    >${escapeHtml(def.label)}</button>
  `;
}

function wireNav(): void {
  if (!overlayRoot) return;
  const buttons = overlayRoot.querySelectorAll<HTMLButtonElement>(
    ".r1-settings-nav-btn",
  );
  buttons.forEach((btn) => {
    btn.addEventListener("click", () => {
      const section = btn.dataset.section as SectionId | undefined;
      if (!section) return;
      activateSection(section);
    });
    btn.addEventListener("keydown", (event) => {
      if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
      event.preventDefault();
      const idx = SECTIONS.findIndex((s) => s.id === state.active);
      const delta = event.key === "ArrowDown" ? 1 : -1;
      const next = SECTIONS[(idx + delta + SECTIONS.length) % SECTIONS.length];
      activateSection(next.id);
      overlayRoot
        ?.querySelector<HTMLButtonElement>(`#r1-settings-nav-${next.id}`)
        ?.focus();
    });
  });
}

function activateSection(section: SectionId): void {
  state.active = section;
  if (!overlayRoot) return;
  const buttons = overlayRoot.querySelectorAll<HTMLButtonElement>(
    ".r1-settings-nav-btn",
  );
  buttons.forEach((btn) => {
    const match = btn.dataset.section === section;
    btn.setAttribute("aria-selected", match ? "true" : "false");
    btn.tabIndex = match ? 0 : -1;
    btn.classList.toggle("is-active", match);
  });
  const pane = overlayRoot.querySelector<HTMLElement>(
    '[data-role="settings-pane"]',
  );
  if (!pane) return;
  const def = SECTIONS.find((s) => s.id === section);
  if (!def) return;
  pane.innerHTML = "";
  def.render(pane, state);
}

async function loadInitial(): Promise<void> {
  await Promise.all([loadVault()]);
}

// ---------------------------------------------------------------------
// General (empty-state; theme + notifications land in a later phase)
// ---------------------------------------------------------------------

function renderGeneral(body: HTMLElement): void {
  body.innerHTML = `
    <header class="r1-settings-section-header">
      <h3>General</h3>
      <p class="r1-settings-section-hint">
        Theme, language, and notification preferences land in a later phase.
      </p>
    </header>
    <p class="r1-empty">Nothing to configure here yet.</p>
  `;
}

// ---------------------------------------------------------------------
// Providers (R1D-7.2)
// ---------------------------------------------------------------------

function renderProviders(body: HTMLElement, s: SettingsState): void {
  body.innerHTML = `
    <header class="r1-settings-section-header">
      <h3>Providers</h3>
      <p class="r1-settings-section-hint">
        Pick the default provider and verify each endpoint is reachable.
      </p>
    </header>
    <ul class="r1-settings-providers" data-role="providers-list"></ul>
  `;
  const list = body.querySelector<HTMLUListElement>('[data-role="providers-list"]');
  if (!list) return;
  list.innerHTML = s.providers.map(renderProviderRow).join("");
  s.providers.forEach((provider) => wireProviderRow(list, provider));
}

function renderProviderRow(provider: ProviderRow): string {
  const modelOptions = (MODEL_OPTIONS[provider.id] ?? [provider.model])
    .map((m) => `
      <option value="${escapeHtml(m)}" ${m === provider.model ? "selected" : ""}>
        ${escapeHtml(m)}
      </option>`)
    .join("");
  const statusText = provider.status === "configured" ? "configured" : "needs key";
  return `
    <li class="r1-settings-provider-row" data-provider-id="${escapeHtml(provider.id)}">
      <div class="r1-settings-provider-main">
        <label class="r1-settings-provider-default">
          <input
            type="radio"
            name="r1-settings-default-provider"
            value="${escapeHtml(provider.id)}"
            ${provider.is_default ? "checked" : ""}
            data-role="default-radio"
            aria-label="Set ${escapeHtml(provider.name)} as default"
          />
          <span>Default</span>
        </label>
        <div class="r1-settings-provider-name">
          <strong>${escapeHtml(provider.name)}</strong>
          <span class="r1-settings-provider-status" data-status="${provider.status}">
            ${escapeHtml(statusText)}
          </span>
        </div>
      </div>
      <div class="r1-settings-provider-config">
        <label class="r1-settings-field">
          <span class="r1-settings-field-label">Endpoint</span>
          <code class="r1-settings-field-value">${escapeHtml(provider.endpoint)}</code>
        </label>
        <label class="r1-settings-field">
          <span class="r1-settings-field-label">Model</span>
          <select class="r1-settings-select" data-role="model-select">${modelOptions}</select>
        </label>
      </div>
      <div class="r1-settings-provider-test">
        <button type="button" class="r1-btn" data-role="test-btn">Test connection</button>
        <span class="r1-settings-provider-test-result" data-role="test-result" aria-live="polite"></span>
      </div>
    </li>
  `;
}

function wireProviderRow(list: HTMLUListElement, provider: ProviderRow): void {
  const row = list.querySelector<HTMLLIElement>(
    `[data-provider-id="${cssEscape(provider.id)}"]`,
  );
  if (!row) return;

  row
    .querySelector<HTMLInputElement>('[data-role="default-radio"]')
    ?.addEventListener("change", () => {
      state.providers = state.providers.map((p) => ({
        ...p,
        is_default: p.id === provider.id,
      }));
    });

  row
    .querySelector<HTMLSelectElement>('[data-role="model-select"]')
    ?.addEventListener("change", (event) => {
      const target = event.target as HTMLSelectElement;
      state.providers = state.providers.map((p) =>
        p.id === provider.id ? { ...p, model: target.value } : p,
      );
    });

  row
    .querySelector<HTMLButtonElement>('[data-role="test-btn"]')
    ?.addEventListener("click", () => {
      void runProviderTest(row, provider.id);
    });
}

async function runProviderTest(row: HTMLElement, providerId: string): Promise<void> {
  const btn = row.querySelector<HTMLButtonElement>('[data-role="test-btn"]');
  const result = row.querySelector<HTMLSpanElement>('[data-role="test-result"]');
  if (!btn || !result) return;

  state.providerTests.set(providerId, { running: true });
  btn.disabled = true;
  btn.textContent = "Testing…";
  result.textContent = "";
  result.className = "r1-settings-provider-test-result is-running";

  const empty: ProviderTestResult = {
    ok: true,
    latency_ms: 0,
    model: "",
  };
  const provider = state.providers.find((p) => p.id === providerId);
  const response = await invokeStub<ProviderTestResult>(
    "provider_test",
    "R1D-7",
    empty,
    { provider_id: providerId, endpoint: provider?.endpoint, model: provider?.model },
  );

  state.providerTests.set(providerId, { running: false, result: response });
  btn.disabled = false;
  btn.textContent = "Test connection";
  applyProviderTestResult(result, response);
}

function applyProviderTestResult(
  el: HTMLSpanElement,
  r: ProviderTestResult,
): void {
  const base = "r1-settings-provider-test-result";
  if (r.ok) {
    el.className = `${base} is-pass`;
    const model = r.model ? ` (${r.model})` : "";
    el.textContent = `OK ${r.latency_ms}ms${model}`;
    return;
  }
  el.className = `${base} is-fail`;
  el.textContent = r.message ? `Failed: ${r.message}` : "Failed";
}

// ---------------------------------------------------------------------
// Vault (R1D-7.3)
// ---------------------------------------------------------------------

function renderVault(body: HTMLElement, s: SettingsState): void {
  body.innerHTML = `
    <header class="r1-settings-section-header">
      <h3>Vault</h3>
      <p class="r1-settings-section-hint">
        Stored secrets (API keys, passphrases). The raw value never leaves
        the host process — the WebView only sees masked previews.
      </p>
      <div class="r1-settings-section-actions">
        <button type="button" class="r1-btn" data-role="vault-add">Add secret</button>
      </div>
    </header>
    <ul class="r1-settings-vault-list" data-role="vault-list"></ul>
  `;
  body
    .querySelector<HTMLButtonElement>('[data-role="vault-add"]')
    ?.addEventListener("click", () => {
      openVaultEditor({
        id: "",
        name: "",
        kind: "api_key",
        value: "",
      });
    });

  rerenderVaultList(body, s);
}

function rerenderVaultList(body: HTMLElement, s: SettingsState): void {
  const list = body.querySelector<HTMLUListElement>('[data-role="vault-list"]');
  if (!list) return;
  if (s.vault.length === 0) {
    list.innerHTML = `
      <li class="r1-empty">
        No secrets stored yet. Add an API key or passphrase to get started.
      </li>
    `;
    return;
  }
  list.innerHTML = s.vault.map((entry) => renderVaultRow(entry, s)).join("");
  s.vault.forEach((entry) => wireVaultRow(list, entry));
}

function renderVaultRow(entry: VaultEntry, s: SettingsState): string {
  const revealed = s.revealedVaultIds.has(entry.id);
  const suffix = lastFour(entry.masked_preview);
  const display = revealed && suffix
    ? `${"•".repeat(8)}${suffix}`
    : entry.masked_preview;
  return `
    <li class="r1-settings-vault-row" data-vault-id="${escapeHtml(entry.id)}">
      <div class="r1-settings-vault-main">
        <strong class="r1-settings-vault-name">${escapeHtml(entry.name)}</strong>
        <span class="r1-settings-vault-kind">${escapeHtml(entry.kind)}</span>
      </div>
      <code class="r1-settings-vault-value" data-role="vault-value">${escapeHtml(display)}</code>
      <time class="r1-settings-vault-at" datetime="${escapeHtml(entry.updated_at)}">
        ${escapeHtml(entry.updated_at)}
      </time>
      <div class="r1-settings-vault-actions">
        <button type="button" class="r1-btn" data-role="vault-reveal">
          ${revealed ? "Hide" : "Reveal"}
        </button>
        <button type="button" class="r1-btn" data-role="vault-edit">Edit</button>
        <button type="button" class="r1-btn r1-btn-danger" data-role="vault-delete">
          Delete
        </button>
      </div>
    </li>
  `;
}

function wireVaultRow(list: HTMLUListElement, entry: VaultEntry): void {
  const row = list.querySelector<HTMLLIElement>(
    `[data-vault-id="${cssEscape(entry.id)}"]`,
  );
  if (!row) return;

  row
    .querySelector<HTMLButtonElement>('[data-role="vault-reveal"]')
    ?.addEventListener("click", () => {
      if (state.revealedVaultIds.has(entry.id)) {
        state.revealedVaultIds.delete(entry.id);
      } else {
        state.revealedVaultIds.add(entry.id);
      }
      refreshVaultPane();
    });

  row
    .querySelector<HTMLButtonElement>('[data-role="vault-edit"]')
    ?.addEventListener("click", () => {
      openVaultEditor({
        id: entry.id,
        name: entry.name,
        kind: entry.kind,
        value: "",
      });
    });

  row
    .querySelector<HTMLButtonElement>('[data-role="vault-delete"]')
    ?.addEventListener("click", () => {
      openVaultDeleteConfirm(entry);
    });
}

async function loadVault(): Promise<void> {
  const response = await invokeStub<VaultEntry[]>(
    "vault_list",
    "R1D-7",
    [],
  );
  state.vault = response;
  refreshVaultPane();
}

function refreshVaultPane(): void {
  if (state.active !== "vault") return;
  const pane = overlayRoot?.querySelector<HTMLElement>(
    '[data-role="settings-pane"]',
  );
  if (!pane) return;
  rerenderVaultList(pane, state);
}

function openVaultEditor(draft: VaultEditDraft): void {
  const modal = buildInnerModal({
    title: draft.id ? `Edit secret: ${draft.name}` : "Add secret",
    bodyHtml: `
      <label class="r1-settings-field">
        <span class="r1-settings-field-label">Name</span>
        <input
          type="text"
          class="r1-settings-input"
          data-role="vault-name"
          value="${escapeHtml(draft.name)}"
          autocomplete="off"
          spellcheck="false"
        />
      </label>
      <label class="r1-settings-field">
        <span class="r1-settings-field-label">Kind</span>
        <select class="r1-settings-select" data-role="vault-kind">
          <option value="api_key" ${draft.kind === "api_key" ? "selected" : ""}>API key</option>
          <option value="passphrase" ${draft.kind === "passphrase" ? "selected" : ""}>Passphrase</option>
          <option value="other" ${draft.kind === "other" ? "selected" : ""}>Other</option>
        </select>
      </label>
      <label class="r1-settings-field">
        <span class="r1-settings-field-label">Value</span>
        <input
          type="password"
          class="r1-settings-input"
          data-role="vault-value-input"
          value=""
          autocomplete="off"
          spellcheck="false"
        />
        <span class="r1-settings-field-hint">
          Never leaves this process. Host writes it straight to the OS keychain.
        </span>
      </label>
    `,
    primaryLabel: draft.id ? "Save" : "Add",
    onPrimary: async (modalEl) => {
      const name = modalEl
        .querySelector<HTMLInputElement>('[data-role="vault-name"]')
        ?.value.trim() ?? "";
      const kind = (modalEl
        .querySelector<HTMLSelectElement>('[data-role="vault-kind"]')
        ?.value ?? "api_key") as VaultEntryKind;
      const value = modalEl
        .querySelector<HTMLInputElement>('[data-role="vault-value-input"]')
        ?.value ?? "";
      if (!name) return false;
      if (!draft.id && !value) return false;
      const response = await invokeStub<VaultSetResult>(
        "vault_set",
        "R1D-7",
        { ok: true },
        {
          id: draft.id,
          name,
          kind,
          has_new_value: value.length > 0,
        },
      );
      if (!response.ok) return false;
      applyVaultSet(draft.id, name, kind, value);
      return true;
    },
  });
  document.body.appendChild(modal);
  modal
    .querySelector<HTMLInputElement>('[data-role="vault-name"]')
    ?.focus();
}

function applyVaultSet(
  id: string,
  name: string,
  kind: VaultEntryKind,
  rawValue: string,
): void {
  const now = new Date().toISOString();
  const preview = buildMaskedPreview(rawValue);
  if (id) {
    state.vault = state.vault.map((entry) =>
      entry.id === id
        ? {
            ...entry,
            name,
            kind,
            masked_preview: rawValue ? preview : entry.masked_preview,
            updated_at: now,
          }
        : entry,
    );
  } else {
    const newId = `vault-${Date.now()}`;
    state.vault = [
      ...state.vault,
      {
        id: newId,
        name,
        kind,
        masked_preview: preview,
        updated_at: now,
      },
    ];
  }
  refreshVaultPane();
}

function openVaultDeleteConfirm(entry: VaultEntry): void {
  const modal = buildInnerModal({
    title: "Delete secret?",
    bodyHtml: `
      <p class="r1-modal-body">
        Permanently delete the <strong>${escapeHtml(entry.name)}</strong> secret
        from the local keychain? This cannot be undone.
      </p>
    `,
    primaryLabel: "Delete",
    primaryDanger: true,
    onPrimary: async () => {
      const response = await invokeStub<VaultDeleteResult>(
        "vault_delete",
        "R1D-7",
        { ok: true },
        { id: entry.id },
      );
      if (!response.ok) return false;
      state.vault = state.vault.filter((row) => row.id !== entry.id);
      state.revealedVaultIds.delete(entry.id);
      refreshVaultPane();
      return true;
    },
  });
  document.body.appendChild(modal);
}

function buildMaskedPreview(raw: string): string {
  if (!raw) return "";
  const suffix = raw.length >= 4 ? raw.slice(-4) : raw;
  return `${"•".repeat(8)}${suffix}`;
}

function lastFour(preview: string): string {
  const match = preview.match(/([A-Za-z0-9]{1,4})$/);
  return match?.[1] ?? "";
}

// ---------------------------------------------------------------------
// Pass-through sections (see-also links)
// ---------------------------------------------------------------------

function renderLedgerStub(body: HTMLElement): void {
  body.innerHTML = `
    <header class="r1-settings-section-header">
      <h3>Ledger</h3>
      <p class="r1-settings-section-hint">
        Session chains, verify-chain, and crypto-shred are handled in
        the Ledger Browser panel.
      </p>
    </header>
    <p class="r1-settings-seealso">
      See also:
      <a href="#panel-ledger-viewer" data-role="settings-deeplink">Ledger Browser</a>
    </p>
  `;
  wireDeeplink(body);
}

function renderMemoryStub(body: HTMLElement): void {
  body.innerHTML = `
    <header class="r1-settings-section-header">
      <h3>Memory</h3>
      <p class="r1-settings-section-hint">
        Scoped key/value rows, history, and import/export are handled
        in the Memory Bus panel.
      </p>
    </header>
    <p class="r1-settings-seealso">
      See also:
      <a href="#panel-memory-inspector" data-role="settings-deeplink">Memory Bus</a>
    </p>
  `;
  wireDeeplink(body);
}

function renderAdvanced(body: HTMLElement): void {
  body.innerHTML = `
    <header class="r1-settings-section-header">
      <h3>Advanced</h3>
      <p class="r1-settings-section-hint">
        Data-directory picker, diagnostic-bundle export, and the
        <code>.stoke/</code> → <code>.r1/</code> migration tool land in
        R1D-7.4 / R1D-7.5.
      </p>
    </header>
    <p class="r1-empty">Nothing to configure here yet.</p>
  `;
}

function wireDeeplink(body: HTMLElement): void {
  body
    .querySelector<HTMLAnchorElement>('[data-role="settings-deeplink"]')
    ?.addEventListener("click", (event) => {
      event.preventDefault();
      closeSettings();
      const href = (event.currentTarget as HTMLAnchorElement).getAttribute("href");
      if (!href) return;
      const target = document.querySelector<HTMLElement>(href);
      target?.scrollIntoView({ behavior: "smooth", block: "start" });
      target?.focus?.();
    });
}

// ---------------------------------------------------------------------
// Governance (R1D-7.4)
// ---------------------------------------------------------------------

function renderGovernance(body: HTMLElement, s: SettingsState): void {
  const g = s.governance;
  body.innerHTML = `
    <header class="r1-settings-section-header">
      <h3>Governance</h3>
      <p class="r1-settings-section-hint">
        Policy tier and data-lifecycle controls. Changes propagate to
        the host on every field edit.
      </p>
    </header>
    <div class="r1-settings-form">
      <label class="r1-settings-field">
        <span class="r1-settings-field-label">Policy tier</span>
        <select class="r1-settings-select" data-role="gov-tier">
          <option value="community" ${g.tier === "community" ? "selected" : ""}>Community</option>
          <option value="enterprise" ${g.tier === "enterprise" ? "selected" : ""}>Enterprise</option>
        </select>
        <span class="r1-settings-field-hint">
          Enterprise enables HITL gating, signed skill packs, and extended audit retention.
        </span>
      </label>
      <label class="r1-settings-field">
        <span class="r1-settings-field-label">HITL timeout (seconds)</span>
        <input
          type="number"
          class="r1-settings-input"
          data-role="gov-hitl"
          min="5"
          max="86400"
          step="1"
          value="${g.hitl_timeout_seconds}"
        />
        <span class="r1-settings-field-hint">
          How long to wait on a human approval before the prompt auto-rejects.
        </span>
      </label>
      <label class="r1-settings-field">
        <span class="r1-settings-field-label">Retention policy</span>
        <select class="r1-settings-select" data-role="gov-retention">
          ${RETENTION_OPTIONS.map(([value, label]) => `
            <option value="${value}" ${g.retention === value ? "selected" : ""}>
              ${escapeHtml(label)}
            </option>
          `).join("")}
        </select>
      </label>
      <label class="r1-settings-field r1-settings-field-inline">
        <input
          type="checkbox"
          data-role="gov-shred"
          ${g.crypto_shred ? "checked" : ""}
        />
        <span>
          <strong>Enable crypto-shred</strong>
          <span class="r1-settings-field-hint">
            Tombstones payloads when retention expires; hashes stay on-chain so verify still passes.
          </span>
        </span>
      </label>
    </div>
    <footer class="r1-settings-section-footer">
      <span class="r1-settings-save-status" data-role="gov-status" aria-live="polite"></span>
    </footer>
  `;

  body
    .querySelector<HTMLSelectElement>('[data-role="gov-tier"]')
    ?.addEventListener("change", (event) => {
      const tier = (event.target as HTMLSelectElement).value as PolicyTier;
      state.governance.tier = tier;
      void pushGovernance(body, { tier });
    });

  body
    .querySelector<HTMLInputElement>('[data-role="gov-hitl"]')
    ?.addEventListener("change", (event) => {
      const raw = (event.target as HTMLInputElement).value;
      const parsed = Number.parseInt(raw, 10);
      const clamped = Number.isFinite(parsed)
        ? Math.max(5, Math.min(86400, parsed))
        : DEFAULT_GOVERNANCE.hitl_timeout_seconds;
      state.governance.hitl_timeout_seconds = clamped;
      (event.target as HTMLInputElement).value = String(clamped);
      void pushGovernance(body, { hitl_timeout_seconds: clamped });
    });

  body
    .querySelector<HTMLSelectElement>('[data-role="gov-retention"]')
    ?.addEventListener("change", (event) => {
      const retention = (event.target as HTMLSelectElement).value as RetentionPolicy;
      state.governance.retention = retention;
      void pushGovernance(body, { retention });
    });

  body
    .querySelector<HTMLInputElement>('[data-role="gov-shred"]')
    ?.addEventListener("change", (event) => {
      const crypto_shred = (event.target as HTMLInputElement).checked;
      state.governance.crypto_shred = crypto_shred;
      void pushGovernance(body, { crypto_shred });
    });
}

async function pushGovernance(
  body: HTMLElement,
  patch: Partial<GovernanceState>,
): Promise<void> {
  const status = body.querySelector<HTMLSpanElement>('[data-role="gov-status"]');
  if (status) {
    status.className = "r1-settings-save-status is-running";
    status.textContent = "Saving…";
  }
  const response = await invokeStub<GovSetResult>(
    "gov_set",
    "R1D-7",
    { ok: true },
    { ...state.governance, ...patch },
  );
  if (!status) return;
  if (response.ok) {
    status.className = "r1-settings-save-status is-pass";
    status.textContent = "Saved";
  } else {
    status.className = "r1-settings-save-status is-fail";
    status.textContent = "Save failed";
  }
}

// ---------------------------------------------------------------------
// Small inner-modal helper (Add/Edit/Delete confirmations)
// ---------------------------------------------------------------------

interface InnerModalOpts {
  title: string;
  bodyHtml: string;
  primaryLabel: string;
  primaryDanger?: boolean;
  onPrimary: (modal: HTMLElement) => Promise<boolean>;
}

function buildInnerModal(opts: InnerModalOpts): HTMLElement {
  const modal = document.createElement("div");
  modal.className = "r1-modal r1-settings-inner-modal";
  modal.setAttribute("role", "dialog");
  modal.setAttribute("aria-modal", "true");
  modal.innerHTML = `
    <div class="r1-modal-panel">
      <h3 class="r1-modal-title">${escapeHtml(opts.title)}</h3>
      <div class="r1-modal-body">${opts.bodyHtml}</div>
      <div class="r1-modal-actions">
        <button type="button" class="r1-btn" data-role="inner-cancel">Cancel</button>
        <button
          type="button"
          class="r1-btn ${opts.primaryDanger ? "r1-btn-danger" : "r1-btn-primary"}"
          data-role="inner-primary"
        >${escapeHtml(opts.primaryLabel)}</button>
      </div>
    </div>
  `;
  modal
    .querySelector<HTMLButtonElement>('[data-role="inner-cancel"]')
    ?.addEventListener("click", () => modal.remove());
  modal
    .querySelector<HTMLButtonElement>('[data-role="inner-primary"]')
    ?.addEventListener("click", async () => {
      const primary = modal.querySelector<HTMLButtonElement>(
        '[data-role="inner-primary"]',
      );
      if (primary) {
        primary.disabled = true;
        primary.textContent = "Working…";
      }
      const ok = await opts.onPrimary(modal);
      if (ok) {
        modal.remove();
      } else if (primary) {
        primary.disabled = false;
        primary.textContent = opts.primaryLabel;
      }
    });
  return modal;
}

// ---------------------------------------------------------------------
// Daemon (spec desktop-cortex-augmentation §5 + item 27)
// ---------------------------------------------------------------------

function renderDaemon(body: HTMLElement, s: SettingsState): void {
  const d = s.daemon;
  const modeLabel =
    d.mode === "external"
      ? "External (r1 serve)"
      : d.mode === "sidecar"
        ? "Bundled sidecar"
        : "Unknown — not yet probed";
  body.innerHTML = `
    <header class="r1-settings-section-header">
      <h3>Daemon</h3>
      <p class="r1-settings-section-hint">
        Connection to the r1 daemon. The desktop prefers an externally-installed
        daemon (faster startup) and falls back to the bundled sidecar.
      </p>
    </header>
    <div class="r1-settings-form">
      <label class="r1-settings-field">
        <span class="r1-settings-field-label">URL</span>
        <code class="r1-settings-field-value" data-role="daemon-url">${escapeHtml(d.url || "—")}</code>
      </label>
      <label class="r1-settings-field">
        <span class="r1-settings-field-label">Mode</span>
        <code class="r1-settings-field-value" data-role="daemon-mode">${escapeHtml(modeLabel)}</code>
      </label>
      <label class="r1-settings-field">
        <span class="r1-settings-field-label">Version</span>
        <code class="r1-settings-field-value" data-role="daemon-version">${escapeHtml(d.version || "—")}</code>
      </label>
      <label class="r1-settings-field">
        <span class="r1-settings-field-label">Uptime</span>
        <code class="r1-settings-field-value" data-role="daemon-uptime">${escapeHtml(formatUptime(d.uptimeS))}</code>
      </label>
    </div>
    <footer class="r1-settings-section-footer">
      <button type="button" class="r1-btn" data-role="daemon-reconnect">Reconnect</button>
      <button type="button" class="r1-btn" data-role="daemon-install">Install as service…</button>
      <span class="r1-settings-save-status" data-role="daemon-status" aria-live="polite"></span>
    </footer>
  `;

  body
    .querySelector<HTMLButtonElement>('[data-role="daemon-reconnect"]')
    ?.addEventListener("click", () => {
      void runDaemonReconnect(body);
    });

  body
    .querySelector<HTMLButtonElement>('[data-role="daemon-install"]')
    ?.addEventListener("click", () => {
      openDaemonInstallHelp();
    });
}

async function runDaemonReconnect(body: HTMLElement): Promise<void> {
  const status = body.querySelector<HTMLSpanElement>(
    '[data-role="daemon-status"]',
  );
  if (status) {
    status.className = "r1-settings-save-status is-running";
    status.textContent = "Reconnecting…";
  }
  const response = await invokeStub<{
    url: string;
    mode: "external" | "sidecar";
    version: string;
    uptime_s: number;
  }>(
    "daemon_status",
    "R1D-augmentation",
    {
      url: state.daemon.url,
      mode: state.daemon.mode === "unknown" ? "external" : state.daemon.mode,
      version: state.daemon.version,
      uptime_s: state.daemon.uptimeS,
    },
  );
  state.daemon = {
    url: response.url,
    mode: response.mode,
    version: response.version,
    uptimeS: response.uptime_s,
  };
  if (state.active === "daemon") activateSection("daemon");
  if (status) {
    status.className = "r1-settings-save-status is-pass";
    status.textContent = "Connected";
  }
}

function openDaemonInstallHelp(): void {
  // The host emits the platform-appropriate `r1 serve --install`
  // string via discovery::install_command_for_host_os; UI shows it
  // in a copy-paste box. Until the wizard verb is wired, we surface
  // the three known shapes so the user can pick one.
  const modal = buildInnerModal({
    title: "Install r1 as a system service",
    bodyHtml: `
      <p class="r1-modal-body">
        Run the appropriate command in your terminal so r1 starts at login
        and the desktop attaches faster on subsequent launches.
      </p>
      <pre class="r1-modal-pre"><code>r1 serve --install --launchd          # macOS
r1 serve --install --systemd-user      # Linux
r1 serve --install --task-scheduler    # Windows</code></pre>
    `,
    primaryLabel: "Got it",
    onPrimary: async () => true,
  });
  document.body.appendChild(modal);
}

function formatUptime(seconds: number): string {
  if (!seconds || seconds < 0) return "—";
  if (seconds < 60) return `${seconds}s`;
  const m = Math.floor(seconds / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${m % 60}m`;
  const d = Math.floor(h / 24);
  return `${d}d ${h % 24}h`;
}

// ---------------------------------------------------------------------
// Auto-start (spec §10 + item 27)
// ---------------------------------------------------------------------

function renderAutostart(body: HTMLElement, s: SettingsState): void {
  const a = s.autostart;
  body.innerHTML = `
    <header class="r1-settings-section-header">
      <h3>Auto-start</h3>
      <p class="r1-settings-section-hint">
        Start R1 Desktop at login. Uses the OS-appropriate hook
        (Login Items on macOS, Run-key on Windows, XDG autostart on Linux).
      </p>
    </header>
    <div class="r1-settings-form">
      <label class="r1-settings-field r1-settings-field-inline">
        <input
          type="checkbox"
          data-role="autostart-toggle"
          ${a.enabled ? "checked" : ""}
        />
        <span>
          <strong>Start R1 Desktop at login</strong>
          <span class="r1-settings-field-hint">
            ${a.loaded
              ? "Reflects the current OS-side hook state."
              : "Probing OS state… (this loads on first visit)"}
          </span>
        </span>
      </label>
    </div>
    <footer class="r1-settings-section-footer">
      <span class="r1-settings-save-status" data-role="autostart-status" aria-live="polite"></span>
    </footer>
  `;

  body
    .querySelector<HTMLInputElement>('[data-role="autostart-toggle"]')
    ?.addEventListener("change", (event) => {
      const next = (event.target as HTMLInputElement).checked;
      void setAutostart(body, next);
    });

  if (!a.loaded) void loadAutostartProbe(body);
}

async function loadAutostartProbe(body: HTMLElement): Promise<void> {
  // The plugin-autostart `isEnabled` check round-trips to the OS via
  // tauri-plugin-autostart. Through invokeStub we get a deterministic
  // false until the wired-up call lands; once it does the toggle
  // reflects the real state.
  const enabled = await invokeStub<boolean>(
    "autostart_is_enabled",
    "R1D-augmentation",
    false,
  );
  state.autostart = { enabled, loaded: true };
  if (state.active === "autostart") activateSection("autostart");
  void body; // body closure no longer needed; activateSection re-renders.
}

async function setAutostart(body: HTMLElement, enabled: boolean): Promise<void> {
  const status = body.querySelector<HTMLSpanElement>(
    '[data-role="autostart-status"]',
  );
  if (status) {
    status.className = "r1-settings-save-status is-running";
    status.textContent = enabled ? "Enabling…" : "Disabling…";
  }
  const response = await invokeStub<{ ok: boolean }>(
    enabled ? "autostart_enable" : "autostart_disable",
    "R1D-augmentation",
    { ok: true },
  );
  state.autostart = {
    enabled: response.ok ? enabled : !enabled,
    loaded: true,
  };
  if (status) {
    status.className = response.ok
      ? "r1-settings-save-status is-pass"
      : "r1-settings-save-status is-fail";
    status.textContent = response.ok ? "Saved" : "Failed";
  }
  if (state.active === "autostart") activateSection("autostart");
}

// ---------------------------------------------------------------------
// Lanes density (spec §8 + item 27)
// ---------------------------------------------------------------------

function renderLanes(body: HTMLElement, s: SettingsState): void {
  const density = s.lanes.density;
  const choices: Array<{ value: LaneDensity; label: string; hint: string }> = [
    {
      value: "verbose",
      label: "Verbose",
      hint: "Lane card shows full last-event preview + status text.",
    },
    {
      value: "normal",
      label: "Normal",
      hint: "Lane card shows last-event preview (default).",
    },
    {
      value: "summary",
      label: "Summary",
      hint: "Lane card shows just status glyph + title.",
    },
  ];
  body.innerHTML = `
    <header class="r1-settings-section-header">
      <h3>Lanes</h3>
      <p class="r1-settings-section-hint">
        Control how much detail each lane card renders in the sidebar
        and pop-out windows.
      </p>
    </header>
    <fieldset class="r1-settings-form" data-role="lanes-density">
      <legend class="r1-visually-hidden">Lane density</legend>
      ${choices
        .map(
          (c) => `
            <label class="r1-settings-field r1-settings-field-inline">
              <input
                type="radio"
                name="r1-settings-lane-density"
                value="${c.value}"
                ${density === c.value ? "checked" : ""}
              />
              <span>
                <strong>${escapeHtml(c.label)}</strong>
                <span class="r1-settings-field-hint">${escapeHtml(c.hint)}</span>
              </span>
            </label>
          `,
        )
        .join("")}
    </fieldset>
    <footer class="r1-settings-section-footer">
      <span class="r1-settings-save-status" data-role="lanes-status" aria-live="polite"></span>
    </footer>
  `;

  body
    .querySelectorAll<HTMLInputElement>(
      'input[name="r1-settings-lane-density"]',
    )
    .forEach((input) => {
      input.addEventListener("change", (event) => {
        const value = (event.target as HTMLInputElement).value as LaneDensity;
        void setLaneDensity(body, value);
      });
    });
}

async function setLaneDensity(
  body: HTMLElement,
  density: LaneDensity,
): Promise<void> {
  const status = body.querySelector<HTMLSpanElement>(
    '[data-role="lanes-status"]',
  );
  if (status) {
    status.className = "r1-settings-save-status is-running";
    status.textContent = "Saving…";
  }
  state.lanes.density = density;
  const response = await invokeStub<{ ok: boolean }>(
    "prefs_set_lane_density",
    "R1D-augmentation",
    { ok: true },
    { density },
  );
  if (status) {
    status.className = response.ok
      ? "r1-settings-save-status is-pass"
      : "r1-settings-save-status is-fail";
    status.textContent = response.ok ? "Saved" : "Failed";
  }
}

// ---------------------------------------------------------------------
// HTML helpers
// ---------------------------------------------------------------------

function escapeHtml(raw: string): string {
  return raw
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

function cssEscape(raw: string): string {
  return raw.replace(/["\\]/g, "\\$&");
}
