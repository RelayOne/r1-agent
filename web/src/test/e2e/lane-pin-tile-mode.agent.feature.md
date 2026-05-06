# Feature: lane-pin → tile mode

Pinning lanes flips the chat pane into a tile grid that auto-lays-out
between 1, 1×2, 1×3, and 2×2 depending on count.

## Scenario: progressive pin → reflow

```gherkin
Given I open "/d/local/sessions/<sessionId>"
And the session has 4 active lanes
Then `[data-testid="chat-pane"]` has `data-tile-mode="false"`
When I click `[data-testid="lane-row-lane-a-pin"]`
Then `[data-testid="chat-pane"]` has `data-tile-mode="true"`
And `[data-testid="tile-grid"]` has `data-tile-count="1"`
When I pin lane-b
Then `[data-testid="tile-grid"]` has `data-tile-count="2"`
And the grid uses two columns (md breakpoint)
When I pin lane-c
Then `[data-testid="tile-grid"]` has `data-tile-count="3"`
When I pin lane-d
Then `[data-testid="tile-grid"]` has `data-tile-count="4"`
And the grid is `grid-cols-2` (2x2)
When I unpin lane-d via `[data-testid="tile-grid-tile-lane-d-unpin"]`
Then `[data-testid="tile-grid"]` has `data-tile-count="3"`
```
