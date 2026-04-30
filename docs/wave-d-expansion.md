# Wave D Expansion Features

Wave D lands the first operator-facing slice of the expansion features from the parity-and-superiority SOW.

## Commands

- `stoke cf --mission mission.json --change reviewer.model=claude --change verify.tier_max=T6`
- `stoke why-broken --input regression.json`
- `stoke self-tune --baseline baseline.json --candidates candidates.json`

All three commands are JSON-driven in this first slice. That keeps the package logic deterministic and testable while the broader live-ledger and UI wiring is still in flight.

## Smoke

Counterfactual replay:

```bash
go run ./cmd/stoke cf --mission ./testdata/wave-d/mission.json --change reviewer.model=claude
```

Decision bisector:

```bash
go run ./cmd/stoke why-broken --input ./testdata/wave-d/regression.json
```

Self tune:

```bash
go run ./cmd/stoke self-tune --baseline ./testdata/wave-d/baseline.json --candidates ./testdata/wave-d/candidates.json
```
