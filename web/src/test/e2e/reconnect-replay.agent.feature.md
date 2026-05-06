# Feature: reconnect → Last-Event-ID replay

The daemon is killed mid-stream; restarting it must let the web app
resume without dropping or duplicating events. Drives the
ResilientSocket replay path.

## Scenario: kill daemon, restart, no dupes

```gherkin
Given I am in a session and the assistant is streaming a reply
When the daemon process is killed
Then `[data-testid="status-bar"]` shows `data-connection="reconnecting"`
When the daemon is restarted within 30 seconds
Then `[data-testid="status-bar"]` returns to `data-connection="open"`
And the message log contains the same number of bubbles it did before the kill,
plus any events the daemon emitted while the WS was reconnecting
And no `[data-testid^="message-bubble-"]` appears more than once
And `[data-testid="connection-lost-banner"]` did not render
```
