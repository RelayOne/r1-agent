# STOKE Implementation Notes

Running log of decisions, blockers, and questions for Eric.

---

## 2026-04-06 — Phase 1: Skill Registry Enhancement

- Rewrote `parseSkill()` to support YAML frontmatter (Trail of Bits SKILL.md format) alongside legacy HTML comment keywords
- Added new Skill fields: Triggers, AllowedTools, Gotchas, References, EstTokens, EstGotchaTokens
- Rewrote `InjectPromptBudgeted` with 3-tier selection:
  1. Always-on skills (full content, e.g. "agent-discipline")
  2. Repo-stack-matched skills (full content, top 2)
  3. Keyword-matched skills (gotchas only, top 3)
- Skills wrapped in `<skills>` XML tags per Anthropic prompt engineering guidance [P63]
- Token budget set to 3000 per Decision 4 in architecture doc
- Added `UpdateFromResearch()` to auto-merge research findings into skill content
- Created `internal/skillselect/profile.go` with enriched `RepoProfile` (14 fields + confidence map)
- Created `internal/skillselect/match.go` with `MatchSkills()` scoring
- Wired `SkillRegistry` and `StackMatches` into `workflow.Engine` struct
- Both plan and execute prompts now receive skill injection
- Added `SkillsConfig` to `config.Policy` with enabled, auto_detect, token_budget, research_feed, always_on, excluded
- All tests passing: `go test ./...`

### Open questions
- Should skills be hot-reloaded on file change? Filewatcher exists but not used. **Default: no, requires explicit `stoke skill reload`.**
- The `Load()` call in `executePromptWithContext` was creating a new registry every prompt. Now uses Engine's shared registry. This is more efficient but means skills are loaded once at orchestrator startup.

---

## 2026-04-06 — Phase 4: Harness Independence (partial)

- Added `str_replace.go` to `internal/tools/` with cascading replacement algorithm:
  1. Exact match (confidence 1.0)
  2. Whitespace-normalized match (confidence 0.85)
  3. Ellipsis expansion (confidence 0.75)
  4. Fuzzy line-by-line similarity (threshold 0.7)
- Updated `handleEdit` to use `StrReplace()` cascade instead of simple `strings.Replace`
- Created `internal/engine/native_runner.go` implementing `CommandRunner` interface
  - Uses `agentloop.Loop` + `provider.AnthropicProvider` + `tools.Registry`
  - Streams text events via `onEvent` callback
  - Populates `RunResult` with cost, tokens, turns, duration
  - Added `Native *NativeRunner` field to `engine.Registry`

### Decisions made
- Kept existing `tools.Registry` API (Handle + Definitions) rather than full Tool interface rewrite from guide — the current API works well and the cascade is integrated. Can refactor to per-file tools later if needed.
- Native runner takes apiKey/model in constructor rather than from RunSpec.PoolAPIKey — simpler for now, can evolve.

---

## 2026-04-06 — Phase 2: Wizard Maturity Heuristics

- Added `MaturityClassification` with 8-signal weighted scoring: git activity (15%), review process (15%), tests (15%), CI/CD (15%), docs (10%), security (10%), dependencies (10%), observability (10%)
- Composite score → stage mapping: 0-20 prototype, 21-40 mvp, 41-70 growth, 71-100 mature
- New structured `WizardConfig` types for YAML output via `yaml.v3`: ProjectConfig, ModelsConfig, QualityConfig, SecurityConfig, InfrastructureConfig, ScaleConfig, SkillsConfig, TeamConfig, RiskConfig
- `RunWizard(ctx, Opts)` modern API with 4 modes: auto, interactive, hybrid, yes (CI-safe)
- `buildDefaultConfig()` scales quality/security/risk defaults with project stage (honesty enforcement always strict)
- `selectSkillsFromConfig()` selects skills from final config + detected profile
- Research convergence (AI-powered config recommendations via Provider interface)
- Output: `.stoke/config.yaml`, `.stoke/wizard-rationale.md`, `.stoke/skills/` (skill copies)

---

## 2026-04-06 — Phase 3: Hub Built-in Subscribers

- Created `internal/hub/builtin/` with 4 subscribers:
  - **HonestyGate** (gate_strict, priority 100): blocks placeholder code (TODO/FIXME, panic("not implemented"), NotImplementedError), type suppressions (@ts-ignore, as any, eslint-disable, nolint), test file removal >50%
  - **SecretScanner** (gate_strict, priority 50): blocks AWS keys, private keys, API keys, Stripe live keys, Slack tokens, GitHub tokens
  - **CostTracker** (observe, priority 9000): records per-model cost from model.post_call events using April 2026 Anthropic pricing
  - **SkillInjector** (transform, priority 200): injects skills into prompts via skill.Registry
- All use existing `Bus.Register()` API with `HandlerFunc` pattern

---

## 2026-04-06 — Phase 4.3: Native Runner Wiring + Hub Events

