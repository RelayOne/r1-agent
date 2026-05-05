# Beacon Protocol

The Beacon Protocol gives an R1 runtime a cryptographically verifiable
remote-control surface without trusting the relay that carries the
traffic.

## What shipped in this phase

- `internal/beacon/identity`: Beacon, operator, and device identities
  using Ed25519 plus signed device certificates.
- `internal/beacon/pairing`: `/claimme` challenge/response flow with
  fingerprint verification inputs and SAS derivation.
- `internal/beacon/session`: X25519 + HKDF + ChaCha20-Poly1305 session
  setup with per-frame replay rejection.
- `internal/beacon/token`: signed capability tokens with constitution
  binding, cost caps, deny rules, and delegation limits.
- `internal/ledger/nodes/beacon.go`: append-only ledger nodes for claim,
  device, session, token, command, and federation events.

## Pairing flow

1. The beacon creates a short-lived challenge containing its identity,
   fingerprint, ephemeral X25519 public key, and nonce-derived spoken
   words.
2. The operator device receives that challenge out-of-band, creates its
   own ephemeral X25519 key, and returns a response carrying the device
   identity and operator master key reference.
3. Both sides derive the same SAS value from the challenge and response
   nonces plus the exchanged ephemeral keys.
4. Once the operator confirms the SAS match, the operator can issue a
   signed device certificate to bind that device to the operator.

## Session flow

- Each session uses fresh X25519 ephemeral keys.
- HKDF derives separate outbound and inbound traffic keys.
- Frames are encrypted with ChaCha20-Poly1305.
- The frame counter is part of the nonce and is tracked for replay
  rejection.

## Capability tokens

Capability tokens are Ed25519-signed documents that bind:

- the issuing operator,
- the subject operator,
- allowed beacon IDs,
- allow and deny permission lists,
- a constitution hash,
- a maximum delegation depth,
- and a budget cap.

The runtime can authorize an operation by checking the beacon ID,
permission pattern, deny rules, and cost cap before the command is
executed.
