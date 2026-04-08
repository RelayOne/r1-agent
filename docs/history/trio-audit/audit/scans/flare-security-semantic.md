# Flare Security & Semantic Audit

**Date:** 2026-04-01
**Scope:** 11 files across control-plane, placement, firecracker manager, networking, reconciler, store, and TypeScript SDK
**Auditor:** Claude Code (semantic scan)

---

## Summary

| Severity | Count |
|----------|-------|
| CRITICAL | 8     |
| HIGH     | 12    |

---

## Findings

### F-001: MISSING_AUTH (critical) -- Internal auth bypassed when FLARE_INTERNAL_KEY is empty

**File:** `flare/cmd/control-plane/main.go`, lines 87-95
**File:** `flare/cmd/placement/main.go`, lines 77-84

The `internalAuth` middleware checks `cp.internalKey != ""` before enforcing the key. If `FLARE_INTERNAL_KEY` is unset (empty string), all internal endpoints are completely unauthenticated. This includes `POST /internal/hosts/register` and `POST /internal/hosts/heartbeat`, which allow any attacker to register a rogue host and receive VM traffic.

```go
if cp.internalKey != "" && r.Header.Get("X-Internal-Key") != cp.internalKey {
```

**Fix:** Require `FLARE_INTERNAL_KEY` at startup (fatal if empty), or invert the logic to deny by default when unset. At minimum, log a warning at startup if the key is empty.

---

### F-002: MISSING_AUTH (critical) -- Ingress proxy has no authentication

**File:** `flare/cmd/control-plane/main.go`, lines 147-156, 662-692
**File:** `flare/cmd/placement/main.go`, lines 96-98, 114-152

The VM ingress proxy on `INGRESS_PORT` (default 8080) has zero authentication. Any network-reachable client can proxy HTTP requests to any running VM by knowing its hostname or machine ID. The placement daemon's ingress (`/_vm/{machineID}/...`) is also unauthenticated and directly routes to guest VMs by machine ID -- an attacker who guesses or enumerates machine IDs gets direct access.

**Fix:** Add authentication to the ingress proxy (e.g., Cloudflare Access headers, mTLS, or a shared secret). At minimum, bind ingress to localhost if it should only receive traffic from a local tunnel.

---

### F-003: SECURITY (critical) -- Command injection via Exec function

**File:** `flare/internal/firecracker/manager.go`, lines 458-486

The `Exec` method concatenates user-provided `cmd` and `args` into a single string passed as a shell argument to SSH:

```go
fullCmd := cmd
if len(args) > 0 {
    fullCmd += " " + strings.Join(args, " ")
}
sshCmd := exec.CommandContext(ctx, "ssh", ..., fmt.Sprintf("root@%s", vm.IPAddress), fullCmd)
```

SSH remote commands are executed through a shell on the remote side, so `cmd` or `args` values like `; rm -rf /` would execute arbitrary commands. Although exec is disabled at the API level (returns 501), the function exists and could be re-enabled. The code also trusts `vm.IPAddress` without validation -- a corrupted IP could redirect SSH to an attacker's machine.

**Fix:** If exec is ever re-enabled, use a protocol that doesn't involve shell expansion (e.g., Firecracker's vsock API). Validate `vm.IPAddress` is an actual IP. Remove or mark the function as deprecated with a clear security warning.

---

### F-004: RACE_CONDITION (critical) -- Non-atomic desired state update + placement call

**File:** `flare/cmd/control-plane/main.go`, lines 459-479 (startMachine), 489-509 (stopMachine)

`startMachine` calls `UpdateMachineDesired(ctx, m.ID, "running")` then calls placement. If placement fails, desired_state is already set to "running" but observed_state remains unchanged. The reconciler will then repeatedly try to start a VM that may be in an unrecoverable state, creating an infinite retry loop.

```go
cp.store.UpdateMachineDesired(r.Context(), m.ID, "running")  // committed immediately
resp, err := cp.callPlacement(...)                             // can fail
```

Same pattern in `stopMachine`: desired is set to "stopped" before confirming the stop succeeded.

**Fix:** Either (a) wrap the desired-state update and placement call in a transaction with rollback on failure, or (b) only update desired_state on successful placement response, or (c) accept the current behavior but add a max-retry/circuit-breaker in the reconciler for machines stuck in a desired!=observed loop.

---

### F-005: RACE_CONDITION (critical) -- startMachine capacity check is not atomic

**File:** `flare/cmd/control-plane/main.go`, lines 450-458

