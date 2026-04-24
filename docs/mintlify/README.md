# Mintlify docs source for R1

This directory contains the Mintlify-ready MDX sources for
`docs.r1.dev`. The Mintlify site configuration lives at
`docs/mint.json` at the top of the `docs/` tree (one level up).

## Scope

Per the portfolio-level Mintlify work order
(`plans/work-orders/work-mintlify-docs.md`), R1 docs follow
**Option (a): docs live in-repo at `docs/`**; Mintlify pulls
via its GitHub App on each push.

This seed ships:

- `docs/mint.json` — site config (navigation, theme tokens,
  topbar, tabs, footer) with brand primary `#6366F1` (indigo)
  and teal accent per §9.2.
- `docs/mintlify/introduction.mdx` — landing page seed.
- `docs/mintlify/quickstart.mdx` — five-minute walkthrough seed.
- `docs/mintlify/install.mdx` — install paths seed.
- `docs/mintlify/rename/stoke-to-r1.mdx` — rename notice with
  identifier-mapping table.

The full chapter tree (from work-mintlify-docs §2.1) is wired
into `mint.json` navigation. Prose for each subsequent chapter
ports from existing `docs/*.md` during phase execution, per
§0 of the work order which explicitly scopes content-writing
to the per-phase execution windows (D1 for R1).

## Belongs to the sibling sites repo

Items that live outside this repo (in the Mintlify org
configuration and the per-org sibling work orders):

- Mintlify org account provisioning.
- DNS / CNAME setup for `docs.r1.dev`.
- Mintlify GitHub App installation and path-scoping.
- Deploy credentials, SSL certificates, analytics integration.
- The `@goodventures/mintlify-components` shared component
  package (rename banner, portfolio footer, cross-product link
  resolver, compare-table, version banner, degradation-mode card).
- The umbrella `docs.goodventures.studio` site (lives in a
  separate `goodventures-docs` repo per §4.2).

## Source material

Most chapter pages wired into `mint.json` navigation will be
ported from existing `docs/*.md` files in this repo:

- `docs/README.md` → getting-started landing
- `docs/ARCHITECTURE.md` → governance / core-concepts split
- `docs/FEATURE-MAP.md` → concepts index
- `docs/HOW-IT-WORKS.md` → concepts tour
- `docs/operator-guide.md`, `docs/deploy-executor.md`,
  `docs/browser-executor.md` → operator guide section
- `docs/mcp-security.md` → security section
- `docs/trustplane-integration.md` → integrations section
- `docs/stoke-protocol.md` → protocols section (to be renamed
  `r1-protocol.md` per rename-in-flight)
- `docs/bench-swebench.md`, `docs/anti-deception-matrix.md`,
  `docs/bench-corpus-format.md` → benchmarks section

When porting, rename `.md` → `.mdx` only if the page needs
Mintlify components (`<Callout>`, `<Tabs>`, `<CodeGroup>`,
`<Card>`). Mintlify accepts both extensions.

## References

- `plans/work-orders/work-mintlify-docs.md` — portfolio-level
  Mintlify work order (all 9 products + umbrella).
- `plans/work-orders/work-r1-rename.md` — rename-in-flight
  tracker; drives the rename-notice page.
- `plans/work-orders/work-goodventures-sites.md` §Phase D —
  origin of the Mintlify phase.
