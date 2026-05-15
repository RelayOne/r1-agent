# r1.run deploy state — historical snapshot replaced

This file used to describe the initial 2026-05-05 deploy incident and recovery. It is no longer the authoritative deploy handoff.

## Use instead

- [`plans/TRUTH-STATE-2026-05-15.md`](TRUTH-STATE-2026-05-15.md) for the verified live state
- [`docs/DEPLOYMENT.md`](../docs/DEPLOYMENT.md) for the operator runbook

## What is now confirmed

- Cloudflare DNS for `r1.run` is already present, not pending.
- 12 public custom-domain mappings are healthy:
  - `platform|api|downloads|admin`
  - across `prod|staging|dev`
- All 12 public hostnames returned `200` on `/livez` during the 2026-05-15 audit.
- The public Cloud Run footprint is 4 services × 3 envs.

## Why this file remains

- It preserves the record that the original stalled-cert / DNS story happened.
- It should not be used for current operator decisions.
