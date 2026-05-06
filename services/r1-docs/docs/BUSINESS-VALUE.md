# Business Value

## Linear chat is over. Your AI thinks in parallel.

Every coding assistant on the market today does the same thing: you type a
message, the model streams a single thread of reasoning back, you read it,
you type the next thing. The agent is busy thinking; you are busy waiting.
The agent finishes thinking; you are busy reading. The conversation is a
single file you take turns writing in.

r1 changes the shape of the conversation.

When you send a message, the main agent gets to work. Beside it — at the
same time, sharing the same context — six specialist sub-cognitions run in
parallel. One looks through your repository's memory for prior decisions
that apply. One drafts updates to the project plan. One drafts up to three
clarifying questions in case the request was ambiguous. One watches for
governance violations and refuses end-of-turn if any critical rule fires.
One curates new facts worth remembering. One journals everything to a
durable WAL so a daemon restart doesn't lose state.

You don't see linear text. You see **lanes** — parallel cognitive threads,
each with a status, a stream of activity, a cost tick, and a set of pinned
findings. When you want to focus on one, you pin it. When two of them are
both interesting, you tile them side by side. When one of them is taking
too long, you kill it. When the whole agent is going down a wrong path,
you type "actually, do X instead" mid-stream and the agent's Router decides
in real time whether to interrupt the current turn, steer it softly, queue
your message as a separate task, or just have a conversation about it.

This isn't a UI redesign. This is a different shape of agentic computation.

## Why this is the right time

Three things changed in the last year that make parallel cognition practical:

**Prompt caching with 1-hour TTLs.** Anthropic's cache lets you keep a large
shared system prompt warm across N concurrent calls and pay 10% of input
tokens on cache hits. In 2024 you couldn't run six concurrent specialists
without paying six times for the same context. In 2026 you can.

**Goal-shaped tool definitions.** The MCP standard, the AI SDK 6 `useChat`
hook, and the `@ai-sdk/elements` card library converged on a vocabulary —
tool calls, reasoning blocks, plan updates, message parts — that is
agent-readable AND human-readable. The same envelope drives the UI and
drives external agents driving the UI.

**Long-running daemons replacing per-shell processes.** Watchman, devcontainers,
language servers — every productive developer tool moved to a per-user
singleton daemon model. r1 follows the same pattern. One process,
many sessions, instant switch, journal-backed reconnect.

If you tried to ship parallel-cognition agents in 2023, you would have
spent your runway on infrastructure. The infrastructure exists now.

## Concrete user scenarios

### Scenario 1 — The mid-turn redirect

You're chatting with r1 about adding a JWT auth middleware. The agent
starts generating a Go file. Halfway through, you realize the project
already has an auth library you want to reuse.

In a linear chat tool, you wait for the agent to finish, scroll through
several screens of generated code, type "wait, use lib/auth/jwt.go
instead", wait for the agent to apologize, regenerate, and verify. The
partial work is now committed history; the model has to rationalize it.

In r1, you type "actually use lib/auth/jwt.go" while the stream is mid-flight.
The Router (a Haiku 4.5 LLM call with four tools) reads your message in
under two seconds, picks `interrupt`, cancels the in-flight turn, drains
the SSE stream cleanly, drops the partial assistant message **before it
ever enters the committed history**, and appends a synthetic user message:
"Interrupted. New direction: use lib/auth/jwt.go." The agent picks up
fresh, with no rationalization debt.

### Scenario 2 — The memory you didn't know you had

A junior on your team asks r1 to set up Postgres connection pooling. r1's
MemoryRecall Lobe runs in parallel, searches your wisdom store, finds a
six-month-old decision: "We standardized on pgx with a max-conns of 25;
this has been load-tested." It surfaces as a Note in the lane sidebar
before the main agent has even chosen a library.

The junior sees the prior decision attached to this turn. The main agent
sees it in its context. The chosen library is consistent with team
standards on the first try. No one had to remember to look.

### Scenario 3 — The clarifying question, asked at the right moment

You ask r1 to "deploy it." Deploy what, where? In a normal chat the agent
either guesses (often wrong) or asks immediately and stalls. The
ClarifyingQ Lobe runs in parallel: it detects actionable ambiguity, drafts
up to three clarifying questions tagged by category (scope, constraint,
preference, data, priority), but holds them until the action loop is idle
— never mid-tool-call. When the agent reaches a safe boundary, you see
two pinned questions: "Which environment? prod or staging?" and "Should
this be a blue-green deploy or in-place?" You answer both in one message;
the work resumes correctly.

### Scenario 4 — Three projects, one window

