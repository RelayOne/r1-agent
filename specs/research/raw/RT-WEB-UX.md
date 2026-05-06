# RT-WEB-UX — Web UI for `r1 serve`: WebSocket auth + chat UX references

Status: research, raw notes
Date: 2026-05-02
Scope: inputs for the `r1-server-ui-v2.md` build spec.

---

## PART A — WebSocket auth + cross-origin for a local daemon

### A1. Authenticating browser → local WS (`r1 serve`)

The browser WebSocket API has three auth-relevant constraints:

- **Cannot set custom request headers** on the upgrade (no `Authorization: Bearer …` from the page). Custom headers only work for server-to-server clients. [websockets RTD] [WebSocket.org guide]
- **Cookies do go** with the upgrade if same-site / same-origin (subject to `SameSite`).
- The `Sec-WebSocket-Protocol` ("subprotocol") header **can be set** by the browser via the `protocols` constructor argument and is a known token-smuggling channel. [websocket.org guide]

Ranked options for `r1 serve` (UI and daemon on the loopback, but UI may be served from a separate dev server such as Vite on `:5173` while the daemon is on `:7777`):

1. **Token via `Sec-WebSocket-Protocol`** — `new WebSocket(url, ["r1.bearer", token])`. The browser sends it in the upgrade; the server reads `Sec-WebSocket-Protocol`, validates, and echoes one accepted subprotocol back. Avoids URL logging, avoids the cookie cross-port problem on loopback (different ports = different origins), and is the closest a browser gets to a real auth header. Used in production by k8s-dashboard, Hugging Face, and others. [WebSocket.org / Ably guides]
2. **Short-lived token in query string**, fetched from a same-origin `/auth/ws-ticket` endpoint that requires the daemon's session cookie. The query token is single-use and expires in ~30 s, so even if it appears in logs it's already burned. This is the canonical "ephemeral ticket" pattern recommended by Ably. [Ably FAQ]
3. **Cookie-based** (`HttpOnly; SameSite=Strict; Secure` on the daemon). Works only if the UI is served by the same origin as the WS — i.e., the daemon itself, not a separate Vite dev server. Best long-term option once we ship a built UI from `r1 serve` itself.
4. **First-message auth** (open WS, send `{type:"hello", token}` as message 1). Documented as "most secure" in [websockets RTD] but loses HTTP status codes — failed auth has to be a close-frame, which is harder to debug. Use only as a fallback.

**Recommendation for r1**: option (1) for dev (UI served separately), option (3) for production (UI baked into the daemon). Implement both; the client picks based on whether `document.cookie` contains the daemon session cookie.

Anti-pattern: sending the long-lived API token in the URL query — it lands in access logs and shell history. [Ably blog]

### A2. CORS / Origin enforcement for a localhost daemon

Key fact: **WebSockets are not subject to CORS in browsers**. Cross-origin WS requests still execute; CORS preflight does not happen. The server **must** enforce `Origin` itself. [dev.to "WebSockets bypassing SOP/CORS", Solita blog]

For a loopback daemon the threat model is **Cross-Site WebSocket Hijacking (CSWSH)** + **DNS rebinding**: an attacker page on `evil.com` opens `ws://localhost:7777/ws` and rides the user's session cookie. Mitigations:

