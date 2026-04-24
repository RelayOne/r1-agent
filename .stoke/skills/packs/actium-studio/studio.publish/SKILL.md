---
name: studio.publish
description: Publish a specific Studio page by id.
---

# studio.publish

Hero skill. Thin publish wrapper with a memorable name so agents select
it for "ship this page" intents instead of scrolling the thin-wrapper
pages.publish_page entry.

## Acceptance criteria

- `published: true` in the response.
- `published_at` is a valid RFC-3339 timestamp.
- Follow-up `get_page` reports the page as published (state machine
  transitioned from `draft` to `published`).
