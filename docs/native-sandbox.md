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
phases where the workflow sets `RunSpec.SandboxEnabled`.

**Containment completed (as of this change):**

- **Host-exec tools are denied under sandbox.** When a sandbox policy is
  active on the tool registry, `notebook_cell_run` (shells out to `jupyter`
  on the host) and `cron_create` (writes the host crontab, runs later
  outside any sandbox) are dropped from the advertised tool set AND fail
  closed if called anyway (`errSandboxDenied*`). Both would otherwise be a
  silent unsandboxed-execution escape hatch. The operator drops the sandbox
  (`R1_NATIVE_SANDBOX=off`) to use them. Read-only siblings
  (`notebook_read`, `cron_list`, `cron_delete`) stay available.
- **`git` works in a linked worktree.** The policy now resolves the
  worktree's real git directories — the per-worktree gitdir
  (`<parent>/.git/worktrees/<name>`) and the parent repo's common dir
  (`<parent>/.git`), both OUTSIDE the worktree tree — and adds them to the
  write allowlist, so `git status`/`git commit` no longer fail closed inside
  the sandbox. Resolution is pure file parsing (reads the `.git` gitdir file
  and the `commondir` marker); no `git` subprocess, works offline. A normal
  checkout needs nothing extra (its `.git` is already under the worktree
  grant).

**Flipping the default to on is gated on** the remaining item: the network
posture. Egress defaults to *allow* (so `go mod download` / `npm install`
work in the execute phase) and no backend can do the per-domain filtering
the CLI sandbox gets — so the default posture contains the filesystem but
NOT exfiltration. Until an egress story exists (domain allowlisting, or a
default-deny with an explicit escape for module fetches), the sandbox stays
opt-in; operators enable it explicitly with `R1_NATIVE_SANDBOX=on`
(fail-closed once engaged — see below), and harden egress with
`R1_NATIVE_SANDBOX_NET=deny`.

## Backends

| Backend | Selection | Filesystem | Network | Notes |
|---|---|---|---|---|
| `bwrap` | primary (auto) | host fs read-only + writable binds (worktree, toolchain caches) + tmpfs//dev-null masks over credential paths | `--unshare-net` when egress denied | canary probe catches hosts where userns is blocked (AppArmor, containers) |
| `landlock` | fallback (auto) | strict allow-list (baseline system paths + worktree); `$HOME` deliberately absent; `/run` NOT granted wholesale, only the DNS-resolver runtime dirs (`/run/systemd/resolve`, `/run/NetworkManager`, `/run/resolvconf`, plus the resolved `/etc/resolv.conf` target) | TCP bind/connect deny on ABI >= 4; refuses (fail-closed) to deny egress on ABI < 4; **fails closed** when a daemon socket is reachable under egress-deny (Landlock can't block AF_UNIX connect — use bwrap) | re-execs `r1 __sandbox-exec` because `landlock_restrict_self` binds the calling process; `Available` runs a `__sandbox-exec --probe` self-test so binaries that don't route the helper (r1-server/r1-bench) fail closed at wiring time, not mid-mission; raw syscalls on vendored x/sys, zero new deps |
| `docker` | explicit opt-in only | only the binds are mounted; runs as `--user <host-uid>:<gid>` so created files aren't root-owned | `--network=none` when egress denied | requires `R1_SANDBOX_IMAGE`; never auto-selected — the image must carry the project toolchain |

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
  `~/.config/gh`, `~/.config/gcloud`, `worktree/.env(.local)`, plus the
  well-known **daemon control sockets** (`/run/docker.sock`,
  `/var/run/docker.sock`, `/run/containerd`, `/run/podman`, `/run/crio`,
  `/run/dbus/system_bus_socket`, `/run/systemd/private`). Landlock cannot
  mask inside an allowed tree (allow-list-only), so `worktree/.env` stays
  readable under that fallback; `~/.ssh` etc. and the daemon sockets are
  unreadable there by construction ($HOME and `/run` are not allow-listed).

## Known narrowings (deliberate, documented)

- **Egress is boolean.** `RunSpec.SandboxDomains` (the CLI sandbox's
  domain allowlist) is NOT enforceable with kernel primitives — no DNS or
  domain filtering. Default is allow, because `go mod download` /
  `npm install` in the execute phase would break otherwise. The default
  posture therefore contains the filesystem but NOT exfiltration; harden
  with `R1_NATIVE_SANDBOX_NET=deny`.
- **Unix sockets are not covered by egress denial.** `--unshare-net`
  (bwrap) and Landlock's TCP-only net restriction cut *network* egress but
  do NOT block `AF_UNIX` sockets — a reachable `/run/docker.sock` is a full
  host escape regardless of the network posture. The two backends differ in
  what they can do about it:
  - **bwrap genuinely contains it**: the daemon sockets (rootful under
    `/run` and `/var/run`, and the rootless docker/podman sockets under
    `/run/user/<uid>/`) are in the `DenyRead` mask, so bwrap mounts
    `/dev/null` / a tmpfs over each and `connect()` fails.
  - **Landlock CANNOT contain it.** Landlock does not mediate
    `connect()`/`bind()` on `AF_UNIX` pathname sockets at all
    (`landlock(7)` limitation) — keeping the socket out of the read
    allow-list does nothing to stop a connect. Rather than hand back false
    assurance, the Landlock backend **fails closed** (`Available` returns an
    error naming the socket and recommending `R1_NATIVE_SANDBOX=bwrap`) when
    a daemon socket is reachable *and* egress is denied. Under egress-allow
    the socket presence is only warned about.
  Sockets created after wrap time, or daemon sockets not on the list, remain
  a residual gap on the bwrap backend.
- **bwrap baseline is `--ro-bind / /`**: the host fs is readable minus
  the mask list; secrets outside the default masks remain readable.
  Landlock's allow-list is stricter.
- A file created inside the writable worktree after wrap time (e.g. the
  model re-creating `.env`) is not masked — binds are taken once per
  command.
- Env leakage (e.g. `ANTHROPIC_API_KEY` in `os.Environ()`) is out of
  scope here; a follow-up env scrub is tracked separately.
- Only the `bash` tool is *wrapped*. `grep`/`glob`/read/write tools go
  through their own path-safety checks; `env_exec` runs in its own
  execution environment. The other host-exec tools that can't be wrapped —
  `notebook_cell_run`, `cron_create` — are *denied* while the sandbox is
  engaged rather than left as an escape hatch (see Containment completed).

## Tests

`internal/sandbox` policy/argv/selection tests are pure and always run.
Enforcement tests (`integration_linux_test.go`) probe the host and
`t.Skip` cleanly when bwrap/Landlock are unavailable (CI is a bare golang
container). The package's test binary doubles as the re-exec helper via
`TestMain`, so the Landlock path is exercised without building `cmd/r1`.
