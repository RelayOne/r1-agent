---
name: studio.site_status
description: Composite site health check for a Studio site.
---

# studio.site_status

Hero skill. Combines `get_site`, `get_scaffold_status`, and
`get_staging_info` into a single summary so agents don't have to pick
which of the three to call for a "what's happening?" intent.

## Acceptance criteria

- Response `status` is one of the four enum values.
- `pages_total`, `pages_published`, `pages_draft` are non-negative integers
  with `published + draft ≤ total`.
- Staging section populated iff `include_staging: true` or the site has
  a staging env.
