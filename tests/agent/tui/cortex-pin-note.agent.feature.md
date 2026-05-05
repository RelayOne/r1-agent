# tests/agent/tui/cortex-pin-note.agent.feature.md

<!-- TAGS: smoke, tui, cortex -->
<!-- DEPENDS: cortex-core, cortex-concerns, tui-lanes -->

## Scenario: Pinning a Workspace Note from the TUI

- Given a fresh r1d daemon at "stdio"
- And a session "${SESSION_ID:-s-tui-cortex-1}" is active
- And the TUI is launched with the dashboard model
- And the cortex Workspace contains a Note with text "remember: prefer SQLite"
- When I focus the lane "cortex-workspace"
- And I press the key "p"
- Then within 1 second r1.cortex.notes reports the Note with field "pinned": true
- And the TUI snapshot a11y tree shows the Note with state "pinned"="true"

## Scenario: Re-pinning is a no-op (idempotency)

- Given the cortex Workspace contains a Note already pinned
- When I press the key "p" again on the focused Note
- Then r1.cortex.notes still reports the Note with field "pinned": true
- And no error toast appears in the TUI snapshot

## Tool mapping (informative)
- "cortex Workspace contains" -> r1.cortex.notes
- "press the key" -> r1.tui.press_key
- "TUI snapshot" -> r1.tui.snapshot
