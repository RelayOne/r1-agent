# Trust Layer

This document refers to the Beacon Hub Trust Layer, not verification
descent.

## Status

The in-process hub trust verification engine
(`internal/beacon/trust`, `internal/beacon/trust/kinds`) and the
attestation runtime that would have consumed it
(`internal/beacon/runtime`, `internal/beacon/review`,
`internal/beacon/transport`) were removed in July 2026 (audit finding
A036, `audit/complete-systems-2026-07-01.md`): no binary ever
constructed them, so paired devices could never actually deliver
pause / rotate / attest signals. In-process session-control approvals
ship via the `sessionctl` approval path instead.

## What remains shipped

- `internal/ledger/nodes/trust.go`: ledger-native trust signal,
  cooldown, ban, device attestation, and federation signal node types.
  These are the append-only audit records a future hub trust layer
  would write; the node schemas are live in the ledger today.
- The Beacon pairing/token halves described in
  `docs/BEACON-PROTOCOL.md` (`internal/beacon/identity`, `pairing`,
  `session`, `token`), which carry their own signature, replay, and
  capability checks.

## Design intent (for a future reimplementation)

The removed engine enforced, per signal frame: a pinned hub identity
in a local trust root, Ed25519 frame-signature verification, a
freshness window, nonce replay rejection, and a hardcoded whitelist of
signal kinds (`display_to_user`, `ask_user_and_execute_on_approve`,
`pause`, `rotate_session_key`, `force_resurgence`, `attest_state`,
`request_offline_review`). Any revival should keep that shape: a hub
must never be able to extend the protocol by sending arbitrary action
names, and rejected signals must still produce ledger output so the
operator can audit why a frame was dropped.
