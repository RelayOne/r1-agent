# R01-rn — React Native hello screen

Smallest possible RN SOW: one screen, one component, one test using
`@testing-library/react-native`. Scaffolded via Expo's managed
workflow so there's no native iOS/Android build dependency.

## Scope

Create a minimal Expo project structure (no `expo init` — write files
directly):

- `package.json` with scripts `test` and dev deps `expo`, `react`,
  `react-native`, `@testing-library/react-native`, `jest-expo`, `jest`.
- `App.tsx` exporting a default `<View>` with a `<Text testID="greeting">`
  showing `Hello, React Native!`.
- `__tests__/App.test.tsx` rendering `<App />` via
  `@testing-library/react-native` and asserting the greeting is
  present via `findByTestId("greeting")`.
- `babel.config.js` with `babel-preset-expo`.
- `tsconfig.json` extending `expo/tsconfig.base`.
- `jest.config.js` preset `jest-expo`.

## Acceptance

- All files exist.
- `npm install` (or `pnpm install`) exits 0.
- `npm test` (or `pnpm test`) exits 0; the App test passes.
- No native modules required (pure JS test).

## What NOT to do

- No navigation library (`react-navigation`).
- No state library (`redux`, `zustand`).
- No networking.
- No Android/iOS-specific code.
- No screenshots / e2e via detox.
