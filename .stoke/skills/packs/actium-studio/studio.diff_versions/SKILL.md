---
name: studio.diff_versions
description: Compare two Studio snapshots and return a structured diff.
---

# studio.diff_versions

Hero skill. Composes `list_snapshots` + per-page fetches + client-side
diff. Read-only; safe to call repeatedly.

## Acceptance criteria

- Response contains the base and head snapshot ids verbatim.
- `pages_changed` array is well-formed (each entry has `page_id` and
  valid `change_type`).
- Re-running the skill against the same pair yields the same diff
  (determinism — content-addressed snapshots).
