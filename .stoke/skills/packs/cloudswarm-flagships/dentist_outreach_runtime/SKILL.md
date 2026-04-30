---
name: dentist_outreach_runtime
description: Deterministic runtime contract for the CloudSwarm dentist-outreach flagship flow.
---

# dentist_outreach_runtime

Use this capability when a CloudSwarm operator has already chosen the
dentist-outreach flagship and needs the exact R1-side runtime contract:
required credentials, hero-skill order, approval gates, and the summary
shape that the flow should return to the workspace.

## Acceptance criteria

- Requires at least one dental service line in `markets`.
- Requires a non-empty `location`.
- Restricts `crm` to `hubspot`, `google_sheets`, or `salesforce`.
- Returns the four-step hero-skill chain:
  `brave_search` → `hunter_io` → `gmail_draft` → `hubspot_create`.
- Emits approval guidance for every outbound send and CRM mutation.
