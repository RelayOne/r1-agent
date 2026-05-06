# RT-AGENTIC-TEST: Agentic Interfaces for Driving and Testing r1-agent UIs

**Date:** 2026-05-02
**Scope:** Make every r1-agent UI surface (CLI, TUI, web, desktop) programmatically driveable so external AI agents can use and test them.
**Author:** Research-task (WebSearch + WebFetch citations inline).

---

## TL;DR Recommendation

**Adopt MCP as the primary agentic protocol; supplement with Playwright MCP for the web UI; keep teatest+fixture-driven JSON-RPC for the TUI.** Standardize on a single principle:

> **"Every action a human can take through a UI MUST have a documented, idempotent, schema-validated agent equivalent in the MCP server. The UI is a view over the API; never the reverse."**

Concretely:

1. Extend `internal/mcp/` with a new `r1_server.go` that exposes lanes/cortex/sessions/missions/worktrees as **tools** (mutations) and **resources** (read-only state).
2. For r1d's web UI, expose **Playwright MCP** (`@playwright/mcp`) so any agent (Claude, Codex, browser-use) can drive it deterministically through accessibility snapshots.
3. For the Bubble Tea TUI, build a thin JSON-RPC harness on top of `charmbracelet/x/exp/teatest` (`Send` + `WaitFor` + `FinalModel`) and expose it as MCP tools (`tui_press_key`, `tui_snapshot`, `tui_assert`).
4. Bake **deterministic ARIA labels + stable test IDs** into every UI control so DOM-driven agents (Stagehand-class) outperform vision-driven ones (which lag DOM by 12–17 pp on common tasks).
5. Adopt **agent contract tests**: every UI feature ships with a Gherkin-style scenario file an LLM judge can execute end-to-end.

Standardize on: **MCP (Model Context Protocol, spec 2025-11-25)** as the wire format for every programmatic surface. It is the de-facto "USB-C for AI-native applications" in 2026 and is now backed by Microsoft (Playwright MCP), Storybook, Cloudflare Agents, and OpenAI's Apps SDK.

---

## 1. MCP Server Patterns for UI

### Best practices (2026)

- **Goal-shaped tools, not API mirrors.** "Do not treat your MCP server as a wrapper around your full API schema. Build tools optimized for specific user goals and reliable outcomes. Fewer, well-designed tools often outperform many granular ones, especially for agents with small context windows or tight latency budgets." [^essamamdani] [^webfuse-cheat]
- **Scoped permissions per server.** Multiple narrowly-scoped MCP servers > one fat server. Reduces over-privileged access and makes auditing tractable. [^essamamdani]
- **Token-aware tool docs.** Detailed parameter descriptions reduce errors but balloon context. Use schema introspection (`mytool schema <command>`) so the agent fetches docs on demand. [^clig]
- **Read-only by default.** "Always prefer read-only modes for untrusted agents." Mutations gated by explicit capability flag (`--caps=write`). Mirrors Playwright MCP's `--caps=vision`/`--caps=testing` opt-ins. [^playwright-mcp-docs]
- **Errors as data.** "A 429 from GitHub should not crash the agent; it should produce a tool result the model can reason about." Map every internal error to a stable error taxonomy node (we already have `stokerr/`). [^essamamdani]

### Tools to add to `internal/mcp/` for r1's UI

| Category | Tool name | Purpose |
|---|---|---|
| Lanes | `lanes_list`, `lanes_get`, `lanes_create`, `lanes_pause`, `lanes_resume`, `lanes_terminate` | Drive harness/stances lifecycle |
| Cortex | `cortex_state`, `cortex_dispatch`, `cortex_subscribe` | Read consensus loop state, enqueue events |
| Sessions | `session_list`, `session_get`, `session_attempts`, `session_diff` | Drive session/replay surfaces |
| Missions | `mission_start`, `mission_status`, `mission_converge`, `mission_artifacts` | Drive `mission/` runner |
| Worktrees | `worktree_list`, `worktree_diff`, `worktree_merge`, `worktree_destroy` | Drive worktree surface |
| Bus | `bus_publish`, `bus_subscribe` (SSE) | Direct event bus access for tests |
| Verify | `verify_run`, `verify_protected_files`, `verify_scope` | Drive `verify/` pipeline |
| TUI | `tui_press_key`, `tui_snapshot`, `tui_assert_view`, `tui_get_model` | Drive Bubble Tea (see §3) |
| Web | (delegate to Playwright MCP) | Drive r1d browser UI |

