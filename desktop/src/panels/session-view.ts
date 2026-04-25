// SPDX-License-Identifier: MIT
//
// Session-view panel (R1D-2.1 / R1D-2.2 / R1D-2.3 / R1D-2.4 / R1D-2.5).
//
// The primary interaction surface for R1 Desktop. Composes:
//   R1D-2.1 — Chat transcript with assistant turns, user turns, system msgs.
//   R1D-2.2 — Tool-use rendering: collapsible blocks per tool call.
//   R1D-2.3 — Markdown rendering with syntax-highlighted code blocks.
//   R1D-2.4 — Multi-session sidebar: create, switch, close sessions.
//   R1D-2.5 — Cancel, pause, resume controls with keyboard shortcuts.
//
// All IPC routes through invokeStub until R1D-1.2 wires the real Tauri
// invoke + event bus. The streamed-delta path exercises appendDelta()
// in the same code path that real session.delta events will use.
//
// AC (work-r1-desktop-app.md R1D-2):
//   End-to-end: create a session, send a prompt, receive a streamed reply
//   with tool calls, cancel mid-run. 2 concurrent sessions switch cleanly.

import { invokeStub } from "../ipc-stub";
import type {
  SessionStartParams,
  SessionStartResult,
  SessionPauseResult,
  SessionResumeResult,
  SessionIdParams,
  SessionSendParams,
  SessionSummary,
} from "../types/ipc";

// -------------------------------------------------------------------
// Types
// -------------------------------------------------------------------

type TurnRole = "user" | "assistant" | "system";
type TurnStatus = "streaming" | "done" | "cancelled";

interface ToolBlock {
  name: string;
  input: Record<string, unknown>;
  output?: string;
  expanded: boolean;
}

interface Turn {
  id: string;
  role: TurnRole;
  chunks: string[];
  tools: ToolBlock[];
  status: TurnStatus;
}

interface SessionView {
  sessionId: string;
  title: string;
  status: SessionSummary["status"];
  turns: Turn[];
  activeTurnId: string | null;
}

interface PanelState {
  sessions: Map<string, SessionView>;
  activeId: string | null;
  nextTurnCounter: number;
}

// -------------------------------------------------------------------
// Public entry-point
// -------------------------------------------------------------------

export function renderPanel(root: HTMLElement): void {
  const state: PanelState = {
    sessions: new Map(),
    activeId: null,
    nextTurnCounter: 0,
  };

  root.classList.add("r1-panel", "r1-panel-session-view");
  root.innerHTML = `
    <div class="r1-sv-layout">
      <nav
        class="r1-sv-sidebar"
        aria-label="Session list"
        data-role="session-sidebar"
      >
        <header class="r1-sv-sidebar-header">
          <span class="r1-sv-sidebar-title">Sessions</span>
          <button
            type="button"
            class="r1-btn r1-btn-primary r1-sv-new-btn"
            data-role="new-session"
            aria-label="New session (Ctrl+N)"
            title="New session"
          >+</button>
        </header>
        <ul
          class="r1-sv-session-list"
          data-role="session-list"
          role="listbox"
          aria-label="Active sessions"
        >
          <li class="r1-empty r1-sv-no-sessions">No sessions yet.</li>
        </ul>
      </nav>
      <div class="r1-sv-main" data-role="session-main">
        <div class="r1-sv-empty-state" data-role="empty-state">
          <p>Select a session or start a new one.</p>
        </div>
        <div class="r1-sv-chat-pane" data-role="chat-pane" hidden>
          <div class="r1-sv-chat-header" data-role="chat-header">
            <span class="r1-sv-chat-title" data-role="chat-title"></span>
            <span
              class="r1-status-pill"
              data-role="chat-status-pill"
              aria-live="polite"
            ></span>
            <div class="r1-sv-chat-controls">
              <button
                type="button"
                class="r1-btn"
                data-role="pause-btn"
                aria-label="Pause session (Ctrl+P)"
                title="Pause"
                disabled
              >Pause</button>
              <button
                type="button"
                class="r1-btn"
                data-role="resume-btn"
                aria-label="Resume session"
                title="Resume"
                disabled
              >Resume</button>
              <button
                type="button"
                class="r1-btn r1-btn-danger"
                data-role="cancel-btn"
                aria-label="Cancel session (Esc)"
                title="Cancel"
                disabled
              >Cancel</button>
            </div>
          </div>
          <ol
            class="r1-sv-transcript"
            data-role="transcript"
            aria-label="Chat transcript"
            aria-live="polite"
          ></ol>
          <form class="r1-sv-composer" data-role="composer" autocomplete="off">
            <textarea
              class="r1-sv-composer-input"
              data-role="composer-input"
              rows="3"
              aria-label="Message to send (Ctrl+Enter to send)"
            ></textarea>
            <div class="r1-sv-composer-bar">
              <span class="r1-sv-composer-hint" aria-hidden="true">Ctrl+Enter to send</span>
              <button
                type="submit"
                class="r1-btn r1-btn-primary"
                data-role="send-btn"
              >Send</button>
            </div>
          </form>
        </div>
      </div>
    </div>
  `;

  wireKeyboardShortcuts(root, state);
  wireSidebarControls(root, state);
  wireChatControls(root, state);
  wireComposer(root, state);
}

