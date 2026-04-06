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

### What's next
- Wire native runner into workflow dispatch (need `--runner native` flag or config option)
- Add hub event publishing for tool execution (EvtToolPreUse/EvtToolPostUse)
- Phase 2 (wizard) and Phase 3 (hub enhancements) per implementation guide

### Decisions made
- Kept existing `tools.Registry` API (Handle + Definitions) rather than full Tool interface rewrite from guide — the current API works well and the cascade is integrated. Can refactor to per-file tools later if needed.
- Native runner takes apiKey/model in constructor rather than from RunSpec.PoolAPIKey — simpler for now, can evolve.
