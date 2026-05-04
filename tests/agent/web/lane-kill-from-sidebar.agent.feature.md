# tests/agent/web/lane-kill-from-sidebar.agent.feature.md

<!-- TAGS: web, lanes, idempotency -->
<!-- DEPENDS: lanes-protocol, web-chat-ui -->

## Scenario: Killing a lane from the sidebar produces an idempotent state

- Given a session with id "${SESSION_ID}" running at least 2 lanes
- And the web UI is focused on the agents sidebar
- When I click the button with name "Kill lane memory-curator"
- Then within 2 seconds r1.lanes.list reports lane "memory-curator" with status "cancelled"
- And the cortex Workspace contains a Note with tag "lane_cancelled" and lobe "memory-curator"
- When I click the button with name "Kill lane memory-curator" again
- Then r1.lanes.list still reports lane "memory-curator" with status "cancelled"
- And no error toast is visible in the UI snapshot

## Negative case: missing API counterpart
- Given the lint scanner ran on this PR
- Then no React component in web/src/ has an onClick handler without a matching r1.* MCP tool
