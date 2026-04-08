# Deterministic Scan
## Findings (critical:0 high:4 medium:5)
- [high] ./stoke/internal/scan/scan_test.go:45 — Console debug: 	os.WriteFile(filepath.Join(dir, "debug.js"), []byte("console.log('debug');
"), 0644)
- [medium] ./stoke/internal/scan/scan_test.go:11 — TypeScript any: 	os.WriteFile(filepath.Join(dir, "bad.ts"), []byte("// @ts-ignore
const x: any = 1;
"), 0644)
- [medium] ./stoke/internal/scan/scan_test.go:31 — TypeScript any: 	os.WriteFile(filepath.Join(dir, "cast.ts"), []byte("const x = foo as any;
"), 0644)
- [high] ./stoke/internal/scan/scan_test.go:11 — Type/lint suppressed: 	os.WriteFile(filepath.Join(dir, "bad.ts"), []byte("// @ts-ignore
const x: any = 1;
"), 0644)
- [high] ./stoke/internal/scan/scan_test.go:97 — Type/lint suppressed: 	os.WriteFile(filepath.Join(dir, "bad.ts"), []byte("// @ts-ignore
"), 0644)
- [high] ./stoke/internal/scan/scan_test.go:98 — Type/lint suppressed: 	os.WriteFile(filepath.Join(dir, "also_bad.ts"), []byte("// @ts-ignore
"), 0644)
- [medium] ./stoke/internal/scan/scan_test.go:109 — Lint suppressed: 	os.WriteFile(filepath.Join(dir, "bad.py"), []byte("x = 1  # noqa: E501
"), 0644)
- [medium] ./stoke/internal/scan/scan_test.go:114 — Lint suppressed: 		if f.Rule == "no-noqa" { found = true }
- [medium] ./stoke/internal/scan/scan_test.go:117 — Lint suppressed: 		t.Error("expected no-noqa finding")

