# offline-sync

> Offline-first patterns, data synchronization, conflict resolution, and mobile persistence

<!-- keywords: offline, sync, conflict, merge, crdt, optimistic, pessimistic, queue, cache, local storage, indexeddb, sqlite, react native -->

## Critical Rules

1. **Offline is not an error state.** Design for offline-first. The app must be fully functional without network. Sync when connectivity returns.

2. **Never lose user work.** If a user creates/edits data offline, it must survive app restart, crash, and eventually sync. Use write-ahead logging or persistent queues.

3. **Conflict resolution must be deterministic.** "Last write wins" is a policy, not a bug. But you must choose: LWW, merge, or ask user. Document the policy.

4. **Sync must be idempotent.** Network retries mean the same operation may arrive multiple times. Use idempotency keys for mutations.

5. **Show sync status to the user.** Users need to know: synced, pending, conflict, error. Don't hide sync state.

## Sync Patterns

### Operation-Based Sync (recommended)
- Queue operations (create, update, delete) with timestamps
- Replay operations in order on reconnect
- Server applies operations idempotently
- Handles concurrent edits naturally

### State-Based Sync
- Send full current state to server
- Server computes diff against known state
- Simpler but larger payloads and merge complexity

### CRDT (Conflict-free Replicated Data Types)
- Counters: G-Counter (grow-only), PN-Counter (positive-negative)
- Sets: G-Set (grow-only), OR-Set (observed-remove)
- Maps: OR-Map for nested structures
- Automatically merge without conflicts
- Trade-off: limited operations, larger state

## Conflict Resolution Strategies

| Strategy | Use When |
|----------|----------|
| Last-write-wins | Low-conflict data (settings, preferences) |
| Server-wins | Authoritative source (inventory, pricing) |
| Client-wins | User-generated content (notes, drafts) |
| Merge | Compatible changes (different fields modified) |
| Ask user | Incompatible changes to same field |

### Three-Way Merge
```
base (common ancestor) + local changes + remote changes
→ If same field changed both sides → conflict
→ If different fields changed → auto-merge
→ If same change made → no-op (already converged)
```

## Mobile Persistence Stack

### React Native
- **SQLite** (`expo-sqlite` or `react-native-sqlite-storage`): Structured data
- **MMKV** (`react-native-mmkv`): Key-value, 30x faster than AsyncStorage
- **File system** (`expo-file-system`): Binary data, images, downloads
- **Never AsyncStorage for large data.** It serializes everything to JSON strings.

### Queue Implementation
```
1. User action → write to local DB + add to sync queue
2. Show optimistic UI immediately
3. Background sync: dequeue, POST to server, mark synced on 2xx
4. On conflict (409): resolve per policy, update local
5. On network error: re-queue with exponential backoff
```

## Common Gotchas

- **Clock skew between devices.** Don't rely on client timestamps for ordering. Use server-generated logical clocks (Lamport/vector clocks).
- **Partial sync.** If sync sends 10 items and fails on #7, the first 6 must not re-send. Track per-item sync status.
- **App killed during sync.** iOS/Android can kill background processes. Each sync step must be atomic. Resume from last successful step.
- **Cache invalidation on schema change.** App update changes local DB schema. Include schema version. Migrate on launch.
- **Attachment sync.** Large files (images, videos) must sync separately from data. Don't block data sync on slow file upload.
- **Push notifications for sync triggers.** Don't poll. Use push/WebSocket to trigger sync when server has new data.
