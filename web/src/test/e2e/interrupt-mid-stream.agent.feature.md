# Feature: interrupt mid-stream

Clicking Stop sends an interrupt envelope, drops the partial
assistant turn (RT-CANCEL-INTERRUPT), and re-enables the composer.

## Scenario: long stream, user stops, partial dropped

```gherkin
Given I am in a session with a configured model
When I send a message that triggers a long response
Then `[data-testid="stop-button"]` is visible
And `[data-testid="composer-send"]` is hidden or disabled
When I click `[data-testid="stop-button"]`
Then the partial assistant `[data-testid^="message-bubble-"]` disappears
And `[data-testid="composer-send"]` becomes enabled again
And `[data-testid="composer-textarea"]` is no longer disabled
And `[data-testid="stop-button"]` unmounts
```
