# Wave A WAL

## 2026-04-29

INTENT: land the first branchable Wave A slice by integrating Parity-2 artifacts and ledger-native plan approval into `r1-agent` on `feat/r1-parity-wave-a`.

DONE: artifact storage, artifact/annotation node types, Antigravity import-export conversion, `r1 artifact` CLI, and `r1 plan --approve` ledger emission landed in this branch.

Evidence:

- `internal/artifact/`
- `internal/ledger/nodes/artifact.go`
- `internal/ledger/nodes/artifact_annotation.go`
- `cmd/r1/artifact_cmd.go`
- `internal/plan/artifact.go`