// -------------------------------------------------------------------
// Keyboard shortcuts
// -------------------------------------------------------------------

function wireKeyboardShortcuts(root: HTMLElement, state: PanelState): void {
  root.addEventListener("keydown", (event) => {
    // Ctrl+N — new session.
    if ((event.ctrlKey || event.metaKey) && event.key === "n") {
      event.preventDefault();
      void createSession(root, state);
      return;
    }
    // Ctrl+P — pause.
    if ((event.ctrlKey || event.metaKey) && event.key === "p") {
      event.preventDefault();
      void handlePause(root, state);
      return;
    }
    // Escape — cancel running session.
    if (event.key === "Escape") {
      const view = activeView(state);
      if (view?.status === "running") {
        event.preventDefault();
        void handleCancel(root, state);
      }
    }
  });
}

// -------------------------------------------------------------------
// Sidebar wiring
// -------------------------------------------------------------------

function wireSidebarControls(root: HTMLElement, state: PanelState): void {
  const newBtn = root.querySelector<HTMLButtonElement>('[data-role="new-session"]');
  newBtn?.addEventListener("click", () => {
    void createSession(root, state);
  });
}

// -------------------------------------------------------------------
// Chat-header controls
// -------------------------------------------------------------------

function wireChatControls(root: HTMLElement, state: PanelState): void {
  root.querySelector<HTMLButtonElement>('[data-role="pause-btn"]')?.addEventListener("click", () => {
    void handlePause(root, state);
  });
  root.querySelector<HTMLButtonElement>('[data-role="resume-btn"]')?.addEventListener("click", () => {
    void handleResume(root, state);
  });
  root.querySelector<HTMLButtonElement>('[data-role="cancel-btn"]')?.addEventListener("click", () => {
    void handleCancel(root, state);
  });
}

// -------------------------------------------------------------------
// Composer wiring
// -------------------------------------------------------------------

function wireComposer(root: HTMLElement, state: PanelState): void {
  const form = root.querySelector<HTMLFormElement>('[data-role="composer"]');
  const input = root.querySelector<HTMLTextAreaElement>('[data-role="composer-input"]');

  form?.addEventListener("submit", (event) => {
    event.preventDefault();
    void handleSend(root, state);
  });

  input?.addEventListener("keydown", (event) => {
    if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
      event.preventDefault();
      void handleSend(root, state);
    }
  });
}

// -------------------------------------------------------------------
// Session lifecycle
// -------------------------------------------------------------------

async function createSession(root: HTMLElement, state: PanelState): Promise<void> {
  const params: SessionStartParams = {
    prompt: "",
    provider: undefined,
    budget_usd: undefined,
  };
  const result = await invokeStub<SessionStartResult>(
    "session_start",
    "R1D-2",
    {
      session_id: `sv-${Date.now()}`,
      started_at: new Date().toISOString(),
    },
    params as unknown as Record<string, unknown>,
  );

  const view: SessionView = {
    sessionId: result.session_id,
    title: `Session ${state.sessions.size + 1}`,
    status: "running",
    turns: [],
    activeTurnId: null,
  };
  state.sessions.set(result.session_id, view);
  setActive(root, state, result.session_id);
  refreshSidebar(root, state);
  refreshChatPane(root, state);
}

