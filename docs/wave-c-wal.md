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