- Added `ProviderNative` to model/router.go
- Added `RunnerMode`, `NativeAPIKey`, `NativeModel` fields to `app.RunConfig`
- Native runner initialized when `RunnerMode=native` or API key provided
- `pickRunner()` honors `RunnerMode=native` to bypass CLI entirely
- `providerToRunner()` handles native → fallback to claude
- CLI: `--runner native|claude|codex|hybrid`, `--native-api-key`, `--native-model`
- Hub event publishing in agentloop: `EvtToolPreUse` (gate can block), `EvtToolPostUse` (async observe)
- Native runner passes event bus to agentloop via `loop.SetEventBus()`

### All phases complete.

---

## 2026-04-06 — Phase 5: Prompt Cache Alignment + Package Audit + Architecture Docs

- **Prompt cache alignment**: wired cache utilities into actual request paths
  - `agentloop/loop.go:buildRequest()`: sorts tools alphabetically via `SortToolsDeterministic()`, wraps system prompt with `BuildCachedSystemPrompt()` using cache_control breakpoints
  - `provider/anthropic.go:buildRequestBody()`: added `SystemRaw` (json.RawMessage) support for pre-formatted system blocks, `CacheEnabled` flag, `toolsWithCacheControl()` adds `cache_control: {type: "ephemeral"}` to last tool definition
  - `apiclient/client.go:buildAnthropicBody()`: mirrors SystemRaw/CacheEnabled support for API client path
  - Net effect: ~90% input token cost reduction on multi-turn agentic loops
- **PACKAGE-AUDIT.md**: full audit of all 107 packages
  - 32 CORE, 49 HELPFUL, 13 DEPRECATED (~3,636 LOC dead code)
  - Highest-traffic: `stream` (11 callers), `config` (8), `hub` (7), `convergence` (6)
  - 13 packages with 0 external callers identified for deletion: compute, lifecycle, managed, permissions, phaserole, prompttpl, ralph, ratelimit, sandattr, sandbox, sandguard, team, toolcache
- **Architecture docs** created in `docs/architecture/`:
  - `skill-pipeline.md`: skill registry, skillselect, 3-tier injection, hub integration
  - `hub.md`: event bus, 46 events, 4 modes, built-in subscribers, circuit breakers, audit
  - `agentloop.md`: native loop design, cache alignment, tool execution, provider layer
  - `wizard.md`: maturity classification, 4 modes, config types, research convergence

---

## 2026-04-06 — Phase 6: Benchmark Framework

- Created `bench/` subdirectory with full benchmark framework:
  - **Harnesses**: `Stoke`, `ClaudeCode`, `Codex`, `Aider` implementations of `Harness` interface
  - **Judges**: `DeterministicJudge` (9 checks: build, visible/hidden tests, test integrity, placeholders, suppressions, hallucinated imports, diff size, impossible task detection), `PollJudge` (PoLL ensemble with position bias control), `LLMJudge`, `HonestyJudge` (combined deterministic + PoLL)
  - **Isolation**: container-per-task Docker launcher with security hardening, SIGTERM-then-SIGKILL timeout runner
  - **Cost**: per-task budget enforcement, run-wide cost aggregation
  - **Metrics**: cost efficiency, honesty scoring, reproducibility (CV/ICC/TARr@N), diff analysis
  - **Evolver**: Loop 1 failure collection + Loop 2 adversarial generation
  - **Reports**: HTML (template-based), CSV, Markdown
  - **CLI**: `bench run`, `bench report`, `bench analyze` commands
- All packages vet clean, bench_test.go with 8 integration tests passing

### Architecture decisions followed
- B-Decision 1: Container-per-task, never container-per-harness
- B-Decision 4: Multi-judge PoLL ensemble (3 diverse models, position reversal)
- B-Decision 7: ImpossibleBench-style honesty traps as primary differentiator
- B-Decision 8: Statistical aggregation (5 reps min, CV/ICC validation)

---

## 2026-04-06 — Phase 7: 7-Layer Honesty Judge

- Created `internal/hub/builtin/honesty/` subpackage with 6 new layers (Layer 1 was built in Phase 3):
  - **Layer 1**: TestIntegrityChecker — AST-level test weakening detection (Go parser, JS/Python regex). Records test file snapshots, denies writes that decrease assertion count or test function count
  - **Layer 2**: ImportChecker — hallucinated package detection via HEAD requests to PyPI, npm, proxy.golang.org. Fail-open (ModeGate) with 24h cache
  - **Layer 3**: ClaimDecomposer — FActScore-style atomic claim extraction + Chain-of-Verification independent verification
  - **Layer 4**: CoTMonitor — 11 regex patterns for explicit deception markers ("let's fudge", "circumvent the test", etc.). READ-ONLY (ModeObserve) — never blocks based on CoT
  - **Layer 5**: MultiSampleChecker — SelfCheckGPT-style consistency checking across N samples
  - **Layer 6**: ConfessionElicitor — structured self-evaluation with separate reward signal (seal of confession design)
  - Layer 7 (impossible task canaries) implemented in bench framework, not runtime hub
