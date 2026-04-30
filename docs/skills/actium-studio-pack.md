# Actium Studio Skill Pack

**Status (2026-04-30): Done — R1S-1 through R1S-6 landed, including the `stoke skills pack install` operator path for the bundled pack. Hero scaffold works end-to-end; thin wrappers fixture-tested.**

Operator guide for running Actium Studio capabilities as R1 skills.
Companion to work order
`/home/eric/repos/plans/work-orders/work-r1-actium-studio-skills.md`.

## What ships

The pack is an opt-in bundle at
`.stoke/skills/packs/actium-studio/` (canonical post-rename:
`.r1/skills/packs/actium-studio/` — the resolver reads both).

- **56 skill manifests** covering the bundled Actium Studio surface:
  - **5 hand-authored heroes** — `scaffold_site`, `update_content`,
    `publish`, `diff_versions`, `site_status`.
  - **51 thin wrappers** — one per eligible Studio MCP tool (sites,
    pages, blog, blog taxonomy, seo, media, settings, snapshots,
    forms, navigation, redirects, analytics, theme, staging, roles
    read-only, billing).
- **Not shipped** (per work-order §1.1):
  - `invite_member`, `update_member_role`, `remove_member` —
    membership admin is operator-console-only.
  - `list_templates` — the Studio endpoint is unshipped.

## Installation

```bash
stoke skills pack install --pack actium-studio
```

Confirm registration:

```bash
go test ./internal/skill/... -run TestActiumStudioPackRegisters
```

## Configuration

Single config block under `studio_config` in `.stoke/config.json` (or
via env vars; precedence is canonical `R1_*` first, legacy `STOKE_*`
with deprecation WARN during the 90-day dual-accept window ending
2026-07-23).

```json
{
  "studio_config": {
    "enabled": true,
    "transport": "http",
    "http": {
      "base_url": "https://studio.actium.dev",
      "scopes_header": "studio:sites:scaffold",
      "token_env": "ACTIUM_STUDIO_TOKEN"
    },
    "stdio_mcp": {
      "command": ["npx", "actium-studio-mcp"]
    },
    "llm": {
      "openrouter_base_url": "https://relaygate.dev/openrouter/v1",
      "default_model": "claude-sonnet-4"
    }
  }
}
```

### Env-var overrides

| Canonical | Legacy | Field |
|---|---|---|
| `R1_ACTIUM_STUDIO_ENABLED` | `STOKE_ACTIUM_STUDIO_ENABLED` | `Enabled` |
| `R1_ACTIUM_STUDIO_TRANSPORT` | `STOKE_ACTIUM_STUDIO_TRANSPORT` | `Transport` |
| `R1_ACTIUM_STUDIO_BASE_URL` | `STOKE_ACTIUM_STUDIO_BASE_URL` | `HTTP.BaseURL` |
| `R1_ACTIUM_STUDIO_SCOPES` | `STOKE_ACTIUM_STUDIO_SCOPES` | `HTTP.ScopesHeader` |
| `R1_ACTIUM_STUDIO_TOKEN_ENV` | `STOKE_ACTIUM_STUDIO_TOKEN_ENV` | `HTTP.TokenEnv` |

The bearer token itself is never stored in config — `TokenEnv` names
the env var that holds it. R1 reads `os.Getenv(TokenEnv)` per-call.

## Transport choice

| | HTTP (default) | stdio-MCP |
|---|---|---|
| Remote Studio instance | Yes | No (localhost only) |
| Composite heroes | Yes | No (fall back to HTTP) |
| Depends on Node runtime | No | Yes (`actium-studio-mcp` npm) |
| Subprocess lifecycle | n/a | Lazy spawn, reused, 3-crash disable |
| Auth | Bearer + `X-Studio-Scopes` | Env vars on subprocess |

Both transports satisfy the same `Transport` interface and can be
swapped by flipping `studio_config.transport` with zero skill changes.

## Degradation

When Studio is unreachable:

- `studioclient.IsUnavailable(err)` returns true.
- The skill dispatcher surfaces
  `actium_studio_unavailable: Studio endpoint not reachable — check
  studio_config or disable this step` to the agent.
- The R1 session continues. No other skill is affected.
- No cross-product hard requirement (pack is opt-in, degrades opt-out).