Pattern: every tool returns `{ok: bool, data, error_code, error_message, links: {self, related}}` — same envelope as Slack's Web API. [^slack-api]

### Reference implementations
- `microsoft/playwright-mcp` — 50+ tools, structured accessibility snapshots, opt-in capability flags. [^playwright-mcp-repo]
- `storybook/mcp` — exposes component metadata, usage snippets, types. Establishes the "self-healing loop" pattern: agents run interaction+a11y tests, fix their own bugs before review. [^storybook-mcp]
- `kkokosa/repl-mcp` — interactive REPL for testing MCP servers (use this in r1's CI). [^repl-mcp]
- `f/mcptools` — CLI client for stdio + HTTP MCP transports (drop-in for r1's `cmd/r1` discovery). [^mcptools]
- `HKUDS/CLI-Anything` — wraps any CLI as MCP server; reference for our `cmd/r1` → MCP bridge. [^cli-anything]

---

## 2. Browser Automation by Agents (2026)

### Landscape

> "Five browser-control agent stacks dominate 2026: Playwright + Claude (DX leader, deterministic + agentic), Stagehand (cleanest abstraction over Playwright), Browserbase (managed runtime + CDP-as-a-service), Anthropic Computer Use (vision-driven), and OpenAI Computer-Using-Agent (cloud-only, OpenAI-locked)." [^digitalapplied]

> "Browser-automation agents bifurcated in 2025-2026 between DOM-driven approaches (Playwright + Claude, Stagehand, Browserbase) and vision-driven approaches (Anthropic Computer Use, OpenAI CUA), with **DOM-driven stacks being 12–17 percentage points more reliable** on common tasks." [^digitalapplied]

### Maturity matrix (May 2026)

| Stack | Maturity | Strengths | Weaknesses |
|---|---|---|---|
| **Playwright + Playwright MCP** | GA, official Microsoft | Deterministic, structured a11y snapshots, 50+ tools, Docker image | Token-heavy a11y trees |
| **Stagehand v3** | Production (Browserbase) | Natural-language `act/extract/observe/agent` primitives, CDP-native, 44% faster than v2 | Newer, smaller community |
| **browser-use** | Mature OSS | Python-first, ChatBrowserUse model 3-5× faster on browser tasks, agentic loop built-in | Python-only; less deterministic |
| **Anthropic Computer Use** | Beta (research preview) | Works on any pixels (desktop apps too) | OSWorld 14.9%; experimental, error-prone |
| **OpenAI Operator (CUA)** | Beta, cloud-only | WebVoyager 87%, WebArena 58.1%, OSWorld 38.1% | OpenAI-locked, no self-host |

### Recommendation for r1

- **Web UI → Playwright MCP.** Mature, official, schema-validated, accessibility-snapshot-first. r1 already has Playwright in CI patterns. The MCP server boots with `npx @playwright/mcp@latest`. [^playwright-mcp-docs] [^playwright-mcp-repo]
- **Don't adopt Computer Use yet** for our own test harness. The 14.9% OSWorld score and beta status make it unsuitable as a CI gate. Revisit when Anthropic ships Windows support (expected Q3 2026). [^tech-insider-cu]
- **Stagehand as fallback** for natural-language flake-debugging when DOM selectors break.

---

## 3. TUI Testing/Driving by Agents

### State of teatest

Charmbracelet ships `teatest` (in `charmbracelet/x/exp/teatest`), the "official" Bubble Tea harness. It provides:

- `Send(msg)` — inject keyboard/tea.Msg into running program
- `WaitFor(condition, timeout)` — poll output until predicate matches
- `FinalOutput()` / `FinalModel()` — assert end state
- Golden-file output snapshotting [^teatest-blog]

**Gaps for agent driving:**

- teatest is **experimental** ("`x/exp` namespace, no compat guarantees"). [^teatest-blog]
- Heavy and "less deterministic when commands trigger asynchronous effects." [^teatest-blog]
- Some teams build custom harnesses (e.g. Noteleaf's `TUITestSuite` struct) for "no real terminal, no threads, no default renderer" headless control. [^patternmatched]

### Recommendation: teatest + JSON-RPC + MCP shim

1. Wrap `tea.Program` behind a thin RPC layer (`internal/tui/rpc/`):
   - `tui.start(model_name, args)` → session_id
   - `tui.send_key(session_id, key)` → ack
   - `tui.snapshot(session_id)` → `{view: string, model: json, focus: string}`
   - `tui.wait_for(session_id, regex|json_path, timeout)` → match
   - `tui.terminate(session_id)`
2. Surface those RPC methods as MCP tools (`tui_press_key`, `tui_snapshot`, etc., per §1).
3. Use `lipgloss.SetColorProfile(termenv.Ascii)` in the RPC wrapper so output is deterministic in CI. [^teatest-blog]
4. For high-fidelity tests, also expose `tui.get_model(session_id, json_path)` for direct `tea.Model` introspection (matches the Noteleaf pattern). [^patternmatched]

This gives external agents the same "send key, read view, assert model" loop they already use for browsers — but over MCP, no terminal emulator required.

---

## 4. Component Testing — Storybook-style for AI

### Status

Storybook 9 (2026) is "a full-fledged component testing platform that lets you write interaction tests, run visual regression checks, validate accessibility, and execute everything in CI without spinning up your entire application." [^autonoma] **Storybook MCP** (official, March 2026) exposes:

- Component metadata
- Usage snippets
- Type info
- Stories as fixtures

…all to coding agents in "an optimized payload." Benchmarks show "better quality code faster with fewer tokens." [^storybook-mcp] [^storybook-ai]

### "Agent contract testing" as an emerging pattern

> "AI agents excel at API contract testing along with repetitive, structured testing like regression suites, smoke tests, cross-browser validation, and visual regression checks." [^pctech-2026]

> "Three patterns are converging for 2026: framework standardization, multi-agent testing needs, and autonomous test generation. … The fourth wave emerging now is **goal-oriented prompt testing**: you describe what the application should do in natural language, and the agent determines how to test it, executes the tests, and reports results." [^sitepoint]

### Recommendation

- For r1d's web UI, adopt **Storybook + Storybook MCP** as the component contract layer. Every component ships with stories that double as agent fixtures.
- Author **goal-oriented test files** (`*.agent.test.md`) — Gherkin-flavored markdown describing intent. An LLM judge consumes them and drives Playwright MCP / TUI MCP to verify.
- Run **autonomous self-healing loops**: when a story's a11y or interaction test fails, the agent gets the failure + diff and patches before opening a PR. [^storybook-mcp]

---

## 5. Programmatic Web UI Driving Without a Browser

### The Slack pattern

Slack's Web API "is a collection of HTTP RPC-style methods, all with URLs in the form `https://slack.com/api/METHOD_FAMILY.method`." It is RPC-shaped JSON, not strict JSON-RPC 2.0. Every UI action is mirrored 1:1 by a Web API method (post message, react, list channels, etc.). For real-time, `apps.connections.open` returns a Socket Mode WebSocket URL. [^slack-api]

> All Web API responses contain a JSON object with a top-level boolean `ok` indicating success, with `error` carrying a short machine-readable code on failure. [^slack-api]

### Recommendation

For r1d's web UI, expose two parallel surfaces:

1. **HTTP RPC (the Slack pattern)**: `POST /api/<domain>.<verb>` returns `{ok, data, error}`. Every button in the UI calls one of these. The agent calls the same URL.
2. **WebSocket/SSE event stream**: `GET /api/events` for real-time push (subscriber sees the same events the React client sees).
3. Wrap both in MCP via a thin adapter (`internal/mcp/r1d_web.go`) so agents that prefer MCP get tool-call ergonomics.

This guarantees feature parity: if a UI element exists without a corresponding RPC + MCP tool, the build fails (lint rule).

---

## 6. Test Harness DSL

### Options surveyed

- **Cucumber/Gherkin** — Given/When/Then plain-English scenarios. 2026 platforms (BlinqIO, Testsigma) target BDD teams; AI virtual testers "generate, maintain, and extend coverage in the language teams already speak." Self-healing engines reduce maintenance ~90%. [^pctech-2026] [^accelq]
- **Playwright codegen** — records UI interactions, emits Playwright code. Useful for bootstrapping but not declarative.
- **Goal-oriented prompts** — natural-language intent, agent decides how to test. Emerging "fourth wave." [^sitepoint]

### Recommendation

Adopt **Gherkin-flavored markdown** (`*.agent.feature.md`) as the test DSL. Pros:

- LLMs already speak Gherkin fluently.
- Steps map cleanly to MCP tool calls (`When I press <key>` → `tui_press_key`).
- Self-healing-friendly: if a step breaks because of a UI change, the LLM judge proposes a patch.

Avoid building a bespoke DSL. The marginal value is low and the LLM-friendliness of Gherkin is already proven. [^kobiton]

---

## 7. Reference Project Deep-Dives

### browser-use ([github.com/browser-use/browser-use](https://github.com/browser-use/browser-use))

- Python-first agentic browser control library.
- Architecture: `Agent(task, llm, browser)` orchestrates a perceive → decide → act loop.
- LLMs supported: ChatBrowserUse (proprietary, "3-5× faster + SOTA accuracy"), Claude, Gemini.
- Browser control uses **Playwright** under the hood.
- Action interface: DOM analysis identifies clickable targets; agents reference them by index (`browser-use click 5`, `browser-use type "Hello"`).
- Custom tools via `@tools.action(description=…)` decorator. [^browser-use-repo]

### Microsoft Playwright MCP ([github.com/microsoft/playwright-mcp](https://github.com/microsoft/playwright-mcp))

- Official Microsoft. `npx @playwright/mcp@latest`. Docker: `mcr.microsoft.com/playwright/mcp`.
- 50+ tools across: core automation, tabs, network, storage, devtools, vision (opt-in), testing (opt-in).
- **Accessibility snapshots, not screenshots** — operates on structured a11y trees with role + accessible name + ARIA. Deterministic targeting. No vision model required. [^playwright-mcp-repo]
- Best for: "exploratory automation, self-healing tests, or long-running autonomous workflows where maintaining continuous browser context outweighs token cost concerns." [^playwright-mcp-repo]
- Trade-off: a11y trees are token-heavy. For deterministic CI, a CLI invocation can be more token-efficient than MCP. [^playwright-mcp-repo]

### Anthropic Computer Use (May 2026)

- Beta / research preview. Available across Sonnet 3.5 v2 / 3.7 / 4 / 4.5 / 4.6, Haiku 4.5, Opus 4. [^anthropic-cu-docs]
- Claude Code 2.1.76 ships computer use under three features: scheduled tasks (`/loop`), background agents with worktree isolation, and "Dispatch" (autonomous use while user is away). [^tech-insider-cu]
- macOS desktop preview live (March 2026); Windows expected Q3 2026. [^tech-insider-cu]
- OSWorld 14.9% (screenshot-only) — meaningfully behind OpenAI Operator (38.1%). Don't gate CI on it yet. [^workos-cu-vs-cua]

### Devin's test harness

- Devin uses a **shell + code editor + browser** sandbox. SWE-bench eval: agent runs end-to-end on a GitHub issue → reset test files → diff → apply patch → run eval.
- Open-source eval harness: `github.com/CognitionAI/devin-swebench-results`. [^cognition-swe]
- Devin scored 13.86% on SWE-bench at launch; since surpassed by Claude/GPT-4o/Gemini.
- Pattern to steal: **canonical eval loop** = (run agent) → (reset) → (extract diff) → (apply test patch) → (run tests). Exactly mirrors r1's `verify/` package.

### Stagehand v3 ([browserbase/stagehand](https://github.com/browserbase/stagehand))

- Four primitives: `act`, `extract`, `observe`, `agent`. [^stagehand-2026]
- v3 dropped Playwright dependency, went CDP-native. 44.11% faster on iframe + shadow-root.
- Tightly integrated with Browserbase managed cloud (stealth, session recording, proxy rotation).
- Use as a **secondary** agent driver when natural-language resilience matters more than determinism. [^stagehand-2026]

---

## 8. Good UX for Agent-Driveable UIs

### Core principle

> "If your tests can't find it with a role-based selector, some of your users probably can't either. Fix the UI rather than work around it in the test by adding a `data-testid`." [^tkdodo]

Accessibility-first design **is** agent-friendly design. WAI-ARIA roles + accessible names + states + properties give a deterministic semantic tree that a11y tools, screen readers, **and** Playwright MCP all consume. [^aria-apg]

### Concrete patterns

1. **Every actionable element has a stable accessible name.** `aria-label` or visible text. No "Click me" — meaningful verb + noun. [^aria-apg]
2. **No random-suffix IDs.** Avoid the templating-system anti-pattern `cta_heading_349234`. Use deterministic IDs derived from content + scope (we already have `contentid/`). [^aria-apg-impl]
3. **Hierarchy via `aria-labelledby` / `aria-describedby`** — gives agents the same parent-child semantics they get from a11y trees. [^aria-apg]
4. **`data-testid` only as a last resort** — for dynamic content (modals, dropdowns, dynamically loaded lists) where role+name isn't unique. Strip in production builds. [^bugbug-testid]
5. **Keyboard parity.** Every action reachable by keyboard. Playwright MCP, Computer Use, browser-use, and human assistive tech all converge on keyboard. [^aria-apg]
6. **State on the element, not the visual.** `aria-pressed`, `aria-expanded`, `aria-busy` — agents read these directly. Don't encode "loading" purely as a CSS class. [^aria-techniques]
7. **Predictable focus order.** Skip-links, focus rings, no focus traps. Agents (and a11y users) navigate by tab. [^aria-apg]
8. **Idempotent actions.** Re-clicking a button twice should be safe. Mirrors MCP best-practice "errors as data" — agents retry. [^essamamdani]

### r1-specific applications

- For the web UI: every React component ships with a Storybook story declaring `role`, `name`, expected `aria-*` states. Storybook MCP makes this machine-readable. [^storybook-mcp]
- For the TUI: emit a synthetic accessibility tree alongside the rendered view (key = stable element ID, value = role + name + state). MCP tool `tui_snapshot` returns both view string + accessibility tree. The agent never has to OCR the box-drawing characters.
- For the CLI: `--json` on every command, NDJSON for streams, `r1 schema <command>` for introspection. Match the [clig.dev](https://clig.dev) baseline + the agent-native checklist. [^clig] [^undercurrent]

---

## Recommended Approach (one-pager)

### Architecture

```
External Agent (Claude/Codex/browser-use)
        │
        │ MCP (stdio | SSE | HTTP)
        ▼
┌────────────────────────────────────────┐
│  r1 MCP Server (internal/mcp/r1_server.go)         │
│  - lanes / cortex / sessions / missions / worktrees │
│  - bus pub/sub, verify, plan/scope                  │
│  - tui_* tools (delegates to teatest harness)       │
└────┬──────────────────────┬────────────┘
     │                      │
     │ delegates            │ delegates
     ▼                      ▼
┌──────────────┐    ┌────────────────────────┐
│ TUI harness  │    │ Playwright MCC (web UI)│
│ (teatest +   │    │ (npx @playwright/mcp)  │
│  RPC shim)   │    │                        │
└──────────────┘    └────────────────────────┘
```

### Standardize on

- **Protocol:** MCP (modelcontextprotocol.io spec 2025-11-25). [^mcp-spec]
- **Browser driver:** Playwright + Playwright MCP (Microsoft official). [^playwright-mcp-repo]
- **TUI driver:** teatest + custom JSON-RPC shim → MCP tools.
- **DSL:** Gherkin-flavored markdown (`*.agent.feature.md`).
- **Component contracts:** Storybook + Storybook MCP for the web UI.
- **Locator strategy:** semantic ARIA roles → accessible names → deterministic IDs → `data-testid` (last resort).

### Governing principle

> **Every action a human can take through a UI MUST have a documented, idempotent, schema-validated agent equivalent reachable through MCP. The UI is a view over the API; never the reverse.**

Lint rule: any new UI control without a corresponding MCP tool fails CI.

---

## Citations

[^essamamdani]: [The Complete Guide to Model Context Protocol (MCP) in 2026](https://www.essamamdani.com/blog/complete-guide-model-context-protocol-mcp-2026) — Essa Mamdani, 2026.
[^webfuse-cheat]: [MCP Cheat Sheet (2026)](https://www.webfuse.com/mcp-cheat-sheet) — Webfuse.
[^mcp-spec]: [MCP Specification 2025-11-25](https://modelcontextprotocol.io/specification/2025-11-25) — modelcontextprotocol.io.
[^playwright-mcp-docs]: [Playwright MCP](https://playwright.dev/docs/getting-started-mcp) — playwright.dev.
[^playwright-mcp-repo]: [microsoft/playwright-mcp](https://github.com/microsoft/playwright-mcp) — GitHub.
[^storybook-mcp]: [Storybook MCP sneak peek](https://storybook.js.org/blog/storybook-mcp-sneak-peek/) — Storybook blog.
[^storybook-ai]: [Storybook for AI](https://storybook.js.org/ai) — storybook.js.org.
[^repl-mcp]: [kkokosa/repl-mcp](https://github.com/kkokosa/repl-mcp) — GitHub.
[^mcptools]: [f/mcptools](https://github.com/f/mcptools) — GitHub.
[^cli-anything]: [HKUDS/CLI-Anything](https://github.com/HKUDS/CLI-Anything) — GitHub.
[^slack-api]: [Slack Web API](https://api.slack.com/web) — api.slack.com.
[^digitalapplied]: [Browser Automation AI Agents: Playwright vs Stagehand (2026)](https://www.digitalapplied.com/blog/browser-automation-ai-agents-playwright-stagehand-2026) — Digital Applied.
[^browser-use-repo]: [browser-use/browser-use](https://github.com/browser-use/browser-use) — GitHub.
[^anthropic-cu-docs]: [Computer use tool — Claude API Docs](https://docs.anthropic.com/en/docs/build-with-claude/computer-use).
[^tech-insider-cu]: [Claude Computer Use 2026](https://tech-insider.org/anthropic-claude-computer-use-agent-2026/).
[^workos-cu-vs-cua]: [Anthropic's Computer Use versus OpenAI's CUA](https://workos.com/blog/anthropics-computer-use-versus-openais-computer-using-agent-cua) — WorkOS.
[^stagehand-2026]: [Stagehand vs Browser Use vs Playwright (2026)](https://www.nxcode.io/resources/news/stagehand-vs-browser-use-vs-playwright-ai-browser-automation-2026) — NxCode.
[^teatest-blog]: [Writing Bubble Tea Tests](https://charm.land/blog/teatest/) — Charm.
[^patternmatched]: [Testing Bubble Tea Interfaces](https://patternmatched.substack.com/p/testing-bubble-tea-interfaces) — Pattern Matched.
[^autonoma]: [Storybook vs Playwright: Component Testing 2026](https://getautonoma.com/blog/storybook-vs-playwright-component-testing) — Autonoma.
[^pctech-2026]: [Best AI Agents for Software Testing in 2026](https://pctechmag.com/2026/04/best-ai-agents-for-software-testing-in-2026/) — PC Tech Magazine.
[^sitepoint]: [AI Agent Testing Automation: Developer Workflows for 2026](https://www.sitepoint.com/ai-agent-testing-automation-developer-workflows-for-2026/) — SitePoint.
[^accelq]: [Top 13 BDD Testing Tools (2026)](https://www.accelq.com/blog/bdd-testing-tools/) — AccelQ.
[^kobiton]: [Cucumber Testing: A Key to Generative AI](https://kobiton.com/blog/cucumber-testing-a-key-to-generative-ai-in-test-automation/) — Kobiton.
[^cognition-swe]: [Cognition SWE-bench technical report](https://cognition.ai/blog/swe-bench-technical-report) — Cognition.
[^aria-apg]: [ARIA Authoring Practices Guide](https://www.w3.org/WAI/ARIA/apg/) — W3C WAI.
[^aria-apg-impl]: [Which ARIA Attributes Should I Use?](https://www.easternstandard.com/blog/which-aria-attributes-should-i-use-accessibility-and-aria-tags-for-common-ui-patterns/) — Eastern Standard.
[^aria-techniques]: [ARIA Techniques | Techniques for WCAG 2.0](https://www.w3.org/TR/WCAG20-TECHS/aria) — W3C.
[^bugbug-testid]: [Why Should You Use data-testid Attributes?](https://bugbug.io/blog/software-testing/data-testid-attributes/) — Bugbug.
[^tkdodo]: [Test IDs are an a11y smell](https://tkdodo.eu/blog/test-ids-are-an-a11y-smell) — TkDodo.
[^clig]: [Command Line Interface Guidelines](https://clig.dev/) — clig.dev.
[^undercurrent]: [Rewrite Your CLI for Agents](https://www.theundercurrent.dev/p/rewrite-your-cli-for-agents-or-get) — The Undercurrent.
