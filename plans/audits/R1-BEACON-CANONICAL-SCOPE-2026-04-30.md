# R1 Beacon Canonical Scope

Date: 2026-04-30
Repo: `RelayOne/r1-agent`
Worktree base: `origin/main` at `e8608b1`
Source bundle: `/tmp/r1-beacon-ext/`

## Inputs reviewed

- `r1-beacon-protocol 2.pdf`
- `r1-trust-layer.pdf`
- `r1-missing-primitives 2.pdf`
- `r1-full-scopes.pdf`
- `r1-beacon-package 2.tar` starter code extracted to `/tmp/r1-beacon-ext/beacon-pkg/r1-beacon-package`

## Current repo reality

- `origin/main` now includes deterministic skills, skill wizard, and parity wave A through `e8608b1`.
- The ledger substrate is real and append-only, with blinded content commitments and content-addressed IDs in [internal/ledger/ledger.go](/home/eric/repos/.tmp-worktrees/r1-agent-r1-beacon/internal/ledger/ledger.go:1).
- The node registry exists and currently registers the shipped node types through `nodes.Register(...)` factories in [internal/ledger/nodes/nodes.go](/home/eric/repos/.tmp-worktrees/r1-agent-r1-beacon/internal/ledger/nodes/nodes.go:10).
- The docs currently use “trust layer” to mean verification-descent behavior, not the Beacon Hub trust-signals system described in the PDFs; see [docs/FEATURE-MAP.md](/home/eric/repos/.tmp-worktrees/r1-agent-r1-beacon/docs/FEATURE-MAP.md:77) and [docs/mintlify/feature-map.mdx](/home/eric/repos/.tmp-worktrees/r1-agent-r1-beacon/docs/mintlify/feature-map.mdx:15).
- TrustPlane/Truecom client-side DPoP and replay protection already exist for HTTP identity flows in [internal/trustplane/dpop/dpop.go](/home/eric/repos/.tmp-worktrees/r1-agent-r1-beacon/internal/trustplane/dpop/dpop.go:1), but there is no Beacon transport package yet.
- `rg --files /home/eric/repos/.tmp-worktrees/r1-agent-r1-beacon/internal | rg '/beacon/'` returned no results, so `internal/beacon/*` does not exist on trunk.

## Canonical additions required by the source bundle

### 1. Beacon protocol foundation

Required from the spec:

- Outbound-only Beacon daemon that maintains a Hub connection.
- Cryptographic identities for beacon, operator, and device.
- `/claimme` pairing flow with QR/spoken challenge, fingerprint check, SAS verification, and device-cert issuance.
- End-to-end encrypted operator↔beacon session over Hub relay using X25519 + HKDF + ChaCha20-Poly1305.
- Capability tokens with constitution binding, scoped permissions, denials, cost cap, and delegation chain validation.
- Hub relay surface for beacon registration, operator registration, session open/close, token revoke, federation handshake, and audit query.
- Beacon-as-MCP-server / MCP-compatible control surface.

Starter code already sketches the first four subdomains:

- `internal/beacon/identity`
- `internal/beacon/pairing`
- `internal/beacon/session`
- `internal/beacon/token`

from `/tmp/r1-beacon-ext/beacon-pkg/r1-beacon-package/internal/beacon/...`

### 2. Trust Layer over Beacon

Required from the spec:

- Agent-side pinned trust root for Hub signing keys.
- Signed trust-signal frames with freshness window and nonce replay rejection.
- Hardcoded signal kinds only:
  - `display_to_user`
  - `ask_user_and_execute_on_approve`
  - `pause`
  - `rotate_session_key`
  - `force_resurgence`
  - `attest_state`
  - `request_offline_review`
- Hub-side policy engine for disconnect, ban, nudge, cooldown, and federation signal propagation.
- Agent-side handler that drops unsigned, expired, replayed, and unknown-kind signals, and records the outcome in the ledger.

Starter code already sketches:

- `internal/beacon/trust/handler.go`
- `internal/beacon/trust/dispatcher.go`
- `internal/beacon/trust/kinds/*`
- `internal/beacon/hub/trust/policy.go`

### 3. New ledger node families

The PDFs and full-scope doc require at least these new node types.

Trust layer:

- `trust_signal`
- `hub_ban`
- `hub_cooldown`
- `device_attestation`
- `federation_signal`

Beacon protocol:

- `beacon_claim`
- `beacon_device_attached`
- `beacon_device_revoked`
- `beacon_session_opened`
- `beacon_session_closed`
- `beacon_token_issued`
- `beacon_token_used`
- `beacon_token_revoked`
- `beacon_delegate_created`
- `beacon_command`
- `beacon_command_result`
- `beacon_federation_handshake`

These are strict additions to the existing registry in [internal/ledger/nodes](/home/eric/repos/.tmp-worktrees/r1-agent-r1-beacon/internal/ledger/nodes/nodes.go:1).

### 4. Missing primitives explicitly called out by the audit PDFs

Items that intersect the Beacon/Trust build directly:

- Worktree-per-stance / isolated concurrent work surfaces.
- Session recap / resume from ledger and checkpoint state.
- Mission pending approvals that delegates can resolve through scoped tokens.
- Voice/mobile attachment flow and remote approvals through Beacon.
- Routines/notifications targeting named beacons.
- Cross-repo operator surfaces that keep per-repo provenance and ledger linkage.
- Artifact-backed remote review flows so offline review / attest-state can reference reproducible evidence.

