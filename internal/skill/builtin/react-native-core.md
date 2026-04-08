# react-native-core

> Core React Native patterns for navigation, native modules, platform code, and build configuration

<!-- keywords: react-native, expo, metro, native modules, bridge -->

## Navigation with React Navigation

1. **Stack navigator** is the default for screen-to-screen transitions. Wrap your app in `NavigationContainer` and define screens with `createNativeStackNavigator`. Prefer native stack over JS stack for better performance.

2. **Tab and drawer navigators** compose inside a stack. Nest `createBottomTabNavigator` or `createDrawerNavigator` as a screen within your root stack. Never nest a stack inside a stack without good reason — it doubles headers and back buttons.

3. **Type-safe navigation** requires declaring a `RootStackParamList` type and passing it to `createNativeStackNavigator<RootStackParamList>()`. This catches wrong route names and missing params at compile time.

4. **Deep linking** needs a `linking` config on `NavigationContainer`. Define `prefixes` and a `config` mapping URL paths to screen names:
   ```tsx
   const linking = {
     prefixes: ['myapp://', 'https://myapp.com'],
     config: { screens: { Profile: 'user/:id' } },
   };
   ```

5. **Screen options** like `headerShown: false`, `gestureEnabled`, and `animation` go on `Screen` components or in `screenOptions` on the navigator for global defaults.

## Native Module Integration

1. **Turbo Modules** (New Architecture) replace the old bridge. Define a spec in `src/NativeMyModule.ts` using `TurboModuleRegistry.getEnforcing<Spec>('MyModule')`. The codegen produces C++ bindings from the JS spec.

2. **Legacy bridge modules** use `NativeModules.MyModule`. Always null-check: `NativeModules.MyModule?.doThing()`. The bridge is asynchronous — every call returns a Promise or uses callbacks.

3. **Fabric components** replace the old `requireNativeComponent`. Define a component spec with `codegenNativeComponent<NativeProps>('MyView')`. This enables synchronous rendering on the new architecture.

4. **Linking native code** in bare workflow: run `npx pod-install` after adding iOS dependencies. For Android, most libraries auto-link via `react-native.config.js`. Manual linking is rarely needed post-RN 0.60.

## Platform-Specific Code

1. **File extensions**: `Button.ios.tsx` and `Button.android.tsx` are auto-resolved by Metro. Import as `./Button` and the bundler picks the right file per platform.

2. **Platform.select** for inline differences:
   ```tsx
   const styles = StyleSheet.create({
     container: {
       padding: Platform.select({ ios: 20, android: 16 }),
     },
   });
   ```

3. **Platform.OS** checks for conditional logic: `if (Platform.OS === 'ios') { ... }`. Use sparingly — prefer platform files for large divergences.

4. **Platform.Version** checks OS version: `Platform.Version >= 33` on Android (API level), `parseInt(Platform.Version, 10) >= 16` on iOS.

## Metro Bundler Configuration

1. **metro.config.js** customizes resolution and transformation. Use `getDefaultConfig` from `@react-native/metro-config` as the base and merge your overrides.

2. **Asset extensions**: add custom asset types via `resolver.assetExts`. Add source extensions via `resolver.sourceExts` (e.g., `.cjs`, `.mjs`).

3. **Monorepo support**: set `watchFolders` to include workspace root and sibling packages. Set `nodeModulesPaths` to resolve from the root `node_modules`.

4. **Tree shaking** is not built into Metro. Use `@rnx-kit/metro-serializer-esbuild` for production bundle size reduction, or rely on Hermes dead code elimination.

## Expo vs Bare Workflow

1. **Expo Go** is for prototyping only. It limits you to the Expo SDK — no custom native code. Eject to a dev client as soon as you need a native module not in Expo.

2. **Expo Dev Client** (`expo-dev-client`) gives you a custom runtime that supports any native module while keeping Expo tooling (EAS Build, OTA updates, config plugins).

3. **Config plugins** modify native projects without touching Xcode/Android Studio. Write a plugin to add entitlements, permissions, or Gradle dependencies:
   ```js
   // app.plugin.js
   const { withInfoPlist } = require('@expo/config-plugins');
   module.exports = (config) =>
     withInfoPlist(config, (cfg) => {
       cfg.modResults.NSCameraUsageDescription = 'Camera access needed';
       return cfg;
     });
   ```

4. **EAS Build** handles signing, provisioning, and CI. Use `eas build --profile production` for store builds. Local builds with `npx expo run:ios` are faster for development iteration.

5. **OTA updates** via `expo-updates` push JS bundle changes without app store review. Never ship native code changes via OTA — they require a new binary build.

6. **Bare workflow** gives full native project control. Choose this when you need heavy native customization, custom build systems, or frameworks like Brownfield integration into existing native apps.
