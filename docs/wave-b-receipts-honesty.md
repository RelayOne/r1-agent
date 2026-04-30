# Wave B: Receipts And Honesty

Wave B turns the Wave A ledger substrate into an operator contract.

## What shipped

- `stoke receipt record` persists a task receipt under `.r1/receipts/index.jsonl`.
- `stoke receipt list` queries persisted receipts by task and kind.
- `stoke receipt export` writes a single receipt to JSON for audit handoff.
- Receipts can be HMAC-signed with `--signing-key`.
- `stoke honesty refuse` records a refusal in the ledger when R1 should not make a claim.
- `stoke honesty why-not` records skipped, deferred, or downgraded actions with evidence and optional override identity.
- `stoke cost report` persists an honest-cost rollup with provider buckets and human-minute equivalents.

## CLI examples

```bash
stoke receipt record --task task-17 --summary "Implemented replay export" --body "diff body" --signing-key "$R1_RECEIPT_KEY"
stoke receipt list --task task-17
stoke honesty refuse --task task-17 --claim "LIVE-VERIFIED" --reason "missing curl evidence"
stoke honesty why-not --task task-17 --action "gh pr merge" --reason "checks still pending"
stoke cost report --task task-17 --human-hourly-usd 180 --json
```