- **Strict allowlist on `Origin`**. Accept only:
  - `http://localhost:<port>` and `http://127.0.0.1:<port>` for known local ports (the daemon's own port, plus a configurable list of dev ports — Vite 5173, Next 3000)
  - `http://[::1]:<port>`
  - the explicit "served-from-self" origin
  Reject anything else with HTTP 403 during the upgrade. [WebSocket.org / OWASP CSWSH]
- **Validate `Host` header** against the same allowlist — defends against DNS rebinding where a malicious DNS record points `attacker.com` at `127.0.0.1`. The browser sends `Host: attacker.com` but expects the daemon to serve it; reject. [GitHub blog DNS rebinding, host-validation middleware]
- **Bind to loopback only** by default (`127.0.0.1:7777`, not `0.0.0.0`). Already standard for local agent daemons (Ollama, MLflow, Devin local, etc.). [MLflow self-hosting docs]
- **No wildcard** `Access-Control-Allow-Origin: *` on the HTTP side either; mirror the same allowlist.

### A3. CSRF on WS for loopback

Yes, you still need it. CSWSH **is** CSRF for WebSockets. Cookie + lax origin = exploitable.

- If you go cookie-based (option A1.3), add a **CSRF token** that the client must send in the first WS message AND in the connect URL/subprotocol. The CSRF token is set into a non-HttpOnly cookie at session creation; JS reads it and forwards. Standard double-submit pattern. [OWASP CSWSH cheat sheet]
- If you go subprotocol-token (A1.1) or ticket (A1.2), **the token itself is the CSRF defense** — JS on `evil.com` cannot read or guess it. No additional CSRF token needed.

This is why option (1) is the cleanest for r1: it collapses auth + CSRF into one mechanism.

### A4. Reconnect with token refresh

Patterns from production WS clients (Ably, Phoenix Channels, Socket.IO):

- Wrap the `WebSocket` in a "resilient socket" with exponential backoff + jitter (250 ms → 8 s, capped).
- On `close` with code 4401 (custom: "auth expired"), call `/auth/ws-ticket` to mint a new ticket, then reconnect.
- Server emits a `auth.expiring_soon` message ~60 s before token expiry; client pre-fetches and reconnects gracefully.
- Persist last-seen event ID and replay missed events on reconnect (the daemon's WAL bus already supports this — see `bus/` package).

---

## PART B — Reference UX patterns (May 2026 landscape)

### B1. claude.ai

- Two-pane: left sidebar (conversation list, "Projects", model picker at top), main column (messages + composer).
- **Streaming**: text streams token-by-token; tool calls render as **collapsible cards inline in the message** ("Searched the web", "Read file"), expandable to show inputs/outputs. Code blocks use Shiki-style syntax highlighting and a copy button.
- **Stop button**: replaces the send button while streaming; sends an interrupt and the partial message stays.
- **Artifacts**: right-side panel that opens when an artifact is created (code, doc, SVG). Toggled per-message.
- **Model picker**: top of composer (moved from header in 2025).
- **Multi-conversation sidebar**: flat list with date grouping ("Today", "Yesterday", "Last 7 days"). Pinned/starred conversations on top.
- Source: [Claude Design overview, MindStudio guides, IntuitionLabs UI comparison].

### B2. chatgpt.com

- Same two-pane shape. Sidebar groups: Pinned, Today, Yesterday, then by week.
- **2026 changes**: model picker moved into composer; thinking-effort slider lives inside the model picker. Composer "shimmers" while streaming. [OpenAI release notes May 2026]
- **Tool cards**: ChatGPT Apps SDK formalizes "card / carousel / fullscreen" display modes — apps render as inline cards, can promote to fullscreen.
- **Canvas**: side-pane editor for documents/code, mirrors Claude artifacts.

### B3. Cursor 3 "Glass" (Apr 2026) — strongest reference for r1

Cursor 3 explicitly **replaced the Composer side-pane with an Agents Window** designed for parallel agents. Key elements:

- **Right sidebar lists every agent** (local + cloud + Slack/GitHub/Linear-triggered), each with status and progress dots.
- **Agent Tabs**: multiple agents tiled side-by-side or in a grid; each has its own scratchpad, diff, and conversation.
- **Worktree-per-agent** isolation (mirrors r1's `worktree/` package — direct alignment).
- "All local and cloud agents appear in the sidebar." Sidebar is the single source of truth for parallel work.
- Diff view is consolidated across files per-agent.
- Sources: [cursor.com/blog/cursor-3, InfoQ, devtoolpicks review, dev.to Cursor 3 Glass review].

This is the pattern r1 should copy almost verbatim. r1's "lanes" map 1:1 to Cursor's agents.

### B4. Devin 2.0

- **Multi-pane workspace per session**: Shell + IDE + Browser, plus a planner pane on the left.
- **Top-level dashboard** lists all running Devins (think "tabs of full IDEs"). Each Devin runs in its own VM.
- **Replay timeline**: every command/edit/browser action recorded; scrubbable. Strong reference for r1's `replay/` package surfacing in the UI.
- Sources: [cognition.ai/blog/devin-2, Devin docs, Analytics Vidhya Devin 2.0 review].

### B5. bolt.new / v0.dev / lovable.dev

- **bolt.new**: split view with chat on left, WebContainer preview on right. Multi-agent in 2026 (db agent + UI agent).
- **v0**: chat on left, generated component preview + code tabs on right.
- **lovable.dev**: chat on left, live preview on right; "Chat Mode Agent" reasons across steps and file inspection without writing code until approved — a useful pattern for r1's plan-then-execute split.
- All three converge on **"chat-left, work-right"** with the work pane being live (preview, diff, terminal). Sources: [NextFuture, UIBakery, ToolJet 2026 comparisons].

### B6. Open-source chat UIs to mine

| Project | What to copy | Source |
|---|---|---|
| **LobeChat** | Most polished UI in the OSS space. Plugin/artifact system, multi-modal, conversation sidebar with agents. | [GitHub lobehub/lobe-chat] |
| **Open WebUI** | Tools/Pipelines plugin model, RAG, robust streaming. Lightweight. | [openwebui.com] |
| **LibreChat** | Artifacts side panel that auto-renders React/HTML/Mermaid; agents + MCP support. 2026 roadmap: programmatic tool calling, queued follow-up messages, graceful interrupt. | [librechat.ai/docs/features/artifacts] |
| **big-AGI** | Branching conversations, "beam" multi-model fan-out — direct analogue to r1 specexec. | [github.com/enricoros/big-agi] |
| **Chatbot UI** (mckaywrigley) | Minimal, easy-to-fork base; good for the v0 daemon UI. | [github.com/mckaywrigley/chatbot-ui] |

### B7. Markdown + code rendering (May 2026)

- **Streamdown** (`vercel/streamdown`) — drop-in replacement for `react-markdown` purpose-built for LLM streams. Handles unterminated/partial Markdown gracefully. Built-in: GFM, Shiki code blocks (copy + download buttons), KaTeX math, Mermaid, `rehype-harden` security. **This is the anchor library.** [github.com/vercel/streamdown, streamdown.ai]
- **AI Elements** (`@ai-sdk/elements`, redirects to `elements.ai-sdk.dev`) — 20+ shadcn-conforming components: `Message`, `Tool`, `CodeBlock`, `Reasoning`, `Sources`, `InlineCitation`, `Suggestion`, `PromptInput`, `Conversation`, `ChainOfThought`, `Plan`, `Confirmation`, `Context`, `Attachments`, `Checkpoint`, `Shimmer`, `Queue`. Tightly integrated with `useChat`. [elements.ai-sdk.dev]
- **Shiki** — server-side highlighting only when needed; Streamdown wraps it. Avoid running Prism/highlight.js client-side at this point.
- **Vercel AI SDK 6** `useChat` — `message.parts` array, typed tool parts (`tool-getWeather`), streaming tool inputs. Maps cleanly to r1's tool schema. [vercel.com/blog/ai-sdk-6]

---

## PART C — Concrete UX decisions for r1

### C1. Where do "lanes" sit?

**Right sidebar, Cursor-3-style**, full height. Each lane = one row with:
- status dot (planning / running / blocked / done / error)
- short title (mission/branch name)
- worktree indicator
- progress glyph (steps complete / total)
- click to focus → main pane swaps to that lane's chat + diff

Rationale:
- Cursor 3 validated this with their pivot from Composer side-pane. [InfoQ]
- Devin uses a top dashboard, but that costs a click per switch — bad for the "watch 5 things at once" use case.
- Floating panels lose context.
- Below-input was tried in early Continue.dev, gets buried as chat scrolls.

Secondary surface: **"Agent Tabs"** mode that tiles 2–4 lanes side-by-side in the main pane (Cursor 3 grid layout) for power users. Toggleable.

### C2. Multi-instance switcher (multiple `r1 serve` daemons)

**Top-left workspace switcher column, Slack-style**, with daemon icons. 48 px wide column to the left of the conversation sidebar. Each icon = one daemon (could be local-machine, remote SSH host, future cloud daemon). Click to switch context; the conversation sidebar + lanes panel re-render against the selected daemon's WS.

Reasons:
- Slack's column is a proven pattern for "totally separate contexts with no shared state" — exactly the daemon model.
- Linear's workspace switcher is a dropdown, but that's optimized for "one workspace at a time"; r1 users will frequently want to glance across machines.
- Keyboard: `Cmd+1`..`Cmd+9` for first nine daemons, `Cmd+Shift+S` to toggle visibility (Slack parity).

### C3. Workdir picker

**Hybrid VS Code "Open Folder" flow**:
- Big "Open Folder" button when no workdir is set on the current daemon, identical visual treatment to vscode.dev's empty state.
- File System Access API `showDirectoryPicker()` for browsers that support it; the handle is persisted in IndexedDB so subsequent visits skip the picker (Chrome's "persistent permissions" feature). [developer.chrome.com persistent permissions]
- For Firefox/Safari (no FSA API): fall back to a server-side picker that lets the user paste a path, with autocomplete from `r1 serve --allowed-roots`.
- **One workdir per chat / lane**, Cursor-style. Switching workdir creates a new chat by default; users rarely want to mid-stream re-root.
- Recent workdirs pinned in the sidebar header (last 5).

---

## Appendix: Library shortlist for the v0 web UI

| Concern | Library | Notes |
|---|---|---|
| Markdown / streaming | `streamdown` | **anchor**; everything else fits around it |
| Component primitives | `@ai-sdk/elements` + shadcn/ui | Tool cards, reasoning blocks, plan view ready-made |
| Streaming hook | `@ai-sdk/react` `useChat` (AI SDK 6) | `message.parts` model maps directly to r1 tool schema |
| Code highlighting | `shiki` | via Streamdown |
| Math | `katex` | via Streamdown plugin |
| Diagrams | `mermaid` | via `@streamdown/mermaid` |
| Diff view | `react-diff-view` or `diff2html` | for lane diff cards |
| WS client | hand-rolled wrapper around native `WebSocket` | with subprotocol auth + reconnect+ticket refresh |
| State | `zustand` | per-daemon stores; one store instance per daemon connection |
| Routing | `react-router` v7 | nested routes for daemon → chat → lane |
| Folder picker | native `showDirectoryPicker` + IndexedDB persistence; server-side fallback | |

---

## Sources

WebSocket auth / CORS:
- [websockets RTD — Authentication](https://websockets.readthedocs.io/en/stable/topics/authentication.html)
- [Ably — Essential guide to WebSocket authentication](https://ably.com/blog/websocket-authentication)
- [Ably FAQ — token in query params](https://faqs.ably.com/is-it-secure-to-send-the-access_token-as-part-of-the-websocket-url-query-params)
- [WebSocket.org — Security guide](https://websocket.org/guides/security/)
- [Solita — Securing WebSocket Endpoints](https://dev.solita.fi/2018/11/07/securing-websocket-endpoints.html)
- [dev.to — WebSockets bypassing SOP/CORS](https://dev.to/pssingh21/websockets-bypassing-sop-cors-5ajm)
- [Pentest-Tools — CSWSH](https://pentest-tools.com/blog/cross-site-websocket-hijacking-cswsh)
- [GitHub blog — DNS rebinding attacks](https://github.blog/security/application-security/dns-rebinding-attacks-explained-the-lookup-is-coming-from-inside-the-house/)
- [host-validation middleware](https://github.com/brannondorsey/host-validation)
- [SuperTokens — WS session verification](https://supertokens.com/docs/additional-verification/session-verification/with-websocket)

Reference UIs:
- [Cursor 3 blog](https://cursor.com/blog/cursor-3)
- [Cursor 3 changelog](https://cursor.com/changelog/3-0)
- [InfoQ — Cursor 3 agent-first interface](https://www.infoq.com/news/2026/04/cursor-3-agent-first-interface/)
- [DEV — Cursor 3 Glass replaced Composer](https://dev.to/gabrielanhaia/cursor-3-glass-replaced-composer-with-an-agents-window-1pcg)
- [Cognition — Devin 2.0](https://cognition.ai/blog/devin-2)
- [Devin docs](https://docs.devin.ai/get-started/devin-intro)
- [OpenAI — ChatGPT release notes](https://help.openai.com/en/articles/6825453-chatgpt-release-notes)
- [Apps SDK UI guidelines](https://developers.openai.com/apps-sdk/concepts/ui-guidelines)
- [IntuitionLabs — Conversational AI UI comparison 2025](https://intuitionlabs.ai/articles/conversational-ai-ui-comparison-2025)
- [Bolt vs Lovable vs v0 2026 — UI Bakery](https://uibakery.io/blog/bolt-vs-lovable-vs-v0)
- [LibreChat artifacts](https://www.librechat.ai/docs/features/artifacts)
- [LibreChat 2026 roadmap](https://www.librechat.ai/blog/2026-02-18_2026_roadmap)
- [Open WebUI tools](https://docs.openwebui.com/features/extensibility/plugin/tools/)
- [billmei/every-chatgpt-gui (catalog)](https://github.com/billmei/every-chatgpt-gui)

Libraries:
- [vercel/streamdown](https://github.com/vercel/streamdown)
- [streamdown.ai](https://streamdown.ai/)
- [AI Elements](https://elements.ai-sdk.dev/)
- [Vercel AI SDK 6 release](https://vercel.com/blog/ai-sdk-6)
- [useChat reference](https://ai-sdk.dev/docs/reference/ai-sdk-ui/use-chat)

File System Access:
- [Chrome — File System Access API](https://developer.chrome.com/docs/capabilities/web-apis/file-system-access)
- [Chrome — Persistent permissions](https://developer.chrome.com/blog/persistent-permissions-for-the-file-system-access-api)
- [MDN — File System API](https://developer.mozilla.org/en-US/docs/Web/API/File_System_API)
- [WICG explainer](https://github.com/WICG/file-system-access/blob/main/EXPLAINER.md)

Workspace switcher:
- [Slack — Switch between workspaces](https://slack.com/help/articles/1500002200741-Switch-between-workspaces)
- [Linear Agent changelog Mar 2026](https://linear.app/changelog/2026-03-24-introducing-linear-agent)
- [LukeW — Agent management interface patterns](https://www.lukew.com/ff/entry.asp?2106=)
