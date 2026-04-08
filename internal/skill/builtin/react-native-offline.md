# react-native-offline

> Offline-first patterns for React Native including storage, sync, and queue-based mutations

<!-- keywords: react-native offline, asyncstorage, watermelondb, netinfo -->

## Storage Options

1. **AsyncStorage** is a simple key-value store. Best for small data (user prefs, tokens, flags). It is unencrypted, asynchronous, and has a 6MB default limit on Android. Never store large datasets here — reads block the JS thread on deserialization.

2. **MMKV** (`react-native-mmkv`) is a JSI-backed key-value store that is 30x faster than AsyncStorage. Synchronous reads/writes, encryption support, and no size limit in practice. Use it as the default replacement for AsyncStorage:
   ```tsx
   import { MMKV } from 'react-native-mmkv';
   const storage = new MMKV();
   storage.set('user.token', token);
   const token = storage.getString('user.token');
   ```

3. **WatermelonDB** is a SQLite-backed reactive database for large, relational datasets (1000+ records). It runs queries on a separate native thread and lazy-loads records. Define models with decorators:
   ```tsx
   class Post extends Model {
     static table = 'posts';
     @text('title') title;
     @text('body') body;
     @date('created_at') createdAt;
   }
   ```

4. **Decision guide**: MMKV for key-value under 50MB. WatermelonDB for relational data, queries, or reactive lists. AsyncStorage only if you cannot add native dependencies (Expo Go).

## Network State Detection

1. **`@react-native-community/netinfo`** provides `NetInfo.fetch()` for one-shot checks and `NetInfo.addEventListener` for continuous monitoring:
   ```tsx
   useEffect(() => {
     const unsub = NetInfo.addEventListener(state => {
       setOnline(state.isConnected && state.isInternetReachable);
     });
     return () => unsub();
   }, []);
   ```

2. **`isConnected` vs `isInternetReachable`**: `isConnected` means a network interface is active (WiFi associated). `isInternetReachable` means actual internet access (verified by a server ping). Always check both.

3. **Custom reachability endpoint**: configure `NetInfo.configure({ reachabilityUrl: 'https://api.yourapp.com/ping' })` to verify against your own server instead of the default Google/Apple endpoints.

4. **Debounce network transitions.** Mobile connections flap frequently. Wait 2-3 seconds after a "reconnected" event before triggering sync to avoid firing on transient connections.

## Optimistic Updates with Rollback

1. **Apply mutations to local state immediately**, then enqueue the server request. If the request fails, roll back the local state to the previous value.

2. **Track pending mutations** with a status field: `synced`, `pending`, `failed`. Display a subtle indicator (sync icon, muted color) for pending items so users know the state is tentative.

3. **Conflict resolution**: when the server rejects a mutation (409 Conflict), fetch the server's version and present a merge UI or apply last-write-wins. Never silently discard user work.

4. **React Query pattern**: use `useMutation` with `onMutate` (cache previous, set optimistic data), `onError` (restore previous from context), and `onSettled` (invalidate query). This gives you automatic rollback with minimal boilerplate.

## Background Sync

1. **`react-native-background-fetch`** runs periodic tasks when the app is backgrounded. iOS grants ~30 seconds every 15+ minutes (system-scheduled). Android uses JobScheduler with similar constraints:
   ```tsx
   BackgroundFetch.configure({
     minimumFetchInterval: 15,
     stopOnTerminate: false,
     startOnBoot: true,
   }, async (taskId) => {
     await syncPendingMutations();
     BackgroundFetch.finish(taskId);
   });
   ```

2. **Do not rely on exact timing.** The OS batches background tasks for battery efficiency. Critical syncs (payments, messages) should also trigger on app foreground via `AppState.addEventListener`.

3. **Headless JS** (Android only) runs JS tasks without a UI. Register via `AppRegistry.registerHeadlessTask`. Useful for push-notification-triggered sync.

## Queue-Based Mutation Architecture

1. **Persist the mutation queue** to MMKV or WatermelonDB. A queue entry includes: `id`, `timestamp`, `endpoint`, `method`, `payload`, `retryCount`, `status`.

2. **Process sequentially** for dependent mutations (create then update). Use parallel processing only for independent operations. Exponential backoff with jitter for retries: cap at 5 retries, then mark `failed` and surface to the user.

3. **Idempotency keys**: attach a UUID to each mutation as a header (`Idempotency-Key`). The server deduplicates, making retries safe even after ambiguous network timeouts.

4. **Queue compaction**: merge multiple offline edits to the same resource into one mutation. Sending only the final state saves bandwidth and reduces conflict risk.