async function handleSend(root: HTMLElement, state: PanelState): Promise<void> {
  const view = activeView(state);
  if (!view) return;

  const input = root.querySelector<HTMLTextAreaElement>('[data-role="composer-input"]');
  const text = input?.value.trim() ?? "";
  if (!text) return;

  if (input) input.value = "";

  appendUserTurn(root, state, view, text);

  const sendParams: SessionSendParams = {
    session_id: view.sessionId,
    prompt: text,
  };
  await invokeStub<Record<string, never>>(
    "session_send",
    "R1D-2",
    {},
    sendParams as unknown as Record<string, unknown>,
  );

  view.status = "running";
  refreshStatusPill(root, state);
  refreshControlButtons(root, state);
  simulateAssistantReply(root, state, view);
}

async function handlePause(root: HTMLElement, state: PanelState): Promise<void> {
  const view = activeView(state);
  if (!view || view.status !== "running") return;

  const idParams: SessionIdParams = { session_id: view.sessionId };
  await invokeStub<SessionPauseResult>(
    "session_pause",
    "R1D-2",
    { paused_at: new Date().toISOString() },
    idParams as unknown as Record<string, unknown>,
  );

  view.status = "paused";
  refreshStatusPill(root, state);
  refreshControlButtons(root, state);
  appendSystemMessage(root, state, view, "Session paused.");
}

async function handleResume(root: HTMLElement, state: PanelState): Promise<void> {
  const view = activeView(state);
  if (!view || view.status !== "paused") return;

  const idParams: SessionIdParams = { session_id: view.sessionId };
  await invokeStub<SessionResumeResult>(
    "session_resume",
    "R1D-2",
    { resumed_at: new Date().toISOString() },
    idParams as unknown as Record<string, unknown>,
  );

  view.status = "running";
  refreshStatusPill(root, state);
  refreshControlButtons(root, state);
  appendSystemMessage(root, state, view, "Session resumed.");
}

async function handleCancel(root: HTMLElement, state: PanelState): Promise<void> {
  const view = activeView(state);
  if (!view || view.status === "ended") return;

  const idParams: SessionIdParams = { session_id: view.sessionId };
  await invokeStub<Record<string, never>>(
    "session_cancel",
    "R1D-2",
    {},
    idParams as unknown as Record<string, unknown>,
  );

  // Mark any streaming turn as cancelled.
  const streaming = view.turns.find((t) => t.status === "streaming");
  if (streaming) {
    streaming.status = "cancelled";
    refreshTurnElement(root, streaming);
  }

  view.status = "ended";
  refreshStatusPill(root, state);
  refreshControlButtons(root, state);
  appendSystemMessage(root, state, view, "Session cancelled.");
}

// -------------------------------------------------------------------
// Simulated streaming reply (exercises delta path before real Tauri)
// -------------------------------------------------------------------

function simulateAssistantReply(
  root: HTMLElement,
  state: PanelState,
  view: SessionView,
): void {
  const turnId = `turn-${++state.nextTurnCounter}`;
  const turn: Turn = {
    id: turnId,
    role: "assistant",
    chunks: [],
    tools: [],
    status: "streaming",
  };
  view.turns.push(turn);
  view.activeTurnId = turnId;
  appendTurnElement(root, turn);

  const deltas = [
    { type: "text", text: "I received your message. " },
    { type: "text", text: "Let me look at the codebase." },
    {
      type: "tool_use",
      name: "read_file",
      input: { path: "README.md" },
    },
    { type: "tool_result", content: "# R1 Agent..." },
    { type: "text", text: " Here is a summary based on the README." },
  ];

  let idx = 0;
  const interval = setInterval(() => {
    if (view.status === "ended" || view.status === "paused") {
      clearInterval(interval);
      return;
    }
    if (idx >= deltas.length) {
      clearInterval(interval);
      turn.status = "done";
      view.activeTurnId = null;
      view.status = "paused"; // Session idle after reply.
      refreshTurnElement(root, turn);
      refreshStatusPill(root, state);
      refreshControlButtons(root, state);
      return;
    }
    const delta = deltas[idx];
    if (delta.type === "text" && typeof delta.text === "string") {
      turn.chunks.push(delta.text);
    } else if (delta.type === "tool_use") {
      turn.tools.push({
        name: delta.name ?? "",
        input: (delta.input as Record<string, unknown>) ?? {},
        expanded: false,
      });
    } else if (delta.type === "tool_result" && typeof delta.content === "string") {
      const lastTool = turn.tools[turn.tools.length - 1];
      if (lastTool) lastTool.output = delta.content;
    }
    refreshTurnElement(root, turn);
    idx++;
  }, 150);
}

