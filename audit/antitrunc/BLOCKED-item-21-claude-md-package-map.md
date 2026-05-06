# BLOCKED — item 21 (CLAUDE.md package map update)

## Status

STATUS: BLOCKED (file-permission policy)

## Spec checklist line

> 21. [ ] Update CLAUDE.md package map: add `antitrunc/  Anti-truncation enforcement (layered defense against scope self-reduction)`.

## Why blocked

The Claude Code harness denies write access to the project's
`CLAUDE.md` via both:

- Edit tool — returns: "File is in a directory that is denied by
  your permission settings."
- Bash heredoc / sed / tee — guarded by
  `.claude/hooks/guard-bash-writes.sh` which blocks any pattern
  matching `\.claude/(hooks|settings)|CLAUDE\.md|\.mcp\.json`.

Both guards are by design: CLAUDE.md is part of the agent harness
configuration and must be edited out-of-band by the operator.

## Operator action required

Insert the following line in `CLAUDE.md` under the
`--- AGENT BEHAVIOR ---` section:

```
antitrunc/                         Anti-truncation enforcement (layered defense against scope self-reduction)
```

Suggested anchor: directly after the `handoff/` line (alphabetical
within the section).

## Verification

After the line is added, the `r1 antitrunc verify` CLI test suite
in `cmd/r1/antitrunc_cmd_test.go` and the documentation in
`docs/ANTI-TRUNCATION.md` will all be in agreement with the package
map.

## Related items

The same content is reflected in:

- `docs/ANTI-TRUNCATION.md` (item 20)
- `docs/FEATURE-MAP.md` (item 22)
- `docs/ARCHITECTURE.md` (item 22)
- root `README.md` (item 22)
