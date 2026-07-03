# Native bash sandbox (`internal/sandbox`)

OS-level containment for the native agent loop's `bash` tool. The CLI
runners (Claude/Codex subprocesses) have always received a sandbox via the
per-worktree `settings.json` (`config.SandboxSettings`, fail-closed,
decision #6). The native runner executed `bash -c` directly on the host
with only process-group isolation — it silently dropped the very same
`RunSpec.SandboxEnabled` request the CLI path honors. This package closes
that asymmetry.

## Enforcement layers

Ordering inside `tools.Registry.handleBash`:

1. `bashBreakerCheck` — the always-on regex floor (catastrophic commands
   only). Unchanged; runs before the sandbox is consulted.
2. **OS sandbox** (this package) — wraps command construction when a
   sandbox is wired via `Registry.SetSandbox`.
3. Existing process-group / Cancel / WaitDelay plumbing — unchanged; the
   wrapper never precludes group-kill semantics.

## Engagement (opt-in)

The sandbox is **opt-in**: it engages only when `R1_NATIVE_SANDBOX` is set
to a non-`off` value (`on`/`auto`/`bwrap`/`landlock`/`docker`; `on` aliases
`auto`). An unset variable means "do not engage", even on the execute/verify
phases where the workflow sets `RunSpec.SandboxEnabled`. This is deliberate:
the current containment wraps only the `bash` tool, so `notebook_cell_run`
(jupyter) and `cron_create` (crontab) still exec on the host, and a
sandboxed bash cannot run `git` in a linked worktree (the parent repo's
`.git` is outside the allowlist). A default-on sandbox with those gaps would
be false assurance.

**Flipping the default to on is gated on** completing containment:
(a) route `notebook_cell_run`/`cron_create` through the sandbox — or deny
them while it is engaged; and (b) add the worktree's real `.git` directory
to the read allowlist so `git` works in linked worktrees. Until then,
operators opt in explicitly with `R1_NATIVE_SANDBOX=on` (fail-closed once
engaged — see below).

## Backends

| Backend | Selection | Filesystem | Network | Notes |
|---|---|---|---|---|
| `bwrap` | primary (auto) | host fs read-only + writable binds (worktree, toolchain caches) + tmpfs//dev-null masks over credential paths | `--unshare-net` when egress denied | canary probe catches hosts where userns is blocked (AppArmor, containers) |
| `landlock` | fallback (auto) | strict allow-list (baseline system paths + worktree); `$HOME` deliberately absent | TCP bind/connect deny on ABI >= 4; refuses (fail-closed) to deny egress on ABI < 4 | re-execs `r1 __sandbox-exec` because `landlock_restrict_self` binds the calling process; raw syscalls on vendored x/sys, zero new deps |
| `docker` | explicit opt-in only | only the binds are mounted | `--network=none` when egress denied | requires `R1_SANDBOX_IMAGE`; never auto-selected — the image must carry the project toolchain |

## Fail-closed contract

Once engaged (via `R1_NATIVE_SANDBOX`, see Engagement above) and no backend
can enforce the policy, the native runner refuses to dispatch — the error
names the opt-out. An explicitly named mode never falls through to another
backend. The ONLY silent-passthrough is the kill switch (`=off`) or leaving
the variable unset (opt-in default).

## Configuration (env-first, `R1_*` with `STOKE_*` legacy twins)

| Variable | Values | Default | Meaning |
|---|---|---|---|
| `R1_NATIVE_SANDBOX` | `auto`\|`bwrap`\|`landlock`\|`docker`\|`off` | `auto` | backend selection; `off` is the kill switch; typos fail closed at wiring time |
| `R1_NATIVE_SANDBOX_NET` | `allow`\|`deny` | `allow` | egress as a whole; unrecognized values fail toward deny |
| `R1_SANDBOX_IMAGE` | image ref | unset | required by (and only used by) the docker backend |

## Default policy

- **AllowWrite**: the worktree (from `RunSpec.SandboxAllowWrite`), plus
  toolchain caches (`~/.cache`, `~/go/pkg`, `~/.npm`) so builds work —
  a sandboxed command can poison those caches for later host builds;
  operators who care trade that for cold caches.
- **DenyRead masks** (bwrap/docker): `~/.ssh`, `~/.aws`, `~/.gnupg`,
  `~/.netrc`, `~/.docker`, `~/.kube`, `~/.claude`, `~/.codex`,
  `~/.config/gh`, `~/.config/gcloud`, and `worktree/.env(.local)`.
  Landlock cannot mask inside an allowed tree (allow-list-only), so
  `worktree/.env` stays readable under that fallback; `~/.ssh` etc. are
  unreadable there by construction ($HOME is not allow-listed).

## Known narrowings (deliberate, documented)

- **Egress is boolean.** `RunSpec.SandboxDomains` (the CLI sandbox's
  domain allowlist) is NOT enforceable with kernel primitives — no DNS or
  domain filtering. Default is allow, because `go mod download` /
  `npm install` in the execute phase would break otherwise. The default
  posture therefore contains the filesystem but NOT exfiltration; harden
  with `R1_NATIVE_SANDBOX_NET=deny`.
- **bwrap baseline is `--ro-bind / /`**: the host fs is readable minus
  the mask list; secrets outside the default masks remain readable.
  Landlock's allow-list is stricter.
- A file created inside the writable worktree after wrap time (e.g. the
  model re-creating `.env`) is not masked — binds are taken once per
  command.
- Env leakage (e.g. `ANTHROPIC_API_KEY` in `os.Environ()`) is out of
  scope here; a follow-up env scrub is tracked separately.
- Only the `bash` tool is wrapped. `grep`/`glob`/read/write tools go
  through their own path-safety checks; `env_exec` runs in its own
  execution environment.

## Tests

`internal/sandbox` policy/argv/selection tests are pure and always run.
Enforcement tests (`integration_linux_test.go`) probe the host and
`t.Skip` cleanly when bwrap/Landlock are unavailable (CI is a bare golang
container). The package's test binary doubles as the re-exec helper via
`TestMain`, so the Landlock path is exercised without building `cmd/r1`.