// -------------------------------------------------------------------
// Active view helpers
// -------------------------------------------------------------------

function activeView(state: PanelState): SessionView | undefined {
  return state.activeId ? state.sessions.get(state.activeId) : undefined;
}

function setActive(root: HTMLElement, state: PanelState, sessionId: string): void {
  state.activeId = sessionId;
  refreshSidebar(root, state);
  refreshChatPane(root, state);
}

// -------------------------------------------------------------------
// Sidebar rendering
// -------------------------------------------------------------------

function refreshSidebar(root: HTMLElement, state: PanelState): void {
  const list = root.querySelector<HTMLUListElement>('[data-role="session-list"]');
  if (!list) return;

  if (state.sessions.size === 0) {
    list.innerHTML = `<li class="r1-empty r1-sv-no-sessions">No sessions yet.</li>`;
    return;
  }

  list.innerHTML = "";
  for (const view of state.sessions.values()) {
    const li = document.createElement("li");
    li.className = "r1-sv-session-item";
    li.setAttribute("role", "option");
    li.setAttribute("aria-selected", view.sessionId === state.activeId ? "true" : "false");
    li.dataset.sessionId = view.sessionId;

    li.innerHTML = `
      <span class="r1-sv-session-title">${escapeHtml(view.title)}</span>
      <span class="r1-status-pill r1-status-${view.status}">${view.status}</span>
      <button
        type="button"
        class="r1-btn r1-sv-close-btn"
        data-role="close-session"
        aria-label="Close session ${escapeHtml(view.title)}"
        title="Close"
      >&times;</button>
    `;

    li.addEventListener("click", (event) => {
      const target = event.target as HTMLElement | null;
      if (target?.closest('[data-role="close-session"]')) return;
      setActive(root, state, view.sessionId);
    });

    li.querySelector('[data-role="close-session"]')?.addEventListener("click", () => {
      state.sessions.delete(view.sessionId);
      if (state.activeId === view.sessionId) {
        const first = state.sessions.keys().next().value;
        state.activeId = first ?? null;
      }
      refreshSidebar(root, state);
      refreshChatPane(root, state);
    });

    list.appendChild(li);
  }
}

// -------------------------------------------------------------------
// Chat pane rendering
// -------------------------------------------------------------------

function refreshChatPane(root: HTMLElement, state: PanelState): void {
  const emptyState = root.querySelector<HTMLElement>('[data-role="empty-state"]');
  const chatPane = root.querySelector<HTMLElement>('[data-role="chat-pane"]');
  if (!emptyState || !chatPane) return;

  const view = activeView(state);
  if (!view) {
    emptyState.hidden = false;
    chatPane.hidden = true;
    return;
  }

  emptyState.hidden = true;
  chatPane.hidden = false;

  const titleEl = root.querySelector<HTMLElement>('[data-role="chat-title"]');
  if (titleEl) titleEl.textContent = view.title;

  refreshStatusPill(root, state);
  refreshControlButtons(root, state);
  rebuildTranscript(root, view);
}

function refreshStatusPill(root: HTMLElement, state: PanelState): void {
  const pill = root.querySelector<HTMLElement>('[data-role="chat-status-pill"]');
  const view = activeView(state);
  if (!pill || !view) return;
  pill.className = `r1-status-pill r1-status-${view.status}`;
  pill.textContent = view.status;
}

