// SPDX-License-Identifier: MIT
//
// R1D-2 tests — session-view panel (workspace selector + session list + main interaction).
//
// AC from work-r1-desktop-app.md R1D-2:
//   - Session view renders with sidebar + composer + chat pane.
//   - Multi-session sidebar exists (create, switch, list).
//   - Chat transcript, tool-use blocks, markdown rendering present.
//   - Cancel / pause / resume controls present.
//
// Tests use jsdom DOM via vitest environment:"jsdom".

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { renderPanel, buildTurnElement } from "./panels/session-view";

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

function makeRoot(): HTMLElement {
  const div = document.createElement("div");
  document.body.appendChild(div);
  return div;
}

function cleanup(root: HTMLElement): void {
  root.remove();
}

// -----------------------------------------------------------------------
// R1D-2.1 — Chat transcript + composer
// -----------------------------------------------------------------------

describe("session-view — R1D-2.1 chat transcript + composer", () => {
  let root: HTMLElement;
  beforeEach(() => {
    vi.spyOn(console, "info").mockImplementation(() => {});
    root = makeRoot();
    renderPanel(root);
  });

  it("mounts with the r1-panel-session-view class", () => {
    expect(root.classList.contains("r1-panel-session-view")).toEqual(true);
  });

  it("renders a transcript list with aria-label", () => {
    const transcript = root.querySelector('[data-role="transcript"]');
    expect(transcript).not.toBeNull();
    expect(transcript!.getAttribute("aria-label")).toBe("Chat transcript");
  });

  it("renders the composer form", () => {
    const form = root.querySelector('[data-role="composer"]');
    expect(form).not.toBeNull();
    expect(form!.tagName).toBe("FORM");
  });

  it("renders the composer textarea with correct aria-label", () => {
    const textarea = root.querySelector('[data-role="composer-input"]');
    expect(textarea).not.toBeNull();
    expect(textarea!.getAttribute("aria-label")).toContain("Message to send");
  });

  it("renders the send button", () => {
    const sendBtn = root.querySelector('[data-role="send-btn"]');
    expect(sendBtn).not.toBeNull();
    expect(sendBtn!.textContent).toBe("Send");
  });

  it("shows empty-state before any session is active", () => {
    const emptyState = root.querySelector('[data-role="empty-state"]');
    expect(emptyState).not.toBeNull();
  });

  it("chat pane is hidden by default (no active session)", () => {
    const chatPane = root.querySelector<HTMLElement>('[data-role="chat-pane"]');
    expect(chatPane).not.toBeNull();
    expect(chatPane!.hidden).toEqual(true);
  });

  afterEach(() => cleanup(root));
});

// -----------------------------------------------------------------------
// R1D-2.2 — Tool-use rendering
// -----------------------------------------------------------------------

describe("session-view — R1D-2.2 tool-use rendering via buildTurnElement", () => {
  it("renders a user turn with correct role label", () => {
    const turn = {
      id: "turn-1",
      role: "user" as const,
      chunks: ["Hello!"],
      tools: [],
      status: "done" as const,
    };
    const li = buildTurnElement(turn as Parameters<typeof buildTurnElement>[0]);
    expect(li.tagName).toBe("LI");
    expect(li.classList.contains("r1-sv-turn-user")).toEqual(true);
    expect(li.textContent).toContain("You");
  });

  it("renders an assistant turn with R1 role label", () => {
    const turn = {
      id: "turn-2",
      role: "assistant" as const,
      chunks: ["I can help."],
      tools: [],
      status: "done" as const,
    };
    const li = buildTurnElement(turn as Parameters<typeof buildTurnElement>[0]);
    expect(li.classList.contains("r1-sv-turn-assistant")).toEqual(true);
    expect(li.textContent).toContain("R1");
  });

  it("renders tool blocks for assistant turns with tools", () => {
    const turn = {
      id: "turn-3",
      role: "assistant" as const,
      chunks: ["Running tool."],
      tools: [
        {
          name: "bash",
          input: { cmd: "ls -la" },
          output: "total 0",
          expanded: false,
        },
      ],
      status: "done" as const,
    };
    const li = buildTurnElement(turn as Parameters<typeof buildTurnElement>[0]);
    const toolBlock = li.querySelector(".r1-sv-tool-block");
    expect(toolBlock).not.toBeNull();
    const toolName = li.querySelector(".r1-sv-tool-name");
    expect(toolName!.textContent).toBe("bash");
  });

  it("renders expand/collapse toggle button for tool block", () => {
    const turn = {
      id: "turn-4",
      role: "assistant" as const,
      chunks: [],
      tools: [{ name: "read_file", input: { path: "/x" }, expanded: false }],
      status: "done" as const,
    };
    const li = buildTurnElement(turn as Parameters<typeof buildTurnElement>[0]);
    const toggle = li.querySelector<HTMLButtonElement>('[data-role="tool-toggle"]');
    expect(toggle).not.toBeNull();
    expect(toggle!.getAttribute("aria-expanded")).toBe("false");
  });

  it("renders streaming indicator for streaming turns", () => {
    const turn = {
      id: "turn-5",
      role: "assistant" as const,
      chunks: ["..."],
      tools: [],
      status: "streaming" as const,
    };
    const li = buildTurnElement(turn as Parameters<typeof buildTurnElement>[0]);
    expect(li.classList.contains("is-streaming")).toEqual(true);
    const indicator = li.querySelector(".r1-sv-streaming-indicator");
    expect(indicator).not.toBeNull();
  });

  it("renders cancelled badge for cancelled turns", () => {
    const turn = {
      id: "turn-6",
      role: "assistant" as const,
      chunks: [],
      tools: [],
      status: "cancelled" as const,
    };
    const li = buildTurnElement(turn as Parameters<typeof buildTurnElement>[0]);
    expect(li.classList.contains("is-cancelled")).toEqual(true);
    const badge = li.querySelector(".r1-sv-cancelled-badge");
    expect(badge).not.toBeNull();
  });
});

