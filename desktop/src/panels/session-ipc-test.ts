// SPDX-License-Identifier: MIT
//
// Session IPC-test surface (R1D-1.5).
//
// A single-page acceptance surface for the R1D-1 phase: verify that the
// Tauri host can spawn an r1 subprocess, stream its NDJSON output into
// the WebView, and respond to a cancel command. All IPC goes through
// `invokeStub` until R1D-1.2 wires the real Tauri `invoke` + event bus.
//
// Sub-deliverables covered:
//   R1D-1.5 — Prompt input + reply display pane + session-start button.
//
// AC (from work-r1-desktop-app.md R1D-1):
//   Typing a prompt in the IPC-test UI spawns an r1 subprocess; the
//   subprocess's stdout event stream parses without error; the reply
//   display pane renders the streamed response in under 500 ms after each
//   event arrives; cancel button SIGTERMs the subprocess cleanly.

import { invokeStub } from "../ipc-stub";
import type {
  SessionStartParams,
  SessionStartResult,
  SessionIdParams,
} from "../types/ipc";

// -------------------------------------------------------------------
// State
// -------------------------------------------------------------------

interface PanelState {
  sessionId: string | null;
  running: boolean;
  eventLog: string[];
}

// -------------------------------------------------------------------
// Public entry-point
// -------------------------------------------------------------------

