# R07-rn — Bottom tab navigation, 3 screens

RN app with react-navigation bottom-tab navigator. Exercises
navigation, state per-screen, and tab-switching tests.

## Scope

- `App.tsx` renders a `NavigationContainer` + `createBottomTabNavigator`
  with three tabs:
  - `Home` → `<HomeScreen />` shows `<Text testID="home-title">Home</Text>`
  - `Counter` → `<CounterScreen />` with useState counter (inc button
    `testID="inc"`, display `testID="count"`)
  - `About` → `<AboutScreen />` shows `<Text testID="about-title">About</Text>`
- Each tab's label has `accessibilityLabel` matching the route name.
- `__tests__/App.test.tsx`:
  - Initial render: home-title visible, not about-title.
  - Tap About tab → about-title visible, home-title NOT visible.
  - Go to Counter, press inc 3 times, switch tabs, return → count retained.

## Acceptance

- `package.json` deps include `@react-navigation/native`,
  `@react-navigation/bottom-tabs`, `react-native-screens`,
  `react-native-safe-area-context`.
- `pnpm install` + `pnpm test` exit 0.
- At least 3 tests pass.

## What NOT to do

- No persistence.
- No stack nav within tabs — just the bottom tabs.
