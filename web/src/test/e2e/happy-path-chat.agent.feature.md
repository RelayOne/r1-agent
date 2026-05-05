# Feature: happy-path chat

The simplest end-to-end path: an operator lands on the empty home,
creates a session, sends a message, watches a streamed response land,
and confirms a tool card renders inline.

## Background

- The daemon is running at `http://127.0.0.1:7777`.
- The web bundle is mounted by `r1 serve --web=:7777`.
- A model alias `claude-opus-4-7` is reachable via the daemon.

## Scenario: send a first message and see a streamed reply

```gherkin
Given I open "/"
Then I see the empty-state landing page
When I click `[data-testid="new-session-cta"]`
Then a dialog `[data-testid="new-session-dialog"]` opens
When I select model "claude-opus-4-7" via `[data-testid="new-session-model"]`
And I fill `[data-testid="new-session-workdir"]` with the test repo path
And I click `[data-testid="new-session-submit"]`
Then I am navigated to `/d/local/sessions/<sessionId>`
When I focus `[data-testid="composer-textarea"]`
And I type "summarize the README"
And I press "Cmd+Enter"
Then `[data-testid="composer-send"]` becomes disabled while streaming
And a `[data-testid^="message-bubble-"]` with `data-role="assistant"` appears
And it carries `aria-live="polite"` while streaming
When the stream completes
Then `[data-testid^="message-part-tool-"]` is visible if the model called any tools
And the tool card auto-collapses once it reaches `data-state="output-available"`
And `[data-testid="status-bar-cost"]` updates to a non-zero USD value
```
