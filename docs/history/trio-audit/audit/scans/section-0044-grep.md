# Deterministic Scan
## Findings (critical:0 high:1 medium:3)
- [medium] ./stoke/internal/workflow/workflow.go:498 — TypeScript any: 			// Exact set comparison: any difference in file sets fails the task.
- [medium] ./stoke/internal/workflow/workflow.go:729 — TypeScript any: 		sb.WriteString("  - Use @ts-ignore, as any, or eslint-disable
")
- [high] ./stoke/internal/workflow/workflow.go:729 — Type/lint suppressed: 		sb.WriteString("  - Use @ts-ignore, as any, or eslint-disable
")
- [medium] ./stoke/internal/workflow/workflow.go:729 — Lint suppressed: 		sb.WriteString("  - Use @ts-ignore, as any, or eslint-disable
")

