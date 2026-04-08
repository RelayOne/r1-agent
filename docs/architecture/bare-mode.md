# --bare Mode Audit

## Policy

`--bare` is explicitly forbidden for task dispatch. The spec states:

> Never use `--bare` for dispatched tasks — Anthropic documents that `--bare`
> skips hooks, skills, plugins, MCP servers, auto memory, and CLAUDE.md.

`claude -p` (without `--bare`) is the correct invocation pattern. It loads
hooks, enforcer guards, and project context (CLAUDE.md).

## Audit Results

Grep of `--bare` across the codebase:

| Location | Usage | Status |
|----------|-------|--------|
| `docs/stoke-spec-final.md` | Design decision documentation | OK — documents the prohibition |
| `internal/engine/claude.go` | Not used | OK — uses `-p` without `--bare` |
| `internal/engine/codex.go` | Not used | OK — uses `exec` subcommand |
| `cmd/stoke/main.go` | Not used | OK |

## Conclusion

`--bare` is not used anywhere in the Go source code. The prohibition is
documented in the spec and enforced by the fact that the Claude runner
constructs its arguments without `--bare`. The PreToolUse hook additionally
blocks nested `claude -p` invocations, which would be the vector for an
agent to invoke `--bare` on its own.

No code changes needed. This question is closed.
