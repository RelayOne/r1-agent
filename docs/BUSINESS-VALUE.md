# Business Value

A pitch-deck narrative for r1 — for marketing, investors, partners, and anyone who needs to understand the product without reading any code.

---

## The problem

You hire AI agents to build software. They almost finish the job.

That's not a typo. The frontier coding agents available today — Claude Code, Cursor's background agent, Devin, Codex — share a behavior that costs operators hours of every day: **they call work "done" before it actually is.** They claim to have completed a five-step task when only three are done. They drop the rest into "follow-up." They invent "rate limits" or "load balance constraints" to justify stopping. They self-truncate.

Even when you tell them not to, they do it anyway. Because the model produces tokens, not commitments — and prompt-level instructions are a polite suggestion to a system that's optimizing for completion of THIS turn, not completion of YOUR PROJECT.

This costs real money. A senior engineer babysits the agent, re-reads the diff, notices the "I'll defer this to a follow-up" three commits later, manually re-prompts. The promised 10× productivity becomes 2-3× — half of which is consumed by the supervision tax.

Worse: when the agent finally finishes, you have no audit trail of what was verified vs. what was waved through. Did the test suite actually run? Was the diff actually reviewed? Did anyone notice that step 4 was silently skipped?

---

## Who r1 is for

**Engineering leaders** running coding-agent workflows on real production codebases who:

- Spend their day reading diffs the agent claimed were complete.
- Need to defend agent-generated changes to security, compliance, or auditors.
- Want parallel work without losing the context-sharing that makes shared-thread reasoning faster than committee-of-subagents.
- Have multiple machines, multiple workdirs, multiple concurrent missions — and want one daemon and one wire to drive them all.
- Are tired of choosing between "open" (GPT-OSS-style — no governance) and "closed" (proprietary SaaS — no introspection).

**Senior individual contributors** who want a coding agent that works the way they do: plan → implement → verify → review, with explicit gates, content-addressed evidence, and the ability to interrupt mid-stream without poisoning the conversation.

**Platform teams** building agent-driven internal developer platforms who need a runtime that:

- Is provider-agnostic (Claude / Codex / OpenRouter / direct API / lint-only fallback).
- Surfaces every cognitive thread as a first-class UI primitive (not just "AI is thinking…").
- Provides one wire (MCP) for every UI action, so external agents can drive r1 the same way humans do.
- Refuses to ship work that isn't actually done.

---

## How r1 solves it

### Before r1

> You: "Add request ID middleware and update the OpenAPI spec."
>
> Agent: *creates middleware, doesn't update spec, says* "Successfully added request ID middleware. Spec update deferred to follow-up."
>
> You: *re-read the diff, notice the spec wasn't touched, manually re-prompt*
>
> Agent: *adds spec update*
>
> You: *re-read again to confirm*

### After r1

> You: "Add request ID middleware and update the OpenAPI spec."
>
> Agent: *creates middleware, attempts to say "deferred to follow-up"*
>
> r1 anti-truncation gate: *refuses end_turn — the active plan has 2 items and only 1 is checked*
>
> Agent: *forced to continue; updates spec*
>
> Agent: *attempts end_turn*
>
> r1 verification descent: *runs build + test + vet*
>
> r1 cross-model reviewer: *Codex reads the diff and checks against AC*
>
> Agent: *commits with both items checked*
>
> r1 anti-truncation verifier (post-commit): *cross-checks the commit's "done" claim against the actual checklist; classifies as Verified*

The work is done correctly the first time. The supervision tax drops to near-zero. The audit trail is content-addressed and replayable.

---

## Key benefits

### Refuses to lie about completion
Seven independently-effective layers of machine-mechanical enforcement run in deterministic Go code at the host process layer — not in the model's prompt. The model demonstrably ignores prompt-level instructions to defeat self-truncation. r1 doesn't ask politely; it refuses the `end_turn` API call until the work is actually done. Operators can override (with an explicit flag); the LLM cannot.

**What this means for you**: the agent cannot stop until the plan is checked. You stop spending evenings re-reading agent output for skipped items.

### Parallel cognition that shares context
While the main thread implements, six specialist Lobes work in parallel — pulling memory, watching the plan, drafting clarifying questions, gating end-of-turn on critical findings, draining events to durable storage, curating "should-remember" facts. Unlike multi-subagent setups, these Lobes share full context with the main thread (they read the same conversation history, hit the same prompt-cache breakpoint), so they're not duplicating work — they're augmenting it.

