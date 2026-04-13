# Enforcer — Quick Reference

## Install
```bash
bash setup.sh
# Edit CLAUDE.md: build/test/lint commands + architecture
git add -A && git commit -m "enforcer"
./runclaude          # First run: logs in, saves auth to .claude-config/
```

## Daily usage
```bash
./runclaude          # Opens Claude Code with hooks enforced (--yolo for skip-permissions)
/scope               # Figure out what to build
/build               # Build it
```

## Build new
```
/scope                       # Figure out what to build (interactive + research + specs)
/build                       # Build everything that's scoped and ready
```

## Or use individual commands directly
```
/research-spec my-feature    # Full research loop → spec
/write-spec my-feature       # Quick interview → spec (no research)
/format-spec docs/scope.md   # Convert existing doc into proper spec
/review-spec specs/X.md      # Audit spec against 10 failure modes
/build-from-spec specs/X.md  # Execute a specific spec file
```

## Fix existing
```
/fix-and-complete
```

## Full audit
```
/quick-scan                  # Deterministic only, free, fast
/scan-and-repair             # Full: map → grep → subagents → fix → test remediation
/test-remediation            # Standalone: audit tests → fix fakes → add coverage
```

## Session management
```
/handoff          # Save state (auto-loaded next session)
/resume           # Continue after rate limit or account switch
/catchup          # Rebuild context from git after /clear
```

## Rate limit recovery
If you hit a rate limit mid-work:
1. All progress is auto-committed (one commit per completed task)
2. Plan files track which items are done (checkboxes)
3. Options: wait, switch to a different repo's account, or use API key
4. To switch: cd to another repo that uses a different account, `git clone` the work repo there, `./runclaude`, then `/resume`
5. Or: `export ANTHROPIC_API_KEY=sk-...` then `./runclaude` — API key overrides OAuth
6. NEVER restart from scratch — all state is in git + plan files

## Test mode
```bash
touch .claude/test-mode-active   # Unlock
rm .claude/test-mode-active      # Lock
```

## Key architecture
- Rules live in task checkboxes, not CLAUDE.md
- ONE subagent per task (build), ONE per pattern per section (scan), ONE per fix (repair), ONE per test (test remediation)
- Supervisor verifies each subagent against its own checklist
- Hooks enforce: test locking, stub rejection, completion verification
- Test remediation is a first-class phase, not a side effect of code fixes
- 20 semantic scan patterns including 5 test-specific (fake tests, weak assertions, coverage gaps, missing error tests, missing integration tests)