You have three repos open: a Go backend, a React frontend, an infra repo.
In conventional tools you have three terminal tabs, three IDE windows,
three running agents — each with its own context, none of them aware of
each other. Cost tracking is a spreadsheet you forget to fill out.

In r1, one daemon (`r1 serve`) hosts three sessions, each bound to its
working directory. Cmd+1, Cmd+2, Cmd+3 in the web UI switch between them.
A pinned lane in session 1 keeps streaming while you're looking at session
2. Cost rolls up per session and across all three. Daemon restart? Each
session's `journal.ndjson` replays automatically; reconnecting clients
emit `Last-Event-ID` and resume mid-stream.

### Scenario 5 — An agent driving r1

You have a sweep of 80 SWE-bench tasks. You want a runner agent that
spawns r1 sessions, sends each task, watches the lanes, kills runaways,
and collects results — all without a human in the loop.

In r1, that runner agent is just an MCP client. It calls
`r1.session.start({workdir})`, then `r1.session.send({session_id,
message})`, then `r1.lanes.subscribe({session_id})` to stream events,
then `r1.lanes.kill({session_id, lane_id})` when a lane misbehaves. Every
UI action a human can take has an idempotent schema-validated MCP
equivalent. The CI lint refuses to merge a UI component that doesn't have
a corresponding MCP tool. The runner agent reads the same protocol your
human users do.

### Scenario 6 — Reading the agent's mind

When the model is working on a hard task, you want to see what it's doing.
In linear chat tools you see the streamed text and infer; in agentic UIs
you sometimes see tool calls. Neither tells you what the agent is
thinking *about* — only what it's saying.

In r1 you see the cortex Workspace. The PlanUpdate Lobe pinned a
suggestion to add a new task. The MemoryRecall Lobe pinned a relevant
prior decision. The RuleCheck Lobe pinned a critical drift warning that's
about to refuse end-of-turn. You can read the agent's working memory in
real time, on a six-pane tile grid, alongside the main thread.

## Why this matters commercially

### Faster convergence

Six cognitions per turn instead of one means fewer turns to converge on
the right output. Memory recall happens in parallel with planning;
clarifying questions get asked while the main agent is still drafting.
Each cognition is small, cheap (Haiku 4.5 with 90% cache hit rate), and
specialized. The aggregate budget is capped at 30% of the main thread's
output tokens — adding cognitive depth costs less than adding turn count.

### No context loss

Every coding agent on the market today works around context limits with
**subagent isolation**: spin off a sub-task with a tiny context, hope the
sub-task gets the right result, hope it can be summarized back. r1's
Lobes are not subagents. They share the **same** message history, the
**same** system prompt cache breakpoint, the **same** tool ordering. They
can't lose context because they're operating on the same context.

### Agent-driveable everything

Every action a human takes through a UI has an idempotent, schema-validated
MCP equivalent. This isn't a "we have an API" claim. This is a CI lint
that fails the build when a React component, a Bubble Tea key handler, or
a Tauri menu item doesn't have a corresponding MCP tool. UI is a view
over the API; never the reverse. Buyers shopping for an agent platform
that won't lock them into a single UI vendor get exactly what they need.

### Same daemon, three surfaces

The Bubble Tea TUI, the React + Vite web app, and the Tauri 2 desktop
shell all talk to the same `r1d` daemon over the same lanes-protocol JSON
envelope. A single user can keep the TUI open in their terminal, the web
app pinned in a browser tab, and the desktop app pop-out windows on a
secondary monitor — all driving the same sessions, all seeing the same
state, all backed by the same journal-on-disk. A team can adopt whichever
surface matches their workflow.

### Trust without vendor lock-in

The harness is fully self-hostable. The STEWARDSHIP commitment is on the
record: no functional feature migrates from self-hosted to cloud-only,
ever. Encryption-at-rest is a layered concern with its own spec. The
content-addressed ledger and journaled WAL mean every decision is
auditable; every signed skill pack is verifiable at runtime; the
`os.Chdir` lint is a CI gate before multi-session is enabled — because
silently leaking workdir between concurrent sessions is the kind of bug
buyers stop trusting you over. We catch it at compile time.

## What buyers care about — and what r1 ships against each

**Faster engineering throughput.** Parallel cognition + journaled
multi-session + agent-driveable everything compress what used to take
many turns into fewer. Scoped: faster convergence per ticket, more
tickets per day.

**Auditable AI decisions.** Every cognition publishes typed Notes; every
Note is durable in the WAL; every action emits a hub event with a
ledger-node ID. The content-addressed ledger is the source of truth. When
a prod incident lands and you need to know "why did the agent do X", you
have a Merkle-chained answer instead of a vibe.

