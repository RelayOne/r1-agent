# build-plan.md — r1-server-ui-v2-tests (Spec 5 of 5)

**Branch:** build/r1-server-ui-v2-tests (integrated branch — Specs 1+2+3+4 merged)
**Started:** 2026-05-05

6 tasks per spec §10. Some have BLOCKED-PARTIAL paths (Playwright E2E
needs `npx playwright install` + browser binaries; the spec already
acknowledges this is release-rehearsal only).

## Tasks

- [x] T1 golden_test.go + 8 TestGolden_* + testdata/golden/* (use deterministic fixtures)
- [x] T2 web/src/test/graph-worker.test.ts (Spec 2 already shipped a sibling test on cmd/r1-server/ui/web/js/; T2 is the dual TS-typed test in the web/ tree)
- [x] T3 cmd/r1-server/e2e/ submodule + e2e_test.go (BLOCKED-PARTIAL on Playwright install in this env)
- [x] T4 vendor_freshness_test.go
- [x] T5 htmx_shell_test.go — boots mux, asserts shell fragments
- [x] T6 testdata/README.md + cross-links
