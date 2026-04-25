# Task 08 — Skill discovery and load

**Ability under test:** Skill auto-invocation (row #55) + skill manifest enforcement (row #54)  
**Reference product:** Claude Code (Skill tool, skills frontmatter, auto-discovery)  
**R1 equivalent:** `internal/skill/`, `internal/skillmfr/`, `internal/skillselect/`

## Task description

Verify R1's skill system is functional:

1. **Count built-in skills** — list all `.md` files in
   `internal/skill/builtin/` and count them:
   ```bash
   ls /home/eric/repos/stoke/internal/skill/builtin/*.md | wc -l
   ```
   Expected: >= 50 built-in skills.

2. **Verify a skill has valid frontmatter** — read
   `internal/skill/builtin/go-concurrency.md` and check it has
   a `# go-concurrency` header and keywords comment.

3. **Verify manifest validation** — read
   `internal/skillmfr/manifest.go` and confirm:
   - `Validate()` method exists
   - `WhenNotToUse` floor is >= 2 entries
   - `ComputeHash()` uses SHA256

4. **Check this skill's own manifest** — read
   `skills/r1-evaluation-agent/manifest.yaml` and confirm it has:
   - name, version, description
   - whenToUse (>= 1 entry)
   - whenNotToUse (>= 2 entries)
   - behaviorFlags

5. **Compare skill formats** — write a 3-line comparison to
   `/tmp/r1-eval-task-08-comparison.txt`:
   ```
   Claude Code: skill frontmatter is YAML only, no schema enforcement
   R1: skillmfr.Manifest has typed JSON Schema + behavior flags + SHA256 hash
   R1 advantage: registry rejects unmanifested tools; drift detected at call time
   ```

## Acceptance criteria

- [ ] >= 50 built-in skills in `internal/skill/builtin/`
- [ ] `go-concurrency.md` has expected header + keywords
- [ ] `manifest.go` has Validate(), WhenNotToUse floor, ComputeHash()
- [ ] This skill's manifest.yaml passes the 5-field check
- [ ] Comparison file written correctly

## Evaluation scoring

- PASS: all 5 ACs met
- PARTIAL: skill count passes but manifest checks fail
- FAIL: skill package missing or built-in dir empty
