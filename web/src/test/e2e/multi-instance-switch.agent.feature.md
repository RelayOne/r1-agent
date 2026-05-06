# Feature: multi-instance switch

Two daemons must stay isolated — separate stores, separate cost
counters, separate session lists. The user switches between them
via Cmd+1 / Cmd+2.

## Background

- Two daemons are running: `local` on `127.0.0.1:7777`,
  `remote` on `127.0.0.1:7778`.

## Scenario: state stays partitioned by daemon id

```gherkin
Given I open "/d/local"
Then `[data-testid="session-list"]` shows the local sessions only
When I press "Cmd+2"
Then I am navigated to "/d/remote"
And `[data-testid="session-list"]` shows the remote sessions only
And `[data-testid="status-bar-cost"]` reflects only the remote daemon's cost
When I press "Cmd+1"
Then I am navigated back to "/d/local"
And the cost counter swaps back to local
```