Some of these are already partially present:

- Artifact storage and artifact ledger nodes landed in [internal/artifact/store.go](/home/eric/repos/.tmp-worktrees/r1-agent-r1-beacon/internal/artifact/store.go:1) and [internal/ledger/nodes/artifact.go](/home/eric/repos/.tmp-worktrees/r1-agent-r1-beacon/internal/ledger/nodes/artifact.go:1).
- Skill wizard and migration adapter landed in [internal/r1skill/wizard/wizard.go](/home/eric/repos/.tmp-worktrees/r1-agent-r1-beacon/internal/r1skill/wizard/wizard.go:1) and [internal/r1skill/wizard/adapter/adapter.go](/home/eric/repos/.tmp-worktrees/r1-agent-r1-beacon/internal/r1skill/wizard/adapter/adapter.go:1).

## Gap analysis vs trunk

### Already present and reusable

- Append-only ledger, content commitments, and registry plumbing.
- Replay/session substrate under `internal/replay/`.
- Policy, consent, delegation, bus, checkpoint, and plan packages that Beacon can reuse.
- Artifact store for attestation and offline review payloads.
- DPoP signing and anti-replay patterns in the TrustPlane client as reference logic for Hub trust signaling.

### Missing entirely

- `internal/beacon/*` protocol packages.
- Hub relay binary / package wiring for beacon sessions.
- Beacon-specific ledger node definitions and registry tests.
- Beacon CLI commands such as `r1 beacon claimme`, `r1 beacon revoke-device`, `r1 token issue`, `r1 token import`.
- Remote-control docs: `BEACON-PROTOCOL.md`, `TRUST-LAYER.md`.
- Feature-map / business-value entries for Beacon transport and Hub trust controls.

### Present but needs adaptation

- `internal/trustplane/*` naming and semantics are for TrustPlane/Truecom HTTP settlement, not Beacon Hub trust signaling.
- `internal/replay/*` covers local session replay, but not encrypted Beacon session continuity or Hub-mediated reconnect.
- Existing “trust layer” docs describe verification descent, so new docs must avoid naming collision and explicitly distinguish:
  - verification descent
  - Beacon Hub Trust Layer

## Implementation slices

### Phase 5A: `feat/r1-beacon-protocol`

- Add `internal/beacon/identity`
- Add `internal/beacon/pairing`
- Add `internal/beacon/session`
- Add `internal/beacon/token`
- Add `internal/beacon/ledgerlink` or fold those shapes into `internal/ledger/nodes`
- Add minimal `internal/beacon/hub` relay skeleton
- Add CLI entry points for claim/revoke/token workflows
- Tests:
  - identity creation and cert chain
  - pairing full flow + SAS mismatch
  - token issue/verify/authorize/delegation
  - encrypted session roundtrip + replay rejection

### Phase 5B: `feat/r1-trust-layer`

- Add `internal/beacon/trust`
- Add `internal/beacon/hub/trust`
- Implement signed signal verification, pinning, replay defense, and safe handler dispatch
- Implement initial signal kinds from starter package
- Add trust ledger nodes
- Tests:
  - signature rejection
  - expiry rejection
  - nonce replay rejection
  - ask-exec allowlist enforcement
  - trust signal ledger write

### Phase 5C: `feat/r1-missing-primitives`

- Land small, independent primitives that Beacon depends on but trunk still lacks
- Prioritize only slices directly required for Beacon MVP:
  - pending approval queue integration
  - recap/resume surfaces that reference beacon sessions
  - notification targets that address a named beacon
  - offline review / device attestation artifact links
- Keep each primitive in a small PR if it can stand alone without blocking Beacon transport

## Recommended code placement

- `internal/beacon/identity`
- `internal/beacon/pairing`
- `internal/beacon/session`
- `internal/beacon/token`
- `internal/beacon/trust`
- `internal/beacon/hub`
- `internal/ledger/nodes/beacon.go`
- `internal/ledger/nodes/trust.go`
- `cmd/r1` or `cmd/r1` subcommands for operator workflows, depending on existing CLI ownership
- `docs/BEACON-PROTOCOL.md`
- `docs/TRUST-LAYER.md`

## Acceptance bar

- Unit tests for every new package.
- Integration tests covering claim flow, token validation, encrypted session roundtrip, and trust-signal processing.
- Replay tests proving duplicate counters / duplicate nonces are rejected.
- Docs updated:
  - `docs/FEATURE-MAP.md`
  - `docs/HOW-IT-WORKS.md`
  - `docs/BUSINESS-VALUE.md`
  - `docs/BEACON-PROTOCOL.md`
  - `docs/TRUST-LAYER.md`
- Live proof still required after merge:
  - actual deployed Hub/beacon endpoint probe
  - actual reconnect / replay probe
  - actual token revoke propagation probe

## WAL

- Scope extraction complete.
- Starter package extracted and compared against trunk.
- Phase 5 can proceed in parallel across protocol, trust, and small primitive slices, but trunk first needs the foundational `internal/beacon/*` packages and ledger node expansion.
