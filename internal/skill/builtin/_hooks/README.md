# Stoke hook files

Hooks are per-role / per-scenario / per-phase markdown blurbs that
stoke injects into specific LLM call sites on top of the universal
context. Where universal context gives EVERY agent the project's
coding standards and gotchas, hooks are narrow: "when the Phase-2
repair loop dispatches a worker, inject THIS additional guidance".

## Directory structure

```
internal/skill/builtin/_hooks/
  agents/         <role>.md     — per-agent hooks (worker-*, judge-*, planner-*, chat-*)
  scenarios/      <name>.md     — situational hooks (retry-attempt, fix-dag-session, ...)
  phases/         <name>.md     — phase-transition hooks (0-briefing, 1-4-integration-review, ...)
```

Each file has three layers of content, applied in order:

1. **Builtin** — embedded in the stoke binary from this directory.
2. **User** — `$HOME/.stoke/hooks/<kind>/<name>.md` (if present).
3. **Project** — `<repoRoot>/.stoke/hooks/<kind>/<name>.md` (if present).

Layers are **appended**, not replaced. Later layers extend earlier ones;
they don't wipe them. If you want to override builtin content entirely,
delete it from this directory in a fork — there's no per-layer replace
semantics, by design. Missing user/project files are silently skipped.

## File format

```markdown
# <Title>

> One-line description of when this fires.

<!-- keywords: <optional, comma, separated> -->

## Intent

<Why this hook exists. What to keep in mind during this step.>

## Baseline rules

- <bullet>
- <bullet>

## Anti-patterns to avoid   (optional)

- <bullet>
```

Content can be sparse. Even just the header + "(no additional hook
rules)" is fine — the wiring exists, and users extend by editing.

## Hooks are ADDITIVE

Hooks do NOT replace the universal context
(`internal/skill/builtin/_universal/`). Both are injected into every
relevant call. Use the universal layer for project-wide rules; use
hooks for role/scenario/phase-specific nudges.
