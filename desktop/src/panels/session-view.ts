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
// Session control routes through invokeStub, which delegates to real
// Tauri `invoke` when the desktop runtime is present. The transcript
// listens for real Tauri event payloads in desktop builds. The host
// emits server-pushed transcript deltas on `r1://events` and local
// subprocess lifecycle notifications on `session://started` /
// `session://ended`; plain-browser stub mode is explicitly
// non-streaming and does not synthesize assistant output.
//
// AC (work-r1-desktop-app.md R1D-2):
//   End-to-end: create a session, send a prompt, receive a streamed reply
//   with tool calls, cancel mid-run. 2 concurrent sessions switch cleanly.

import { invokeStub } from "../ipc-stub";
import type {
  ServerEvent,
  SessionStartParams,
  SessionStartResult,
  SessionPauseResult,
  SessionResumeResult,
  SessionIdParams,
  SessionSendParams,
  SessionSummary,
} from "../types/ipc";
import { mountLaneRail, type LaneRailHandle } from "./lane-rail";

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
  laneRail: LaneRailHandle | null;
  eventBridgeMode: "unknown" | "live" | "stub";
  eventBridgePromise: Promise<void> | null;
}

// -------------------------------------------------------------------
// Public entry-point
// -------------------------------------------------------------------

export function renderPanel(root: HTMLElement): void {
  const state: PanelState = {
    sessions: new Map(),
    activeId: null,
    nextTurnCounter: 0,
    laneRail: null,
    eventBridgeMode: "unknown",
    eventBridgePromise: null,
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
      <aside
        class="r1-sv-lane-rail"
        aria-label="Cognition lanes"
        data-role="lane-rail"
        hidden
      ></aside>
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
  wireLaneRail(root, state);
  void ensureSessionEventBridge(root, state);
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
  await ensureSessionEventBridge(root, state);

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
  if (state.eventBridgeMode !== "live") {
    view.status = "paused";
    refreshStatusPill(root, state);
    refreshControlButtons(root, state);
    appendSystemMessage(
      root,
      state,
      view,
      "Prompt sent. Live reply streaming requires the Tauri desktop runtime; stub mode does not synthesize assistant output.",
    );
  }
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
// Session event stream
// -------------------------------------------------------------------

interface TauriEventEnvelope<T> {
  payload: T;
}

type TauriListenFn = <T>(
  event: string,
  handler: (event: TauriEventEnvelope<T>) => void,
) => Promise<() => void>;

async function ensureSessionEventBridge(root: HTMLElement, state: PanelState): Promise<void> {
  if (state.eventBridgePromise) return state.eventBridgePromise;
  state.eventBridgePromise = initSessionEventBridge(root, state);
  return state.eventBridgePromise;
}

async function initSessionEventBridge(root: HTMLElement, state: PanelState): Promise<void> {
  if (!hasTauriRuntime()) {
    state.eventBridgeMode = "stub";
    return;
  }
  const { listen } = await import("@tauri-apps/api/event");
  state.eventBridgeMode = "live";
  const tauriListen = listen as TauriListenFn;
  const topics = ["r1://events", "session://started", "session://ended"] as const;
  await Promise.all(
    topics.map((topic) =>
      tauriListen<ServerEvent>(topic, (event) => {
        applyServerEvent(root, state, event.payload);
      }),
    ),
  );
}

function hasTauriRuntime(): boolean {
  return typeof window !== "undefined" && "__TAURI__" in window;
}

function applyServerEvent(root: HTMLElement, state: PanelState, event: ServerEvent): void {
  const view = state.sessions.get(event.session_id);
  if (!view) return;

  switch (event.event) {
    case "session.started":
      view.status = "running";
      refreshStatusPill(root, state);
      refreshControlButtons(root, state);
      return;
    case "session.delta":
      applySessionDelta(root, state, view, event.payload);
      return;
    case "session.ended":
      finalizeSessionTurn(root, state, view, event.reason);
      return;
    default:
      return;
  }
}

function applySessionDelta(
  root: HTMLElement,
  state: PanelState,
  view: SessionView,
  payload: Record<string, unknown>,
): void {
  const turn = ensureStreamingAssistantTurn(root, state, view);
  const deltaType = typeof payload.type === "string" ? payload.type : "";

  if (deltaType === "text" && typeof payload.text === "string") {
    turn.chunks.push(payload.text);
  } else if (deltaType === "tool_use") {
    turn.tools.push({
      name: typeof payload.name === "string" ? payload.name : "",
      input: isRecord(payload.input) ? payload.input : {},
      expanded: false,
    });
  } else if (deltaType === "tool_result" && typeof payload.content === "string") {
    const lastTool = turn.tools[turn.tools.length - 1];
    if (lastTool) lastTool.output = payload.content;
  }

  refreshTurnElement(root, turn);
}

function ensureStreamingAssistantTurn(
  root: HTMLElement,
  state: PanelState,
  view: SessionView,
): Turn {
  const activeTurn = view.activeTurnId
    ? view.turns.find((turn) => turn.id === view.activeTurnId)
    : undefined;
  if (activeTurn?.role === "assistant" && activeTurn.status === "streaming") {
    return activeTurn;
  }

  const turn: Turn = {
    id: `turn-${++state.nextTurnCounter}`,
    role: "assistant",
    chunks: [],
    tools: [],
    status: "streaming",
  };
  view.turns.push(turn);
  view.activeTurnId = turn.id;
  view.status = "running";
  refreshStatusPill(root, state);
  refreshControlButtons(root, state);
  appendTurnElement(root, turn);
  return turn;
}

function finalizeSessionTurn(
  root: HTMLElement,
  state: PanelState,
  view: SessionView,
  reason: "ok" | "cancelled" | "error",
): void {
  const activeTurn = view.activeTurnId
    ? view.turns.find((turn) => turn.id === view.activeTurnId)
    : undefined;
  if (activeTurn?.status === "streaming") {
    activeTurn.status = reason === "ok" ? "done" : "cancelled";
    refreshTurnElement(root, activeTurn);
  }
  view.activeTurnId = null;
  view.status = reason === "ok" ? "paused" : "ended";
  refreshStatusPill(root, state);
  refreshControlButtons(root, state);
  if (reason === "error") {
    appendSystemMessage(root, state, view, "Session ended with an error.");
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
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
  refreshLaneRail(root, state);
}

// -------------------------------------------------------------------
// Lane rail mount (R1D-augmentation: spec §8 + checklist item 22)
// -------------------------------------------------------------------

function wireLaneRail(root: HTMLElement, state: PanelState): void {
  const railEl = root.querySelector<HTMLElement>('[data-role="lane-rail"]');
  if (!railEl) return;
  state.laneRail = mountLaneRail(railEl);
}

function refreshLaneRail(root: HTMLElement, state: PanelState): void {
  const railEl = root.querySelector<HTMLElement>('[data-role="lane-rail"]');
  if (!railEl || !state.laneRail) return;
  const view = activeView(state);
  if (!view) {
    railEl.hidden = true;
    state.laneRail.attach(null);
    return;
  }
  railEl.hidden = false;
  state.laneRail.attach(view.sessionId);
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
  const existing = root.querySelector<HTMLLIElement>(
    `[data-turn-id="${escapeAttributeValue(turn.id)}"]`,
  );
  if (!existing) {
    appendTurnElement(root, turn);
    return;
  }
  const updated = buildTurnElement(turn);
  existing.replaceWith(updated);
  const transcript = root.querySelector<HTMLOListElement>('[data-role="transcript"]');
  if (transcript) transcript.scrollTop = transcript.scrollHeight;
}

// buildTurnElement renders one transcript turn (prose + per-tool collapsible
// blocks). Each tool block carries its own stable `data-tool-idx` so the
// expand/collapse toggle mutates the correct tool (a prior non-indexed variant
// hard-coded idx=0, making every tool block toggle tools[0]).
function buildTurnElement(turn: Turn): HTMLLIElement {
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

// -------------------------------------------------------------------
// Markdown renderer (R1D-2.3)
// -------------------------------------------------------------------

function renderMarkdown(text: string): string {
  if (!text) return "";

  // SECURITY (stored-XSS fix): HTML-escape the FULL text before any markdown
  // transform. Turn prose originates from streamed LLM output and tool_result
  // content, which may contain raw tags (e.g. `<img src=x onerror=...>`). This
  // renderer's output is written via li.innerHTML, so any un-escaped tag would
  // execute inside the Tauri renderer (which holds host IPC). Escaping up-front
  // neutralizes every raw tag; the markdown regexes below then run against the
  // already-escaped string. Because the text is escaped once here, the code /
  // inline-code branches must NOT re-escape (that would double-encode `&`).
  let html = escapeHtml(text);

  // Fenced code blocks (``` lang\ncode\n```). Backticks and newlines survive
  // escaping, so the fence structure is preserved; `code` is already escaped.
  html = html.replace(
    /```(\w*)\n([\s\S]*?)```/g,
    (_match, lang, code: string) => {
      // `lang` is [\w]* so it cannot carry markup; escaping is a harmless no-op
      // that also avoids double-encoding.
      const langAttr = lang ? ` class="language-${lang}"` : "";
      return `<pre class="r1-sv-code-block"><code${langAttr}>${code.replace(/\n$/, "")}</code></pre>`;
    },
  );

  // Inline code (`code`). Content is already escaped above.
  html = html.replace(/`([^`\n]+)`/g, (_m, code: string) => `<code>${code}</code>`);

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

function escapeAttributeValue(raw: string): string {
  if (typeof CSS !== "undefined" && typeof CSS.escape === "function") {
    return CSS.escape(raw);
  }
  return raw.replace(/\\/g, "\\\\").replace(/"/g, '\\"');
}

// Export the canonical builder (per-tool indexed version).
export { buildTurnElement };
