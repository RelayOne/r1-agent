# tests/agent/tui/lanes-kill.agent.feature.md

<!-- TAGS: smoke, tui, lanes -->
<!-- DEPENDS: lanes-protocol, tui-lanes -->

## Scenario: Killing a lane from the TUI sidebar marks it cancelled

- Given a fresh r1d daemon at "stdio"
- And a session is started with workdir "/tmp/agentic-test-tui-1"
- And the TUI is launched with the dashboard model
- When I focus the lane "memory-curator"
- And I press the key "x"
- Then within 2 seconds r1.lanes.list reports lane "memory-curator" with status "cancelled"
- And the TUI snapshot's a11y tree contains a status badge with state "cancelled" for "memory-curator"

## Scenario: Re-pressing kill on an already-killed lane is idempotent

- Given a session with id "${SESSION_ID}" and lane "memory-curator" already cancelled
- When I focus the lane "memory-curator"
- And I press the key "x"
- Then r1.lanes.list still reports lane "memory-curator" with status "cancelled"
- And no error toast appears in the TUI snapshot

## Tool mapping (informative, runner derives automatically)
- "focus the lane" -> r1.tui.focus_lane
- "press the key" -> r1.tui.press_key
- "r1.lanes.list reports" -> r1.lanes.list
- "TUI snapshot" -> r1.tui.snapshot
