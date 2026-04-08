# react-patterns

> React/Next.js patterns, performance, state management, and common pitfalls

<!-- keywords: react, nextjs, component, hook, useState, useEffect, useMemo, rendering, rerender, suspense, server component, hydration, ssr -->

## Critical Rules

1. **Never mutate state directly.** `state.items.push(x)` doesn't trigger re-render. Always create new references: `setItems([...items, x])`.

2. **useEffect is not componentDidMount.** It runs after every render unless deps are specified. Empty deps `[]` = mount only. Missing deps = stale closure bugs.

3. **Keys must be stable and unique.** Never use array index as key for dynamic lists. Use IDs. Wrong keys cause state mix-ups between items.

4. **Server components cannot use hooks or browser APIs.** `useState`, `useEffect`, `window`, `document` — none work in RSC. Add `'use client'` directive when needed.

5. **Hydration mismatch is a bug, not a warning.** Server HTML must exactly match client render. Date formatting, random values, and browser-only content cause mismatches.

## Performance Patterns

### Prevent Unnecessary Re-renders
- `React.memo()` for pure components that receive stable props
- `useMemo()` for expensive computations (not for every variable)
- `useCallback()` for functions passed as props to memoized children
- **Don't over-optimize.** Profile first. Most re-renders are fast.

### Virtualization for Long Lists
- Use `react-window` or `@tanstack/virtual` for 100+ item lists
- Never render 1000 DOM nodes. Virtual lists render ~20 visible items.

### Code Splitting
```jsx
const HeavyComponent = lazy(() => import('./HeavyComponent'))
// Wrap in Suspense
<Suspense fallback={<Loading />}>
  <HeavyComponent />
</Suspense>
```

### Image Optimization
- Next.js: Use `<Image>` component (auto lazy-load, resize, WebP)
- Set explicit `width` and `height` to prevent layout shift
- Use `priority` for above-the-fold images

## State Management

### When to Use What
| Scope | Solution |
|-------|----------|
| Component-local | `useState` |
| Parent-child (2 levels) | Props |
| Deep tree (3+ levels) | Context or state library |
| Server state | `@tanstack/query` or SWR |
| Complex client state | Zustand, Jotai, or Redux Toolkit |
| URL state | `useSearchParams` |
| Form state | `react-hook-form` or native |

### Server State is Not Client State
- Use React Query/SWR for API data. Don't put API responses in Redux.
- Stale-while-revalidate: show cached data, refetch in background.
- Optimistic updates: update UI immediately, rollback on error.

## Common Gotchas

- **Stale closures in useEffect.** Referencing state inside a callback captures the value at render time. Use refs or include in deps.
- **Infinite loops with useEffect.** Creating objects/arrays in render and including them in deps triggers infinite re-renders.
- **Context causes full subtree re-render.** Split contexts by update frequency. Don't put fast-changing state in a context used by many components.
- **`dangerouslySetInnerHTML` is an XSS vector.** Sanitize with DOMPurify if you must use it.
- **Missing error boundaries.** An unhandled error in any component crashes the entire app. Wrap feature areas in error boundaries.
- **`useLayoutEffect` warning in SSR.** Use `useEffect` for SSR-compatible code. `useLayoutEffect` only runs in browser.
