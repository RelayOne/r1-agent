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
//   - Providers   : read-only inventory; host-side test/save verbs pending
//   - Vault       : explicit unavailable state until host vault verbs land
//   - Ledger      : pass-through link to the ledger browser panel
//   - Memory      : pass-through link to the memory inspector panel
//   - Governance  : explicit unavailable state until host save verb lands
//   - Advanced    : empty-state copy; lands in R1D-7.4 / R1D-7.5
//
// The daemon subsection is live because the host exposes `daemon_status`
// and `daemon_install_command`. Auto-start and lane density are live
// through tauri-plugin-autostart / tauri-plugin-store (audit A054).
// Provider tests, vault persistence, and governance saves are not wired
// in this desktop build, so those sections render truthful unavailable
// states instead of invoking missing handlers.

import { invokeStub } from "../ipc-stub";
import {
  getAutostart,
  setAutostart,
  type AutostartResult,
} from "../lib/autostart";
import {
  getLaneDensity,
  isLaneDensity,
  setLaneDensity,
  type LaneDensity,
} from "../lib/lanePrefs";
import {
  hasLocalKey,
  providerNeedsKey,
  selectedProviderId,
} from "../onboarding/onboarding";
import type {
  PolicyTier,
  ProviderRow,
  RetentionPolicy,
  VaultEntry,
  VaultEntryKind,
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

interface GovernanceState {
  tier: PolicyTier;
  hitl_timeout_seconds: number;
  retention: RetentionPolicy;
  crypto_shred: boolean;
}

interface DaemonInfoState {
  url: string;
  mode: "external" | "sidecar" | "unknown";
  version: string;
  uptimeS: number;
}

interface AutostartUiState {
  /** Persisted desired state (prefs.json), once loaded. */
  desired: boolean;
  /** Live OS-side hook state; null = could not be probed. */
  actual: boolean | null;
  /** True once getAutostart() resolved for this panel visit. */
  loaded: boolean;
  /** Probe/persist infrastructure unreachable (non-Tauri build). */
  unavailable: string | null;
  /** Last toggle error surfaced from AutostartResult.error. */
  error: string | null;
  /** True while a probe or toggle is in flight. */
  busy: boolean;
}

interface LanesPrefsState {
  density: LaneDensity;
  loaded: boolean;
  busy: boolean;
  error: string | null;
}

interface SettingsState {
  active: SectionId;
  providers: ProviderRow[];
  vault: VaultEntry[];
  governance: GovernanceState;
  daemon: DaemonInfoState;
  autostart: AutostartUiState;
  lanes: LanesPrefsState;
}

// Static provider catalog (name / endpoint / default model only).
// Status and default badges are DERIVED at render time from what
// onboarding actually stored locally — never fabricated (audit A086).
// The seeds below carry the pessimistic resting state; see
// withDerivedProviderTruth().
const SEED_PROVIDERS: ProviderRow[] = [
  {
    id: "claude",
    name: "Claude (Anthropic)",
    endpoint: "https://api.anthropic.com",
    model: "claude-opus-4-7",
    is_default: false,
    status: "needs_key",
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
    status: "not_probed",
  },
];

/**
 * Derive truthful status/default flags for the static catalog
 * (audit A086): a keyed provider is "configured" only when onboarding
 * stored a local API key for it; keyless providers (Ollama) stay
 * "not_probed" because nothing in this build checks the endpoint;
 * the Default badge tracks the provider actually chosen in
 * onboarding, or nothing when no choice was recorded.
 */
function withDerivedProviderTruth(rows: ProviderRow[]): ProviderRow[] {
  const chosen = selectedProviderId();
  return rows.map((row) => {
    const needsKey = providerNeedsKey(row.id);
    const status: ProviderRow["status"] =
      needsKey === false
        ? "not_probed"
        : hasLocalKey(row.id)
          ? "configured"
          : "needs_key";
    return { ...row, status, is_default: chosen === row.id };
  });
}

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
    vault: [],
    governance: { ...DEFAULT_GOVERNANCE },
    daemon: {
      url: "",
      mode: "unknown",
      version: "",
      uptimeS: 0,
    },
    autostart: {
      desired: false,
      actual: null,
      loaded: false,
      unavailable: null,
      error: null,
      busy: false,
    },
    lanes: { density: "normal", loaded: false, busy: false, error: null },
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
  await Promise.resolve();
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
  // Re-derive on every render so the panel reflects keys/choices the
  // user saved in onboarding since the last open (audit A086).
  s.providers = withDerivedProviderTruth(s.providers);
  body.innerHTML = `
    <header class="r1-settings-section-header">
      <h3>Providers</h3>
      <p class="r1-settings-section-hint">
        This build exposes the provider inventory only. Live connection
        tests are not wired in the desktop host yet.
      </p>
    </header>
    <div class="r1-settings-unavailable" data-role="providers-unavailable">
      <p class="r1-empty">
        Provider changes here are read-only until the desktop host implements provider settings IPC.
        The rows below are a static catalog; the status chip only reflects
        whether onboarding stored a local API key — endpoints are never probed.
      </p>
    </div>
    <ul class="r1-settings-providers" data-role="providers-list"></ul>
  `;
  const list = body.querySelector<HTMLUListElement>('[data-role="providers-list"]');
  if (!list) return;
  list.innerHTML = s.providers.map(renderProviderRow).join("");
}