The capacity check in `startMachine` reads host capacity and compares it to the machine's requirements, but this read is not in a transaction with any reservation. Two concurrent start requests for VMs on the same host can both pass the check and overcommit the host.

```go
available := host.CapacityCPUs - host.UsedCPUs - host.ReservedCPUs
availableMem := host.CapacityMemMB - host.UsedMemMB - host.ReservedMemMB
if m.GuestCPUs > available || m.GuestMemMB > availableMem {
    // reject
}
// No reservation is made -- another request can slip through
```

**Fix:** Use `SELECT ... FOR UPDATE` on the host row inside a transaction, then atomically increment `used_cpus`/`used_mem_mb` before calling placement. Or use the same `PlaceAndReserve` pattern that `createMachine` uses.

---

### F-006: MISSING_AUTH (critical) -- Placement daemon health endpoint exposes host details

**File:** `flare/cmd/placement/main.go`, lines 323-331

`GET /health` is unauthenticated and returns `host_id`, CPU/memory usage, and total VM count. This is useful reconnaissance information for an attacker.

```go
apiMux.HandleFunc("GET /health", d.handleHealth)  // no internalAuth wrapper
```

**Fix:** Either wrap `/health` with `internalAuth`, or strip sensitive fields from the unauthenticated response (return only `{"status":"ok"}`).

---

### F-007: RACE_CONDITION (critical) -- Reconciler ClaimDriftedMachines is two-phase without transaction

**File:** `flare/internal/store/store.go`, lines 463-497

`ClaimDriftedMachines` runs two separate SQL statements: an UPDATE to claim machines, then a SELECT to read them back. These are not in a transaction. Between the UPDATE and SELECT, another process could modify the claimed machines, or the 10-second window check (`claimed_at > now() - interval '10 seconds'`) could miss machines if the system clock drifts or the UPDATE takes longer than expected.

**Fix:** Wrap both statements in a single transaction, or use a CTE (`WITH updated AS (UPDATE ... RETURNING *) SELECT * FROM updated`).

---

### F-008: MISSING_VALIDATION (high) -- No app name validation on createApp

**File:** `flare/cmd/control-plane/main.go`, lines 241-249

`createApp` accepts any `app_name` without validation -- empty strings, extremely long strings, strings with special characters, SQL-safe but semantically meaningless names. The `OrgID` is hardcoded to `"org_ember"` which means all apps belong to the same org regardless of the authenticated user.

**Fix:** Validate `app_name`: non-empty, max length, alphanumeric+dash pattern. Make `OrgID` derived from the authenticated identity, not hardcoded.

---

### F-009: MISSING_VALIDATION (high) -- CPUs/MemoryMB allow value 0 after default assignment

**File:** `flare/cmd/control-plane/main.go`, lines 293-308

The validation checks `cpus < 0` but the default assignment on line 296 sets `cpus = 4` when it's 0. This means `cpus = 0` never reaches the `< 0` check. However, a negative value passed as JSON (e.g., `"cpus": -1`) would be caught. The real issue: there is no minimum check -- `cpus = 0` is silently defaulted rather than being an explicit error. A user sending `{"cpus": 0}` may not realize they're getting 4 CPUs.

Additionally, `memMB` and `cpus` are `int` types, so extremely large values could pass the upper bound check on 32-bit systems due to integer overflow.

**Fix:** Validate before applying defaults, or document the defaulting behavior in the API response. Use explicit minimum bounds (`cpus >= 1`). Consider using `int32` with explicit bounds.

---

### F-010: MISSING_ERROR_HANDLING (high) -- Heartbeat swallows errors and returns 200

**File:** `flare/cmd/control-plane/main.go`, lines 649-658

The heartbeat endpoint always returns HTTP 200, even when the store update fails:

```go
err := cp.store.Heartbeat(r.Context(), req.HostID, req.UsedCPUs, req.UsedMem)
writeJSON(w, 200, map[string]bool{"ok": err == nil})
```

If a host sends heartbeats that silently fail (e.g., host not registered), the host believes it's registered while the control plane doesn't track it. VMs placed on this "ghost host" will fail.

**Fix:** Return HTTP 500 or 404 on heartbeat failure so the placement daemon can re-register.

---

### F-011: MISSING_ERROR_HANDLING (high) -- Multiple store operations ignore errors in hot paths

**File:** `flare/cmd/control-plane/main.go`, lines 459, 477-478, 489, 507-508, 519, 533-535

