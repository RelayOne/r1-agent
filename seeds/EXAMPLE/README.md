# EXAMPLE seed profile

A seed profile is a directory `<seeds-path>/<profile>/` selected at run time with
`--seed-profile <name> --seeds-path <dir>`.

## Seed tiers (SeedResolver seam)

Assembled role -> domain -> task and PREPENDED to the worker system prompt
(never replaces it). Any tier may be absent and is skipped.

- `role.md`   — T0, stable identity / operating posture
- `domain.md` — T1, domain conventions / glossary / constraints
- `task.md`   — T2, narrowest task-specific guidance

## Action spec (ActionAuthorizer seam, optional)

If a profile carries a signed action spec, the runtime gates every tool call at
the tool boundary (reject-before-execute) against it:

- `action-spec.json`  — a `SignedActionSpec`:
  `{ "principal": "...", "allow": ["read_file","grep"],
     "requireApproval": ["bash"], "expiresAt": "2999-01-01T00:00:00Z",
     "signature": "<base64 Ed25519 over canonical {principal,allow,requireApproval,expiresAt}>" }`
- `action-pubkey.pem` — the principal's SPKI PEM Ed25519 public key.

A tool not in `allow` (and not `*`) is denied before it executes; a
`requireApproval` tool routes to human approval; an expired or wrongly-signed
spec fails the run closed. The default backend is a signed allowlist; a stronger
authorizer swaps in by config with zero caller changes.

These files are per-tenant secrets and are gitignored (only this EXAMPLE profile
is tracked, and it deliberately ships no real key or spec — authz stays disabled
for EXAMPLE).