function renderProviderRow(provider: ProviderRow): string {
  const statusText =
    provider.status === "configured"
      ? "configured (local key)"
      : provider.status === "not_probed"
        ? "not probed"
        : "needs key";
  return `
    <li class="r1-settings-provider-row" data-provider-id="${escapeHtml(provider.id)}">
      <div class="r1-settings-provider-main">
        <span class="r1-settings-provider-default-state">
          ${provider.is_default ? "Default" : "Available"}
        </span>
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
          <code class="r1-settings-field-value">${escapeHtml(provider.model)}</code>
        </label>
      </div>
      <div class="r1-settings-provider-test" data-role="providers-readonly-note">
        <span class="r1-settings-provider-test-result is-readonly">
          Host-side test/save controls unavailable in this build.
        </span>
      </div>
    </li>
  `;
}

// ---------------------------------------------------------------------
// Vault (R1D-7.3)
// ---------------------------------------------------------------------

function renderVault(body: HTMLElement): void {
  body.innerHTML = `
    <header class="r1-settings-section-header">
      <h3>Vault</h3>
      <p class="r1-settings-section-hint">
        Stored secrets require host keychain handlers that are not present in this build.
      </p>
    </header>
    <div class="r1-settings-unavailable" data-role="vault-unavailable">
      <p class="r1-empty">
        Vault listing, add/edit, reveal, and delete are unavailable because the desktop host does not implement the vault IPC surface.
      </p>
    </div>
  `;
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
        Governance persistence requires a host save handler that is not present in this build.
      </p>
    </header>
    <div class="r1-settings-unavailable" data-role="gov-unavailable">
      <p class="r1-empty">
        Governance controls are read-only until the desktop host implements the governance IPC surface.
      </p>
      <dl class="r1-settings-readonly-list">
        <div><dt>Policy tier</dt><dd>${escapeHtml(g.tier)}</dd></div>
        <div><dt>HITL timeout</dt><dd>${g.hitl_timeout_seconds}s</dd></div>
        <div><dt>Retention</dt><dd>${escapeHtml(g.retention)}</dd></div>
        <div><dt>Crypto-shred</dt><dd>${g.crypto_shred ? "enabled" : "disabled"}</dd></div>
      </dl>
    </div>
  `;
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
      void openDaemonInstallHelp();
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

async function openDaemonInstallHelp(): Promise<void> {
  const installCommand = await invokeStub<string>(
    "daemon_install_command",
    "R1D-augmentation",
    `r1 serve --install --launchd          # macOS
r1 serve --install --systemd-user      # Linux
r1 serve --install --task-scheduler    # Windows`,
  );
  const modal = buildInnerModal({
    title: "Install r1 as a system service",
    bodyHtml: `
      <p class="r1-modal-body">
        Run the appropriate command in your terminal so r1 starts at login
        and the desktop attaches faster on subsequent launches.
      </p>
      <pre class="r1-modal-pre"><code>${escapeHtml(installCommand)}</code></pre>
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
  if (a.unavailable) {
    // Truthful fallback: the plugin bridge rejected (non-Tauri build
    // or missing ACL grant) — say so instead of faking a toggle.
    body.innerHTML = `
    <header class="r1-settings-section-header">
      <h3>Auto-start</h3>
      <p class="r1-settings-section-hint">
        Start R1 Desktop automatically at login.
      </p>
    </header>
    <div class="r1-settings-unavailable" data-role="autostart-unavailable">
      <p class="r1-empty">
        Auto-start remains unchanged: the autostart/store plugin bridge is
        not reachable in this environment (${escapeHtml(a.unavailable)}).
      </p>
    </div>
  `;
    return;
  }
  const actualLabel = !a.loaded
    ? "probing…"
    : a.actual === null
      ? "could not be probed"
      : a.actual
        ? "registered"
        : "not registered";
  const drift = a.loaded && a.actual !== null && a.actual !== a.desired;
  body.innerHTML = `
    <header class="r1-settings-section-header">
      <h3>Auto-start</h3>
      <p class="r1-settings-section-hint">
        Start R1 Desktop automatically at login. The preference persists to
        prefs.json (tauri-plugin-store) and the OS-side hook is reconciled
        at every app start.
      </p>
    </header>
    <div class="r1-settings-form" data-role="autostart-form">
      <label class="r1-settings-field">
        <input
          type="checkbox"
          data-role="autostart-toggle"
          ${a.desired ? "checked" : ""}
          ${a.loaded && !a.busy ? "" : "disabled"}
        />
        <span>Start at login</span>
      </label>
      <p class="r1-settings-field" data-role="autostart-actual">
        OS-side login hook: ${escapeHtml(actualLabel)}${
          drift
            ? " — drifted from the saved preference; launch-time reconciliation re-syncs it"
            : ""
        }
      </p>
      <span
        class="r1-settings-save-status${a.error ? " is-fail" : ""}"
        data-role="autostart-status"
        aria-live="polite"
      >${a.error ? escapeHtml(a.error) : ""}</span>
    </div>
  `;
  const toggle = body.querySelector<HTMLInputElement>(
    '[data-role="autostart-toggle"]',
  );
  toggle?.addEventListener("change", () => {
    void applyAutostartToggle(toggle.checked);
  });
  if (!a.loaded && !a.busy) void loadAutostartState();
}

/** First-visit probe: desired (prefs.json) + actual (OS hook). */
async function loadAutostartState(): Promise<void> {
  state.autostart = { ...state.autostart, busy: true };
  try {
    const probe = await getAutostart();
    state.autostart = {
      desired: probe.desired,
      actual: probe.actual,
      loaded: true,
      unavailable: null,
      error: null,
      busy: false,
    };
  } catch (err) {
    state.autostart = {
      ...state.autostart,
      busy: false,
      loaded: false,
      unavailable: err instanceof Error ? err.message : String(err),
    };
  }
  if (state.active === "autostart") activateSection("autostart");
}

async function applyAutostartToggle(value: boolean): Promise<void> {
  state.autostart = { ...state.autostart, busy: true, error: null };
  if (state.active === "autostart") activateSection("autostart");
  let res: AutostartResult;
  try {
    res = await setAutostart(value);
  } catch (err) {
    state.autostart = {
      ...state.autostart,
      busy: false,
      unavailable: err instanceof Error ? err.message : String(err),
    };
    if (state.active === "autostart") activateSection("autostart");
    return;
  }
  state.autostart = {
    desired: res.state.desired,
    actual: res.state.actual,
    loaded: true,
    unavailable: null,
    error: res.ok ? null : (res.error ?? "Auto-start toggle failed."),
    busy: false,
  };
  if (state.active === "autostart") activateSection("autostart");
}

// ---------------------------------------------------------------------
// Lanes density (spec §8 + item 27)
// ---------------------------------------------------------------------

const LANE_DENSITY_OPTIONS: Array<[LaneDensity, string, string]> = [
  ["verbose", "Verbose", "Full delta detail per lane card"],
  ["normal", "Normal", "Status plus last-event preview"],
  ["summary", "Summary", "Status line only"],
];

function renderLanes(body: HTMLElement, s: SettingsState): void {
  const l = s.lanes;
  body.innerHTML = `
    <header class="r1-settings-section-header">
      <h3>Lanes</h3>
      <p class="r1-settings-section-hint">
        Lane-card detail level. Persisted to prefs.json
        (tauri-plugin-store); open lane rails re-render immediately.
      </p>
    </header>
    <fieldset class="r1-settings-form" data-role="lanes-density">
      <legend class="r1-settings-field-label">Density</legend>
      ${LANE_DENSITY_OPTIONS.map(
        ([id, label, hint]) => `
        <label class="r1-settings-field">
          <input
            type="radio"
            name="r1-lane-density"
            value="${id}"
            data-role="lanes-density-input"
            ${l.density === id ? "checked" : ""}
            ${l.busy ? "disabled" : ""}
          />
          <span>${label}</span>
          <small>${hint}</small>
        </label>`,
      ).join("")}
    </fieldset>
    <span
      class="r1-settings-save-status${l.error ? " is-fail" : ""}"
      data-role="lanes-status"
      aria-live="polite"
    >${l.error ? escapeHtml(l.error) : ""}</span>
  `;
  body
    .querySelectorAll<HTMLInputElement>('[data-role="lanes-density-input"]')
    .forEach((input) => {
      input.addEventListener("change", () => {
        if (input.checked && isLaneDensity(input.value)) {
          void applyLaneDensity(input.value);
        }
      });
    });
  if (!l.loaded && !l.busy) void loadLaneDensityState();
}

/** First-visit read of the persisted density (null = keep default). */
async function loadLaneDensityState(): Promise<void> {
  state.lanes = { ...state.lanes, busy: true };
  const stored = await getLaneDensity();
  state.lanes = {
    density: stored ?? state.lanes.density,
    loaded: true,
    busy: false,
    error: null,
  };
  if (state.active === "lanes") activateSection("lanes");
}

async function applyLaneDensity(density: LaneDensity): Promise<void> {
  const previous = state.lanes.density;
  state.lanes = { ...state.lanes, busy: true, error: null };
  const ok = await setLaneDensity(density);
  state.lanes = {
    density: ok ? density : previous,
    loaded: true,
    busy: false,
    error: ok
      ? null
      : "Couldn't persist lane density — the host preferences store is unavailable in this environment.",
  };
  if (state.active === "lanes") activateSection("lanes");
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
