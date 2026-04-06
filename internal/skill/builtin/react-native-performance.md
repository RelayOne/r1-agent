# react-native-performance

> React Native performance optimization for lists, animations, memory, and runtime configuration

<!-- keywords: react-native performance, flatlist, hermes, reanimated, jsi -->

## FlatList Optimization

1. **Always provide `getItemLayout`** when items have fixed heights. This eliminates async layout measurement and makes `scrollToIndex` instant:
   ```tsx
   getItemLayout={(data, index) => ({
     length: ITEM_HEIGHT,
     offset: ITEM_HEIGHT * index,
     index,
   })}
   ```

2. **Set `windowSize` to a small value** (default is 21). A value of 5 means 5 viewports of content are rendered — 2 above, the visible area, and 2 below. Lower values reduce memory but increase blank flicker on fast scrolls.

3. **`removeClippedSubviews={true}`** detaches off-screen views from the native hierarchy. Useful on Android for long lists. Can cause rendering bugs on iOS — test carefully.

4. **`maxToRenderPerBatch`** controls how many items render per frame. Default is 10. Lower it for complex items to avoid frame drops, raise it for simple items to fill faster.

5. **`keyExtractor` must return a string.** Use stable IDs, never array indices. Wrong keys cause items to remount, losing internal state and wasting renders.

6. **Use `FlashList` from Shopify** as a drop-in replacement. It recycles cells like native list views, achieving 5-10x better scroll performance on large datasets. Requires `estimatedItemSize`.

## Hermes Engine

1. **Hermes is the default engine since RN 0.70.** It compiles JS to bytecode at build time, reducing startup time by 50%+ and memory usage by 30%+.

2. **Verify Hermes is active**: `const isHermes = () => !!global.HermesInternal;`. In dev, check the Metro log for "Hermes" in the engine line.

3. **Hermes does not support `with` statements, `Proxy` in older versions, or some Intl features.** Use `hermes-eslint` to catch unsupported syntax. For full Intl, polyfill via `@formatjs/intl-pluralrules` and similar packages.

4. **Profiling with Hermes**: enable `hermesFlags: ['-emit-async-break-check']` in metro config, then use the Hermes sampling profiler via `HermesInternal.enableSamplingProfiler()`. Open the resulting `.cpuprofile` in Chrome DevTools.

## React Native Reanimated

1. **Reanimated v3 runs animations on the UI thread** via worklets. Shared values (`useSharedValue`) update without crossing the bridge. Animated styles use `useAnimatedStyle`:
   ```tsx
   const offset = useSharedValue(0);
   const animatedStyle = useAnimatedStyle(() => ({
     transform: [{ translateX: offset.value }],
   }));
   ```

2. **`withTiming`, `withSpring`, `withDecay`** are the core animation drivers. Compose them with `withSequence`, `withDelay`, and `withRepeat` for complex choreography.

3. **Layout animations**: add `entering={FadeIn}` and `exiting={FadeOut}` props to `Animated.View` for automatic mount/unmount transitions.

4. **Gesture Handler v2** pairs with Reanimated. Use `Gesture.Pan()`, `.Tap()`, `.Pinch()` composed via `Gesture.Simultaneous()` or `Gesture.Exclusive()`. Run gesture callbacks as worklets for zero-bridge-crossing interaction.

## JSI (JavaScript Interface)

1. **JSI enables synchronous native calls** by exposing C++ host objects directly to JS. No serialization, no bridge queue. Libraries like MMKV and Reanimated use JSI internally.

2. **Writing a JSI module**: create a C++ `HostObject` subclass, register it via `runtime.global().setProperty()`. The JS side sees a plain object with synchronous methods.

3. **JSI is not type-safe at the boundary.** Validate arguments in C++ before using them. Throwing a `jsi::JSError` propagates cleanly to JS catch blocks.

## Memory Leak Prevention

1. **Clean up event listeners** in `useEffect` return functions. `EventEmitter.addListener` returns a subscription — call `subscription.remove()` on unmount.

2. **Clear timers**: every `setTimeout` and `setInterval` must be cleared. Store the ID and call `clearTimeout`/`clearInterval` in the cleanup function.

3. **Abort fetch requests** with `AbortController`. Pass `signal` to `fetch()` and call `controller.abort()` on unmount. Unaborted requests can update state on unmounted components.

4. **Avoid closures over large objects** in long-lived callbacks. This pins the entire closure scope in memory. Extract only the values you need.

5. **Monitor with `PerformanceObserver`** or Flipper's memory plugin. On Android, watch for `OutOfMemoryError` in Logcat. On iOS, use Xcode Instruments Allocations.

## Image Optimization

1. **Use `react-native-fast-image`** (backed by SDWebImage/Glide) for network images. It provides disk + memory caching, priority loading, and progressive JPEG support:
   ```tsx
   <FastImage
     source={{ uri: url, priority: FastImage.priority.high }}
     resizeMode={FastImage.resizeMode.cover}
   />
   ```

2. **Specify image dimensions** explicitly. Unspecified dimensions cause layout thrashing. Use `aspectRatio` in styles if the exact size is unknown but the ratio is fixed.

3. **Prefetch critical images** with `FastImage.preload([{ uri }])` during navigation transitions so images are cached before the target screen mounts.

4. **Use WebP format** for 25-35% smaller files than PNG/JPEG. Both platforms support WebP. Convert at the CDN or build level.

5. **Progressive loading**: show a blurred low-res placeholder (blurhash or thumbhash) while the full image loads. `react-native-blurhash` renders compact hash strings into blurred previews with minimal CPU cost.
