# Trust Layer

This document refers to the Beacon Hub Trust Layer, not verification
descent.

## Purpose

The Beacon Hub Trust Layer gives a relay hub a narrowly-scoped way to
warn or constrain an agent without decrypting its session payloads.

## What shipped in this phase

- `internal/beacon/trust`: pinned-root verification, signed signal
  frames, freshness checks, and nonce replay rejection.
- `internal/beacon/trust/kinds`: the hardcoded signal kinds from the
  canonical scope.
- `internal/ledger/nodes/trust.go`: ledger-native trust signal,
  cooldown, ban, device attestation, and federation signal nodes.

## Signal model

The agent only accepts these kinds:

- `display_to_user`
- `ask_user_and_execute_on_approve`
- `pause`
- `rotate_session_key`
- `force_resurgence`
- `attest_state`
- `request_offline_review`

Unknown kinds are rejected. A hub cannot extend the protocol by sending
arbitrary action names.

## Verification chain

Every signal goes through the same pre-dispatch checks:

1. The hub identity must be pinned in the local trust root.
2. The frame signature must verify with the pinned Ed25519 key.
3. The frame must still be within its freshness window.
4. The nonce must not have been seen before.
5. The kind must be one of the hardcoded protocol kinds.

Rejected signals still produce ledger output so the operator can audit
why the frame was dropped.
