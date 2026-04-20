# R08-rn — Persistent todo list with AsyncStorage

Add persistence to an R03-style todo list using `@react-native-async-storage/async-storage`.
Exercises effects, async side-effects, and mocking AsyncStorage in jest.

## Scope

- `App.tsx` renders `<PersistedTodoScreen />`.
- `src/PersistedTodoScreen.tsx`:
  - Load todos from AsyncStorage key `"todos"` on mount.
  - Add/delete behavior same as R03 (input + add button + list + delete).
  - On every state change (add, delete), write JSON-stringified todos
    back to AsyncStorage.
  - Show `<Text testID="loading">` while initial load pending.
- `src/storage.ts` wraps AsyncStorage with typed `loadTodos`/`saveTodos`.
- `__tests__/PersistedTodoScreen.test.tsx`:
  - Mock `@react-native-async-storage/async-storage` via
    `jest.mock('@react-native-async-storage/async-storage', () => require('@react-native-async-storage/async-storage/jest/async-storage-mock'))`.
  - Test 1: mock getItem returns `[]` → empty list rendered.
  - Test 2: add item → mock setItem called with correct JSON.
  - Test 3: mock getItem returns existing `[{id:"1", text:"existing"}]`
    → list shows "existing".

## Acceptance

- `package.json` deps include `@react-native-async-storage/async-storage`.
- `pnpm install` + `pnpm test` exit 0.
- At least 3 tests pass.

## What NOT to do

- No network calls.
- No encryption/Keychain.
- No migration logic.
