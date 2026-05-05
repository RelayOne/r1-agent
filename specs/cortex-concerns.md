<!-- STATUS: done -->
<!-- BUILD_STARTED: 2026-05-02 -->
<!-- BUILD_COMPLETED: 2026-05-02 -->
<!-- CREATED: 2026-05-02 -->
<!-- DEPENDS_ON: cortex-core -->
<!-- BUILD_ORDER: 2 -->

# Cortex Concerns (v1 Lobes) — Implementation Spec

## Overview

This spec implements the six v1 Lobes that ship on top of the cortex-core substrate. Each Lobe is a Go type implementing the `cortex.Lobe` interface defined by `cortex-core` (spec 1) and runs as a goroutine inside the cortex with read-only access to message history and write access to the `cortex.Workspace` via `Publish(Note)`. Two Lobes are deterministic (no LLM, free, run continuously): `MemoryRecallLobe`, `WALKeeperLobe`, and `RuleCheckLobe`. Three Lobes are Haiku-4.5-driven and respect the per-turn cost budget: `PlanUpdateLobe`, `ClarifyingQLobe`, `MemoryCuratorLobe`. (Note: per the decisions log D-2026-05-02-05, RuleCheck is deterministic, so the breakdown is 3 deterministic + 3 LLM.)

Lobes consume existing primitives — they do not reinvent search, persistence, or supervisor logic. They only translate observations into `Note` records that the main agent loop reads at three checkpoints (pre-turn, pre-end-turn, mid-turn router).

## Stack & Versions

- Go 1.26 (matches `go.work`).
- Cortex-core API from spec 1: `cortex.Workspace`, `cortex.Lobe` (interface), `cortex.Note`, `cortex.Round`, `cortex.Spotlight`.
- Existing internal packages reused (no new deps):
  - `internal/memory` — `*memory.Store` + `memory.Entry`.
  - `internal/wisdom` — `*wisdom.Store` + `wisdom.Learning`.
  - `internal/tfidf` — `*tfidf.Index` + `tfidf.Result`.
  - `internal/supervisor` + `internal/supervisor/rules/*` — `supervisor.Rule`, `supervisor.Supervisor`.
  - `internal/bus` — durable WAL-backed event bus (`bus.Bus`, `bus.Event`).
  - `internal/hub` — typed in-memory event hub (different from durable `bus`).
  - `internal/conversation.Runtime` — RWMutex-backed history.
  - `internal/agentloop` — main thread.
  - `internal/apiclient` (or `internal/provider`) — Anthropic SSE client for Haiku LLM calls.
  - `internal/promptcache` — cache-aligned prompt construction.
  - `internal/costtrack` — per-turn budget enforcement.
- Anthropic Haiku 4.5 default for LLM Lobes (input $1/M, cache write 1.25x, cache hit 0.10x, output $5/M, per RT-CONCURRENT-CLAUDE-API §5).
- 1-hour cache (`cache_control: {"type": "ephemeral", "ttl": "1h"}`) for Lobe system prompts (RT-CONCURRENT-CLAUDE-API §2; D-C7).

## Existing Patterns the Lobes Consume

| Pattern | Surface | Used by |
|---|---|---|
| `memory.NewStore(memory.Config{Path:...})` then `*Store.Recall(query, limit) []Entry`, `*Store.RecallForFile(file)`, `*Store.Remember(cat, content, tags...) *Entry`, `*Store.RememberWithContext(...)`, `*Store.Save()`. | `internal/memory/memory.go` | `MemoryRecallLobe`, `MemoryCuratorLobe` |
| `wisdom.NewStore()` then `*Store.Learnings() []Learning`, `*Store.FindByPattern(hash) *Learning`, `*Store.Record(taskID, Learning)`. | `internal/wisdom/wisdom.go` | `MemoryRecallLobe` (read), `MemoryCuratorLobe` (write) |
| `tfidf.NewIndex()`, `idx.AddDocument(path, content)`, `idx.AddDocumentChunked(path, content)`, `idx.Finalize()`, `idx.Search(query, topK) []Result`. | `internal/tfidf/search.go` | `MemoryRecallLobe` (indexes memory + wisdom corpora at startup; reindexes on `MemoryCuratorLobe.Save`) |
| `supervisor.New(cfg, bus, ledger)` + `*Supervisor.RegisterRules(rules ...Rule)` + `*Supervisor.Start(ctx)`. Rule interface: `Name() string`, `Pattern() bus.Pattern`, `Priority() int`, `Evaluate(ctx, evt, ledger) (bool, error)`, `Action(ctx, evt, bus) error`, `Rationale() string`. | `internal/supervisor/core.go`, `internal/supervisor/rules/*` | `RuleCheckLobe` (subscribes to `supervisor.rule.fired` events and converts each fire into a Note) |
| `bus.Bus.Publish(evt)`, `bus.Bus.Subscribe(pat, handler) *Subscription`. WAL-backed; survives restart. | `internal/bus/` | `WALKeeperLobe` (drains hub events into WAL) |
| `hub.Bus.Register(sub Subscriber)` — typed in-memory hub. `Subscriber{ID, Filter func(EventType) bool, Handler func(ctx, *Event) *HookResponse}`. There is no `Subscribe` method; "subscribe to all" is expressed as a Subscriber whose `Filter` returns true unconditionally. | `internal/hub/` | `WALKeeperLobe` (source) |

