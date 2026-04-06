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

### What's next
- Phase 5: Prompt cache alignment, package audit (PACKAGE-AUDIT.md)
- Phase 6: Benchmark framework
- Phase 7: Honesty Judge (7-layer deception detection)
- Phase 8: Additional skill library extraction