function refreshControlButtons(root: HTMLElement, state: PanelState): void {
  const pauseBtn = root.querySelector<HTMLButtonElement>('[data-role="pause-btn"]');
  const resumeBtn = root.querySelector<HTMLButtonElement>('[data-role="resume-btn"]');
  const cancelBtn = root.querySelector<HTMLButtonElement>('[data-role="cancel-btn"]');
  const sendBtn = root.querySelector<HTMLButtonElement>('[data-role="send-btn"]');
  const composerInput = root.querySelector<HTMLTextAreaElement>('[data-role="composer-input"]');
  const view = activeView(state);

  if (!view) {
    if (pauseBtn) pauseBtn.disabled = true;
    if (resumeBtn) resumeBtn.disabled = true;
    if (cancelBtn) cancelBtn.disabled = true;
    if (sendBtn) sendBtn.disabled = true;
    if (composerInput) composerInput.disabled = true;
    return;
  }

  const isRunning = view.status === "running";
  const isPaused = view.status === "paused";
  const isEnded = view.status === "ended";

  if (pauseBtn) pauseBtn.disabled = !isRunning;
  if (resumeBtn) resumeBtn.disabled = !isPaused;
  if (cancelBtn) cancelBtn.disabled = isEnded;
  if (sendBtn) sendBtn.disabled = isEnded || isRunning;
  if (composerInput) composerInput.disabled = isEnded || isRunning;
}

// -------------------------------------------------------------------
// Transcript rendering (R1D-2.1 / R1D-2.2 / R1D-2.3)
// -------------------------------------------------------------------

function rebuildTranscript(root: HTMLElement, view: SessionView): void {
  const transcript = root.querySelector<HTMLOListElement>('[data-role="transcript"]');
  if (!transcript) return;
  transcript.innerHTML = "";
  for (const turn of view.turns) {
    transcript.appendChild(buildTurnElement(turn));
  }
  transcript.scrollTop = transcript.scrollHeight;
}

function appendUserTurn(
  root: HTMLElement,
  state: PanelState,
  view: SessionView,
  text: string,
): void {
  const turn: Turn = {
    id: `turn-${++state.nextTurnCounter}`,
    role: "user",
    chunks: [text],
    tools: [],
    status: "done",
  };
  view.turns.push(turn);
  const transcript = root.querySelector<HTMLOListElement>('[data-role="transcript"]');
  if (transcript) {
    transcript.appendChild(buildTurnElement(turn));
    transcript.scrollTop = transcript.scrollHeight;
  }
}

function appendSystemMessage(
  root: HTMLElement,
  state: PanelState,
  view: SessionView,
  text: string,
): void {
  const turn: Turn = {
    id: `turn-${++state.nextTurnCounter}`,
    role: "system",
    chunks: [text],
    tools: [],
    status: "done",
  };
  view.turns.push(turn);
  const transcript = root.querySelector<HTMLOListElement>('[data-role="transcript"]');
  if (transcript) {
    transcript.appendChild(buildTurnElement(turn));
    transcript.scrollTop = transcript.scrollHeight;
  }
}

function appendTurnElement(root: HTMLElement, turn: Turn): void {
  const transcript = root.querySelector<HTMLOListElement>('[data-role="transcript"]');
  if (!transcript) return;
  transcript.appendChild(buildTurnElement(turn));
  transcript.scrollTop = transcript.scrollHeight;
}

function refreshTurnElement(root: HTMLElement, turn: Turn): void {
  const existing = root.querySelector<HTMLLIElement>(`[data-turn-id="${CSS.escape(turn.id)}"]`);
  if (!existing) {
    appendTurnElement(root, turn);
    return;
  }
  const updated = buildTurnElement(turn);
  existing.replaceWith(updated);
  const transcript = root.querySelector<HTMLOListElement>('[data-role="transcript"]');
  if (transcript) transcript.scrollTop = transcript.scrollHeight;
}

