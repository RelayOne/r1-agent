# Feature: deep-link lane focus

Direct navigation to `/d/<daemon>/sessions/<sid>/lanes/<lid>` must
render the lane focus view without first redirecting through `/`.

## Scenario: paste-in URL

```gherkin
Given I open "/d/local/sessions/seed-session/lanes/seed-lane" directly
Then I am NOT redirected through "/"
And `[data-testid="lane-tile-seed-lane"]` is visible
And the URL stays exactly "/d/local/sessions/seed-session/lanes/seed-lane"
And `[data-testid="status-bar"]` shows a healthy connection state
```
