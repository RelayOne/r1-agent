# Universal context

Two markdown files in this directory are embedded into the stoke binary
via `//go:embed` and injected into **every** agent system prompt at
runtime — coding workers, briefing LLM, integration reviewer, task
judge, fix-DAG planner, reasoning loops.

## Files

- `coding-standards.md` — baseline output discipline: no stubs, real
  error handling, test-file semantics, verification before declaring
  done. Respected by every agent.
- `known-gotchas.md` — short-form recurring LLM failure patterns
  (`PATTERN → CORRECT`), organized by domain (pnpm, TypeScript, Go,
  etc).

## Overriding / extending

Users can append additional rules (not replace — layers are additive):

1. **User global**: `$HOME/.stoke/coding-standards.md`,
   `$HOME/.stoke/known-gotchas.md`
2. **Project local**: `<repoRoot>/.stoke/coding-standards.md`,
   `<repoRoot>/.stoke/known-gotchas.md`

## Merge order

`builtin` → `$HOME/.stoke/*` → `<repoRoot>/.stoke/*`. Each present
layer is appended after the previous. Missing files are silently
skipped.

## Format

LLM-dense bullets. Assume the reader is a language model, not a human.
Every bullet should state one concrete rule or one `wrong → right`
transformation. No prose paragraphs.

## Warning

These files are the safety floor. If you replace (rather than extend)
the builtin set, you lose the baseline guarantees that keep workers
from shipping stubs, bypasses, and unverified work.