When the pack is disabled (`enabled: false`):

- `studioclient.Resolve()` returns `ErrStudioDisabled` without a
  network call.
- `IsUnavailable` also returns true for the disabled error, so the
  dispatcher reuses the same UI path.

## Typed errors

From `internal/studioclient/errors.go`:

| Sentinel | When |
|---|---|
| `ErrStudioDisabled` | `studio_config.enabled: false` |
| `ErrStudioUnavailable` | DNS fail, dial refused, subprocess crash, ctx cancel |
| `ErrStudioAuth` | HTTP 401, `TokenEnv` unset, MCP `ErrAuthMissing` |
| `ErrStudioScope` | HTTP 403, MCP `ErrPolicyDenied` |
| `ErrStudioNotFound` | HTTP 404 |
| `ErrStudioValidation` | HTTP 400 / 422, unknown skill, missing path field, stdio `IsError` |
| `ErrStudioTimeout` | Per-call context deadline exceeded, HTTP 408 / 504 |
| `ErrStudioServer` | HTTP 5xx after retry exhaustion |

Use `errors.Is(err, studioclient.ErrStudioX)` to branch.
`errors.As(err, &*StudioError)` exposes `.Tool`, `.Status`,
`.BodyExcerpt` for richer diagnostics.

## Observability

Every invocation emits one `InvocationEvent` to the optional
`EventPublisher` sink:

```go
type InvocationEvent struct {
    Transport string        // "http" | "stdio-mcp"
    Tool      string        // "studio.scaffold_site"
    Status    int           // HTTP status; 0 for stdio
    Duration  time.Duration
    OK        bool
    ErrorKind string        // "auth"|"scope"|"unavailable"|... on failure
}
```

No PII, no payload body, no token echo.

## Retry & timeout policy

- 3 attempts on HTTP 5xx with exponential backoff (200ms, 400ms, 800ms,
  capped at 4s). No retry on 4xx.
- Per-call timeouts: 60s for `scaffold_site`, `trigger_seo_audit`,
  `promote_staging`, `restore_snapshot`. 30s for all others.
- Context cancellation is respected instantly; canceled contexts
  classify as `ErrStudioUnavailable`, deadline-exceeded as
  `ErrStudioTimeout`.

## Remaining work

| Phase | Status | Notes |
|---|---|---|
| R1S-1.1 config plumbing | Landed | 88ab285 |
| R1S-1.2 top-level config load | Inherited gap | Integration with existing `config.Policy` loader pending |
| R1S-1.3 env resolver | Landed | 88ab285 |
| R1S-1.4 `r1 skills pack` CLI | Landed | `stoke skills pack install --pack actium-studio` |
| R1S-1.5 pack dir + README | Landed | PR #55 |
| R1S-2 HTTP transport | Landed | 1cd010e |
| R1S-3 stdio-MCP transport | Landed | 0fb2e38 |
| R1S-4 all 56 manifests | Landed | PRs #55 / #64 / #67 |
| R1S-5 integration tests | Landed | 4b435ad |
| R1S-6 docs | Landed (this file) | |

## Troubleshooting

- **`ErrStudioAuth` from an invocation the browser can hit** — check
  that `$ACTIUM_STUDIO_TOKEN` (or whatever `TokenEnv` names) is
  exported in the shell running R1.
- **`ErrStudioValidation` with `unknown tool`** — skill name typo, or
  the skill is a composite hero being invoked under stdio-MCP. Switch
  `transport: http` or use a thin alternative.
- **`ErrStudioServer` after 3 attempts** — Studio is in a degraded
  state. Session continues; retry later.
- **Deprecation WARN about `STOKE_*` env var** — the 90-day rename
  window ends 2026-07-23. Rename your env var to `R1_*` before then.

## References

- Work order: `plans/work-orders/work-r1-actium-studio-skills.md`
- Rename: `plans/work-orders/work-r1-rename.md` (§S1-1 env dual-accept, §S1-5 skill-dir dual-resolve)
- Portfolio alignment: `plans/work-orders/verification/PORTFOLIO-ALIGNMENT.md`
- CloudSwarm integration: `plans/work-orders/scope/CLOUDSWARM-R1-INTEGRATION.md` §5.7
