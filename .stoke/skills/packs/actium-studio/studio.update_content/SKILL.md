---
name: studio.update_content
description: Edit a page and optionally publish in one call.
---

# studio.update_content

Hero skill. Composes Studio's `update_page` and `publish_page` tools
into a single atomic action so content-ops flows don't need to juggle
two skills per edit.

## Acceptance criteria

- Response echoes `page_id` and a populated `updated_at` timestamp.
- When `publish: true`, `published` is true and `published_url` is a
  valid HTTPS URL.
- Follow-up `get_page` (via studio.site_status or direct) returns the
  new content (read-after-write consistency).
