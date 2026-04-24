---
name: studio.scaffold_site
description: Create an Actium Studio site from a natural-language brief.
---

# studio.scaffold_site

Hero skill. One call → structured site + landing-page content + optional
deploy. Primary CloudSwarm integration surface (hero-skill registry §5.7).

## Acceptance criteria

- `site_id` returned as a non-empty string.
- `status` is one of `ready`, `scaffolding`, `failed`.
- When `deploy: true`, `url` is populated with a valid HTTPS URL within 120s.
- `steps` array contains `invent_structure`, `generate_content`, `persist`,
  and `deploy` (the last may be `not-run` when `deploy:false`).
