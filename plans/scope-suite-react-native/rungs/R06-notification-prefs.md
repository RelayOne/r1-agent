# R06-rn — Notification preferences screen

Form-based RN screen with controlled inputs, validation, and submit.
Exercises form state, conditional rendering, and multi-field reducers.

## Scope

- `App.tsx` renders `<PrefsScreen />`.
- `src/PrefsScreen.tsx`:
  - `useReducer` for prefs state: `{email, sms, push, digest, quietStart, quietEnd}`.
  - `<Switch testID="email">`, `<Switch testID="sms">`, `<Switch testID="push">`.
  - `<SegmentedControl testID="digest">` with values "off"/"daily"/"weekly"
    (fallback: three `<Pressable>` with role "button").
  - `<TextInput testID="quietStart">` and `<TextInput testID="quietEnd">` — HH:MM.
  - `<Pressable testID="submit">Save</Pressable>` validates and calls
    `onSubmit(prefs)` prop.
  - Shows `<Text testID="error">` when invalid (e.g., quietStart = "25:00").
- `__tests__/PrefsScreen.test.tsx`:
  - Default renders all 3 switches + digest selector.
  - Toggle email → state updates; submit → onSubmit receives prefs.
  - Set quietStart="25:00" → error shown, onSubmit NOT called.

## Acceptance

- `pnpm install` + `pnpm test` exit 0.
- At least 3 tests pass.

## What NOT to do

- No form library (formik/react-hook-form).
- No persistence.
- No navigation.
