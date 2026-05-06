# Wave B: Receipts And Honesty

Wave B turns the Wave A ledger substrate into an operator contract.

## What shipped

- `r1 receipt record` persists a task receipt under `.r1/receipts/index.jsonl`.
- `r1 receipt list` queries persisted receipts by task and kind.
- `r1 receipt export` writes a single receipt to JSON for audit handoff.
- Receipts can be HMAC-signed with `--signing-key`.
- `r1 honesty refuse` records a refusal in the ledger when R1 should not make a claim.
- `r1 honesty why-not` records skipped, deferred, or downgraded actions with evidence and optional override identity.
- `r1 cost report` persists an honest-cost rollup with provider buckets and human-minute equivalents.

## CLI examples

```bash
r1 receipt record --task task-17 --summary "Implemented replay export" --body "diff body" --signing-key "$R1_RECEIPT_KEY"
r1 receipt list --task task-17
r1 honesty refuse --task task-17 --claim "LIVE-VERIFIED" --reason "missing curl evidence"
r1 honesty why-not --task task-17 --action "gh pr merge" --reason "checks still pending"
r1 cost report --task task-17 --human-hourly-usd 180 --json
```
