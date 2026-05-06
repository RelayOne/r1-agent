# STATUS: BLOCKED — Item 33 (per-component stories)

Spec 8 §12 item 33:

> Author `*.stories.tsx` for every component in `web/src/components/`,
> each with the `parameters.agentic.actionables` contract from §7.

This item is **BLOCKED** in build/agentic-test-harness because the
required upstream is not present:

| Required input                  | Source spec        | Branch                | Status                          |
|---------------------------------|--------------------|-----------------------|---------------------------------|
| `web/src/components/*.tsx`      | spec 6 (web-chat-ui)| build/web-chat-ui     | **NOT MERGED into this worktree** |

This worktree is branched from
[`1887faaf checkpoint: start build agentic-test-harness`] — a tip
that does not yet include the spec 6 web/ tree. Without the
component sources, this item cannot author per-component stories
because there is nothing to wrap. The `web/` directory in this
checkpoint contains only the scaffold (.storybook/ config + package
.json from items 31-32).

## Resolution path

This item unblocks automatically when:

1. Spec 6 (`build/web-chat-ui`) merges, bringing
   `web/src/components/{App,ChatLog,LaneSidebar,Composer,Workspace,
   ToastTray,SessionPicker,...}.tsx` and friends.
2. Whoever does the merge runs `make agent-features-update` (item
   21) to record the initial golden a11y trees, then ships
   `*.stories.tsx` files under `web/src/components/` — one per
   actionable component, each declaring:

   ```ts
   parameters: {
     a11y: { role: "...", name: "..." },
     agentic: {
       actionables: [
         { role: "button", name: /^Kill lane /, mcp_tool: "r1.lanes.kill" },
         // ... one entry per interactive descendant
       ],
     },
   }
   ```

3. The lint at `tools/lint-view-without-api/` (item 35) cross-checks
   every `actionables[*].mcp_tool` against `R1ToolNames()` from this
   spec's `r1_server_catalog.go`. Mismatches fail the build.

## Why this is not a silent skip

Per the user's mandate ("Do NOT self-reduce scope. STATUS: BLOCKED
with full reason for cross-spec dependencies; never silent skip"),
this file is committed as the explicit BLOCKED record so:

- The next maintainer reading `git log` for build/agentic-test-
  harness sees the gap immediately.
- The Makefile target `make storybook-mcp-validate` (defined in
  item 34) refers to this file as the BLOCKED reason when no
  stories are present.
- Item 38 (lint cross-spec validation) explicitly carries STATUS:
  BLOCKED until this file is removed AND every web component has
  a matching story.
