# R1 Deployment

This document summarizes how `R1` is developed, validated, and prepared for production from the current repo state.

## Local and Validation Commands

```bash
go test ./...
make test || true
```

## Deployment Posture

- Go test remains the primary validation spine.
- Current checkout has active daemon and rules work plus untracked local artifacts outside this docs commit.
- Deployment narrative should stay focused on governed runtime packaging rather than generic SaaS operations.

## Surfaces That Matter Most

| Path | Role |
|---|---|
| `cmd` | CLI and runtime entry points. |
| `internal` | Daemon, execution, cloud, and governance internals. |
| `docs` | Canonical narrative set. |
| `bench` | Benchmarks and supporting evaluation inputs. |
| `desktop` | Desktop-facing docs and surfaces. |

## Release Readiness Checklist

- Confirm the canonical docs and root README match the shipped repo state.
- Run the product's build, test, and lint or equivalent validation path.
- Smoke-check the highest-intent user surfaces after structural edits.
- Keep domain, auth, billing, and compliance language aligned with what is actually live.

## Current Caveats

- Daemon and runtime policy work are active in the current checkout.
- Broader portfolio adoption of deterministic skills is still unfolding.

---

Last updated: 2026-05-01
