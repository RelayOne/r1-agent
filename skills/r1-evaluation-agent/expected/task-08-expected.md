# Expected Output — Task 08 (Skill discovery and load)

## Pass criteria (pinned assertions)

### Built-in skill count
- `ls internal/skill/builtin/*.md | wc -l` returns >= 50
- All files are .md skill files

### go-concurrency.md contents
- First line is `# go-concurrency` header
- Contains `<!-- keywords:` comment with goroutine-related keywords

### manifest.go inspection
- `Validate()` method exists in the file
- `WhenNotToUse` has a comment/check for >= 2 entries
- `ComputeHash()` calls `sha256.Sum256`

### This skill's manifest.yaml
- name: r1-evaluation-agent (or r1-evaluation-agent)
- version is present and non-empty
- description is present and non-empty
- whenToUse has >= 1 item
- whenNotToUse has >= 2 items (including quota warning + frequency warning)
- behaviorFlags present

### Comparison file
- 3 lines as specified
- Accurately represents manifest enforcement difference

## Allowed variance

- Built-in skill count may grow; floor of 50 is the invariant.
- manifest.yaml field order may vary.

## Failure indicators

- Skill count < 50 (regression in builtin skills)
- ComputeHash not using SHA256
- This skill's manifest.yaml fails any of the 5-field checks