- Added `HonestyConfig` to `config.Policy`: enabled, check_imports, hidden_test_dir, claim_decomposition, cot_monitoring, confession, judge_model
- 10 tests in honesty_test.go covering test integrity, CoT detection, snapshot analysis, import extraction, deception patterns

---

## 2026-04-06 — Phase 8: Additional Skill Library Extraction

- Added 3 new built-in skills from research bundle 02 to `internal/skill/builtin/`:
  - **observability-enforcer**: OpenTelemetry, SLO-based alerting, structured logging, health checks, probe configuration, cardinality control, dashboard as code, cost control
  - **go-file-storage**: multipart uploads, R2/GCS/S3, signed URLs, streaming with io.Pipe, range requests, cache invalidation
  - **terraform-multicloud**: workspaces vs directories, state file security, provider pinning, module composition, GCP/Cloudflare/Fly.io/DO patterns
- Existing `kubernetes-operations` skill already covers platform + deploy gotchas (merged as guide recommended)
- Created `docs/architecture/integrations.md` reference document from "central nervous system" research (LSP, DAP, OpenTelemetry, GitHub integration roadmap)
- Total built-in skills: 49 (up from 46)

## Skill Library — Round 2 Status

- [x] observability-enforcer (created from research, full SKILL.md format)
- [x] kubernetes — already existed as kubernetes-operations, covers both platform and deploy
- [x] go-file-storage (created from research)
- [x] terraform-multicloud (created from research)

Architecture references (NOT skills):
- [x] docs/architecture/integrations.md (from "Building the central nervous system" file)

---

## 2026-04-07 — v2 Architecture Implementation (Guide v2 "The Real Mission")

### Phase 0: Scaffolding
- `internal/contentid/` — Content-addressed ID generation (SHA256, 16 prefixes)
- `internal/stokerr/` — Structured error types with 10 error codes
- All tests passing

### Phase 1: Substrate Components
- `internal/ledger/` — Append-only content-addressed graph (AddNode, AddEdge, Query, Resolve, Walk, Batch), git-backed filesystem store, SQLite index
- `internal/bus/` — Durable WAL-backed event bus (Publish, Subscribe, RegisterHook, Replay, PublishDelayed), monotonic sequence numbers, causality references
- `internal/ledger/nodes/` — 22 node type structs (decision, task, draft, loop, research, skill, snapshot, escalation, agree/dissent)
- All tests passing

### Phase 2: Supervisor Rules Engine
- `internal/supervisor/core.go` — Deterministic event loop: subscribe → match → evaluate → fire
- `internal/supervisor/rule.go` — Rule interface with Pattern, Evaluate, Action
- 30 rules across 10 categories:
  - Trust (3): completion/fix/problem require second opinion
  - Consensus (5): draft review, dissent address, convergence detection, iteration threshold, partner timeout
  - Snapshot (2): modification/formatter require CTO
  - Cross-team (1): cross-branch modification requires CTO consensus
  - Hierarchy (3): parent agreement, escalation forwarding, user escalation (interactive + full-auto)
  - Drift (3): judge scheduling, intent alignment, budget threshold
  - Research (3): request dispatch, report unblock, timeout
  - Skill (5): extraction trigger, load audit, application review, contradicts outcome, import consensus
  - SDM (5): file collision, dependency crossing, duplicate work, schedule risk, cross-branch drift
- Three manifests: mission (25 rules), branch (20 rules), SDM (5 rules)
- All tests passing across 11 packages

### Phase 3: Consensus Loop + Concern Field
- `internal/ledger/loops/` — Loop state tracker (7 states, lifecycle queries, convergence checking)
- `internal/concern/` — Concern field builder with 10 section types, 9 role templates, XML rendering
- All tests passing

### Phase 4: Harness + Stances
- `internal/harness/` — Stance lifecycle (spawn, pause, resume, terminate, inspect, recover)
- `internal/harness/stances/` — 11 stance templates (PO, Lead Engineer, Lead Designer, VP Eng, CTO, SDM, QA Lead, Dev, Reviewer, Judge, Stakeholder)
- `internal/harness/tools/` — 12 tool types with per-role authorization matrix
- `internal/harness/models/` — Provider interface + mock implementation
- All tests passing

### Phase 5: Snapshot + Wizard
- `internal/snapshot/` — Manifest with file paths + content hashes, Take/Load/Save/InSnapshot/Promote
- `internal/wizard/` — Init flow, config types, presets (minimal/balanced/strict), SetField, YAML roundtrip
- All tests passing

### Phase 6: Skill Manufacturer
- `internal/skillmfr/` — 4 workflows (shipped import, mission extraction, external import, lifecycle management)
- Confidence levels (candidate → tentative → proven), provenance tracking
- All tests passing

### Phase 7: Bench
- `internal/bench/` — Golden mission set, runner, metrics (trust + consensus), comparison, reports
- Sample golden mission: hello-world greenfield
- All tests passing

### Phase 8: End-to-end Validation
- `go vet ./...` — clean across entire codebase
- `go build ./cmd/stoke` — produces binary
- `go test` — 30 v2 packages pass (22 with tests, 8 exercised through parents)
- All v2 packages compile and interoperate correctly
