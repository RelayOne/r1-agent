# Wave C WAL

## 2026-04-30

INTENT: land the Wave C superiority slice by extending deterministic skills and the wizard with ledger-backed authoring receipts, registry registration, and queryable session evidence in `r1-agent` on `feat/r1-parity-wave-c`.

DONE: `stoke wizard run` can now persist a `skill_authoring_decisions` node plus linked source / IR / proof artifacts into a real ledger, `stoke wizard register` installs reviewed outputs into `skills/<skill-id>/`, and `stoke wizard query` can inspect either JSON decision logs or ledger-backed sessions.

Evidence:

- `internal/r1skill/wizard/ledgerlink/writer.go`
- `cmd/stoke/wizard_cmd.go`
- `internal/r1skill/registry/registry.go`
- `docs/SKILL-WIZARD.md`
- local smoke: `go run ./cmd/stoke wizard run --from <tmp>/legacy.md --source-format r1-markdown-legacy --mode headless --out-dir <tmp>/out --ledger-dir <tmp>/.r1/ledger --mission-id smoke-wave-c --created-by smoke-test`

## 2026-04-30 W36 canonical docs refresh

INTENT: refresh the canonical six-doc set for `r1-agent`, classify the work as documentation, and align the docs with the pack-registry and deterministic-runtime features already shipped on `main`.

DONE: `README.md`, `docs/ARCHITECTURE.md`, `docs/HOW-IT-WORKS.md`, `docs/FEATURE-MAP.md`, `docs/DEPLOYMENT.md`, and `docs/BUSINESS-VALUE.md` now describe the current parity, deterministic-skills, pack-registry, and runtime-audit baseline instead of the earlier cycle-close snapshot.

Evidence:

- `README.md`
- `docs/ARCHITECTURE.md`
- `docs/HOW-IT-WORKS.md`
- `docs/FEATURE-MAP.md`
- `docs/DEPLOYMENT.md`
- `docs/BUSINESS-VALUE.md`
