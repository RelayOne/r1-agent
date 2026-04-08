# Skill Pipeline Architecture

## Overview

The skill pipeline auto-injects reusable workflow patterns into AI prompts based on keyword matching and repository stack detection. Skills are markdown templates stored on disk with optional YAML frontmatter.

## Components

### skill.Registry (`internal/skill/`)

Central registry that discovers, loads, and indexes skills from three sources (in priority order):

1. **Project skills**: `.stoke/skills/` — checked into the repo
2. **User skills**: `~/.stoke/skills/` — personal library
3. **Built-in skills**: embedded in the binary (`builtin.go`)

Key types:
- `Skill` — name, keywords, triggers, content, gotchas, estimated tokens
- `Registry` — thread-safe map with `Load()`, `Match()`, `InjectPromptBudgeted()`
- `parseSkill()` — supports YAML frontmatter (AllowedTools, Triggers, Gotchas, References) and legacy HTML comment keywords

### skillselect (`internal/skillselect/`)

Stack-aware skill selection:

- `RepoProfile` — 14 detection fields (languages, frameworks, CI system, test framework, etc.) with confidence map
- `DetectProfile(root)` — file-system heuristics to build a profile
- `MatchSkills(profile)` — returns skill names matching the detected stack

### Injection Flow

```
workflow.Engine.Run()
  → skill.DefaultRegistry(repoRoot)
  → skillselect.DetectProfile(repoRoot)
  → skillselect.MatchSkills(profile)
  → Engine stores SkillRegistry + StackMatches
  → prompts.BuildExecutePrompt()
    → registry.InjectPromptBudgeted(prompt, budget=3000, stackMatches, task)
```

### InjectPromptBudgeted — 3-tier selection

Given a token budget (default 3000):

1. **Always-on skills** — full content (e.g., "agent-discipline")
2. **Repo-stack-matched skills** — full content, top 2 by priority
3. **Keyword-matched skills** — gotchas only, top 3

Skills are wrapped in `<skills>` XML tags per Anthropic prompt engineering guidance.

### Hub Integration

The `hub/builtin.SkillInjector` subscriber listens on `prompt.building` and `prompt.skills_matched` events, calling `InjectPromptBudgeted` to transform prompts in the event pipeline.

### Research Feed

`skill.Registry.UpdateFromResearch()` merges AI research findings into skill content at runtime, allowing skills to evolve during a session.

## Configuration

In `.stoke/config.yaml` (via wizard):

```yaml
skills:
  enabled: true
  auto_detect: true
  token_budget: 3000
  always_on: ["agent-discipline"]
  excluded: []
```
