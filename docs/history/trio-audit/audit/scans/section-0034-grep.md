# Deterministic Scan
## Findings (critical:3 high:2 medium:4)
- [critical] ./stoke/internal/hooks/hooks.go:339 — FIXME/HACK: - No placeholder code (TODO, FIXME, NotImplementedError)
- [critical] ./stoke/internal/hooks/hooks.go:341 — Skipped test: - No test.todo or .skip()
- [critical] ./stoke/internal/hooks/hooks.go:339 — Placeholder: - No placeholder code (TODO, FIXME, NotImplementedError)
- [medium] ./stoke/internal/hooks/hooks.go:168 — TypeScript any:     if echo "$TOOL_OUTPUT" | grep -qE '@ts-ignore|as any|eslint-disable|# type: ignore|# noqa|\.only\(' 2>/dev/null; the
- [medium] ./stoke/internal/hooks/hooks.go:337 — TypeScript any: - No @ts-ignore, as any, eslint-disable, # noqa, or // nolint
- [high] ./stoke/internal/hooks/hooks.go:168 — Type/lint suppressed:     if echo "$TOOL_OUTPUT" | grep -qE '@ts-ignore|as any|eslint-disable|# type: ignore|# noqa|\.only\(' 2>/dev/null; the
- [high] ./stoke/internal/hooks/hooks.go:337 — Type/lint suppressed: - No @ts-ignore, as any, eslint-disable, # noqa, or // nolint
- [medium] ./stoke/internal/hooks/hooks.go:168 — Lint suppressed:     if echo "$TOOL_OUTPUT" | grep -qE '@ts-ignore|as any|eslint-disable|# type: ignore|# noqa|\.only\(' 2>/dev/null; the
- [medium] ./stoke/internal/hooks/hooks.go:337 — Lint suppressed: - No @ts-ignore, as any, eslint-disable, # noqa, or // nolint

