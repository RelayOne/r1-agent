# 11-Layer Policy Engine

Stoke enforces security through 11 layers, each independently testable. A task
must pass all layers; no single layer grants a blanket exception.

## Layer Map

| Layer | Enforcement | Package | Test File | Description |
|-------|------------|---------|-----------|-------------|
| 1 | `--tools` flag | `internal/config` | `config/settings_test.go` | Hard builtin tool restriction per phase |
| 2 | MCP isolation | `internal/config` | `config/settings_test.go` | `--strict-mcp-config` + empty config + `mcp__*` deny |
| 3 | `--disallowedTools` | `internal/config` | `config/settings_test.go` | Phase-specific tool deny lists |
| 4 | `--allowedTools` | `internal/config` | `config/settings_test.go` | Auto-approve list (does not grant access) |
| 5 | `settings.json` | `internal/config` | `config/settings_test.go` | Per-worktree sandbox, permissions, apiKeyHelper: null |
| 6 | Worktree isolation | `internal/worktree` | `worktree/worktree_test.go` | Each task runs in its own git worktree |
| 7 | Sandbox | `internal/config` | `config/settings_test.go` | `sandbox.failIfUnavailable: true`, restricted FS/network |
| 8 | `--max-turns` | `internal/engine` | `engine/claude_test.go` | Execution turn limit per phase |
| 9 | Enforcer hooks | `internal/hooks` | `hooks/hooks_test.go` | PreToolUse (block) + PostToolUse (detect) scripts |
| 10 | Verify pipeline | `internal/verify` | `verify/pipeline_test.go` | Build/test/lint + protected files + scope check |
| 11 | Git ownership | `internal/scan` | `scan/scan_test.go` | 18+ deterministic rules: secrets, eval, injection, debug |

## Layer Details

### Layer 1: `--tools` (Builtin Tool Restriction)

Each phase declares which Claude Code tools are allowed:
- **Plan phase**: Read, Glob, Grep, Bash (read-only commands)
- **Execute phase**: Read, Write, Edit, Bash, Glob, Grep
- **Verify phase**: Read, Glob, Grep, Bash (read-only commands)

Passed as `--tools <comma-list>` to the Claude Code CLI.

### Layer 2: MCP Isolation

When `mcp_enabled: false` (plan and verify phases):
1. `--strict-mcp-config` rejects any MCP server not in config
2. `--mcp-config <empty.json>` provides an empty server list
3. `--disallowedTools mcp__*` blocks all MCP tool invocations

### Layer 3-4: Tool Allow/Deny Lists

Phase-specific tool rules from `stoke.policy.yaml`:
- `denied_rules`: Patterns that are always blocked (e.g., `Bash(curl *)` in execute)
- `allowed_rules`: Patterns that auto-approve without user confirmation

### Layer 5: settings.json

Generated per worktree with:
- `sandbox.enabled: true`
- `sandbox.failIfUnavailable: true`
- `apiKeyHelper: null` (suppresses repo helpers in Mode 1)
- `filesystem.allowWrite` scoped to the worktree path
- `network.allowedDomains` from policy config

### Layer 6: Worktree Isolation

Each task gets its own git worktree. Changes are isolated until merge.
Merge uses `git merge-tree --write-tree` for conflict validation before
actual merge. A mutex serializes merges to main.

### Layer 7: Sandbox

Fail-closed: if sandboxing is unavailable on the platform, the task fails
rather than running unsandboxed. Filesystem writes are restricted to the
worktree directory.

### Layer 8: --max-turns

Each phase has a configurable maximum turn limit. Claude Code stops after
this many tool-use turns. Prevents infinite loops and runaway cost.

### Layer 9: Enforcer Hooks

`hooks.Install()` writes PreToolUse and PostToolUse scripts into each worktree.

**PreToolUse blocks:**
- Protected file mutations (`.claude/`, `.stoke/`, `CLAUDE.md`, `.env*`)
- Git destruction (`git stash`, `git reset --hard`, `git push`, `git commit --no-verify`)
- Nested AI sessions (`claude`, `codex` invocations)
- Destructive commands (`rm -rf /`, `sudo rm`, `chmod -R 777`)
- RCE patterns (`curl | bash`, `wget | sh`)
- Hook tampering (removing/modifying Stoke hooks)

**PostToolUse detects:**
- Type bypass patterns (`@ts-ignore`, `as any`, `eslint-disable`)
- Secret leaks in output (API key patterns)

### Layer 10: Verify Pipeline

After execution, before merge:
1. Run build command
2. Run test command
3. Run lint command
4. Check protected files weren't modified
5. Check file scope (agent only modified declared files)

### Layer 11: Deterministic Scan

18+ rules across 5 categories:
- **Type/lint bypasses**: `@ts-ignore`, `as any`, `eslint-disable`, `noqa`, etc.
- **Debug artifacts**: `console.log`, `fmt.Println`, `print()`, `dbg!`
- **Test artifacts**: `.only()`, `.skip()`, TODO/FIXME markers
- **Security**: Hardcoded secrets, `eval()`, `innerHTML`
- **Code quality**: Error assigned to `_` (blank identifier)

## Testing Strategy

Each layer has both positive (green path) and negative (red path) tests.
See `internal/hooks/hooks_test.go`, `internal/scan/scan_test.go`,
`internal/rbac/rbac_test.go`, `internal/verify/pipeline_test.go`, and
`internal/config/policy_test.go` for the full test suites.

Run the self-scan to verify Stoke's own source is clean:
```bash
go test ./internal/scan/ -run TestSelfScan
```