**What this means for you**: faster missions because plan updates and clarifications happen IN PARALLEL with implementation, not as a separate step that adds latency.

### A real chat UI, not a streaming-text terminal
A Cursor-3-Glass three-column React web app: session list on the left, streaming chat in the center with tool/reasoning/plan/diff cards, lanes sidebar on the right, tile mode that pins 2-4 lanes for parallel watching. Same components rendered in a Bubble Tea terminal UI and a Tauri 2 desktop shell. Same daemon backs all three.

**What this means for you**: switch between TUI and web and desktop without losing your session. Watch the test runner, the build, and the deploy at the same time.

### One wire for humans and agents
Every action a human can take through any UI has an idempotent, schema-validated MCP equivalent. External agents (Claude, Codex, Stagehand) drive r1 the same way you do. A CI lint refuses to ship UI without a corresponding MCP tool — it's a build break.

**What this means for you**: integrate r1 into your existing agent stack without writing custom adapters. The UI is a view over the API, never the reverse.

### Content-addressed evidence
Every node in the mission ledger has a `sha256:<hex>` content ID. Every event hits a WAL-backed durable bus. Every cost tick is journaled. Daemon restart replays the journal and emits `daemon.reloaded` to reconnecting clients — your in-flight chat survives a crash invisibly.

**What this means for you**: audit and compliance can replay any past mission deterministically. No "the agent told me it did X" — show me the ledger node.

### Cryptographic chain-of-custody on every redaction
Redactions land in the ledger with an ed25519 signature stamped over a canonical form that includes the public-key fingerprint. The dashboard's "View redactions" side panel renders three states distinctly: `Verified` (green), `legacy unsigned` (gray — record predates the spec), and `tampered` (red — the canonical body was rewritten or the signer was swapped). The keypair lives at `<store-root>/redactions/sign-{priv,pub}.pem`; the public half is the artifact you hand to a third-party auditor.

**What this means for you**: when a regulator or a security reviewer asks "show me what was redacted from this mission and prove no one rewrote the audit trail," you point to the public key plus the redaction log. They run `VerifyRecord` against each entry and see Verified or not — no chain-of-trust handwaving.

### Portable per-session export an auditor can verify offline
Every session can be exported as a single `tracebundle` artifact: chain nodes + edges + content + a canonical-signed manifest carrying the `chain_root_hash`. The hash is `sha256(prev || node_id || content_commitment)` over nodes sorted by `(CreatedAt, ID)` — deterministic, recomputable from the bundle alone, no live ledger access needed. Per-session filtering (by `MissionID` for nodes, by `Edge.Metadata["session_id"]` for edges) means the bundle is a real privacy boundary; the auditor sees exactly the session you authorized them to see, nothing else.

**What this means for you**: a tracebundle is one HTTP GET, and the recipient can answer "is this artifact intact?" without phoning home to your daemon. Long-term retention is just a file on a backup tape; cold-restore audit is a `curl` plus a `cosign verify`-shaped step against your published public key.

### Skill lifecycle that respects the budget
Every Claude / Codex Skill the agent loads becomes a `SkillLoaded` ledger node; every drop becomes a `SkillUnloaded` node with the reason recorded. Two automatic unload paths run alongside the model's own `Drop` calls: phase-exit closure (when a workflow phase ends, every skill loaded into that phase is dropped with `reason="scope_exit"`) and LRU compaction (when context-token pressure rises, oldest-loaded skills are evicted with `reason="compactor"`). The model's prompt rebuild reads the updated skill table on the next round.

**What this means for you**: you stop paying for skill text the agent isn't using anymore, and you stop debugging "why did it suddenly behave differently" — the ledger answers in two clicks.

### Provider-agnostic with a 5-tier fallback
Claude → Codex → OpenRouter → direct API → lint-only. Subscription pool, circuit breaker, OAuth poller. When Claude rate-limits, Codex picks up; when OpenRouter is degraded, the direct API kicks in; when everything is down, lint-only mode keeps the missions on the rails.

**What this means for you**: vendor-lock-out doesn't kill your dev velocity. Cost optimization is automatic (cheap-model floor with escalation only on tagged-critical paths).

