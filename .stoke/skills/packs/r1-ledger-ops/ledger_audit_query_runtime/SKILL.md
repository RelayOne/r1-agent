---
name: ledger_audit_query_runtime
description: Deterministic read-only audit query over an R1 ledger directory.
---

# ledger_audit_query_runtime

Use this capability when an operator needs a structured audit slice from
an existing local ledger without writing custom SQLite or JSON tooling.

## Acceptance criteria

- Requires an explicit `ledger_dir`.
- Supports mission, node-type, author, and time-window filters.
- Returns per-type counts plus an ordered node list.
- Keeps the runtime read-only and only includes raw node content when
  `include_content` is set.