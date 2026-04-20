# R03-rn — Todo list screen

RN screen with an input, add button, and list of todos with
delete capability. Exercises list rendering, keyed items, form
handling.

## Scope

- `App.tsx` renders `<TodoScreen />`.
- `src/TodoScreen.tsx`:
  - `useState<Todo[]>([])` where `Todo = { id: string; text: string }`.
  - `<TextInput testID="input">` bound to draft state.
  - `<Pressable testID="add">Add</Pressable>` pushes draft onto the
    list (if non-empty) and clears input.
  - For each todo render `<View testID={`todo-${id}`}>` with
    `<Text>` for text and `<Pressable testID={`delete-${id}`}>` that
    filters the item out.

- `__tests__/TodoScreen.test.tsx`:
  - No todos → list empty.
  - Type text → press add → todo appears.
  - Add two → delete first → only second remains.
  - Empty draft → add → nothing added.

## Acceptance

- `pnpm install` + `pnpm test` exit 0.
- At least 4 tests pass.

## What NOT to do

- No persistence (AsyncStorage).
- No animations.
- No navigation.
- No state management lib.
