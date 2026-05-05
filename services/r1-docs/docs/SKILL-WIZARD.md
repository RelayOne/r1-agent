# Skill Wizard

The deterministic skill wizard converts source artifacts into canonical `*.r1.json` skills plus compile proofs and a decision ledger.

Primary commands:

```bash
r1 wizard run --from ./legacy-skill.md --source-format r1-markdown-legacy --mode headless --out-dir ./out --ledger-dir ./.r1/ledger --mission-id skill-migrate
r1 wizard migrate --source-dir ./old-skills --source-format codex-toml --output-dir ./out --mode headless
r1 wizard register --skill ./out/my-skill.r1.json --proof ./out/my-skill.proof.json
r1 wizard query --ledger-dir ./.r1/ledger --session-id skill_authoring_decisions-1234abcd --question-prefix caps.
```

Current implementation:

- `internal/r1skill/wizard/wizard.go` builds a minimal skill IR and `SkillAuthoringDecisions` session.
- `internal/r1skill/wizard/adapter/` supports `r1-markdown-legacy`, `openapi`, `zapier`, and `codex-toml`.
- `internal/r1skill/interp/nodes/ask_user.go` adds the operator/headless primitive with constitution-forced interactivity support.
- `internal/r1skill/wizard/ledgerlink/nodes.go` registers `skill_authoring_decisions` as a ledger node type.
- `internal/r1skill/wizard/ledgerlink/writer.go` persists source, IR, proof, and session refs into the ledger graph.

Outputs written by `r1 wizard run`:

- `<skill-id>.r1.json`
- `<skill-id>.proof.json`
- `<skill-id>.decisions.json`
- optional `skill_authoring_decisions` ledger node plus referenced source / IR / proof artifacts when `--ledger-dir` is supplied

`r1 wizard register` installs reviewed outputs under `skills/<skill-id>/skill.r1.json` and `skills/<skill-id>/skill.r1.proof.json`, which makes the deterministic registry load path explicit and repeatable.

This is the operator on-ramp for the deterministic-skill substrate added in PR `#34`.
