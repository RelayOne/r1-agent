---
name: invoice_processor_runtime
description: Deterministic runtime contract for the CloudSwarm invoice-ingestion flagship flow.
---

# invoice_processor_runtime

Use this capability when a CloudSwarm operator has already chosen the
invoice-processor flagship and needs the exact R1-side runtime contract:
required credentials, hero-skill order, approval gates, and the summary
shape that the flow should surface back to the workspace.

## Acceptance criteria

- Requires at least one inbox slug in `accounts`.
- Restricts `destination` to `quickbooks`, `google_sheets`, or `xero`.
- Returns the three-step hero-skill chain:
  `classify_documents` → `extract_structured_data` →
  `reconcile_accounting`.
- Emits approval guidance for large invoices and mismatch conditions.
