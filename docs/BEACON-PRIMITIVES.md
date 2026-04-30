# Beacon Primitives

This phase adds the small integration pieces Beacon needs from the
existing runtime without introducing a second transport stack.

## Named-beacon notifications

`internal/notify.NotifyEvent` now has optional `beacon_id`,
`session_id`, and `artifact_ref` fields so webhook payloads can target
or explain Beacon-specific operator work.

## Artifact-backed offline review envelopes

`internal/beacon/review.Envelope` is the minimal structured shape for an
offline review request. It binds:

- the beacon,
- the session,
- the artifact containing the review evidence,
- the request timestamp,
- and the reason for escalation.

That shape is intentionally small so later Beacon trust handlers can
emit it directly into notifications or artifact-linked workflows.