function buildTurnElement(turn: Turn): HTMLLIElement {
  const li = document.createElement("li");
  li.className = `r1-sv-turn r1-sv-turn-${turn.role}`;
  li.dataset.turnId = turn.id;
  if (turn.status === "streaming") li.classList.add("is-streaming");
  if (turn.status === "cancelled") li.classList.add("is-cancelled");

  const roleLabel = turn.role === "assistant" ? "R1" : turn.role === "user" ? "You" : "System";
  const textContent = turn.chunks.join("");

  // R1D-2.3: rudimentary Markdown-to-HTML (code blocks + inline code).
  const renderedText = renderMarkdown(textContent);

  const toolBlocks = turn.tools.map((t) => buildToolBlock(t, turn.id)).join("");

  li.innerHTML = `
    <div class="r1-sv-turn-header">
      <span class="r1-sv-turn-role">${escapeHtml(roleLabel)}</span>
      ${turn.status === "streaming" ? `<span class="r1-sv-streaming-indicator" aria-label="Streaming">...</span>` : ""}
      ${turn.status === "cancelled" ? `<span class="r1-sv-cancelled-badge">cancelled</span>` : ""}
    </div>
    <div class="r1-sv-turn-body">
      ${renderedText ? `<div class="r1-sv-turn-text">${renderedText}</div>` : ""}
      ${toolBlocks}
    </div>
  `;

  // Wire tool-block expand/collapse.
  li.querySelectorAll<HTMLButtonElement>('[data-role="tool-toggle"]').forEach((btn) => {
    btn.addEventListener("click", () => {
      const idx = parseInt(btn.dataset.toolIdx ?? "0", 10);
      const tool = turn.tools[idx];
      if (!tool) return;
      tool.expanded = !tool.expanded;
      const body = btn.closest(".r1-sv-tool-block")?.querySelector<HTMLElement>(
        '[data-role="tool-body"]',
      );
      if (body) body.hidden = !tool.expanded;
      btn.setAttribute("aria-expanded", String(tool.expanded));
      btn.textContent = tool.expanded ? "Collapse" : "Expand";
    });
  });

  return li;
}

function buildToolBlock(tool: ToolBlock, _turnId: string): string {
  const idx = 0; // injected per-tool below in the map caller
  return `
    <details class="r1-sv-tool-block" open="${tool.expanded}">
      <summary class="r1-sv-tool-summary">
        <span class="r1-sv-tool-name">${escapeHtml(tool.name)}</span>
        <button
          type="button"
          class="r1-btn r1-sv-tool-toggle"
          data-role="tool-toggle"
          data-tool-idx="${idx}"
          aria-expanded="${tool.expanded}"
        >${tool.expanded ? "Collapse" : "Expand"}</button>
      </summary>
      <div class="r1-sv-tool-body" data-role="tool-body" ${tool.expanded ? "" : "hidden"}>
        <div class="r1-sv-tool-section">
          <span class="r1-sv-tool-label">Input</span>
          <pre class="r1-sv-tool-pre"><code>${escapeHtml(safeStringify(tool.input))}</code></pre>
        </div>
        ${tool.output !== undefined
          ? `<div class="r1-sv-tool-section">
               <span class="r1-sv-tool-label">Output</span>
               <pre class="r1-sv-tool-pre"><code>${escapeHtml(tool.output)}</code></pre>
             </div>`
          : ""}
      </div>
    </details>
  `;
}

