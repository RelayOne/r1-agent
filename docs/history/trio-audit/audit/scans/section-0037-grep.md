# Deterministic Scan
## Findings (critical:4 high:6 medium:3)
- [critical] ./stoke/internal/scan/scan.go:59 — FIXME/HACK: 		{ID: "no-todo-fixme", Severity: "low", Pattern: regexp.MustCompile(`(?i)(TODO|FIXME|HACK|XXX):`), Message: "TODO/FIXME
- [critical] ./stoke/internal/scan/scan.go:58 — Skipped test: 		{ID: "no-test-only", Severity: "critical", Pattern: regexp.MustCompile(`\.(only|skip)\(`), Message: ".only() or .skip(
- [critical] ./stoke/internal/scan/scan.go:71 — Placeholder: 		{ID: "no-test-todo", Severity: "high", Pattern: regexp.MustCompile(`test\.todo\(|it\.todo\(`), Message: "Unfinished te
- [critical] ./stoke/internal/scan/scan.go:72 — Placeholder: 		{ID: "no-placeholder-code", Severity: "high", Pattern: regexp.MustCompile(`NotImplementedError|CHANGEME|placeholder|pa
- [high] ./stoke/internal/scan/scan.go:52 — Console debug: 		{ID: "no-console-log", Severity: "medium", Pattern: regexp.MustCompile(`console\.log\(`), Message: "console.log left i
- [high] ./stoke/internal/scan/scan.go:55 — Debug macro: 		{ID: "no-dbg-macro", Severity: "medium", Pattern: regexp.MustCompile(`dbg!\(`), Message: "dbg! macro left in code", Fi
- [medium] ./stoke/internal/scan/scan.go:44 — TypeScript any: 		{ID: "no-as-any", Severity: "high", Pattern: regexp.MustCompile(`as\s+any`), Message: "'as any' assertion bypasses typ
- [high] ./stoke/internal/scan/scan.go:42 — Type/lint suppressed: 		{ID: "no-ts-ignore", Severity: "critical", Pattern: regexp.MustCompile(`@ts-ignore`), Message: "@ts-ignore bypasses ty
- [high] ./stoke/internal/scan/scan.go:43 — Type/lint suppressed: 		{ID: "no-ts-nocheck", Severity: "critical", Pattern: regexp.MustCompile(`@ts-nocheck`), Message: "@ts-nocheck disables
- [high] ./stoke/internal/scan/scan.go:45 — Type/lint suppressed: 		{ID: "no-eslint-disable", Severity: "high", Pattern: regexp.MustCompile(`eslint-disable`), Message: "eslint-disable su
- [high] ./stoke/internal/scan/scan.go:47 — Type/lint suppressed: 		{ID: "no-type-ignore", Severity: "high", Pattern: regexp.MustCompile(`#\s*type:\s*ignore`), Message: "type: ignore sup
- [medium] ./stoke/internal/scan/scan.go:45 — Lint suppressed: 		{ID: "no-eslint-disable", Severity: "high", Pattern: regexp.MustCompile(`eslint-disable`), Message: "eslint-disable su
- [medium] ./stoke/internal/scan/scan.go:46 — Lint suppressed: 		{ID: "no-noqa", Severity: "high", Pattern: regexp.MustCompile(`#\s*noqa`), Message: "noqa suppresses linter warnings",

