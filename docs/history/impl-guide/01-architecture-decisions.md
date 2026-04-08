# 01 — Locked-In Architecture Decisions

These decisions are based on convergent findings across multiple research sources. **You do not have authority to override them without writing a justification to STOKE-IMPL-NOTES.md and waiting for Eric to review.** Each decision includes the research backing so the rationale is preserved.

---

## Decision 1: File editing uses str_replace with cascading fallbacks

**Choice:** Stoke's tool layer uses exact-string-replacement editing as the primary mechanism, with a fallback chain. Whole-file rewrite is only for new files or last-resort fallback after str_replace fails 3 times.

**Cascade order (from Aider):**
1. Exact match (perfect_replace)
2. Whitespace-normalized match
3. Ellipsis expansion (handle `...` markers in old_string)
4. Fuzzy match via Go's `golang.org/x/text/internal/symbol` similarity
5. Cross-file fallback (try other files in current scope)
6. After 3 failures: fall back to whole-file rewrite (for that single file)

**Constraints (from Claude Code's Edit tool):**
- The model must have called `Read` on a file before it can `Edit` it. Track this in agent state.
- `old_string` must be unique in the file. If it appears more than once, reject the edit and ask for more surrounding context (return `is_error: true` with a descriptive message).
- Provide an explicit `replace_all` boolean flag (default `false`) for bulk renames.

**Rejected alternatives:**
- **Whole-file rewrite as default (Cursor's approach):** Requires a custom 70B fast-apply model achieving 1000 tokens/sec; Stoke does not have this. LLMs fail >40% of the time on diff generation per Cursor cofounder Aman Sanger [P67].
- **Codex's V4A patch format:** Specifically RL-trained into GPT-4.1+, won't work reliably with Claude.
- **JSON-wrapped tool output for code (some early designs):** Aider's extensive benchmarking showed all tested models scored worse with JSON-wrapped responses [P67]. Escaping code in JSON distracts models from the coding task.

**Source:** [P62, P67]

---

## Decision 2: Tools are defined as JSON schemas via the Anthropic Messages API native tool_use

**Choice:** When Stoke runs as its own harness (Phase 4), it uses the Anthropic Messages API's native `tool_use` mechanism. Tools are defined as JSON Schema objects in the `tools` array, not embedded in the system prompt as text.

**Tool name regex:** `^[a-zA-Z0-9_-]{1,64}$`

**Required fields per tool:** `name`, `description`, `input_schema` (JSON Schema draft 2020-12)

**Token overhead to budget for:**
- Fixed overhead with `tool_choice: "auto"`: ~346 tokens
- Bash tool definition: ~245 tokens
- Text editor tool: ~700 tokens
- Computer use tool: ~735 tokens

**Tool count target:** 10–15 tools maximum. Anthropic's internal data shows loading all available tools drops Opus 4 accuracy to 49%, while dynamically selecting 3–5 relevant tools per request lifts it to 74%. Beyond 30–50 tools accuracy degrades significantly [P63, P61].

**Source:** [P61, P62, P63]

---

## Decision 3: System prompt placement and skill injection structure

**Choice:** Skills are injected into the system prompt's dynamic section, NOT the user message. Critical reminders may be repeated at the end of the user message (the recency-effect fallback). Use XML tags as structural markers.

**Layered prompt structure (in order, from most stable to most dynamic):**

```
[Layer 1 — Tool definitions]                    Cached. Sorted alphabetically.
[Layer 2 — Static system prompt]                Cached. Role + safety + base instructions.
[Layer 3 — Skill block]                         Cached if stable. Wrapped in <skills>...</skills>.
[Layer 4 — Dynamic system context]              Not cached. Working dir, OS, timestamp, repo profile.
[Layer 5 — Conversation history]                Incrementally cached.
[Layer 6 — Current user message]                Not cached. Critical reminders appended here.
```

**Why:** [P63] Anthropic's senior prompt engineer Zack Witten: "Claude follows instructions slightly better when placed in the user message" — but system prompts persist across turns, enable prompt caching (90% cost reduction), and resist prompt injection. The practical resolution: stable skill content in the system prompt, critical reminders at end of user message.

**XML tag use:** Claude was specifically trained to recognize XML tags as a prompt-organizing mechanism [P63]. Use:
- `<skills>` ... `</skills>` for the skill block
- `<repo_profile>` ... `</repo_profile>` for detected tech stack
- `<wisdom>` ... `</wisdom>` for cross-task learnings
- `<task>` ... `</task>` for the current task description
- `<critical_reminders>` ... `</critical_reminders>` at end of user message

**Source:** [P63]

---

## Decision 4: Skill injection token budget = 3,000 tokens (not 4,000)

**Choice:** Total skill injection budget is 3,000 tokens. This is tighter than the original spec's 4,000.

**Allocation:**
- Always-on skills (agent-discipline): up to 800 tokens, full content
- Top 2 repo-stack-matched skills: up to 500 tokens each (1,000 total), full content
- Top 3 keyword-matched skills: up to 200 tokens each (600 total), gotchas-only
- Wisdom-derived gotchas: up to 300 tokens
- Buffer: 300 tokens

**Why this number:**
- Claude Code itself warns when MCP tools exceed 25,000 tokens
- Anthropic's engineering blog: "tool selection accuracy degrades significantly once you exceed 30–50 available tools"
- Trail of Bits guidance: "Keep the SKILL.md lean (under 2,000 words)" ≈ 2,500–3,000 tokens
- SkillReducer paper (arXiv 2603.29919): compressing skill descriptions by 48% and bodies by 39% **improved** functional quality by 2.8%
- Anthropic Tool Search achieved 46.9% reduction in total agent tokens by loading only 3–5 tools per request

**Source:** [P63, TOB]

---

## Decision 5: Skill format follows Trail of Bits' SKILL.md standard with YAML frontmatter

**Choice:** Stoke's skill registry parses both formats:
- **Primary:** YAML frontmatter (`---` delimited) with `name`, `description`, optional `allowed-tools`, optional `triggers`
- **Backward compatible:** Existing `<!-- keywords: ... -->` HTML comment format

**Required SKILL.md sections:**
1. YAML frontmatter (parsed for routing)
2. **When to Use** — concrete scenarios with keywords/patterns
3. **When NOT to Use** — scenarios where another approach is better
4. **Behavioral guidance** (the body) — under 500 lines, under 2,000 words
5. **Gotchas** — explicit anti-patterns with rebuttals
6. (Security/critical skills only) **Rationalizations to Reject** — preempts the LLM's tendency to dismiss findings

**Constraints from Trail of Bits standards:**
- SKILL.md under 500 lines, under 2,000 words
- Reference chains limited to **one level deep** — SKILL.md can link to `references/foo.md`, but `references/foo.md` cannot link to other reference files
- Skill name in kebab-case, gerund form preferred (`analyzing-contracts` not `contract-analyzer`)
- Description in third-person voice, includes trigger phrases
- Provides behavioral guidance, NOT reference dumps

**Directory structure:**
```
.stoke/skills/
  agent-discipline/
    SKILL.md            ← always loaded
    references/
      examples.md       ← loaded on demand
  postgres/
    SKILL.md
    references/
      connection-pooling.md
      migrations.md
  ...
```

**Source:** [TOB]

---

## Decision 6: Repo tech stack detection uses layered file existence + manifest parsing

**Choice:** Stack detection follows the layered pattern used by GitHub Linguist, Vercel, Nixpacks, and Heroku:

1. **File existence checks** (cheapest, ~80% of detection)
2. **Manifest parsing** (medium cost) — `package.json` deps, `go.mod` imports, `Cargo.toml` deps, `requirements.txt`, `pyproject.toml`
3. **Content sampling** (highest cost, used only for ambiguous cases) — regex scan of config files like `vite.config.ts`, `prisma/schema.prisma`, `docker-compose.yml`
4. **Combinator pattern from Vercel:** Each detector specifies `every` (all must match) or `some` (any must match) using primitives `matchPackage`, `path`, `matchContent`

**Polyglot scanning depth:** 2–3 levels deep for monorepos, prioritizing `apps/`, `packages/`, `services/`, `libs/`, `modules/`, `tools/`.

**Source:** [P64, P73]

---

## Decision 7: Hub event bus has three modes — gate, transform, observe

**Choice:** The unified event bus replacing Stoke's three disconnected hook systems uses three subscriber modes:

| Mode | Sync? | Can block? | Can modify? | Purpose |
|---|---|---|---|---|
| **Gate** | Sync | Yes (deny/allow) | No | Security, policy, completeness checks |
| **Transform** | Sync | No | Yes (mutate event payload, inject content) | Skill injection, context augmentation |
| **Observe** | Async | No | No | Audit, metrics, dashboards, fire-and-forget |

**Dispatch order:** Transforms → Gates → Observes (transforms first so gates see the final mutated state, observers see committed outcomes).

**Hooks-only-tighten semantics:** A gate hook returning "allow" cannot bypass an existing deny. Multiple gate decisions resolve via `deny > defer > ask > allow` precedence (matches Claude Code).

**Failure policy per subscriber (NOT global):**
- Security-critical gates: **fail-closed** (subscriber timeout = action denied)
- Advisory gates: **fail-open** (subscriber timeout = action allowed with warning)
- Transforms: **pass-through** on failure (original event unmodified)
- Observes: **silent skip** on failure (logged in audit, doesn't block pipeline)

**Source:** [P79, P80] — combines Kubernetes admission webhook semantics with Claude Code hook protocol

---

## Decision 8: Hub uses six transports — in-process, Unix socket, gRPC, HTTP webhook, script, MCP

**Choice:** Subscribers can connect via any of six transports. Each implements the same interface.

| Transport | Latency | Use case |
|---|---|---|
| **In-process Go** | ~nanoseconds | Built-in hooks (skill injection, scan, cost tracking) |
| **Unix domain socket** | ~2-5μs (3.2× faster than TCP) | IDE plugins, local tools |
| **gRPC** | ~100μs over UDS, ~125μs over TCP | Structured external services |
| **HTTP webhook** | ~1-10ms | Slack alerts, dashboards, audit services |
| **CLI script** | ~10-100ms | Claude Code-compatible bash hooks |
| **MCP tool** | ~varies | AI agents registering their own hooks |

**Unix socket protocol:** Length-prefixed JSON for gate/transform (precise message boundaries), NDJSON for observe (streaming). Use `SO_PEERCRED` on Linux for kernel-verified peer credentials.

**Source:** [P79]

---

## Decision 9: Circuit breaker per subscriber, not global

**Choice:** Each subscriber gets its own `gobreaker.CircuitBreaker` from `sony/gobreaker`. Configuration:

```go
gobreaker.Settings{
    Name:        subscriberID,
    MaxRequests: 3,    // half-open probe count
    Interval:    60 * time.Second,  // rolling window
    Timeout:     30 * time.Second,  // open duration before half-open
    ReadyToTrip: func(c gobreaker.Counts) bool {
        return c.Requests >= 10 && float64(c.TotalFailures)/float64(c.Requests) >= 0.5
    },
}
```

**Layering order:** Bulkhead → Circuit Breaker → Timeout → Retry → Fallback

**Why:** Without circuit breakers, a dead webhook subscriber causes every gate hook to wait the full timeout (5s) before failing. With 73 event types and dozens of subscribers, this cascades into pipeline unusability. Circuit breakers fail fast (~0μs instead of 5,000ms) once tripped.

**Source:** [P79]

---

## Decision 10: Audit log uses append-only SQLite with optional Ed25519 hash chaining

**Choice:** Every hub event is logged to an append-only SQLite table. Schema:

```sql
CREATE TABLE hub_audit (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id        TEXT NOT NULL UNIQUE,        -- ULID
    event_type      TEXT NOT NULL,
    timestamp       TEXT NOT NULL,
    mission_id      TEXT,
    task_id         TEXT,
    correlation_id  TEXT,
    subscriber_id   TEXT,
    mode            TEXT,                          -- gate|transform|observe
    decision        TEXT,                          -- allow|deny|abstain|n/a
    reason          TEXT,
    latency_ms      INTEGER,
    payload_hash    TEXT,                          -- SHA-256 of payload
    payload_json    TEXT,                          -- full event JSON (optional, configurable)
    prev_hash       TEXT,                          -- SHA-256 of previous row's hash chain
    chain_hash      TEXT,                          -- SHA-256(prev_hash || this row's content)
    signature       BLOB                           -- optional Ed25519
);

CREATE INDEX idx_hub_audit_event_type ON hub_audit(event_type);
CREATE INDEX idx_hub_audit_mission ON hub_audit(mission_id);
CREATE INDEX idx_hub_audit_task ON hub_audit(task_id);
CREATE INDEX idx_hub_audit_timestamp ON hub_audit(timestamp);
```

The hash chain (`chain_hash = SHA256(prev_hash || event_id || event_type || timestamp || payload_hash || decision)`) makes tampering mathematically detectable. Optional Ed25519 signing for SOC 2 compliance — can be added later via the `signature` column without schema migration.

**Important constraint from research [P80]:** Agents must never write directly to the audit log. The hub writes on the agent's behalf. A compromised agent could otherwise delete incriminating events.

**Source:** [P80]

---

## Decision 11: Prompt caching is non-negotiable. Cache hierarchy is tools → system → messages.

**Choice:** Every API call from `agentloop` MUST be structured for cache alignment. Tools are sorted alphabetically (MCP `listTools()` does NOT guarantee order — unsorted tools bust the cache on every turn).

**Cache breakpoints (max 4 per request):**
1. End of tools array
2. End of static system prompt
3. End of skill block
4. End of conversation history (advances forward as conversation grows)

**Cache pricing (April 2026):**
| Model | Base Input | Cache Write 5min | Cache Read |
|---|---|---|---|
| Opus 4.6/4.5 | $5.00/MTok | $6.25/MTok | $0.50/MTok (90% off) |
| Sonnet 4.6/4.5 | $3.00/MTok | $3.75/MTok | $0.30/MTok (90% off) |
| Haiku 4.5 | $1.00/MTok | $1.25/MTok | $0.10/MTok (90% off) |

**Minimum cacheable size:** 1,024 tokens for Sonnet 4.5/4 and Opus 4.1/4. 2,048 for Sonnet 4.6 and Haiku 3.5/3. 4,096 for Opus 4.6/4.5 and Haiku 4.5.

**Concrete savings:** A typical 20-turn coding session on Sonnet costs $0.50–$1.50 with caching versus $2.50+ without — **70–82% input cost reduction**. Real-world Claude Code data shows over 90% of tokens are cache reads.

**CRITICAL anti-pattern:** Spawning Claude Code CLI as a subprocess **destroys caching benefits** because the cache is per-process. This is one of the main reasons for harness independence.

**Source:** [P69, P61]

---

## Decision 12: Workspace isolation = git worktrees + microVMs (already correct)

**Choice:** Stoke's existing `internal/worktree` + `internal/compute` design is correct. Don't change it. The pattern of git worktrees for code isolation + Ember/Flare microVMs for execution isolation is exactly what the research recommends [P70].

**Sweet spot:** 3–5 parallel agents per repository. Beyond that, merge complexity becomes the binding constraint, not compute.

**What needs adding (in Phase 4):** File-scope conflict prevention via the optimistic concurrency pattern. Before an agent writes to a file, check if another concurrent agent has modified it since the worktree was created. If yes, reject with a structured error so the agent can re-read.

**Source:** [P70]

---

## Decision 13: Wizard default mode is "auto-detect, then confirm" — not interactive Q&A

**Choice:** The wizard's default behavior is detect-then-confirm, following Vercel's pattern. It asks only when confidence is below 95% or for subjective preferences that cannot be inferred (cost strategy, scale targets, compliance requirements).

**Three modes:**
1. **Auto** (default): Detect everything possible, propose a complete config, ask only for confirmation
2. **Interactive**: Show all questions, useful for first-time setup
3. **Hybrid**: Detect what's detectable, ask for the rest. For ambiguous detections, use the "Detected X — is this correct?" pattern

**Tier flag:** `--yes` collapses everything to defaults (CI/CD safe). `--advanced` shows the full 50-question flow.

**Stack:** Use `huh` from the Charm ecosystem (Stoke already uses Bubble Tea). Survey is archived as of April 2024 — do NOT use it.

**Source:** [P72]

---

## Decision 14: Wisdom store moves from in-memory to SQLite

**Choice:** `internal/wisdom` becomes a SQLite-backed store like `internal/research`. Same migration pattern, same WAL config. Cross-session persistence is required for long-running projects where learnings should accumulate.

**Schema:**

```sql
CREATE TABLE wisdom_learnings (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id         TEXT NOT NULL,
    category        TEXT NOT NULL,           -- gotcha|decision|pattern
    description     TEXT NOT NULL,
    file_path       TEXT,
    failure_pattern TEXT,                     -- failure fingerprint hash
    skill_match     TEXT,                     -- matched skill name (if any)
    use_count       INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE INDEX idx_wisdom_category ON wisdom_learnings(category);
CREATE INDEX idx_wisdom_failure_pattern ON wisdom_learnings(failure_pattern);
CREATE INDEX idx_wisdom_skill_match ON wisdom_learnings(skill_match);
```

**Backward compat:** Existing in-memory `wisdom.Store` interface stays the same. The new SQLite implementation satisfies the interface. Wisdom callers don't need to change.

**Source:** [P85, existing Stoke architecture]

---

## Decision 15: The harness independence path is three phases, not one big rewrite

**Choice:** Don't try to replace `engine.ClaudeRunner` with a native runner in one step. Three sub-phases:

1. **Tool layer** (`internal/tools/`) — Define tool schemas, build the executor with sandboxing. Test with unit tests and a CLI that accepts tool calls and executes them.
2. **Agentic loop** (`internal/agentloop/`) — Wrap the existing `provider.AnthropicProvider` with the multi-turn tool-use loop. Test with integration tests that hit the real API.
3. **Native runner** (`internal/engine/native_runner.go`) — Implement `engine.CommandRunner` interface using `agentloop`. Drop-in replacement for `ClaudeRunner` and `CodexRunner`.

This allows incremental validation. Phase 4.1 can be tested without the API. Phase 4.2 can be tested without the Stoke pipeline. Phase 4.3 plugs into the existing workflow.

**Source:** Pragmatic engineering, validated against [P61, P62, P67]

---

## Decision 16: Honesty and completeness verification is Stoke's primary moat

**Choice:** Every implementation decision must reinforce Stoke's anti-deception infrastructure. Specifically:

1. **The hub gets dedicated event types for honesty signals:**
   - `verify.test_removed` — agent removed an existing test
   - `verify.test_weakened` — agent reduced test assertion strength
   - `verify.placeholder_inserted` — agent left TODO/FIXME/empty function
   - `verify.hallucinated_import` — import doesn't resolve to a real package
   - `verify.silent_simplification` — agent dropped requirements without flagging
   - `verify.completion_claim_failed` — agent claimed done but hidden criteria not met

2. **Default scan rules** in the hub include AST-level test diff detection. Use `golang.org/x/tools/go/ast/astutil` for Go test files. For JS/TS, shell out to a small wrapper around `@typescript-eslint/parser`.

3. **The wizard has a "Honesty enforcement level" question** in the verification depth section, with `strict` as the default for any project past prototype stage.

4. **Diff size limits enforced as a gate hook:** 200/400/1000 line thresholds map to severity. Above 1000 lines = automatic deny with explanation.

5. **Mutation testing thresholds** are configurable per skill: 70% kill rate for critical paths, 50% for standard, 30% for experimental.

**Source:** [P85, P68]

---

## Now go read `02-implementation-order.md`.
