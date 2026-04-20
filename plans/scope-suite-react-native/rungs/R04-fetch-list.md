# R04-rn — Fetch and render list from mocked API

RN screen that fetches a list from `/api/items` on mount, shows a
loading indicator, then renders items or an error message.
`global.fetch` is mocked in tests.

## Scope

- `App.tsx` renders `<ItemList />`.
- `src/ItemList.tsx`:
  - `useEffect` triggers `fetch("/api/items")` on mount.
  - State: `items: Item[] | null`, `error: string | null`,
    `loading: boolean` (true during fetch).
  - While loading → `<Text testID="loading">Loading…</Text>`.
  - On success → `<View testID="list">` with each `<Text testID={`item-${id}`}>{name}</Text>`.
  - On failure → `<Text testID="error">{error}</Text>`.
  - `Item = { id: string; name: string }`.

- `__tests__/ItemList.test.tsx`:
  - Mocks `global.fetch` via `jest.spyOn`.
  - Case 1: fetch resolves 200 with `[{id:"1",name:"a"},{id:"2",name:"b"}]`.
    Assert loading shown, then items 1 and 2 rendered.
  - Case 2: fetch rejects. Assert error text appears.
  - Case 3: fetch resolves 500. Assert error text appears.

## Acceptance

- `pnpm install` + `pnpm test` exit 0.
- At least 3 tests pass.
- No real HTTP — fetch is always mocked.

## What NOT to do

- No TanStack Query / SWR.
- No real backend.
- No pagination.