**Governance at scale.** Anti-deception contract injected into every
worker prompt; honeypot gates abort end-of-turn on canary leaks and
destructive shell; the supervisor rules engine fires structured policy
events; signed skill packs verify before runtime. Operators who answer to
compliance teams have receipts, not promises.

**Lower per-session cost.** Cache pre-warm pumps a `max_tokens=1` request
on cortex start and every 4 minutes; main + Lobe + warming requests
present byte-identical system blocks; Anthropic prompt cache hits at 10%
of input-token cost. The 30%-of-main per-turn budget cap on Lobe output
keeps the cognitive expansion bounded.

**Choice of model.** Five-provider fallback chain (Claude → Codex →
OpenRouter → direct API → lint-only). Cross-family adversarial review.
Subscription pool with circuit breaker per provider. No buyer is
single-vendor by accident.

**Choice of surface.** TUI, web, desktop. CLI alone if you want it. MCP
client if you want to drive it from another agent. The wire protocol is
the contract.

**Reproducibility.** Every release tagged and signed (cosign keyless
OIDC). Pinned dependency lockfiles across Go and Node. The `go build`,
`go test`, `go vet` triple is the gate; CI also runs `-race`,
`golangci-lint`, `govulncheck`, `gosec`. Race-clean across the whole
repo.

## What r1 is explicitly not

- **Not a multi-agent committee.** Published MAST data (41–86.7% failure
  rates in real multi-agent deployments; 70% accuracy degradation from
  blind agent-adding) says the prevailing "many cooperating agents"
  pattern is how you lose. r1 runs one strong implementer per task, pairs
  it with a cross-family adversarial reviewer, and treats reviewer dissent
  as a merge-blocking signal. The cortex Lobes are not agents — they are
  cognitive specialists writing into a shared workspace, not consensus
  partners. The merge model is **agent-decides** (the Router), not
  vote-and-blend.

- **Not a wrapper around a single model.** The model is a parameter of
  the workflow. SWE-bench Pro shows the same model swings ~15 points
  across scaffolds. The harness is the product.

- **Not a hosted-only platform.** The daemon binds loopback by default;
  remote access is a separate spec; encryption-at-rest is a separate
  spec. Self-hosting is the floor, not the ceiling.

- **Not a vibe-coded "many cooperating agents" toy.** Every cognition
  publishes structured Notes. Every Note is content-addressed. Every
  decision is in the ledger.

## Status

### Done
- Governed mission runtime (plan → execute → verify → review → commit) with
  cross-model adversarial review.
- Append-only content-addressed ledger, durable WAL event bus, supervisor
  rules engine, evidence model.
- Deterministic skill substrate + signed pack distribution + HTTP registry
  with runtime verification.
- 5-provider model resolver, subscription pool, prompt-injection hardening,
  red-team corpus.
- Wave 2 R1-parity: browser tools, Manus operator, multi-language LSP, IDE
  plugins, multi-CI parity, Tauri R1D-1..R1D-12 desktop phases.

### In Progress
- Hardening of the autonomous operator behind a per-mission toggle.
- LSP feature coverage beyond hover/definition/diagnostics.
- Race-clean regression sweep.

### Scoped
- **Cortex / lanes / multi-surface.** Eight specs in `specs/` define the
  next slice — parallel cognition substrate, six v1 Lobes, lanes wire
  format, Bubble Tea v2 lanes panel, `r1 serve` daemon, React web app,
  Tauri augmentation, agentic test harness. Build order is a strict DAG;
  each spec independently buildable with declared deps.
- IDE plugin marketplace publishing (VS Code, JetBrains).
- Ledger redaction with two-level Merkle commitment.

### Scoping
- Cross-machine session migration.
- Encryption-at-rest for journals.
- Broader outward-facing superiority reporting against peer runtimes.

### Potential — On Horizon
- Cross-product deterministic skill exchange + marketplace.
- Cloud daemon beyond loopback singleton.
- Multi-tenant per-host (multiple uids on a shared box).
- OpenTelemetry export of lane events.

## Why we think this matters

There's a generation of coding agents now: Claude Code, Cursor, Codex,
Continue, half-a-dozen open clones. They all share the same shape — a
linear chat against a single model, with tool calls. They differ on
polish, tools, and vendor lock-in.

We think the next generation of agents differs on cognitive shape. Lobes
running in parallel. Lanes as the UI primitive. Notes as the
inter-cognition wire format. A merge model where the agent decides how
your input lands. A daemon hosting many sessions. A protocol that
external agents speak the same way humans do.

That's the bet r1 is making. It's a bet about what an "agent" looks like
in 2027, not 2024. We are building the harness now so it's there when the
shape of the work changes.
