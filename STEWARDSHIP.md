# Stewardship commitments

R1 is un-managed-first: the single binary you build from this repo does
everything the project does. The managed-cloud service is an opt-in
convenience layer, never a gate on core functionality.

## The core commitment

**No functional feature will migrate from self-hosted to cloud-only.**

If a capability ships in the open-source CLI today, it will still ship in
the open-source CLI tomorrow. Managed cloud may offer the same capability
with extra convenience (hosted state, zero-setup pools, centralized audit)
but the local-only path stays supported and stays feature-complete.

This is a hard rule, not a "best effort." Enforcement:

1. Every release includes an acceptance test that builds R1 from source
   without any cloud credentials and runs a golden SOW to completion. A
   release that fails this test is not released.
2. No cloud-only feature flag is accepted into `main`. If a capability is
   cloud-gated, it either ships with a self-hosted equivalent or it doesn't
   ship yet.
3. Breaking changes to the CLI that would require cloud enrollment to
   continue working are treated as bugs, not upgrades.

## Why this matters

The prevailing pattern in agent frameworks — OSS core + paid cloud for
anything that matters — produces systems that work only at the vendor's
pleasure. R1 is a coding + general-assistant harness that operators
rely on in the middle of the night when nothing else works. It has to
keep working when the cloud doesn't, when the network is segmented, and
when the vendor has priorities that aren't yours.

The managed-cloud path exists because some users genuinely prefer hosted
convenience and some teams need centralized audit trails they don't want
to self-operate. Both are reasonable; neither creates a dependency for
users who don't want one.

## What "managed cloud" does NOT mean

- It does not mean better models. The open-source path and the managed
  path call the same provider APIs.
- It does not mean better safety. The supervisor rules, anti-deception
  patterns, and integrity gates are the same binary in both paths.
- It does not mean better performance. The hosted path may have lower
  latency due to edge PoPs, but no speed tier is reserved for managed
  users.
- It does not mean more tools. The tool surface is identical; the
  capability manifests that drive tool registration are the same files.

## What it DOES mean

- Hosted session state (you don't run a database).
- Centralized pool management across devices.
- Cross-agent audit consolidation for teams running multiple R1
  instances.
- Optional identity anchoring via the TrustPlane network.

If any of those things would be useful, `stoke cloud register`. If not,
the local binary is complete.

## Questions

Open an issue. Stewardship is a public commitment, not a legal document —
if you think we're violating the spirit of it, please say so in the open
where the community can weigh in.
