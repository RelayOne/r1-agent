# Markdown To Deterministic Migration

Use the wizard when migrating legacy markdown skills:

```bash
stoke wizard run \
  --from ./legacy/check-pr-coverage.md \
  --source-format r1-markdown-legacy \
  --mode headless \
  --out-dir ./converted
```

What happens:

1. The markdown adapter extracts a description from the source body.
2. The wizard builds deterministic IR and a `SkillAuthoringDecisions` ledger document.
3. The analyzer emits a compile proof.
4. The command writes the IR, proof, and decisions JSON to the output directory.

For bulk migration:

```bash
stoke wizard migrate --source-dir ./legacy --source-format r1-markdown-legacy --output-dir ./converted
```