---

## What makes r1 different

| | r1 | Claude Code | Cursor BG agent | Devin | GPT-OSS / OSS agents |
|---|---|---|---|---|---|
| Refuses self-truncation | ✓ machine-mechanical | prompt-only | prompt-only | prompt-only | varies |
| Cross-model adversarial review | ✓ Codex reviews Claude (and vice versa) | single-model | single-model | single-model | usually none |
| Parallel cognition with shared context | ✓ Cortex GWT-style Lobes | ✗ | ✗ | ✗ | usually subagent-isolation |
| Multi-surface UI (TUI / web / desktop) | ✓ all three on same daemon | terminal-only | IDE-only | proprietary web | usually terminal-only |
| Every UI action has an MCP equivalent | ✓ build-break lint enforces | partial | partial | proprietary | varies |
| Content-addressed audit ledger | ✓ sha256-keyed; replayable | ✗ | ✗ | proprietary | varies |
| Open + self-hostable | ✓ MIT | ✗ proprietary | ✗ proprietary | ✗ proprietary | ✓ |
| Daemon survives restart with replay | ✓ journal.ndjson + daemon.reloaded | ✗ | ✗ | proprietary | varies |

The honest comparison: Claude Code and Cursor are excellent at single-task interactive coding. Devin is excellent at long-running autonomous missions in cloud-hosted sandboxes. r1 is the answer when you need both — and you need to defend the agent's output to a security review.

---

## Business model

r1 is MIT-licensed and self-hostable. The hosted SaaS at `r1.run` provides:

- **Free tier**: docs site, CLI binary downloads, telemetry-opt-in. No agent execution; runs locally.
- **Paid tier (planned)**: license verification, retention/lifecycle email, support, anti-truncation verification API. Pricing tiers TBD.
- **Enterprise (planned)**: SSO via RelayOne MSP, admin panel, audit export, on-prem deployment support.

Core commitment per [STEWARDSHIP.md](../STEWARDSHIP.md): **no functional feature migrates from self-hosted to cloud-only, ever.** Everything r1 does runs on your machine. The SaaS exists to coordinate, distribute, and verify — not to gatekeep.

---

## Traction

### Engineering scope (this session)

