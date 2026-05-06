# tests/agent/web/cortex-publish-and-observe.agent.feature.md

<!-- TAGS: web, cortex, agentic-loopback -->
<!-- DEPENDS: cortex-core, cortex-concerns, web-chat-ui -->

## Scenario: External agent publishes a Note and the UI reflects it

- Given an external agent connected over MCP
- And a session "${SESSION_ID}" with the web UI open
- When the external agent calls r1.cortex.publish with note { text: "remember: prefer SQLite", tags: ["preference"], critical: true }
- Then within 1 second the Workspace pane in the UI shows a Note with text "remember: prefer SQLite"
- And the Note is rendered with role "listitem" and aria-label containing "preference"
- And the Note has aria-current="true" because it is critical
- When the user clicks the button with name "Pin Note"
- Then r1.cortex.notes reports the Note with field "pinned": true
- And re-clicking "Pin Note" leaves "pinned": true (idempotency)
