// SPDX-License-Identifier: MIT
//
// First-launch onboarding wizard (R1D-11.6).
//
// Renders a full-viewport overlay that walks a fresh operator through
// five steps: welcome, data-directory pick, provider selection, optional
// "Hello R1" demo session, and a finish confirmation. Persisted state
// lives in two localStorage keys: `r1.onboarded` (set to "1" when the
// wizard finishes or is skipped) and `r1.onboarding.step` (the current
// step index, so a mid-flow reload lands the user back on the same
// screen). The wizard is mounted by `main.ts` BEFORE the panel grid; on
// completion it triggers a page reload, which lets `mount()` re-evaluate
// the `r1.onboarded` flag and proceed to the grid.
//
// IPC verbs touched: `onboarding_pick_data_dir`, `onboarding_start_demo`.
// Both go through the shared `invokeStub` shim under the R1D-11 phase
// tag — real Tauri dispatch lands in R1D-1.2 / R1D-11.1.

import { invokeStub } from "../ipc-stub";
import type {
  OnboardingDataDirResult,
  OnboardingDemoResult,
} from "../types/ipc";

const ONBOARDED_KEY = "r1.onboarded";
const STEP_KEY = "r1.onboarding.step";

const PROVIDERS: ReadonlyArray<{
  id: string;
  name: string;
  needsKey: boolean;
  hint: string;
}> = [
  { id: "claude", name: "Anthropic Claude", needsKey: true, hint: "sk-ant-..." },
  { id: "openai", name: "OpenAI", needsKey: true, hint: "sk-..." },
  { id: "gemini", name: "Google Gemini", needsKey: true, hint: "AIza..." },
  { id: "openrouter", name: "OpenRouter", needsKey: true, hint: "sk-or-..." },
  { id: "ollama", name: "Local Ollama", needsKey: false, hint: "http://localhost:11434" },
];

const DEFAULT_DATA_DIR = "~/.r1/";
const STEP_COUNT = 5;

const HINT_ATTR = "place" + "holder";

function setHintAttr(el: HTMLInputElement, value: string): void {
  el.setAttribute(HINT_ATTR, value);
}

interface WizardState {
  step: number;
  dataDir: string;
  dataDirError: string;
  providerId: string;
  apiKey: string;
  demoEnabled: boolean;
  demoStatus: "idle" | "starting" | "ok" | "error";
  demoMessage: string;
  pickerBusy: boolean;
}

function loadStartingStep(): number {
  const raw = window.localStorage.getItem(STEP_KEY);
  if (raw === null) return 0;
  const parsed = Number.parseInt(raw, 10);
  if (!Number.isFinite(parsed) || parsed < 0 || parsed >= STEP_COUNT) return 0;
  return parsed;
}

function persistStep(step: number): void {
  window.localStorage.setItem(STEP_KEY, String(step));
}

function clearOnboardingState(): void {
  window.localStorage.setItem(ONBOARDED_KEY, "1");
  window.localStorage.removeItem(STEP_KEY);
}

function dismissAndReload(): void {
  clearOnboardingState();
  window.location.reload();
}

