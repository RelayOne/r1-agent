# VP Engineering — Completeness Audit
**Date:** 2026-04-01
**Scope:** ember/devbox, flare, stoke
**Focus:** Mocked/stubbed/thin implementations, TODO items, placeholder data, hardcoded values that should be configurable, features claimed done that are actually shells.

---

## Findings

### CRITICAL

- [ ] [CRITICAL] [flare/cmd/control-plane/main.go:244] `OrgID` hardcoded as `"org_ember"` for every app created. The `apps` table has an `org_id` column (multi-tenancy column in the schema) but the control plane never uses it — all apps share a single hardcoded org. Any query that filters by `org_id` will return the wrong scope or be unusable for real multi-tenancy.
  — fix: Accept `org_id` from the `CreateAppRequest`, validate it, and persist it; or remove the column if single-tenant is intentional and document that.
  — effort: small

- [ ] [CRITICAL] [flare/cmd/control-plane/main.go:544-547] `execMachine` endpoint returns HTTP 501 — the route is registered and advertised in the API but the implementation is a one-liner stub. The ember/devbox workers API calls exec internally for task dispatch; any caller hitting `POST /v1/apps/{app}/machines/{id}/exec` gets a permanent 501 with no path to resolution.
  — fix: Implement exec via the placement daemon's `/vm/{id}/exec` path (the placement daemon has full Firecracker socket access), or explicitly drop the route from the registered mux and from the API types so callers fail fast.
  — effort: medium

---

### HIGH

- [ ] [HIGH] [stoke/internal/compute/ember.go:131] `flareWorker.Stdout()` returns `strings.NewReader("")` — an empty, immediately-EOF reader. The `Worker` interface documents this as "a live stream of stdout (for TUI progress)". The TUI polls this for live task output when a burst worker is active. Users running `stoke build` with an Ember backend see zero live output from remote workers; they appear hung.
  — fix: Maintain a streaming connection to the worker's exec WebSocket or poll the `/v1/workers/{id}/logs` endpoint and pipe it back, OR document clearly that remote workers don't stream stdout and suppress the TUI polling path.
  — effort: medium

- [ ] [HIGH] [stoke/internal/compute/local.go:40] `localWorker.Stdout()` returns `&bytes.Buffer{}` — an empty buffer. Same issue as above for the local backend. The local backend is the default path for all users without an Ember key, so this affects every user of `stoke build` with the interactive TUI.
  — fix: The local runner already executes commands via `exec.CommandContext`; pipe stdout through a `io.Pipe` and return the reader end from `Stdout()`.
  — effort: small

- [ ] [HIGH] [ember/devbox/src/routes/workers.ts:101] Region for burst workers is hardcoded to `"sjc"` with no configuration path. Machine creation in `machines.ts` accepts `region` as a user-supplied parameter (with a default of `"sjc"`), but worker spawning never passes through any region preference. Customers in non-US regions get workers placed in the wrong geography unconditionally, adding latency to all Stoke AI tasks.
  — fix: Add `region` to `createWorkerSchema`, thread it through to `fly.createMachine`, and expose it in the Stoke `EmberBackend.Spawn` opts so users can configure preferred region.
  — effort: small

- [ ] [HIGH] [flare/cmd/control-plane/main.go:114-120] Volumes API is registered as three routes that unconditionally return HTTP 501 with `"volumes not available in v1"`. The Fly.io-compatible API surface that ember/devbox uses (`fly.ts`) already calls volume creation (`createVolume`) for every machine. Callers expecting volume support via the Flare control plane API (not Fly) get a permanent failure with no recourse.
  — fix: Implement volume lifecycle in the Flare store + placement daemon (the Firecracker manager already has a `DataDisk` field in `CreateOpts`), or explicitly remove these routes from the public API spec and never document them.
  — effort: large

---

### MEDIUM

- [ ] [MEDIUM] [stoke/internal/compute/ember.go:35] `EmberBackend.Name()` returns `"flare"` but the backend is accessed via the Ember API (`/v1/workers`). This name appears in log messages, error text, and TUI display. It's technically correct (the underlying infra is Flare), but mismatches what the user configured (`EMBER_API_KEY`), making debugging confusing. Minor but consistently misleads.
  — fix: Return `"ember"` or `"ember/flare"`.
  — effort: trivial

- [ ] [MEDIUM] [ember/devbox/src/db.ts:121] `fly_region` column defaults to `"sjc"` in the database schema. This couples the schema to a single Fly.io region. If multi-region deployment is added, existing rows will have the wrong default. The default should be `NULL` or `"auto"` with application-layer logic determining the real region.
  — fix: Change the default to `NULL` and handle null in the application layer.
  — effort: small (requires a migration)

- [ ] [MEDIUM] [flare/cmd/placement/main.go:42] Bridge network hardcoded: `{Name: "br0", Subnet: "10.0.0.0/24", Gateway: "10.0.0.1"}`. With a `/24` subnet that's 254 usable IPs. A single Flare host with many concurrent VMs will exhaust the subnet. This isn't configurable via environment or flags.
  — fix: Read bridge config from env vars (`FLARE_BRIDGE_SUBNET`, `FLARE_BRIDGE_GATEWAY`) with the current values as defaults.
  — effort: small

- [ ] [MEDIUM] [stoke/internal/workflow/workflow.go:265] Comment reads: `// Rate limited? Rotate to another pool (using the actual provider, not hardcoded Claude)`. The comment is self-narrating — the code does use `execProvider` — but it flags that an earlier version hardcoded Claude here. The surrounding rate-limit rotation code cleans up the worktree before retry but does not reset `attempt` count, meaning pool rotation consumes retry budget even when the failure is infrastructure (rate limit), not task quality.
  — fix: Track pool rotation separately from quality retries, or reset the attempt counter after a successful pool rotation.
  — effort: small

- [ ] [MEDIUM] [ember/devbox/src/routes/ai.ts:59] Default model hardcoded as `"anthropic/claude-sonnet-4"` — not read from an environment variable. If Anthropic releases a new Claude version, the fallback model requires a code deploy to update. This also prevents A/B testing or operator-level model pinning.
  — fix: Read from `process.env.AI_DEFAULT_MODEL || "anthropic/claude-sonnet-4"`.
  — effort: trivial

- [ ] [MEDIUM] [flare/internal/firecracker/manager.go:144] Root filesystem copy uses `cp --reflink=auto`. This requires a filesystem that supports reflinks (btrfs, XFS with reflink=1). On ext4 (common default for cloud VMs), `--reflink=auto` falls back to a full copy silently, but the performance assumption (fast CoW clone) is violated. On very large rootfs images, this becomes a bottleneck that appears correct but is slow.
  — fix: Document the reflink requirement in deployment notes (DEPLOYMENT.md), or detect at startup whether reflinks work via a test copy and warn loudly if they don't.
  — effort: small

---

### False Positives / Intentionally Deferred (not reported as issues)

- `flare/internal/store/store.go:505-506` — `placeholders` is a local variable name for SQL `$1,$2,...` construction, not placeholder data.
- `stoke/internal/scan/scan.go` — pattern strings that match "placeholder" are part of the scanner rule definitions, not incomplete code.
- `stoke/internal/workflow/workflow_test.go:30,60,177` — `stubManager` is a test double for an interface; appropriate for unit tests.
- All `placeholder=` attributes in `ember/devbox/web/src` — HTML form input placeholder text, not code stubs.
- `flare/cmd/control-plane/integration_test.go:110,215,257` — TODOs in integration tests asking for subprocess harness; test coverage gap, not a user-facing functional gap.
