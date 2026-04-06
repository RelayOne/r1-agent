# react-native-testing

> Testing strategies for React Native covering unit, integration, E2E, and CI configuration

<!-- keywords: react-native testing, detox, maestro, jest native -->

## Jest + React Native Testing Library

1. **React Native Testing Library (RNTL)** is the standard for component tests. Query by user-visible attributes — `getByText`, `getByRole`, `getByTestId` — not by component internals:
   ```tsx
   import { render, fireEvent } from '@testing-library/react-native';
   const { getByText, getByTestId } = render(<LoginScreen />);
   fireEvent.changeText(getByTestId('email-input'), 'user@test.com');
   fireEvent.press(getByText('Sign In'));
   expect(getByText('Welcome')).toBeTruthy();
   ```

2. **`waitFor` and `findBy*`** handle async state. Use `findByText` (which wraps `waitFor`) for elements that appear after data fetches. Set `jest.setTimeout(10000)` for slow async operations in tests.

3. **Test user behavior, not implementation.** Do not test that `setState` was called or that a hook returned a value. Test what the user sees and does. This makes tests resilient to refactors.

4. **Navigation testing**: wrap components in `NavigationContainer` with an `initialState` or use a custom `renderWithNavigation` helper. Assert navigation by checking that the target screen's content renders.

5. **Jest configuration** for React Native:
   ```js
   // jest.config.js
   module.exports = {
     preset: 'react-native',
     setupFilesAfterSetup: ['./jest.setup.js'],
     transformIgnorePatterns: [
       'node_modules/(?!(react-native|@react-native|@react-navigation)/)',
     ],
     moduleNameMapper: { '\\.(png|jpg)$': '<rootDir>/__mocks__/fileMock.js' },
   };
   ```

## Detox for E2E Testing

1. **Detox** runs E2E tests on real simulators/emulators with gray-box synchronization — it waits for animations, network, and timers automatically. No manual `sleep()` calls needed.

2. **Test structure** follows arrange-act-assert:
   ```js
   describe('Login', () => {
     beforeAll(async () => { await device.launchApp(); });
     beforeEach(async () => { await device.reloadReactNative(); });
     it('should login with valid credentials', async () => {
       await element(by.id('email')).typeText('user@test.com');
       await element(by.id('password')).typeText('secret');
       await element(by.text('Sign In')).tap();
       await expect(element(by.text('Dashboard'))).toBeVisible();
     });
   });
   ```

3. **Build configurations**: define `debug` and `release` configs in `.detoxrc.js`. Run debug for local development (faster builds), release for CI (matches production behavior).

4. **Common pitfalls**: Detox auto-waits for the bridge but not for native animations. Use `waitFor(element).toBeVisible().withTimeout(5000)` for elements that depend on native transitions.

## Maestro for Cross-Platform E2E

1. **Maestro** uses YAML flows that work on both iOS and Android without code changes. Define steps like `launchApp`, `tapOn: "Sign In"`, `inputText`, and `assertVisible: "Dashboard"` in a single YAML file.

2. **Advantages over Detox**: simpler setup, no build-time integration, runs on real devices via Maestro Cloud. Disadvantage: less precise synchronization, no gray-box insight. Use Maestro for smoke tests on critical paths; use Detox or RNTL for detailed interaction tests.

## Snapshot Testing Pitfalls

1. **Snapshots rot fast** in React Native. A single style change updates dozens of snapshots. Reviewers rubber-stamp `--updateSnapshot` without reading diffs. Only snapshot stable, low-change components like icons or formatted text.

2. **Prefer assertion-based tests** that verify behavior: "button is disabled when form is invalid" beats a snapshot of the entire form tree.

## Mocking Native Modules

1. **Create manual mocks** in `__mocks__` or `jest.setup.js` for native modules that crash in Jest:
   ```js
   jest.mock('react-native-camera', () => ({
     RNCamera: { Constants: { Type: { back: 'back' } } },
   }));
   jest.mock('@react-native-async-storage/async-storage', () =>
     require('@react-native-async-storage/async-storage/jest/async-storage-mock')
   );
   ```

2. **Libraries often ship mocks.** Check the package README for a `/jest` or `/__mocks__` directory before writing your own. Using official mocks prevents drift.

3. **Mock at the boundary, not deep.** Mock the entire native module, not individual functions within it. This keeps mocks stable across library upgrades.

## CI Setup for iOS/Android Tests

1. **Android CI**: use `ubuntu-latest` with `reactivecircus/android-emulator-runner@v2`. Cache `~/.gradle` for 2-3x faster builds. Start emulator, wait for boot, then run Detox or Maestro.

2. **iOS CI**: use `macos-14` (M1 runners) with `xcrun simctl` for simulator management. Needs Xcode and CocoaPods preinstalled. Cache `Pods/` directory.

3. **Cache aggressively**: cache `node_modules`, `Pods`, and Gradle dependencies. Cold builds take 15-25 min; warm caches cut to 5-10 min. Split suites across runners and use Detox `--workers 2` for parallel execution.

4. **Run unit tests on every PR**, Detox on `main` merges or nightly, and Maestro Cloud on release branches. This balances speed with coverage.
