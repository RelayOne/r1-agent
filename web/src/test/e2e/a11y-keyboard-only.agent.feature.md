# Feature: keyboard-only golden flow

Full session creation, send, lane pin, and stop — all without
touching the mouse or pointer.

## Scenario: ship a session via keyboard alone

```gherkin
Given I open "/d/local"
And focus is on `<body>`
When I press "Tab" until `[data-testid="new-session-cta"]` has focus
And I press "Enter"
Then `[data-testid="new-session-dialog"]` opens with focus trap
When I navigate the dialog by keyboard to fill model + workdir
And I press "Enter" on `[data-testid="new-session-submit"]`
Then I am routed to the new session view
When I press "/"
Then focus moves to `[data-testid="composer-textarea"]`
When I type a short message
And I press "Cmd+Enter"
Then the assistant streams a reply
When I press "Tab" to reach `[data-testid="lane-row-<laneId>-pin"]`
And I press "Enter"
Then the lane is pinned and tile mode toggles on
When I press "Esc" while a stream is active
Then `[data-testid="stop-button"]` fires and the stream halts
And `[data-testid="composer-send"]` becomes focusable again
```