- 4 cortex / multi-surface specs (specs 6/7/8/9) — 171 spec items merged
- 4 final-sweep PRs (#168 / #169 / #170 / #171) — skill-aware compaction, ed25519-signed redaction events, release-rehearsal CI lane, tracebundle v2 export
- 9 Cloud Run SaaS services live across dev/staging/prod
- 175 internal Go packages, 10 cmd binaries
- 1M-iteration anti-truncation soak: 0 false positives, 0 false negatives, 499K true positives at 16,891 iter/sec
- Performance: 3 µs/event end-to-end lane streaming (50 µs target); 262 MB/s journal throughput; 852 µs p99 dispatch latency
- Test surface: 100% Go pass rate, vitest coverage threshold enforced, 9 Playwright e2e flows + axe a11y on every route, 110 cargo tests on the desktop side, plus the Cloud Build release-rehearsal E2E lane firing on every push to `main` and every `^v.*$` tag push

### Product surface

- TUI on Bubble Tea v2 with 72 race-clean tests
- Cursor 3 Glass web app with 137 source files, 80% statements coverage threshold, sibling-tests + sibling-stories enforced via runtime manifests
- Tauri 2 desktop with 110 cargo tests + 4 Playwright e2e
- 38-tool MCP catalog across 10 categories
- Per-OS service-unit installer (launchd / systemd-user / Windows SCM)

### Hosting

- 9 Cloud Run services live, /livez 200 across all 9
- 3 Cloud SQL Postgres 16 instances RUNNABLE
- DNS pending Cloudflare CNAME records (operator action)

---

## The team's unfair advantage

The author of r1 has been running coding agents in production for 18 months across multiple SaaS products in the RelayOne portfolio (`actium`, `cloudswarm`, `coderadar`, `deeptap`, `framebright`, `heroa`, `parentproof`, `relaygate`, `truecom`, `veritize`, `wellytic`). This is not a research project; it's the harness those products use internally.

That means:

- Anti-truncation isn't an idea — it's a documented behavior pattern from real production runs that needed mechanical enforcement.
- The cortex Lobe pattern wasn't designed in a vacuum — it's the answer to "we need plan updates and clarifying questions WITHOUT subagent latency."
- The 5-provider fallback isn't theoretical — it's how the portfolio survived rate-limit windows on each provider.
- The audit ledger isn't compliance theater — it's how the portfolio passes security reviews from clients who don't trust AI in their codebase.

r1 is what survives 18 months of dogfooding inside a working portfolio. Everything in it is there because something broke without it.

---

## What's next

The 6-month roadmap, in priority order:

1. **Real auth** — Path-A Go port of `@relayone/auth-core` provides JwtService + RelayOneSsoClient + PasswordAuth + MagicLinkAuth. SSO via RelayOne MSP for the enterprise tier.
2. **Admin panel at `admin.r1.run`** — clones an existing portfolio admin template; surfaces user / revenue / usage / lane / license-key management.
3. **Tracking + analytics** — PostHog (product analytics + funnels + A/B), Customer.io (retention + lifecycle email), CodeRadar dogfood (in-house error tracking).
4. **Marketing site** — affiliate / SEO / CRO / attribution / retention engineering across the public surface.
5. **Cross-machine session migration** — current daemon is one-host; the next iteration lets you start a session on your laptop and finish it on a cloud sandbox.
6. **Encryption-at-rest for journals** — separate spec already drafted at `specs/encryption-at-rest.md`.
7. **BitBucket Pipelines adapter** — parity with GitHub Actions and GitLab CI. **Shipped 2026-05-12.**
8. **Native MCP server bundle for popular IDEs** — VS Code + JetBrains + Zed without a separate install step.

---

## Completion SOW value props (scoped 2026-05-11)

Fourteen specs scoped this session take r1 from "technically honest agent runtime" — the thesis is right, the open-source code already demonstrates it — to "operationally honest hosted product." Each tier addresses a different audience and a different revenue motion.

### Tier A — turn the honest-agent thesis from technically-true into operationally-true

Tier A is the difference between "r1 is the right architecture" and "you can put your customer's production work on r1 tomorrow." Five specs.

**Prompt-injection hardening (A1).** The honest-agent thesis only holds if a customer's repo can't hijack the agent. Today's promptguard is a foundation; A1 threads it through plan + execute + verify boundaries, stamps every system block with an ed25519 fingerprint that fails verification on tamper, runs an adversarial reviewer over the CL4R1T4S injection corpus, validates per-tool inputs at the MCP wire, and circuit-breaks a session that crosses an injection-attempt budget. The pitch shifts from "we resist prompt injection in the prompt" to "the host process refuses to obey injection attempts and proves it from the ledger." That is the difference between a hand-wave and a security review you can pass.

**P0 platform hardening (A2).** Every agent platform that gets to scale eventually faces the same failure modes: panics on background goroutines that leave the daemon wedged, SIGTERMs that orphan in-flight tool calls, runaway sessions that starve their neighbors, restarts that lose state, observability blind spots that turn an incident into a guessing game. A2 is the boring, load-bearing work that turns r1's runtime from "demoable" into "I can leave it running over a long weekend without paging me." The marketing answer to "is it production-ready" stops being a caveat and starts being a yes.

**One-shot production hardening (A3) — RelayGate inline integration.** RelayGate Phase K-3 wants to call r1 inline on every request, not as a background mission. That requires the `--one-shot` path to be deterministic under SIGTERM, bounded in memory, fail-closed on upstream-model stalls, and prove itself under a 1000-concurrent integration test. A3 ships exactly that, plus a remote audit-ledger publishing path so the operator's ledger of record can live off-host, plus a documented integration contract so the RelayGate team can wire r1 without reading r1 source. The strategic value is unlocking inline-integration as a product motion: the customer's request flows through r1 in the hot path, not through r1 in a side process.

**RelayOne SSO (A4).** Long-lived API keys are a security liability and a usability tax. A4 replaces them with OIDC + PKCE against the RelayOne IdP, per-tenant token isolation, JWKs rotation, and a middleware that gates the admin panel and every future enterprise route. The customer logs in with the identity they already have; the daemon honors per-tenant scope; the operator stops emailing API keys to onboarding contacts. A4 is also the dependency every paid surface eventually needs — Tier B's analytics+retention work and Tier C's session migration both rely on a real auth identity.

**Admin panel (A5).** "What is this customer's session doing right now" without raw SQL access. A5 mounts five read-only routes on the existing `r1-server` process (sessions, tenants, billing, audit, anti-trunc events), gates them through A4's SSO, and gives internal operators, regulators, and support engineers the same view of the system. Support tickets stop escalating to ops. Compliance reviews get answered from a browser. New hires onboard against the panel instead of against `gcloud sql connect`.

### Tier B — close the self-serve adoption loop

Tier B is the difference between "early adopters find us" and "the product compounds." Three specs.

**Product analytics (B1).** Twenty-four events instrumented end-to-end across the activation funnel, three product funnels (activation, mission-success, anti-trunc-fire-recovered), four cohorts (free-active, paid-active, churn-risk, regretted-activation). The product team finally answers "what's the activation rate" with one query, ships A/B tests with confidence intervals, and stops shipping features on vibes. PostHog Group Analytics by tenant means the enterprise tier gets sliced from the self-serve tier without a custom dashboard.

**Lifecycle email (B2).** Six lifecycle triggers — signup, activation, first mission, first completion, anti-trunc fired, budget alert — each backed by a transactional Customer.io template marketing edits without a deploy. The user who signs up on Monday and never returns gets a Tuesday "here's what you missed" email instead of vanishing from the funnel. The GDPR DSAR flow turns export-or-delete from a manual ops task into a self-serve flow. Retention stops being something we'll-figure-out-later and starts being a measurable product surface.

**Self-observability (B3).** r1 already emits structured events on every state transition; B3 just wires eighteen canonical events into CodeRadar with per-environment sampling and makes the CodeRadar dashboard the on-call surface for r1 itself. "We eat our own dogfood" stops being a slogan and starts being a documented practice: every r1 release ships under r1's own observability stack.

### Tier C — widen the competitive moat

Tier C is the work that makes "should we switch agent runtimes" a harder question for the customer to answer in the abstract. Six specs, each addressing a different surface where most competitors don't yet have an answer.

**Cross-machine session portability (C1).** A `.r1session` bundle plus `r1 session export / import / migrate` commands. A customer's session is not stuck to a host — it migrates from a laptop to a cloud sandbox to a desktop with a tamper-evident chain-root-hash continuity proof. The competition's answer to "can I move this session to another machine" is "no, restart it"; ours is "one command, audit chain intact."

**Cost guard-rails by default (C3).** Two-tier token-bucket throttling (per-session + per-tenant) enforced at the MCP boundary; declarative YAML policy the operator edits without a code change; bucket state journaled so a daemon restart honors the in-flight throttle window. Every multi-tenant agent platform eventually meets the customer whose runaway agent ate a month of quota in a weekend; we ship the fix before the customer.

**IDE-native install (C4).** One spec covering Cursor, Windsurf, VS Code, and JetBrains with one `r1 ide install / uninstall / verify` command. The customer installs r1 once; every IDE on the machine sees it. Competitors that require a per-IDE walkthrough lose the install funnel; we win it.

**BitBucket parity (C5).** *Shipped 2026-05-12.* The CI integration story is GitHub Actions, GitLab CI, *and* BitBucket Pipelines. Customers on BitBucket — a non-trivial slice of enterprise — stop being a third-class platform with a "PR open" workaround. Implementation lives under `internal/cicd/bitbucket/`; operator runbook in `docs/integrations/bitbucket-pipelines.md`.

**Hosted-SaaS browser sandbox (C6).** Two interchangeable providers (Browserless managed + an in-house Cloud Run provider), tenant-isolated sandbox, deny-by-default egress policy. Every "scrape this site / fill this form / verify this UI" agent workflow becomes usable on the hosted tier without the customer worrying about cross-tenant browser-fingerprint leakage or unrestricted outbound network access.

**Cross-product skill federation (C7).** Pack-format v2 with an explicit compatibility matrix, federated trust root, runtime adapters for CloudSwarm, Heroa, and Veritize. A skill written for r1 runs in Heroa; a skill from CloudSwarm runs in r1. The RelayOne portfolio stops being a collection of related products and starts being a federated agent platform — and the moat against any single-product competitor compounds with every new portfolio entrant.

---
