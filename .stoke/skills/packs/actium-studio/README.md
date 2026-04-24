# Actium Studio skill pack (seed)

R1 skill pack that exposes Actium Studio capabilities (site scaffolding,
page editing, publishing, snapshots, staging) as first-class R1 skills.

This directory is the SEED from work-r1-actium-studio-skills.md phase
R1S-1.5. It ships 5 of the 6 hero skills with hand-authored manifests so
the pack-load path, validation gates, and registry integration can be
exercised end-to-end. The remaining hero (`studio.list_templates`) plus
47 thin 1:1 wrappers land in phases R1S-4.1 and R1S-4.2.

## Layout

```
packs/actium-studio/
  pack.yaml                        -- pack metadata (name, version, skill_count, deps)
  README.md                        -- this file
  studio.scaffold_site/
    manifest.json                  -- skillmfr.Manifest JSON (loadable + validated)
    SKILL.md                       -- optional operator prose (unparsed by loader)
  studio.update_content/
    ...
  studio.publish/
    ...
  studio.diff_versions/
    ...
  studio.site_status/
    ...
```

`manifest.json` is the authoritative source of truth for the pack loader
(`internal/skillmfr/pack.go` → `LoadPack`). `SKILL.md` is a human-
readable companion; the loader ignores it. The choice of JSON for the
manifest is pragmatic: `skillmfr.Manifest` already round-trips through
`encoding/json`, and JSON Schema fields (`inputSchema`, `outputSchema`)
are awkward inside YAML frontmatter.

## Adding a skill to this pack

1. Pick a name in the `studio.*` namespace (e.g. `studio.list_pages`).
2. Create `<skill_name>/manifest.json` conforming to
   `skillmfr.Manifest` (JSON keys: `name`, `version`, `description`,
   `inputSchema`, `outputSchema`, `whenToUse`, `whenNotToUse`,
   `behaviorFlags`, and optional `recommendedFor`).
3. Validation floors enforced by `skillmfr.Manifest.Validate()` (same
   rules as every other R1 manifest):
   - Name, Version, Description all non-empty.
   - InputSchema and OutputSchema each valid non-empty JSON (not
     `null`, not arbitrary bytes).
   - At least 1 entry in `whenToUse`.
   - At least 2 entries in `whenNotToUse`.
4. Run `go test ./internal/skill/...` — the pack test loads every
   skill manifest in this directory and asserts it parses + validates.
5. Bump `skill_count` in `pack.yaml` to match.
6. For destructive or billing-affecting skills, set
   `behaviorFlags.mutatesState: true` and note the opt-in gate in the
   skill's `whenNotToUse` field.

## Install (current)

`.stoke/skills/packs/actium-studio/` is discovered by
`skillmfr.LoadPack` today. The `r1 skills pack install` CLI (phase
R1S-1.4) will symlink this pack into the active skills directory. For
the rename window, `.r1/skills/packs/actium-studio/` is the canonical
post-rename path; the dual-resolver from work-r1-rename.md S1-5 reads
both.

## Related

- Work order: `/home/eric/repos/plans/work-orders/work-r1-actium-studio-skills.md`
- Manifest schema: `/home/eric/repos/stoke/internal/skillmfr/manifest.go`
- Pack loader: `/home/eric/repos/stoke/internal/skillmfr/pack.go`
