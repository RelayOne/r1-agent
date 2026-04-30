# Skill Wizard

The deterministic skill wizard converts source artifacts into canonical `*.r1.json` skills plus compile proofs and a decision ledger.

Primary commands:

```bash
stoke wizard run --from ./legacy-skill.md --source-format r1-markdown-legacy --mode headless --out-dir ./out
stoke wizard migrate --source-dir ./old-skills --source-format codex-toml --output-dir ./out --mode headless
stoke wizard query --decisions ./out/my-skill.decisions.json --question-prefix caps.
```

Current implementation:

- `internal/r1skill/wizard/wizard.go` builds a minimal skill IR and `SkillAuthoringDecisions` session.
- `internal/r1skill/wizard/adapter/` supports `r1-markdown-legacy`, `openapi`, `zapier`, and `codex-toml`.
- `internal/r1skill/interp/nodes/ask_user.go` adds the operator/headless primitive with constitution-forced interactivity support.
- `internal/r1skill/wizard/ledgerlink/nodes.go` registers `skill_authoring_decisions` as a ledger node type.

Outputs written by `stoke wizard run`:

- `<skill-id>.r1.json`
- `<skill-id>.proof.json`
- `<skill-id>.decisions.json`

This is the operator on-ramp for the deterministic-skill substrate added in PR `#34`.