// -----------------------------------------------------------------------
// R1D-2.3 — Markdown rendering (via buildTurnElement text content)
// -----------------------------------------------------------------------

describe("session-view — R1D-2.3 markdown rendering", () => {
  it("renders fenced code blocks as pre>code", () => {
    const turn = {
      id: "turn-md-1",
      role: "assistant" as const,
      chunks: ["```bash\necho hello\n```"],
      tools: [],
      status: "done" as const,
    };
    const li = buildTurnElement(turn as Parameters<typeof buildTurnElement>[0]);
    const codeBlock = li.querySelector(".r1-sv-code-block");
    expect(codeBlock).not.toBeNull();
    expect(codeBlock!.tagName).toBe("PRE");
  });

  it("renders inline code as code elements", () => {
    const turn = {
      id: "turn-md-2",
      role: "assistant" as const,
      chunks: ["Use `r1 serve` to start."],
      tools: [],
      status: "done" as const,
    };
    const li = buildTurnElement(turn as Parameters<typeof buildTurnElement>[0]);
    const code = li.querySelector("code");
    expect(code).not.toBeNull();
    expect(code!.textContent).toBe("r1 serve");
  });

  it("escapes HTML in tool input to prevent XSS", () => {
    const turn = {
      id: "turn-xss",
      role: "assistant" as const,
      chunks: [],
      tools: [
        {
          name: "tool",
          input: { data: "<script>alert('xss')</script>" },
          expanded: true,
        },
      ],
      status: "done" as const,
    };
    const li = buildTurnElement(turn as Parameters<typeof buildTurnElement>[0]);
    // The raw script tag must not appear unescaped
    expect(li.innerHTML).not.toContain("<script>");
    expect(li.innerHTML).toContain("&lt;script&gt;");
  });
});

// -----------------------------------------------------------------------
// R1D-2.4 — Multi-session sidebar
// -----------------------------------------------------------------------

describe("session-view — R1D-2.4 session sidebar", () => {
  let root: HTMLElement;
  beforeEach(() => {
    vi.spyOn(console, "info").mockImplementation(() => {});
    root = makeRoot();
    renderPanel(root);
  });

  it("renders the session sidebar nav", () => {
    const sidebar = root.querySelector('[data-role="session-sidebar"]');
    expect(sidebar).not.toBeNull();
    expect(sidebar!.getAttribute("aria-label")).toBe("Session list");
  });

  it("renders the new-session button", () => {
    const newBtn = root.querySelector('[data-role="new-session"]');
    expect(newBtn).not.toBeNull();
    expect(newBtn!.getAttribute("aria-label")).toContain("New session");
  });

  it("renders the session-list as a listbox", () => {
    const list = root.querySelector('[data-role="session-list"]');
    expect(list).not.toBeNull();
    expect(list!.getAttribute("role")).toBe("listbox");
  });

  it("shows a no-sessions empty state initially", () => {
    const list = root.querySelector('[data-role="session-list"]');
    expect(list!.textContent).toContain("No sessions");
  });

  afterEach(() => cleanup(root));
});

// -----------------------------------------------------------------------
// R1D-2.5 — Cancel / pause / resume controls
// -----------------------------------------------------------------------

describe("session-view — R1D-2.5 controls", () => {
  let root: HTMLElement;
  beforeEach(() => {
    vi.spyOn(console, "info").mockImplementation(() => {});
    root = makeRoot();
    renderPanel(root);
  });

  it("renders pause button with keyboard hint", () => {
    const pauseBtn = root.querySelector('[data-role="pause-btn"]');
    expect(pauseBtn).not.toBeNull();
    expect(pauseBtn!.getAttribute("aria-label")).toContain("Pause");
  });

  it("renders resume button", () => {
    const resumeBtn = root.querySelector('[data-role="resume-btn"]');
    expect(resumeBtn).not.toBeNull();
    expect(resumeBtn!.getAttribute("aria-label")).toContain("Resume");
  });

  it("renders cancel button with Esc hint", () => {
    const cancelBtn = root.querySelector('[data-role="cancel-btn"]');
    expect(cancelBtn).not.toBeNull();
    expect(cancelBtn!.getAttribute("aria-label")).toContain("Cancel");
  });

  it("controls are disabled when no session is active", () => {
    const pauseBtn = root.querySelector<HTMLButtonElement>('[data-role="pause-btn"]');
    const resumeBtn = root.querySelector<HTMLButtonElement>('[data-role="resume-btn"]');
    const cancelBtn = root.querySelector<HTMLButtonElement>('[data-role="cancel-btn"]');
    expect(pauseBtn!.disabled).toEqual(true);
    expect(resumeBtn!.disabled).toEqual(true);
    expect(cancelBtn!.disabled).toEqual(true);
  });

  it("composer hint mentions Ctrl+Enter shortcut", () => {
    const hint = root.querySelector(".r1-sv-composer-hint");
    expect(hint).not.toBeNull();
    expect(hint!.textContent).toContain("Ctrl+Enter");
  });

  afterEach(() => cleanup(root));
});