Throughout the machine lifecycle handlers, `UpdateMachineDesired`, `UpdateMachineObserved`, `RecordEvent`, `DeleteHostnamesByMachine`, and `DeleteMachine` return errors that are silently discarded:

```go
cp.store.UpdateMachineDesired(r.Context(), m.ID, "running")   // error ignored
cp.store.UpdateMachineObserved(r.Context(), m.ID, "running", "", "")  // error ignored
cp.store.RecordEvent(r.Context(), m.ID, "started", "")        // error ignored
```

If any of these fail, the database state diverges from reality. The machine may appear stopped in the DB while running on the host, or vice versa.

**Fix:** Check all error returns. At minimum, log failures. For critical state transitions (desired/observed updates), return an error to the caller.

---

### F-012: MOCK_IN_PRODUCTION (critical) -- Networking manager state is purely in-memory

**File:** `flare/internal/networking/tap.go`, lines 23-38

The networking `Manager` holds all TAP device allocations, IP assignments, and sequence counters in memory only. If the placement daemon crashes and restarts, `RecoverDevice` rebuilds from `vm.json`, but any VMs whose `vm.json` was corrupted or lost will cause IP/TAP collisions. More critically, `freeIPs` (recycled IPs) is never persisted -- after restart, freed IPs from destroyed VMs are permanently lost from the pool.

**Fix:** Persist the networking state (nextIP, freeIPs, tapSeq) to disk alongside vm.json, or derive it fully from the set of recovered VMs on startup (scan all vm.json files to rebuild freeIPs).

---

### F-013: MISSING_VALIDATION (high) -- No validation on host registration fields

**File:** `flare/cmd/control-plane/main.go`, lines 615-647

`registerHost` validates that `address` is non-empty but does not validate:
- `host_id` (could be empty or malicious)
- `capacity_cpus` and `capacity_memory_mb` (could be 0, negative, or absurdly large)
- `zone` (no format validation)
- `address` (no format validation -- could be a URL, a hostname, or garbage)

The `ingressAddr` is derived by string replacement (`":9090"` -> `":8080"`), which fails silently if the address doesn't contain `:9090`.

```go
ingressAddr := strings.Replace(req.Address, ":9090", ":8080", 1)
```

**Fix:** Validate `host_id` is non-empty, `capacity_cpus > 0`, `capacity_memory_mb > 0`, `zone` matches expected format, and `address` is a valid host:port. Parse the address properly instead of string replacement.

---

### F-014: SECURITY (high) -- Error messages leak internal state

**File:** `flare/cmd/control-plane/main.go`, lines 246, 375, 599

Several error responses include raw error messages from the database or internal systems:

```go
writeJSON(w, 500, map[string]string{"error": err.Error()})  // line 246 - DB error
writeJSON(w, 502, map[string]string{"error": "placement create failed: " + string(body)})  // line 375
writeJSON(w, 500, map[string]string{"error": err.Error()})  // line 599 - hostname create
```

Database errors can reveal table names, column names, constraint names, and connection details. Placement responses could contain internal host IPs.

**Fix:** Return generic error messages to clients. Log the detailed error server-side.

---

### F-015: CONFIG_LEAK (high) -- UpsertHost ignores LastHeartbeat from the Host struct

**File:** `flare/internal/store/store.go`, lines 121-135

`UpsertHost` accepts a `Host` struct with a `LastHeartbeat` field, but the SQL uses `time.Now()` as the 11th parameter instead of `h.LastHeartbeat`. The struct's `LastHeartbeat` is silently ignored. This isn't a bug per se (using server time is correct), but it's a schema mismatch: the struct suggests the caller controls the heartbeat timestamp.

**Fix:** Remove `LastHeartbeat` from the `Host` struct if it should always be server-time, or use `h.LastHeartbeat` if caller control is intended. Make the contract explicit.

---

### F-016: MISSING_ERROR_HANDLING (high) -- persistVM silently fails

**File:** `flare/internal/firecracker/manager.go`, lines 385-392

`persistVM` silently discards both the marshal error and the WriteFile error:

```go
func (m *Manager) persistVM(vm *VM) {
    dir := filepath.Join(m.vmDir, vm.ID)
    data, err := json.MarshalIndent(vm, "", "  ")
    if err != nil {
        return  // silent failure
    }
    os.WriteFile(filepath.Join(dir, "vm.json"), data, 0644)  // error ignored
}
```