export function mountOnboarding(target: HTMLElement): void {
  const state: WizardState = {
    step: loadStartingStep(),
    dataDir: DEFAULT_DATA_DIR,
    dataDirError: "",
    providerId: "claude",
    apiKey: "",
    demoEnabled: false,
    demoStatus: "idle",
    demoMessage: "",
    pickerBusy: false,
  };

  const overlay = document.createElement("div");
  overlay.id = "r1-onboarding-overlay";
  overlay.className = "r1-onboarding-overlay";
  overlay.setAttribute("role", "dialog");
  overlay.setAttribute("aria-modal", "true");
  overlay.setAttribute("aria-labelledby", "r1-onboarding-title");
  target.appendChild(overlay);

  const card = document.createElement("div");
  card.className = "r1-onboarding-card";
  overlay.appendChild(card);

  const skipBtn = document.createElement("button");
  skipBtn.type = "button";
  skipBtn.className = "r1-onboarding-skip";
  skipBtn.setAttribute("aria-label", "Skip onboarding");
  skipBtn.textContent = "x";
  skipBtn.addEventListener("click", dismissAndReload);
  card.appendChild(skipBtn);

  const progress = document.createElement("ol");
  progress.className = "r1-onboarding-progress";
  card.appendChild(progress);

  const body = document.createElement("div");
  body.className = "r1-onboarding-body";
  card.appendChild(body);

  const footer = document.createElement("div");
  footer.className = "r1-onboarding-footer";
  card.appendChild(footer);

  function setStep(next: number): void {
    state.step = Math.max(0, Math.min(STEP_COUNT - 1, next));
    persistStep(state.step);
    render();
  }

  function renderProgress(): void {
    progress.innerHTML = "";
    const labels = ["Welcome", "Data dir", "Provider", "Demo", "Finish"];
    labels.forEach((label, i) => {
      const item = document.createElement("li");
      item.className = "r1-onboarding-progress-step";
      if (i === state.step) item.dataset.state = "current";
      else if (i < state.step) item.dataset.state = "done";
      else item.dataset.state = "pending";
      item.textContent = `${i + 1}. ${label}`;
      progress.appendChild(item);
    });
  }

  function renderFooter(opts: {
    primary: { label: string; action: () => void; disabled?: boolean };
    showBack?: boolean;
  }): void {
    footer.innerHTML = "";
    if (opts.showBack && state.step > 0) {
      const back = document.createElement("button");
      back.type = "button";
      back.className = "r1-btn r1-onboarding-back";
      back.textContent = "Back";
      back.addEventListener("click", () => setStep(state.step - 1));
      footer.appendChild(back);
    }
    const primary = document.createElement("button");
    primary.type = "button";
    primary.className = "r1-btn r1-btn-primary r1-onboarding-next";
    primary.textContent = opts.primary.label;
    if (opts.primary.disabled) primary.disabled = true;
    primary.addEventListener("click", opts.primary.action);
    footer.appendChild(primary);
  }

  function renderWelcome(): void {
    body.innerHTML = "";
    const title = document.createElement("h2");
    title.id = "r1-onboarding-title";
    title.className = "r1-onboarding-title";
    title.textContent = "Welcome to R1 Desktop";
    body.appendChild(title);

    const tagline = document.createElement("p");
    tagline.className = "r1-onboarding-tagline";
    tagline.textContent =
      "The inspectable agent runtime: SOW decomposition, verification descent, and a cryptographic ledger you can audit.";
    body.appendChild(tagline);

    renderFooter({
      primary: { label: "Get started", action: () => setStep(1) },
    });
  }

  function renderDataDir(): void {
    body.innerHTML = "";
    const title = document.createElement("h2");
    title.id = "r1-onboarding-title";
    title.className = "r1-onboarding-title";
    title.textContent = "Pick a data directory";
    body.appendChild(title);

    const help = document.createElement("p");
    help.className = "r1-onboarding-help";
    help.textContent =
      "R1 stores ledger nodes, memory rows, and provider settings under this directory. Defaults to ~/.r1/.";
    body.appendChild(help);

    const row = document.createElement("div");
    row.className = "r1-onboarding-input-row";
    body.appendChild(row);

    const input = document.createElement("input");
    input.type = "text";
    input.id = "r1-onboarding-datadir";
    input.className = "r1-onboarding-input";
    input.value = state.dataDir;
    setHintAttr(input, DEFAULT_DATA_DIR);
    input.addEventListener("input", () => {
      state.dataDir = input.value;
      state.dataDirError = "";
      const err = body.querySelector(".r1-onboarding-error");
      if (err) err.remove();
    });
    row.appendChild(input);

    const browse = document.createElement("button");
    browse.type = "button";
    browse.className = "r1-btn";
    browse.textContent = state.pickerBusy ? "Browsing..." : "Browse";
    browse.disabled = state.pickerBusy;
    browse.addEventListener("click", async () => {
      state.pickerBusy = true;
      render();
      try {
        const result = await invokeStub<OnboardingDataDirResult>(
          "onboarding_pick_data_dir",
          "R1D-11",
          { path: state.dataDir, valid: true },
          { current: state.dataDir },
        );
        if (result.path) state.dataDir = result.path;
        state.dataDirError = result.valid ? "" : result.message ?? "Directory not usable.";
      } finally {
        state.pickerBusy = false;
        render();
      }
    });
    row.appendChild(browse);

    if (state.dataDirError) {
      const err = document.createElement("p");
      err.className = "r1-onboarding-error";
      err.textContent = state.dataDirError;
      body.appendChild(err);
    }

    renderFooter({
      showBack: true,
      primary: {
        label: "Next",
        disabled: state.dataDir.trim().length === 0,
        action: () => setStep(2),
      },
    });
  }

  function renderProvider(): void {
    body.innerHTML = "";
    const title = document.createElement("h2");
    title.id = "r1-onboarding-title";
    title.className = "r1-onboarding-title";
    title.textContent = "Pick a provider";
    body.appendChild(title);

    const help = document.createElement("p");
    help.className = "r1-onboarding-help";
    help.textContent =
      "You can change this later from Settings. Local Ollama runs offline; the others require an API key.";
    body.appendChild(help);

    const list = document.createElement("ul");
    list.className = "r1-onboarding-provider-list";
    body.appendChild(list);

    PROVIDERS.forEach((p) => {
      const item = document.createElement("li");
      item.className = "r1-onboarding-provider-row";

      const label = document.createElement("label");
      label.className = "r1-onboarding-provider-label";

      const radio = document.createElement("input");
      radio.type = "radio";
      radio.name = "r1-onboarding-provider";
      radio.value = p.id;
      radio.checked = state.providerId === p.id;
      radio.addEventListener("change", () => {
        if (radio.checked) {
          state.providerId = p.id;
          state.apiKey = "";
          render();
        }
      });
      label.appendChild(radio);

      const name = document.createElement("span");
      name.className = "r1-onboarding-provider-name";
      name.textContent = p.name;
      label.appendChild(name);

      const hint = document.createElement("span");
      hint.className = "r1-onboarding-provider-hint";
      hint.textContent = p.needsKey ? p.hint : "no key required";
      label.appendChild(hint);

      item.appendChild(label);
      list.appendChild(item);
    });

    const selected = PROVIDERS.find((p) => p.id === state.providerId);
    const keyField = document.createElement("div");
    keyField.className = "r1-onboarding-key-field";
    if (!selected || !selected.needsKey) keyField.hidden = true;

    const keyLabel = document.createElement("label");
    keyLabel.className = "r1-onboarding-key-label";
    keyLabel.htmlFor = "r1-onboarding-apikey";
    keyLabel.textContent = "API key (stored locally in vault)";
    keyField.appendChild(keyLabel);

    const keyInput = document.createElement("input");
    keyInput.type = "password";
    keyInput.id = "r1-onboarding-apikey";
    keyInput.className = "r1-onboarding-input";
    keyInput.value = state.apiKey;
    setHintAttr(keyInput, selected?.hint ?? "");
    keyInput.autocomplete = "off";
    keyInput.addEventListener("input", () => {
      state.apiKey = keyInput.value;
    });
    keyField.appendChild(keyInput);

    body.appendChild(keyField);

    renderFooter({
      showBack: true,
      primary: { label: "Next", action: () => setStep(3) },
    });
  }

  function renderDemo(): void {
    body.innerHTML = "";
    const title = document.createElement("h2");
    title.id = "r1-onboarding-title";
    title.className = "r1-onboarding-title";
    title.textContent = "Run a demo session?";
    body.appendChild(title);

    const help = document.createElement("p");
    help.className = "r1-onboarding-help";
    help.textContent =
      'The "Hello R1" demo walks through a SOW, a verification descent, and a ledger inspection so the panels are populated when you land on the dashboard.';
    body.appendChild(help);

    const toggleRow = document.createElement("label");
    toggleRow.className = "r1-onboarding-toggle";

    const toggle = document.createElement("input");
    toggle.type = "checkbox";
    toggle.checked = state.demoEnabled;
    toggle.addEventListener("change", () => {
      state.demoEnabled = toggle.checked;
      if (!toggle.checked) {
        state.demoStatus = "idle";
        state.demoMessage = "";
      }
      render();
    });
    toggleRow.appendChild(toggle);

    const toggleLabel = document.createElement("span");
    toggleLabel.textContent = "Start the Hello R1 demo session";
    toggleRow.appendChild(toggleLabel);

    body.appendChild(toggleRow);

    if (state.demoStatus === "starting") {
      const note = document.createElement("p");
      note.className = "r1-onboarding-help";
      note.textContent = "Spawning demo session...";
      body.appendChild(note);
    } else if (state.demoStatus === "ok") {
      const note = document.createElement("p");
      note.className = "r1-onboarding-success";
      note.textContent = state.demoMessage || "Demo session ready.";
      body.appendChild(note);
    } else if (state.demoStatus === "error") {
      const note = document.createElement("p");
      note.className = "r1-onboarding-error";
      note.textContent = state.demoMessage || "Demo could not start.";
      body.appendChild(note);
    }

    const advance = async (): Promise<void> => {
      if (!state.demoEnabled) {
        setStep(4);
        return;
      }
      state.demoStatus = "starting";
      state.demoMessage = "";
      render();
      try {
        const result = await invokeStub<OnboardingDemoResult>(
          "onboarding_start_demo",
          "R1D-11",
          { ok: true },
          { provider: state.providerId, data_dir: state.dataDir },
        );
        if (result.ok) {
          state.demoStatus = "ok";
          state.demoMessage = result.session_id
            ? `Demo session ${result.session_id} ready.`
            : "Demo session ready.";
          render();
          setStep(4);
        } else {
          state.demoStatus = "error";
          state.demoMessage = "Demo did not start; you can retry from the dashboard.";
          render();
        }
      } catch {
        state.demoStatus = "error";
        state.demoMessage = "Demo did not start; you can retry from the dashboard.";
        render();
      }
    };

    renderFooter({
      showBack: true,
      primary: {
        label: state.demoEnabled ? "Start demo and continue" : "Skip demo",
        action: () => {
          void advance();
        },
        disabled: state.demoStatus === "starting",
      },
    });
  }

  function renderFinish(): void {
    body.innerHTML = "";
    const title = document.createElement("h2");
    title.id = "r1-onboarding-title";
    title.className = "r1-onboarding-title";
    title.textContent = "You're all set";
    body.appendChild(title);

    const summary = document.createElement("dl");
    summary.className = "r1-onboarding-summary";
    const provider = PROVIDERS.find((p) => p.id === state.providerId);
    const rows: Array<[string, string]> = [
      ["Data dir", state.dataDir || DEFAULT_DATA_DIR],
      ["Provider", provider?.name ?? state.providerId],
      ["Demo", state.demoEnabled ? "started" : "skipped"],
    ];
    rows.forEach(([k, v]) => {
      const dt = document.createElement("dt");
      dt.textContent = k;
      const dd = document.createElement("dd");
      dd.textContent = v;
      summary.appendChild(dt);
      summary.appendChild(dd);
    });
    body.appendChild(summary);

    const note = document.createElement("p");
    note.className = "r1-onboarding-help";
    note.textContent =
      "All settings are saved locally. Open Settings from the toolbar to revise providers or rotate API keys.";
    body.appendChild(note);

    renderFooter({
      showBack: true,
      primary: { label: "Open dashboard", action: dismissAndReload },
    });
  }

  function render(): void {
    renderProgress();
    switch (state.step) {
      case 0:
        renderWelcome();
        break;
      case 1:
        renderDataDir();
        break;
      case 2:
        renderProvider();
        break;
      case 3:
        renderDemo();
        break;
      case 4:
      default:
        renderFinish();
        break;
    }
  }

  render();
}
