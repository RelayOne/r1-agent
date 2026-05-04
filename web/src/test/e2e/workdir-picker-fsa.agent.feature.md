# Feature: workdir picker — FSA + manual fallback

Chromium picks via File System Access API; Firefox falls back to
manual path entry with autocomplete from r1d.listAllowedRoots().

## Scenario A (Chromium): FSA picker

```gherkin
Given I am running on chromium
And I open the workdir badge via `[data-testid="workdir-badge"]`
Then `[data-testid="workdir-picker-dialog"]` opens
And `[data-testid="workdir-picker-fsa"]` is visible
When I click `[data-testid="workdir-picker-fsa"]`
And the OS dialog returns a directory handle
Then `[data-testid="workdir-picker-dialog"]` closes
And the badge label updates to the picked directory
And reloading the page restores the picked directory from IndexedDB
without re-prompting
```

## Scenario B (Firefox): manual path entry

```gherkin
Given I am running on firefox
When I open the workdir badge
Then `[data-testid="workdir-picker-fsa"]` is NOT in the DOM
And `[data-testid="workdir-picker-path"]` is focusable
When I focus the input
Then the datalist suggestions match `r1d.listAllowedRoots()`
When I fill an absolute path and submit
Then the dialog closes and the badge updates
```
