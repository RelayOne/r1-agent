# R02-rn — Counter screen with useState

Tiny RN app: a counter with increment/decrement buttons and a display.
Exercises `useState`, `Pressable`, basic reactivity.

## Scope

Expo/RN project similar in shape to R01 but with:

- `App.tsx` renders `<Counter />`.
- `src/Counter.tsx` — functional component:
  - Uses `useState` for the count (start at 0).
  - `<Text testID="count">{count}</Text>`
  - `<Pressable testID="inc">+</Pressable>` calls `setCount(c => c + 1)`.
  - `<Pressable testID="dec">-</Pressable>` calls `setCount(c => c - 1)`.

- `__tests__/Counter.test.tsx` using `@testing-library/react-native`:
  - Mount renders count as 0.
  - `fireEvent.press` on inc → count becomes 1.
  - Repeated press → count becomes 2.
  - Press on dec → count decrements.

## Acceptance

- `package.json` with expo + RN + jest-expo + testing-library deps.
- `pnpm install` + `pnpm test` exit 0.
- At least 3 tests pass.

## What NOT to do

- No navigation.
- No persistence.
- No styling beyond StyleSheet.create minimum.
