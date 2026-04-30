# r1-ledger-ops

Bundled deterministic read-only governance skills for R1.

Current contents:

- `ledger_audit_query_runtime` — query a local R1 ledger by mission,
  node type, author, and time window, then return a structured audit
  slice with counts and optional raw payloads.
- `metrics_collection_runtime` — snapshot in-process counters, gauges,
  timers, and spend metrics with optional prefix filtering for operator
  review.