export function renderPanel(root: HTMLElement): void {
  const state: PanelState = {
    sessionId: null,
    running: false,
    eventLog: [],
  };

  root.classList.add("r1-panel", "r1-panel-ipc-test");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>IPC Test</h2>
      <span class="r1-panel-subtitle">R1D-1.5 — Tauri&thinsp;↔&thinsp;r1 subprocess validation</span>
    </header>
    <div class="r1-ipc-test-body">
      <div class="r1-ipc-session-bar">
        <span class="r1-ipc-session-label">Session:</span>
        <code class="r1-ipc-session-id" data-role="session-id">—</code>
        <span
          class="r1-status-pill r1-status-idle"
          data-role="status-pill"
          aria-live="polite"
        >idle</span>
      </div>
      <div class="r1-ipc-composer">
        <label class="r1-ipc-composer-label" for="r1-ipc-prompt">Prompt</label>
        <textarea
          id="r1-ipc-prompt"
          class="r1-ipc-prompt"
          data-role="prompt"
          rows="3"
          aria-label="Prompt to send to r1 subprocess"
        ></textarea>
        <span class="r1-ipc-prompt-hint" aria-hidden="true">Type a prompt and press Start (Ctrl+Enter)</span>
        <div class="r1-ipc-actions">
          <button
            type="button"
            class="r1-btn r1-btn-primary"
            data-role="start"
            aria-label="Start session"
          >Start</button>
          <button
            type="button"
            class="r1-btn"
            data-role="cancel"
            aria-label="Cancel running session"
            disabled
          >Cancel</button>
          <button
            type="button"
            class="r1-btn"
            data-role="clear"
            aria-label="Clear reply log"
          >Clear</button>
        </div>
      </div>
      <div class="r1-ipc-reply-pane" data-role="reply-pane" aria-live="polite">
        <p class="r1-empty">No events yet. Start a session above.</p>
      </div>
    </div>
  `;

  wirePanelEvents(root, state);
}

// -------------------------------------------------------------------
// Event wiring
// -------------------------------------------------------------------

function wirePanelEvents(root: HTMLElement, state: PanelState): void {
  const startBtn = root.querySelector<HTMLButtonElement>('[data-role="start"]');
  const cancelBtn = root.querySelector<HTMLButtonElement>('[data-role="cancel"]');
  const clearBtn = root.querySelector<HTMLButtonElement>('[data-role="clear"]');
  const promptEl = root.querySelector<HTMLTextAreaElement>('[data-role="prompt"]');

  startBtn?.addEventListener("click", () => {
    void handleStart(root, state);
  });

  cancelBtn?.addEventListener("click", () => {
    void handleCancel(root, state);
  });

  clearBtn?.addEventListener("click", () => {
    state.eventLog = [];
    renderReplyPane(root, state);
  });

  // Ctrl+Enter or Cmd+Enter submits the form.
  promptEl?.addEventListener("keydown", (event) => {
    if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
      event.preventDefault();
      if (!state.running) {
        void handleStart(root, state);
      }
    }
  });

  // Keyboard shortcut: Escape cancels.
  root.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && state.running) {
      event.preventDefault();
      void handleCancel(root, state);
    }
  });
}

// -------------------------------------------------------------------
// Handlers
// -------------------------------------------------------------------

async function handleStart(root: HTMLElement, state: PanelState): Promise<void> {
  if (state.running) return;

  const promptEl = root.querySelector<HTMLTextAreaElement>('[data-role="prompt"]');
  const prompt = promptEl?.value.trim() ?? "";

  if (!prompt) {
    promptEl?.focus();
    return;
  }

  setRunning(root, state, true);
  appendEvent(root, state, { kind: "info", text: `Starting session with prompt: "${prompt}"` });

  const params: SessionStartParams = { prompt };
  const result = await invokeStub<SessionStartResult>(
    "session_start",
    "R1D-1",
    { session_id: `ipc-test-${Date.now()}`, started_at: new Date().toISOString() },
    params as unknown as Record<string, unknown>,
  );

  state.sessionId = result.session_id;
  updateSessionId(root, state);
  appendEvent(root, state, {
    kind: "started",
    text: `session.started — id=${result.session_id} at=${result.started_at}`,
  });

  // Simulate a streamed-reply sequence so the pane exercises its
  // rendering before real Tauri events arrive (R1D-1.2 wires these).
  simulateStreamedEvents(root, state);
}

async function handleCancel(root: HTMLElement, state: PanelState): Promise<void> {
  if (!state.running || !state.sessionId) return;

  const params: SessionIdParams = { session_id: state.sessionId };
  await invokeStub<Record<string, never>>(
    "session_cancel",
    "R1D-1",
    {},
    params as unknown as Record<string, unknown>,
  );

  appendEvent(root, state, {
    kind: "cancelled",
    text: `session.cancel sent — id=${state.sessionId}`,
  });
  setRunning(root, state, false);
}

// -------------------------------------------------------------------
// Simulated streaming (exercises the reply-pane rendering path)
// -------------------------------------------------------------------

function simulateStreamedEvents(root: HTMLElement, state: PanelState): void {
  const deltas = [
    '{"event":"session.delta","payload":{"type":"text","text":"Hello"}}',
    '{"event":"session.delta","payload":{"type":"text","text":" from R1"}}',
    '{"event":"session.delta","payload":{"type":"tool_use","name":"read_file","input":{"path":"README.md"}}}',
    '{"event":"session.delta","payload":{"type":"tool_result","content":"File contents..."}}',
    '{"event":"session.ended","reason":"ok"}',
  ];
  let idx = 0;
  const interval = setInterval(() => {
    if (!state.running || idx >= deltas.length) {
      clearInterval(interval);
      if (state.running) {
        setRunning(root, state, false);
      }
      return;
    }
    appendEvent(root, state, { kind: "delta", text: deltas[idx] });
    idx++;
  }, 120);
}

// -------------------------------------------------------------------
// UI helpers
// -------------------------------------------------------------------

interface EventEntry {
  kind: "info" | "started" | "delta" | "cancelled" | "error";
  text: string;
}

function appendEvent(root: HTMLElement, state: PanelState, entry: EventEntry): void {
  const ts = new Date().toISOString().slice(11, 23);
  state.eventLog.push(`[${ts}] [${entry.kind}] ${entry.text}`);
  renderReplyPane(root, state);
}

function renderReplyPane(root: HTMLElement, state: PanelState): void {
  const pane = root.querySelector<HTMLDivElement>('[data-role="reply-pane"]');
  if (!pane) return;

  if (state.eventLog.length === 0) {
    pane.innerHTML = `<p class="r1-empty">No events yet. Start a session above.</p>`;
    return;
  }

  pane.innerHTML = `
    <ol class="r1-ipc-event-log" aria-label="IPC event log">
      ${state.eventLog
        .map(
          (line) =>
            `<li class="r1-ipc-event-row"><code>${escapeHtml(line)}</code></li>`,
        )
        .join("")}
    </ol>
  `;
  // Auto-scroll to last entry.
  pane.scrollTop = pane.scrollHeight;
}

function updateSessionId(root: HTMLElement, state: PanelState): void {
  const el = root.querySelector<HTMLElement>('[data-role="session-id"]');
  if (el) el.textContent = state.sessionId ?? "—";
}

function setRunning(root: HTMLElement, state: PanelState, running: boolean): void {
  state.running = running;

  const startBtn = root.querySelector<HTMLButtonElement>('[data-role="start"]');
  const cancelBtn = root.querySelector<HTMLButtonElement>('[data-role="cancel"]');
  const promptEl = root.querySelector<HTMLTextAreaElement>('[data-role="prompt"]');
  const pill = root.querySelector<HTMLSpanElement>('[data-role="status-pill"]');

  if (startBtn) startBtn.disabled = running;
  if (cancelBtn) cancelBtn.disabled = !running;
  if (promptEl) promptEl.disabled = running;

  if (pill) {
    pill.className = `r1-status-pill r1-status-${running ? "running" : "idle"}`;
    pill.textContent = running ? "running" : "idle";
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
