# state-management

> Patterns and decision framework for managing client-side and server-side state in modern applications.

<!-- keywords: state management, redux, zustand, jotai, recoil, context -->

## When to Use Local vs Global State

1. **Local state** (useState/useReducer): form inputs, toggle visibility, animation state, component-specific UI.
2. **Global state**: authenticated user, theme, feature flags, shopping cart, notification queue.
3. **URL state**: current page, filters, search query, sort order, pagination cursor.
4. **Server state**: API responses, cached entities -- use a dedicated data-fetching library, not Redux.
5. Rule of thumb: if only one component and its children need it, keep it local. Lift only when sharing is required.

## Redux Toolkit vs Zustand vs Jotai

| Concern | Redux Toolkit | Zustand | Jotai |
|---|---|---|---|
| Boilerplate | Medium (slices) | Low (single store) | Minimal (atoms) |
| DevTools | Excellent | Good (middleware) | Basic |
| Middleware | Rich ecosystem | Simple API | Derived atoms |
| Bundle size | ~11 KB | ~1 KB | ~2 KB |
| Best for | Large teams, complex flows | Medium apps, rapid dev | Fine-grained reactivity |
| Server-side | Extra setup (NEXT_REDUX_WRAPPER) | Native support | Provider per request |

1. Choose **Redux Toolkit** when you need time-travel debugging, serializable actions, or a large team needs enforced patterns.
2. Choose **Zustand** when you want minimal boilerplate with good devtools and no providers.
3. Choose **Jotai** when your state is many independent atoms with derived computations (spreadsheet-like).

## Server State with TanStack Query / SWR

1. Treat server data as a cache, not application state. Let the library manage staleness.
2. Configure `staleTime` based on data volatility: user profile (5 min), stock price (5 sec), static config (Infinity).
3. Use query keys that include all parameters: `['users', userId, { role }]`.
4. Prefetch on hover for navigation targets: `queryClient.prefetchQuery(...)`.
5. Mutations should `invalidateQueries` on success rather than manually updating cache, unless latency matters.
6. Use `select` to transform/filter data at the hook level to prevent unnecessary re-renders.

## Optimistic Updates Pattern

```typescript
// TanStack Query optimistic update
useMutation({
  mutationFn: updateTodo,
  onMutate: async (newTodo) => {
    await queryClient.cancelQueries({ queryKey: ['todos'] });
    const previous = queryClient.getQueryData(['todos']);
    queryClient.setQueryData(['todos'], (old) =>
      old.map((t) => (t.id === newTodo.id ? { ...t, ...newTodo } : t))
    );
    return { previous };
  },
  onError: (_err, _new, context) => {
    queryClient.setQueryData(['todos'], context.previous);
  },
  onSettled: () => queryClient.invalidateQueries({ queryKey: ['todos'] }),
});
```

1. Always snapshot previous state in `onMutate` for rollback.
2. Cancel in-flight queries to prevent stale data overwriting the optimistic value.
3. Invalidate on `onSettled` (not just `onSuccess`) to guarantee eventual consistency.

## State Normalization for Relational Data

1. Store entities in lookup tables: `{ byId: { '1': {...} }, allIds: ['1', '2'] }`.
2. Use `createEntityAdapter` (Redux Toolkit) for CRUD operations on normalized state.
3. Reference related entities by ID, not by nesting: `{ postId: '1', authorId: '5' }`.
4. Denormalize at the selector/component level using `createSelector` for memoized joins.
5. Normalization prevents update anomalies and reduces payload duplication across slices.

## Persistence and Hydration

1. Use `redux-persist` or Zustand's `persist` middleware for localStorage/sessionStorage.
2. Version your persisted state with a migration function for schema changes.
3. Encrypt sensitive persisted state (tokens, PII) with a per-device key.
4. Hydrate on app load with a loading screen to prevent flash-of-empty-state.
5. Selective persistence: whitelist only state that survives page reload (cart, draft, preferences).
6. Clear persisted state on logout to prevent data leakage between accounts.

## State Machines for Complex Flows (XState)

1. Use state machines when a component has more than 3 boolean flags controlling behavior.
2. Model explicit states: `idle | loading | success | error | retrying` -- no impossible combinations.
3. Guards prevent invalid transitions: cannot go from `idle` to `success` without `loading`.
4. Actions fire side effects on transitions, keeping business logic out of components.
5. Visualize machines with the XState inspector during development.

```typescript
const fetchMachine = createMachine({
  initial: 'idle',
  states: {
    idle: { on: { FETCH: 'loading' } },
    loading: {
      invoke: { src: 'fetchData', onDone: 'success', onError: 'error' },
    },
    success: { on: { REFRESH: 'loading' } },
    error: { on: { RETRY: 'loading' } },
  },
});
```

6. Integrate with React via `useMachine` hook -- the machine becomes the single source of truth for component behavior.
7. Persist machine state for resumable multi-step flows (onboarding, checkout, wizards).