## Lobe shared infra

### `LobePromptBuilder` (cache-aligned)

Every LLM Lobe constructs requests via this helper to maximize cache hits across the warmed-up turn (see cortex-core §F pre-warm).

```go
// internal/cortex/lobes/llm/prompt.go
type LobePromptBuilder struct {
    SystemPrompt string                 // Lobe-specific system prompt; cached 1h
    Tools        []apiclient.Tool       // sorted alphabetically by Name (cache stability)
    Model        string                 // default "claude-haiku-4-5"
    MaxTokens    int                    // default 500 (caps mid-stream cost — RT-CONCURRENT §4)
}

// Build returns a Messages-API request with system prompt marked as cached
// (1-hour TTL) and tools sorted alphabetically. The user message is the
// only un-cached block.
func (b *LobePromptBuilder) Build(userMessage string, history []apiclient.Message) apiclient.MessagesRequest
```

- System prompt block carries `cache_control: {type:"ephemeral", ttl:"1h"}` (RT-CONCURRENT §2).
- Tools sorted alphabetically by name → stable cache prefix (D-C5).
- `MaxTokens` is mandatory; default 500 prevents runaway streams since cancellation does not refund (RT-CONCURRENT §4).
- Concurrency capped externally by cortex-core's LLM-Lobe semaphore (≤5 default; D-C6).

### Escalation hook

LLM Lobes accept an optional `Escalator` callback. On a tagged-critical observation, the Lobe may call `Escalator(ctx, "reason")` which switches the model to `claude-sonnet-4-6` for the *next* call only. Operator can disable per-Lobe via `cortex.lobes.<name>.escalate=false` (config flag added in spec 1).

```go
type Escalator func(ctx context.Context, reason string) (model string)
```

## Per-Lobe specifications

### 1. MemoryRecallLobe (deterministic)

- **Package:** `internal/cortex/lobes/memoryrecall/`
- **Constructor:**
  ```go
  func NewMemoryRecallLobe(mem *memory.Store, wis *wisdom.Store) *MemoryRecallLobe
  ```