If vm.json fails to write after Start(), the PID is not persisted. On daemon restart, the VM appears stopped (PID=0) while the Firecracker process is still running, creating an orphaned VM that consumes resources indefinitely.

**Fix:** Return an error from `persistVM` and handle it in callers. At minimum, log the failure. Consider making Start() fail if persistence fails.

---

### F-017: RACE_CONDITION (high) -- VM struct accessed without lock in multiple places

**File:** `flare/internal/firecracker/manager.go`, lines 196-216 (Create), 395-402 (Destroy)

In `Create`, the VM is constructed and fields are written before it's added to the map. This is safe for Create since no one else has a reference yet. However, in `Destroy`, `Stop` is called (which acquires vm.mu), then `os.RemoveAll` runs, then the VM is removed from the map. Between `Stop` returning and `delete(m.vms, vm.ID)`, another goroutine could call `Get(vmID)`, receive the VM pointer, and attempt to `Start` it after its directory has been deleted.

**Fix:** Hold `m.mu` (write lock) across the entire Destroy operation, or mark the VM as "destroying" to prevent concurrent operations.

---

### F-018: SCHEMA_MISMATCH (high) -- SDK Volume type has no server implementation

**File:** `flare/sdk/typescript/src/index.ts`, lines 33-39, 142-154

The TypeScript SDK defines a `Volume` interface and `volumes.create/list/destroy` methods, but the server returns 501 for all volume endpoints. Users calling `client.volumes.create()` will get a confusing error. The SDK should either not expose these methods or clearly mark them as unavailable.

**Fix:** Remove volume methods from the SDK, or add JSDoc deprecation warnings that volumes are not available in v1. Alternatively, throw a clear client-side error before making the request.

---

### F-019: MISSING_VALIDATION (high) -- Ingress proxy path traversal via machineID

**File:** `flare/cmd/placement/main.go`, lines 114-152

The placement ingress parses `/_vm/{machineID}/...` from the URL path. The `vmID` is extracted by string splitting and used in `d.fcMgr.Get(vmID)`, which looks up an in-memory map. While this is safe against path traversal (it's a map key, not a file path), the `remainder` variable is passed directly to the reverse proxy without sanitization:

```go
req.URL.Path = remainder
```

If `remainder` contains encoded path traversal sequences (e.g., `/../`), the reverse proxy will forward them to the guest VM. This could allow accessing unintended paths on the guest if the guest has a misconfigured web server.

**Fix:** Sanitize `remainder` with `path.Clean()` and reject paths containing `..`.

---

### F-020: MISSING_ERROR_HANDLING (high) -- Placement daemon register failure is fatal but recovery is not

**File:** `flare/cmd/placement/main.go`, lines 70-71, 175-201

If `register()` fails on startup, the daemon exits (`log.Fatalf`). But if the heartbeat fails continuously after startup (lines 204-227), the daemon continues running silently with no recovery mechanism. The control plane will eventually mark the host as dead, but VMs created during the window between heartbeat failure and dead-host detection will be placed on an effectively disconnected host.

**Fix:** Add a consecutive-failure counter to `heartbeatLoop`. After N consecutive failures (e.g., 5), attempt re-registration. After M failures, log a critical error and optionally exit.

---

## Non-Findings (reviewed, no issue)

- **SQL injection:** All database queries use parameterized `$N` placeholders. The one dynamic query (`MarkMachinesLostOnDeadHosts`) builds placeholders programmatically from `deadHostIDs` which come from a previous DB query, not user input. Safe.
- **Path traversal in ImageRef:** `Create` in `manager.go` validates `ImageRef` character-by-character (lines 129-133), rejecting `/`, `..`, etc. Correctly implemented.
- **Reconciler concurrency:** Uses `FOR UPDATE SKIP LOCKED` for work-stealing. Safe for concurrent instances, though F-007 notes the two-phase claim issue.
- **Store field access:** `machineSelect` constant and `scanMachine` are consistent with each other across all callers.

---

## Priority Recommendations

1. **Immediate (before any deployment):** Fix F-001 (require internal key), F-002 (ingress auth), F-005 (atomic capacity check)
2. **Before production traffic:** Fix F-004 (atomic state transitions), F-007 (claim transaction), F-016 (persist VM errors)
3. **Before GA:** Fix F-003 (exec injection), F-010 (heartbeat errors), F-011 (ignored errors), F-012 (networking persistence)
4. **Track for v2:** F-008 (app name validation), F-009 (CPU defaults), F-013 (host registration), F-018 (SDK volumes)