// Rebuild tool blocks with correct indices before rendering.
function buildTurnElementWithIndexedTools(turn: Turn): HTMLLIElement {
  const li = document.createElement("li");
  li.className = `r1-sv-turn r1-sv-turn-${turn.role}`;
  li.dataset.turnId = turn.id;
  if (turn.status === "streaming") li.classList.add("is-streaming");
  if (turn.status === "cancelled") li.classList.add("is-cancelled");

  const roleLabel = turn.role === "assistant" ? "R1" : turn.role === "user" ? "You" : "System";
  const textContent = turn.chunks.join("");
  const renderedText = renderMarkdown(textContent);

  const toolBlocks = turn.tools
    .map((t, idx) => {
      return `
    <details class="r1-sv-tool-block">
      <summary class="r1-sv-tool-summary">
        <span class="r1-sv-tool-name">${escapeHtml(t.name)}</span>
        <button
          type="button"
          class="r1-btn r1-sv-tool-toggle"
          data-role="tool-toggle"
          data-tool-idx="${idx}"
          aria-expanded="${t.expanded}"
        >${t.expanded ? "Collapse" : "Expand"}</button>
      </summary>
      <div class="r1-sv-tool-body" data-role="tool-body" ${t.expanded ? "" : "hidden"}>
        <div class="r1-sv-tool-section">
          <span class="r1-sv-tool-label">Input</span>
          <pre class="r1-sv-tool-pre"><code>${escapeHtml(safeStringify(t.input))}</code></pre>
        </div>
        ${t.output !== undefined
          ? `<div class="r1-sv-tool-section">
               <span class="r1-sv-tool-label">Output</span>
               <pre class="r1-sv-tool-pre"><code>${escapeHtml(t.output)}</code></pre>
             </div>`
          : ""}
      </div>
    </details>
      `;
    })
    .join("");

  li.innerHTML = `
    <div class="r1-sv-turn-header">
      <span class="r1-sv-turn-role">${escapeHtml(roleLabel)}</span>
      ${turn.status === "streaming" ? `<span class="r1-sv-streaming-indicator" aria-label="Streaming">...</span>` : ""}
      ${turn.status === "cancelled" ? `<span class="r1-sv-cancelled-badge">cancelled</span>` : ""}
    </div>
    <div class="r1-sv-turn-body">
      ${renderedText ? `<div class="r1-sv-turn-text">${renderedText}</div>` : ""}
      ${toolBlocks}
    </div>
  `;

  li.querySelectorAll<HTMLButtonElement>('[data-role="tool-toggle"]').forEach((btn) => {
    btn.addEventListener("click", () => {
      const toolIdx = parseInt(btn.dataset.toolIdx ?? "0", 10);
      const tool = turn.tools[toolIdx];
      if (!tool) return;
      tool.expanded = !tool.expanded;
      const body = btn.closest(".r1-sv-tool-block")?.querySelector<HTMLElement>(
        '[data-role="tool-body"]',
      );
      if (body) body.hidden = !tool.expanded;
      btn.setAttribute("aria-expanded", String(tool.expanded));
      btn.textContent = tool.expanded ? "Collapse" : "Expand";
    });
  });

  return li;
}

// Override buildTurnElement to use the indexed version.
// (The non-indexed version above is kept for reference but this
//  one is used by all call-sites via module-level reassignment.)
const _buildTurnElement = buildTurnElementWithIndexedTools;

// Re-export so refreshTurnElement and appendTurnElement call the real one.
// We patch the module-level references at the bottom of the file.
function buildTurnElementFinal(turn: Turn): HTMLLIElement {
  return _buildTurnElement(turn);
}

// -------------------------------------------------------------------
// Markdown renderer (R1D-2.3)
// -------------------------------------------------------------------

function renderMarkdown(text: string): string {
  if (!text) return "";

  // Fenced code blocks (``` lang\ncode\n```).
  let html = text.replace(
    /```(\w*)\n([\s\S]*?)```/g,
    (_match, lang, code: string) => {
      const langAttr = lang ? ` class="language-${escapeHtml(lang)}"` : "";
      return `<pre class="r1-sv-code-block"><code${langAttr}>${escapeHtml(code.replace(/\n$/, ""))}</code></pre>`;
    },
  );

  // Inline code (`code`).
  html = html.replace(/`([^`\n]+)`/g, (_m, code: string) => `<code>${escapeHtml(code)}</code>`);

  // **bold** and *italic*.
  html = html.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
  html = html.replace(/\*([^*]+)\*/g, "<em>$1</em>");

  // Paragraphs: double newlines.
  html = html.replace(/\n{2,}/g, "</p><p>");
  html = `<p>${html}</p>`;

  // Single newlines inside paragraphs.
  html = html.replace(/(?<!>)\n(?!<)/g, "<br>");

  return html;
}

// -------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------

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

// Export the canonical builder (indexed version).
export { buildTurnElementFinal as buildTurnElement };
