# Deterministic Scan
## Findings (critical:0 high:6 medium:4)
- [high] ./stoke/internal/failure/analyzer.go:263 — Console debug: 	{regexp.MustCompile(`console\.log`), "left console.log", "remove debug logging"},
- [medium] ./stoke/internal/failure/analyzer.go:257 — TypeScript any: 	{regexp.MustCompile(`as\s+any`), "used 'as any' assertion", "use a proper type"},
- [high] ./stoke/internal/failure/analyzer.go:256 — Type/lint suppressed: 	{regexp.MustCompile(`@ts-ignore`), "added @ts-ignore", "fix the actual type error"},
- [high] ./stoke/internal/failure/analyzer.go:258 — Type/lint suppressed: 	{regexp.MustCompile(`eslint-disable`), "disabled eslint rule", "fix the lint issue"},
- [high] ./stoke/internal/failure/analyzer.go:259 — Type/lint suppressed: 	{regexp.MustCompile(`# type:\s*ignore`), "used Python type: ignore", "fix the type error"},
- [medium] ./stoke/internal/failure/analyzer.go:258 — Lint suppressed: 	{regexp.MustCompile(`eslint-disable`), "disabled eslint rule", "fix the lint issue"},
- [medium] ./stoke/internal/failure/analyzer.go:260 — Lint suppressed: 	{regexp.MustCompile(`#\s*noqa`), "used noqa to suppress lint", "fix the lint issue"},
- [high] ./stoke/internal/failure/analyzer_test.go:53 — Type/lint suppressed: 	a := Analyze("", "", "found @ts-ignore in diff
  3:1  error  no-ts-ignore")
- [high] ./stoke/internal/failure/analyzer_test.go:60 — Type/lint suppressed: 	a := Analyze("", "", "// eslint-disable-next-line no-unused-vars")
- [medium] ./stoke/internal/failure/analyzer_test.go:60 — Lint suppressed: 	a := Analyze("", "", "// eslint-disable-next-line no-unused-vars")

