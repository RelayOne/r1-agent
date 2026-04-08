# Deterministic Scan
## Findings (critical:3 high:1 medium:3)
- [critical] ./stoke/internal/prompts/prompts.go:370 — FIXME/HACK: 3. Are there any TODO/FIXME items in the changed files?
- [critical] ./stoke/internal/prompts/prompts.go:261 — Placeholder: - Implement the task fully. No stubs, no TODOs, no placeholders.
- [critical] ./stoke/internal/prompts/prompts.go:298 — Placeholder:    - Quality: empty catches, type bypasses, weak tests, placeholder code?
- [medium] ./stoke/internal/prompts/prompts.go:266 — TypeScript any: - Do NOT use @ts-ignore, as any, eslint-disable, or equivalent.
- [medium] ./stoke/internal/prompts/prompts.go:297 — TypeScript any:    - Security: any new injection points, auth bypasses, data leaks?
- [high] ./stoke/internal/prompts/prompts.go:266 — Type/lint suppressed: - Do NOT use @ts-ignore, as any, eslint-disable, or equivalent.
- [medium] ./stoke/internal/prompts/prompts.go:266 — Lint suppressed: - Do NOT use @ts-ignore, as any, eslint-disable, or equivalent.