- **Behavior:** On each new user message and every assistant turn boundary, Lobe takes the last 1000 chars of message history as a query, runs `mem.Recall(query, 5)` and `tfidf.Index.Search` over the wisdom corpus, deduplicates by `Entry.ID`/`Learning.TaskID`, and publishes one Note per top-3 match (severity `info`).
- **Index lifecycle:** TF-IDF index is built at Lobe start from `mem.RecallByCategory(...)` (all categories) + `wis.Learnings()`. `MemoryCuratorLobe` triggers reindex on save by publishing a `cortex.workspace.memory_added` hub event the Lobe subscribes to.
- **Note shape:** (cortex-core's `Note` struct from spec 1 — `LobeID`, `Tags []string`, `Meta map[string]any`; no fields are added to that struct):
  ```go
  cortex.Note{
      ID:       "memrecall-<entry.id|learning.taskID>-<round>",
      LobeID:   "MemoryRecallLobe",
      Severity: cortex.SeverityInfo,
      Tags:     []string{"memory"},
      Title:    fmt.Sprintf("Possibly relevant: [%s] %s", entry.Category, truncate(entry.Content, 60)),
      Body:     entry.Content,
      Meta: map[string]any{
          "refs":                []string{entry.File},
          "expires_after_round": 2, // auto-evict if not consumed
      },
  }
  ```
- **No LLM.** Cost = $0.

### 2. WALKeeperLobe (deterministic)

- **Package:** `internal/cortex/lobes/walkeeper/`
- **Constructor:**
  ```go
  func NewWALKeeperLobe(hub *hub.Bus, wal *bus.Bus, framing WALFraming) *WALKeeperLobe
  ```
- **Behavior:** Registers a `hub.Subscriber` whose `Filter` returns true for every `EventType` (the spec-1 hub has no `SubscribeAll`; "subscribe to all" is expressed via this always-true filter — see Existing Patterns table). For each hub event the handler constructs a `bus.Event` with structured framing — type prefix `cortex.hub.<original_type>`, payload = JSON-marshaled hub event, `CausalRef` = original event ID — and calls `wal.Publish(evt)`. Backpressure: when WAL backlog exceeds 1k pending writes, Lobe drops `info`-severity events and publishes one Note `severity=warning` "WALKeeper backpressure: dropped N info events".
- **Why a Lobe?** Centralizes the bridge so every cortex Note is also durably journaled; survives daemon restart (D-C3).
- **Note shape (only on backpressure or write error):**
  ```go
  cortex.Note{LobeID:"WALKeeperLobe", Severity:cortex.SeverityWarning,
              Tags:[]string{"wal"},
              Title:"WAL backpressure", Body:"dropped 137 info events in 30s"}
  ```
- **No LLM.** Cost = $0.

### 3. RuleCheckLobe (deterministic)

- **Package:** `internal/cortex/lobes/rulecheck/`
- **Constructor:**
  ```go
  func NewRuleCheckLobe(sup *supervisor.Supervisor, wal *bus.Bus) *RuleCheckLobe
  ```
- **Behavior:** Subscribes to `bus.Pattern{TypePrefix: "supervisor.rule.fired"}` on the WAL bus (the supervisor publishes a `supervisor.rule.fired` event after every successful `Action`; the supervisor instance is configured by spec 1). For each fired rule, the Lobe converts the event into a `Note` with severity matching the rule's name prefix (`trust.*`, `consensus.dissent.*` → `critical`; `drift.*`, `cross_team.*` → `warning`; everything else → `info`).
- **Critical Notes refuse `end_turn`** via the existing `agentloop.PreEndTurnCheckFn` (cortex-core wires this; the Lobe just emits the Note).
- **Note shape:**
  ```go
  cortex.Note{
      ID:       "rule-" + evt.ID,
      LobeID:   "RuleCheckLobe",
      Severity: severityFor(ruleName),
      Tags:     []string{"rule:" + ruleName},
      Title:    ruleName + " fired",
      Body:     rationale,                  // from Rule.Rationale()
      Meta: map[string]any{
          "refs":                []string{evt.Scope.TaskID, evt.Scope.BranchID},
          "expires_after_round": 0, // sticky until acknowledged
      },
  }
  ```
- **No LLM.** Cost = $0.

### 4. PlanUpdateLobe (Haiku LLM)

- **Package:** `internal/cortex/lobes/planupdate/`
- **Constructor:**
  ```go
  func NewPlanUpdateLobe(planPath string, runtime *conversation.Runtime,
      client apiclient.Client, escalate Escalator) *PlanUpdateLobe
  ```
- **Trigger cadence:** Every 3rd assistant turn boundary OR whenever the user message contains an action-verb cluster from the cortex-core verb-scan helper. (Avoids spamming Haiku on chit-chat.)
- **Behavior:** Reads current `plan.json` (via existing `internal/plan` package — `plan.Load`), composes a Haiku request that asks the model to (a) list any new tasks the user implied, (b) list deps it newly inferred, (c) flag any plan items that became obsolete. Returns structured JSON. Diff is computed locally; **edits to existing items auto-apply**, but **adds and removes** are queued as a Note `severity=info, tag="plan-confirm"` that the main thread surfaces to the user at idle. Only after user confirmation does the Lobe call `plan.Save(...)`.
- **Tool schema (verbatim):** the Lobe is tool-free — it relies on structured JSON output and an `output_schema` directive in the system prompt; no Anthropic `tools` array.
- **System prompt (verbatim, copy-paste-ready):**

  ```text
  You are PlanUpdateLobe, a background helper inside the r1-agent cortex. You watch the conversation transcript and the current plan.json and propose minimal updates. You DO NOT write to plan.json yourself; you propose JSON deltas that the main agent decides whether to apply.

  Goals (in priority order):
  1. Detect newly-introduced tasks the user mentioned but the plan does not yet contain.
  2. Detect newly-implied dependencies between existing tasks.
  3. Detect plan items that the conversation has rendered obsolete (cancelled, completed out-of-band, scope-cut).
  4. NEVER invent tasks the user did not actually request. Bias toward silence.

  Output format — return ONLY this JSON object, no prose:
  {
    "additions":   [{"id":"<short-slug>","title":"...","deps":["<existing-id>",...]}],
    "removals":    [{"id":"<existing-id>","reason":"..."}],
    "edits":       [{"id":"<existing-id>","field":"title|deps|priority","new":"..."}],
    "confidence":  0.0-1.0,
    "rationale":   "one sentence per non-trivial proposal"
  }

  Rules:
  - If confidence < 0.6, return empty arrays. Silence > noise.
  - Edits to existing items will auto-apply. Additions and removals require user confirmation — phrase them tentatively.
  - Use existing task IDs verbatim. Never renumber.
  - Do NOT include conversational text outside the JSON object.
  ```

- **Note shape:**
  ```go
  cortex.Note{
      LobeID:"PlanUpdateLobe", Severity:cortex.SeverityInfo,
      Tags:[]string{"plan-confirm"},
      Title:"Proposed plan changes (3 adds, 1 remove)",
      Body: jsonDeltaPretty,
      Meta: map[string]any{
          "action_kind":    "user-confirm",
          "action_payload": jsonDelta,
      },
  }
  ```

### 5. ClarifyingQLobe (Haiku LLM)

- **Package:** `internal/cortex/lobes/clarifyq/`
- **Constructor:**
  ```go
  func NewClarifyingQLobe(runtime *conversation.Runtime,
      client apiclient.Client, escalate Escalator) *ClarifyingQLobe
  ```
- **Behavior:** Detects ambiguity by running a short Haiku call after every user turn. If the model reports `ambiguity_score >= 0.5`, the Lobe drafts up to 3 clarifying questions and queues them as Notes. The main thread surfaces them only when the action loop is idle (no in-flight tool_use). User answers fire async into the Workspace as `cortex.user.answered_question` hub events; the Lobe acknowledges by calling `Workspace.Resolve(noteID)`.
- **Tool schema (verbatim):**
  ```json
  [
    {
      "name": "queue_clarifying_question",
      "description": "Queue a clarifying question for the user. Surfaced at idle, never mid-tool-call. Maximum 3 outstanding.",
      "input_schema": {
        "type": "object",
        "properties": {
          "question":   {"type":"string", "description":"One sentence, ≤140 chars."},
          "category":   {"type":"string", "enum":["scope","constraint","preference","data","priority"]},
          "blocking":   {"type":"boolean", "description":"True if work cannot proceed without an answer."},
          "rationale":  {"type":"string", "description":"Why this is unclear, ≤200 chars."}
        },
        "required": ["question","category","blocking"]
      }
    }
  ]
  ```
- **System prompt (verbatim, copy-paste-ready):**

  ```text
  You are ClarifyingQLobe inside r1-agent's cortex. You watch the user's most recent message in context. Your only job is to detect actionable ambiguity and propose at most 3 clarifying questions via the queue_clarifying_question tool.

  Definition of actionable ambiguity:
  - The user gave an instruction the agent cannot execute correctly without one missing fact.
  - Examples: "deploy it" (where? prod?), "make it faster" (what metric? what target?), "fix the test" (which test? what failure?).
  - NOT ambiguity: stylistic preferences the agent can reasonably default, follow-ups already implied by recent context, polite chit-chat.

  Hard constraints:
  - Maximum 3 tool calls. If you have 0 questions, output no tool calls and the assistant message "no_ambiguity".
  - Each question must be answerable in ≤2 sentences.
  - Never ask "are you sure?" — that is not clarification.
  - Never repeat a question already pending in the workspace; you will be told which are pending in the user-message preamble.
  - When the conversation contains technical jargon you do not understand, ask only if it changes the action — not for your own education.

  Surface order: blocking questions first; then "scope"/"constraint"; then everything else.
  ```

- **Note shape:**
  ```go
  cortex.Note{
      LobeID:"ClarifyingQLobe",
      Severity: ifBlocking(cortex.SeverityWarning, cortex.SeverityInfo),
      Tags:[]string{"clarify"},
      Title: question,
      Body: rationale,
      Meta: map[string]any{
          "action_kind":         "user-answer",
          "action_payload":      questionID,
          "expires_after_round": 5, // stale Qs auto-evict
      },
  }
  ```

### 6. MemoryCuratorLobe (Haiku LLM)

- **Package:** `internal/cortex/lobes/memorycurator/`
- **Constructor:**
  ```go
  func NewMemoryCuratorLobe(mem *memory.Store, wis *wisdom.Store,
      client apiclient.Client, escalate Escalator,
      privacyCfg PrivacyConfig) *MemoryCuratorLobe

  type PrivacyConfig struct {
      AutoCurateCategories []memory.Category // default {CatFact} per OQ-7
      SkipPrivateMessages  bool              // default true; respects "private" tag
      AuditLog             io.Writer         // append-only JSON line per auto-write
  }
  ```
- **Behavior:** Every 5 assistant turn boundaries OR on `task.completed` hub events, the Lobe scans the last N messages and asks Haiku to extract "should-remember" moments. Output is a list of candidate `memory.Entry`s. Privacy filter:
  - Drop any candidate whose source message is tagged `private`.
  - Auto-write only candidates whose `Category ∈ PrivacyConfig.AutoCurateCategories` (default `{CatFact}` per OQ-7 in the open-questions log).
  - Other categories (`gotcha`, `pattern`, `preference`, `anti_pattern`, `fix`) are queued as a Note `tag="memory-confirm"` for user approval.
- **Audit trail:** every auto-write is appended to `~/.r1/cortex/curator-audit.jsonl` as `{"ts":..., "entry_id":..., "category":..., "content_sha":..., "source_msg_id":...}`.
- **Tool schema (verbatim):**
  ```json
  [
    {
      "name": "remember",
      "description": "Persist a project-fact to long-term memory. Use sparingly — only durable, non-private facts.",
      "input_schema": {
        "type": "object",
        "properties": {
          "category": {"type":"string", "enum":["gotcha","pattern","preference","fact","anti_pattern","fix"]},
          "content":  {"type":"string", "description":"Single declarative sentence, ≤200 chars."},
          "context":  {"type":"string", "description":"What in the conversation triggered this, ≤200 chars."},
          "file":     {"type":"string", "description":"Related file path, optional."},
          "tags":     {"type":"array", "items":{"type":"string"}}
        },
        "required":["category","content"]
      }
    }
  ]
  ```
- **System prompt (verbatim, copy-paste-ready):**

  ```text
  You are MemoryCuratorLobe inside r1-agent's cortex. You read recent conversation segments and decide whether anything is worth durably remembering.

  WRITE only when ALL of the following hold:
  1. The fact is project-scoped (codebase facts, build commands, conventions, deploy targets, naming patterns) — not personal, not transient, not session-specific.
  2. The user or the agent has already validated the fact in this session (it is stated, not hypothesized).
  3. The fact would still be useful 30 days from now.
  4. The fact is not already in memory (you will see existing memory excerpts in the user-message preamble).

  REFUSE TO WRITE when:
  - The source message is tagged "private" or contains personal/identifying details.
  - The fact is a one-off bug that has already been fixed.
  - The fact is the user's mood, tone, or social preference (e.g. "user prefers terse replies"). Those are not codebase facts.
  - You are uncertain — bias toward silence.

  Output: zero or more remember() tool calls. After tool calls, output the assistant message "curated_<N>" where N is the count, or "curated_0" if none.

  Categories — pick exactly one per call:
  - fact: codebase facts (file layout, command-to-run, deploy target).
  - gotcha: a footgun that bit somebody and would bite again.
  - pattern: a recurring solution the project uses.
  - preference: explicit user preference about how code should be written.
  - anti_pattern: an approach the project explicitly avoids.
  - fix: a specific repair pattern proven to work for a class of bug.

  Default category: fact. The harness only auto-applies "fact" without asking the user; other categories will be queued for confirmation.
  ```

- **Note shape (auto-write):**
  ```go
  cortex.Note{LobeID:"MemoryCuratorLobe", Severity:cortex.SeverityInfo,
              Tags:[]string{"memory"},
              Title:"Remembered: " + truncate(content,60), Body: content}
  ```
- **Note shape (queued for confirm):**
  ```go
  cortex.Note{LobeID:"MemoryCuratorLobe", Severity:cortex.SeverityInfo,
              Tags:[]string{"memory-confirm"},
              Title:"Remember this?", Body: content,
              Meta: map[string]any{
                  "action_kind":    "user-confirm",
                  "action_payload": candidateJSON,
              }}
  ```

## Privacy & Opt-Out

- Per-Lobe enable flags in `~/.r1/config.yaml`:
  ```yaml
  cortex:
    lobes:
      memoryrecall:   {enabled: true}
      walkeeper:      {enabled: true}
      rulecheck:      {enabled: true}
      planupdate:     {enabled: true,  escalate: true}
      clarifyq:       {enabled: true,  escalate: false}
      memorycurator:  {enabled: true,  escalate: false,
                      auto_curate_categories: [fact]}
  ```
- "Private" message taxonomy: any message whose `metadata.tags` contains `"private"` (set via `r1 chat --private` flag or programmatic API). `MemoryCuratorLobe` MUST skip these. `MemoryRecallLobe` MAY index them but MUST NOT surface their content in Notes (only metadata: "private memory exists about $topic").
- Audit trail: `MemoryCuratorLobe.AuditLog` writer mandatory; default path `~/.r1/cortex/curator-audit.jsonl`. CLI: `r1 cortex memory audit` reads + pretty-prints.
- Out-of-the-box default: every Lobe enabled, escalation off except for `planupdate`. Operator can disable per-Lobe in `~/.r1/config.yaml`; changes take effect on daemon restart. (Config hot-reload is intentionally **out of scope** — `internal/config` does not currently expose a watcher; adding one is a separate spec.)

## Test Plan

Each Lobe ships with two test files in its package:

- `*_lobe_test.go` — table-driven unit tests with a fake `cortex.Workspace` and a fake `apiclient.Client` (for LLM Lobes) that returns canned JSON. No real Anthropic calls.
- `*_integration_test.go` — uses the cortex-core test harness (`cortex.NewFake(t)`) to spin up an in-memory Workspace, register the Lobe, drive synthetic messages, and assert published Notes.

Coverage requirements:
- Determinstic Lobes: 90%+ line coverage. Property test: published Notes are deterministic given the same inputs.
- LLM Lobes: assertions against canned model responses. Test the **prompt assembly** (system prompt verbatim, tools sorted alphabetically, max_tokens set, cache_control set) and the **response handling** (tool-call → Note, malformed JSON → no Note + warning log, schema-violating arg → drop call).
- Privacy: `MemoryCuratorLobe` integration test must demonstrate that messages tagged `private` produce zero `remember()` tool calls, regardless of model output.

## Out of Scope

- Cortex-core internals (Workspace mutex strategy, Lobe scheduler, semaphore impl) — see spec 1.
- Lane rendering / TUI / web surfaces — see spec 3 (lanes-protocol) + specs 4–7.
- MCP exposure of Lobes (cross-process Lobe definitions) — see spec 8.
- The Router LLM that handles mid-turn user input — that lives in cortex-core (D-2026-05-02-04). Lobes only publish Notes.
- Mission orchestration, scheduler changes, branch supervision — unchanged from existing.
- New supervisor rules. `RuleCheckLobe` only consumes whatever rules are registered by the supervisor manifest; adding new rules is governance work, not Lobe work.
- Config hot-reload. `internal/config` has no watcher today; per-Lobe enable changes require a daemon restart in this spec.
- Modifications to cortex-core's `Note` struct. This spec uses only the fields cortex-core (spec 1) defines (`ID, LobeID, Severity, Title, Body, Tags, Resolves, EmittedAt, Round, Meta`). Workflow-action data (user-confirm payloads, expiry rounds, refs) is carried in `Note.Meta` per the `meta_keys` convention defined in shared infra item #4.

## Implementation Checklist

> 36 self-contained items, grouped by Lobe. Each item: file path, function/test name, copy-paste-ready prompt text where applicable.

### Shared infra (5 items)

1. [ ] Create `internal/cortex/lobes/llm/prompt.go` with `LobePromptBuilder` struct + `Build(userMessage string, history []apiclient.Message) apiclient.MessagesRequest`. Sort tools alphabetically by `.Name`. Mark system prompt block with `cache_control: {type:"ephemeral", ttl:"1h"}`. Default `MaxTokens=500`. Test name: `TestLobePromptBuilder_SortsToolsAlphabetically`, `TestLobePromptBuilder_SetsCacheControl1h`, `TestLobePromptBuilder_DefaultMaxTokens500`.
2. [ ] Create `internal/cortex/lobes/llm/escalator.go` defining `type Escalator func(ctx context.Context, reason string) (model string)` and a default factory `NewEscalator(allowed bool) Escalator` returning `"claude-sonnet-4-6"` when allowed and `""` otherwise. Test: `TestEscalator_RespectsAllowedFlag`.
3. [ ] Add `cortex.lobes.<name>` config schema to `internal/config/schema.go` per the YAML in §Privacy. Test: `TestConfig_LobeFlagsParse`.
4. [ ] Document the `Note.Meta` key convention in `internal/cortex/lobes/llm/meta_keys.go`: `const (MetaActionKind="action_kind"; MetaActionPayload="action_payload"; MetaExpiresAfterRound="expires_after_round"; MetaRefs="refs")`. Lobes use these keys when populating `Note.Meta` so PlanUpdate/Clarify/Curator can express user-action workflows without modifying cortex-core's `Note` struct (spec 1 freezes that struct). Test: `TestMetaKeys_RoundTripJSON`.
5. [ ] Wire LLM-Lobe semaphore acquisition. Each LLM Lobe calls `cortex.AcquireLLMSlot(ctx)` before any `client.Messages` call and `Release()` after. Test: `TestLLMSlot_BlocksAtCapFiveByDefault`.

### MemoryRecallLobe (4 items)

6. [ ] Create `internal/cortex/lobes/memoryrecall/lobe.go` with `type MemoryRecallLobe struct{...}`, `func NewMemoryRecallLobe(mem *memory.Store, wis *wisdom.Store) *MemoryRecallLobe`, methods `Name() string`, `Run(ctx context.Context, ws *cortex.Workspace) error`. On start, build a `tfidf.Index` from `mem.RecallByCategory(...)` over all categories + `wis.Learnings()`. Test: `TestMemoryRecallLobe_BuildsIndexOnStart`.
7. [ ] Implement `OnRound(round cortex.Round)` handler that takes `round.LastUserMessage()` (last 1000 chars) as query, calls `mem.Recall(query, 5)` and `idx.Search(query, 5)`, dedups, publishes top-3 as Notes. Test: `TestMemoryRecallLobe_PublishesTopThreeNotes`, `TestMemoryRecallLobe_DedupesAcrossSources`.
8. [ ] Register a `hub.Subscriber` whose `Filter` matches `EventType("cortex.workspace.memory_added")` for incremental reindex; rebuild index from scratch each time (acceptable: corpus is small). Test: `TestMemoryRecallLobe_ReindexesOnMemoryAdded`.
9. [ ] Privacy: when an `Entry.Tags` contains `"private"`, publish a Note whose Body is `"private memory exists about: <Entry.File or first 30 chars of context>"` instead of the entry content. Test: `TestMemoryRecallLobe_RedactsPrivateEntries`.

### WALKeeperLobe (3 items)

10. [ ] Create `internal/cortex/lobes/walkeeper/lobe.go` with `type WALKeeperLobe`, `func NewWALKeeperLobe(h *hub.Bus, w *bus.Bus, framing WALFraming) *WALKeeperLobe`, and `WALFraming{TypePrefix string}` (default `"cortex.hub."`). On `Run`, call `h.Register(hub.Subscriber{ID:"walkeeper", Filter: func(hub.EventType) bool { return true }, Handler: ...})` and from the handler forward each event as `bus.Event{Type: framing.TypePrefix+string(evt.Type), Payload: marshaled, CausalRef: evt.ID}`. Test: `TestWALKeeperLobe_FramesAndForwards`.
11. [ ] Implement backpressure: track outstanding pending writes via a buffered channel (`cap=1000`). When channel ≥ `0.9*cap`, drop `info`-severity events and increment a counter; when counter > 0 every 30s emit a single Note `severity=warning, tag=wal`. Test: `TestWALKeeperLobe_DropsInfoOnBackpressure`, `TestWALKeeperLobe_EmitsBackpressureNote`.
12. [ ] Restart-replay verified: write 100 hub events, kill the Lobe, restart, assert WAL contains all 100 (no duplicates due to bus's idempotent ID). Test: `TestWALKeeperLobe_SurvivesRestartNoDup`.

### RuleCheckLobe (3 items)

13. [ ] Create `internal/cortex/lobes/rulecheck/lobe.go` with `type RuleCheckLobe`, `func NewRuleCheckLobe(sup *supervisor.Supervisor, wal *bus.Bus) *RuleCheckLobe`. Subscribe to `bus.Pattern{TypePrefix:"supervisor.rule.fired"}`. Test: `TestRuleCheckLobe_SubscribesToFiredPattern`.
14. [ ] Implement `severityFor(ruleName string) string` — `trust.*`, `consensus.dissent.*` → `critical`; `drift.*`, `cross_team.*` → `warning`; default → `info`. Test: `TestRuleCheckLobe_SeverityMapping` (table-driven over all 9 rule subdirs).
15. [ ] Convert each fired event to a Note with `ID="rule-"+evt.ID`, `Tag="rule:"+ruleName`, `Body=rationale`, `ExpiresAfterRound=0` (sticky). Critical Notes are picked up by cortex-core's `PreEndTurnCheckFn` and refuse `end_turn`. Test: `TestRuleCheckLobe_CriticalNotesAreSticky`, `TestRuleCheckLobe_PreEndTurnGate` (integration).

### PlanUpdateLobe (5 items)

16. [ ] Create `internal/cortex/lobes/planupdate/lobe.go` with `type PlanUpdateLobe`, constructor `NewPlanUpdateLobe(planPath string, runtime *conversation.Runtime, client apiclient.Client, escalate Escalator) *PlanUpdateLobe`. Test: `TestPlanUpdateLobe_Constructs`.
17. [ ] Implement trigger logic: every 3rd assistant turn boundary, OR on user message containing any verb from `cortex.ActionVerbs` (the cortex-core verb-scan helper). Test: `TestPlanUpdateLobe_TriggerCadence`.
18. [ ] Implement Haiku call using `LobePromptBuilder`, `Model="claude-haiku-4-5"`, `MaxTokens=800`. System prompt verbatim from §4 above. **Embed the system prompt as a `const planUpdateSystemPrompt = \`...\`` in `prompt.go` so the test can byte-equality-compare.** Test: `TestPlanUpdateLobe_SystemPromptByteEqual`.
19. [ ] Parse model JSON output. Auto-apply `edits` via `plan.Save(...)`. Queue `additions`+`removals` as a single Note with `Action.Kind="user-confirm"`. On malformed JSON, log warning + emit no Note. Test: `TestPlanUpdateLobe_AutoAppliesEditsButQueuesAddsRemoves`, `TestPlanUpdateLobe_MalformedJSONNoOp`.
20. [ ] On user confirmation event (`cortex.user.confirmed_plan_change`), apply queued additions/removals via `plan.Save`. Test: `TestPlanUpdateLobe_AppliesOnConfirmation`.

### ClarifyingQLobe (5 items)

21. [ ] Create `internal/cortex/lobes/clarifyq/lobe.go` with `type ClarifyingQLobe`, constructor signature from §5. Test: `TestClarifyingQLobe_Constructs`.
22. [ ] Define the `queue_clarifying_question` tool schema verbatim in `tool.go` as a `var clarifyTool = apiclient.Tool{...}`. Test: `TestClarifyingQLobe_ToolSchemaShape` (asserts JSON-equality with the spec snippet).
23. [ ] Embed the system prompt verbatim from §5 as `const clarifySystemPrompt`. Test: `TestClarifyingQLobe_SystemPromptByteEqual`.
24. [ ] Implement turn-after-user trigger: subscribe to `cortex.user.message` hub event; call Haiku once per user turn; cap outstanding clarify Notes at 3 (drop overflow tool calls silently). Test: `TestClarifyingQLobe_CapsAtThreeOutstanding`, `TestClarifyingQLobe_NoQuestionsWhenNotAmbiguous`.
25. [ ] Resolve Notes when matching `cortex.user.answered_question` arrives (match by question ID). Test: `TestClarifyingQLobe_ResolvesOnUserAnswer`.

### MemoryCuratorLobe (6 items)

26. [ ] Create `internal/cortex/lobes/memorycurator/lobe.go` with `type MemoryCuratorLobe`, `type PrivacyConfig` per §6, constructor signature from §6. Test: `TestMemoryCuratorLobe_Constructs`.
27. [ ] Define `remember` tool schema verbatim in `tool.go`. Test: `TestMemoryCuratorLobe_ToolSchemaShape`.
28. [ ] Embed the system prompt verbatim from §6 as `const curatorSystemPrompt`. Test: `TestMemoryCuratorLobe_SystemPromptByteEqual`.
29. [ ] Implement trigger: every 5 assistant turn boundaries OR on `task.completed` hub event. Take last N=20 messages, render into the user message, call Haiku via `LobePromptBuilder` (`MaxTokens=600`). Test: `TestMemoryCuratorLobe_TriggerCadence`.
30. [ ] Implement privacy filter: drop tool calls whose source-message-window includes any message with `tags:["private"]`; auto-apply only `Category ∈ AutoCurateCategories`; queue everything else as confirm-Note. Append every auto-write to `AuditLog` as JSONL. Test: `TestMemoryCuratorLobe_SkipsPrivateMessages`, `TestMemoryCuratorLobe_AutoAppliesOnlyConfiguredCategories`, `TestMemoryCuratorLobe_AppendsAuditLog`.
31. [ ] Add CLI command `r1 cortex memory audit` in `cmd/r1/cortex_memory_audit.go` that reads `~/.r1/cortex/curator-audit.jsonl` and pretty-prints. Test: `TestCmdCortexMemoryAudit_PrintsEntries`.

### Cross-cutting integration tests (5 items)

32. [ ] `internal/cortex/lobes/all_integration_test.go` — `TestAllLobes_BootInFakeCortex`. Spin up `cortex.NewFake(t)` with all six Lobes registered. Run a 10-message synthetic conversation. Assert: at least one Note from each Lobe was published; no panics; no goroutine leaks (use `goleak`).
33. [ ] `TestAllLobes_RespectCostBudget` — assert the LLM Lobes' aggregate output tokens stay under `30% × main_thread_output_tokens` per turn (the budget defined in D-2026-05-02-06 / cortex-core §G).
34. [ ] `TestAllLobes_HonorEnableFlags` — disable each Lobe in turn via config; assert `Run` is never called.
35. [ ] `TestAllLobes_SurviveDaemonRestart` — populate Workspace, kill cortex, restart from WAL, assert all unresolved Notes restored (D-C3 write-through).
36. [ ] `TestAllLobes_NoCacheBustOnFanOut` — make 5 Lobes call Haiku in parallel after pre-warm; assert ≥4 of 5 hit the cache (per RT-CONCURRENT §2 with pre-warming). Use a fake `apiclient.Client` that records `cache_read_input_tokens`.

## Acceptance Criteria

- WHEN a user types an ambiguous instruction THE SYSTEM SHALL publish ≤3 clarifying-question Notes within 1 turn boundary.
- WHEN any `supervisor.rule.fired` event whose name starts with `trust.` or `consensus.dissent.` is received THE SYSTEM SHALL publish a `critical`-severity Note that blocks `end_turn` until acknowledged.
- WHEN `MemoryCuratorLobe` decides to remember a fact AND its category is in `AutoCurateCategories` AND no source message is tagged `private` THE SYSTEM SHALL append one entry to `~/.r1/cortex/curator-audit.jsonl` and call `mem.Save()`.
- WHEN any Lobe is disabled via config THE SYSTEM SHALL skip its `Run` invocation and emit zero Notes from it.
- WHEN the cortex restarts THE SYSTEM SHALL replay all unresolved Notes from the WAL within 500 ms.

## Boundaries — What NOT To Do

- Do NOT modify `internal/memory`, `internal/wisdom`, `internal/tfidf`, or `internal/supervisor` source — Lobes are pure consumers.
- Do NOT add new supervisor rules in this spec — that is a separate governance concern.
- Do NOT spawn subprocesses for Lobes — they are in-process goroutines (per cortex-core §B).
- Do NOT call Anthropic without going through `LobePromptBuilder` — bypasses cache discipline and cost budget.
- Do NOT write to message history directly — only `Workspace.Publish(Note)`. The merge model is agent-decides (D-2026-05-02-04).
- Do NOT remove the privacy filter from `MemoryCuratorLobe` — auto-curation of non-`fact` categories is gated by user confirmation per OQ-7.
