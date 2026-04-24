# R1 Operator Guide

## Auth Modes

R1 supports two authentication modes for Claude Code and Codex CLI.

### Mode 1: Subscription (default)

Uses OAuth-based subscription accounts. Each agent runs inside an isolated `CLAUDE_CONFIG_DIR` or `CODEX_HOME` with its own credentials. API keys are stripped from the environment to prevent accidental leakage.

```bash
stoke build --mode mode1 \
  --claude-config-dir /pool/claude-1 \
  --codex-home /pool/codex-1
```

**What Mode 1 enforces:**
- `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `CODEX_API_KEY` stripped from env
- `AWS_*`, `GOOGLE_*`, `AZURE_*`, `BEDROCK_*`, `VERTEX_*` stripped from env
- `CLAUDE_CODE_USE_BEDROCK`, `CLAUDE_CODE_USE_VERTEX`, `CLAUDE_CODE_USE_FOUNDRY` stripped
- `CLAUDE_CONFIG_DIR` set to the pool's config directory
- `apiKeyHelper: null` in settings.json (prevents repo-supplied helpers from overriding OAuth)
- Only allowlisted env vars pass through: `PATH`, `HOME`, `TERM`, `LANG`, `SHELL`, `TMPDIR`, `USER`, `NODE_PATH`, `XDG_*`, `PWD`, `npm_config_*`, `GIT_*`

### Mode 2: Enterprise / API Key

Uses API keys directly. The full environment is passed through, plus any extra vars you inject.

```bash
export ANTHROPIC_API_KEY=sk-ant-...
stoke build --mode mode2
```

## Pool Setup

Each pool is a directory containing Claude Code or Codex CLI credentials.

### Claude Code pools

```bash
# Create pool directories
mkdir -p /pool/claude-{1,2,3}

# Log in to each pool
CLAUDE_CONFIG_DIR=/pool/claude-1 claude login
CLAUDE_CONFIG_DIR=/pool/claude-2 claude login
CLAUDE_CONFIG_DIR=/pool/claude-3 claude login
```

After login, each directory contains `.credentials.json` with an OAuth token. R1 reads this to poll the usage endpoint.

### Codex CLI pools

```bash
mkdir -p /pool/codex-{1,2}

# Codex defaults to file-based auth when CODEX_HOME is set
CODEX_HOME=/pool/codex-1 codex auth login
CODEX_HOME=/pool/codex-2 codex auth login
```

Codex stores `auth.json` under `CODEX_HOME`. Set `cli_auth_credentials_store` to `"file"` in Codex config if your system defaults to keyring.

### Pool utilization

```bash
stoke pool --claude-config-dir /pool/claude-1
```

Output:
```
⚡ STOKE pool

  5-hour:         ████████░░░░░░░░░░░░ 40%  resets in 2h30m
  7-day:          ██░░░░░░░░░░░░░░░░░░ 10%  resets in 5d12h
```

## Container Pool Runtime (recommended on macOS)

**Claude Code on macOS uses the system Keychain by default.** This breaks multi-pool isolation because all pools share the same Keychain entry. The container runtime solves this by running each pool inside an isolated Docker container with file-based credentials.

### Setup

```bash
# Pull the stoke-pool image (bundles Claude Code, Codex CLI, Node.js, Git)
docker pull ghcr.io/ericmacdougall/stoke-pool:latest

# Initialize container pools (runs interactive login inside the container)
stoke pool init --container --name "Claude Max Account 1"
stoke pool init --container --name "Claude Max Account 2"

# List all pools (shows runtime type)
stoke pools

# Remove a container pool (also removes the Docker volume)
stoke remove-pool claude-3
```

Each `stoke pool init --container` invocation:
1. Creates a Docker volume for isolated credential storage
2. Runs `claude login` inside a temporary container with the volume mounted
3. Registers the pool with `runtime: container` in the manifest

### How it works

When R1 dispatches a task to a container pool, the engine wraps the CLI command in `docker run`:

```
docker run --rm \
  -v stoke-pool-claude-1:/config \
  -v /path/to/worktree:/path/to/worktree \
  -e CLAUDE_CONFIG_DIR=/config \
  ghcr.io/ericmacdougall/stoke-pool:latest \
  claude -p "..." --output-format stream-json ...
```

Each container gets its own credential volume, so OAuth tokens are fully isolated.

### Mixing host and container pools

You can mix host-direct and container pools. R1's pool manager treats them identically for scheduling; the only difference is how the CLI process is spawned.

### Host-direct pools (opt-in on macOS)

If you prefer running directly on the host (e.g., on Linux where Keychain isn't an issue):

```bash
stoke add-claude    # runs claude login directly
stoke add-codex     # runs codex auth login directly
```

### Codex on macOS (host-direct workaround)

Codex supports `cli_auth_credentials_store: "file"` in its config, which bypasses the keyring. If you must run Codex on the host on macOS:

```bash
CODEX_HOME=/pool/codex-1 codex config set cli_auth_credentials_store file
CODEX_HOME=/pool/codex-1 codex auth login
```

## MCP Isolation

When a phase has `mcp_enabled: false` (plan and verify by default), R1 applies triple isolation:

1. `--strict-mcp-config` -- reject any MCP server not in the config
2. `--mcp-config <empty.json>` -- config contains zero servers
3. `--disallowedTools mcp__*` -- block all MCP tool calls

This prevents plugin-provided MCP servers from leaking into read-only phases.

## Sandbox Configuration

R1 generates a `.claude/settings.json` in each worktree with:

```json
{
  "sandbox": {
    "enabled": true,
    "failIfUnavailable": true,
    "allowUnsandboxedCommands": false,
    "filesystem": {
      "allowWrite": ["/path/to/worktree"],
      "allowRead": ["/path/to/worktree"]
    },
    "network": {
      "allowedDomains": ["github.com", "*.npmjs.org"]
    }
  }
}
```

`failIfUnavailable: true` means the agent fails hard if sandboxing is unavailable, rather than running unsandboxed. This is the correct security boundary.

## Troubleshooting

### Claude/Codex not found

```
[missing] claude: exec: "claude": executable file not found in $PATH
```

Install Claude Code: `npm install -g @anthropic-ai/claude-code`
Install Codex CLI: `npm install -g @openai/codex`

Or pass explicit paths: `--claude-bin /usr/local/bin/claude`

### Rate limit exhaustion

```
✗ [TASK-3] rate limited during execute phase
```

R1 rotates pools when one is rate-limited. If all pools are exhausted:
1. Check `stoke pool` for utilization
2. Wait for the 5-hour window to reset
3. Add more pool slots

The circuit breaker trips after 3 consecutive rate limits on the same pool, backing off for 5 minutes.

### Stuck worktrees

If a build crashes mid-run:

```bash
# Check for orphaned worktrees
git worktree list

# Clean up manually
git worktree remove --force .stoke/worktrees/<name>
git worktree prune
git branch -D stoke/<name>
```

Or just run `stoke build` again -- it resumes from the last saved state.

### Merge conflicts

```
merge conflict detected: ...
```

This means two parallel tasks modified the same file. Fix: declare `files` in your plan so the scheduler prevents file-scope conflicts.

### Auth isolation failures

If you see `ANTHROPIC_API_KEY` leaking into Mode 1 runs:
1. Verify `--mode mode1` is set
2. Check that `CLAUDE_CONFIG_DIR` points to a valid pool
3. Run `stoke doctor` to verify tool availability

### Session state corruption

```bash
# Clear session state
rm -rf .stoke/session.json .stoke/history/

# Or check status
stoke status
```

## CI/CD Integration

### GitHub Actions

```yaml
- name: Run R1
  run: |
    stoke build --plan stoke-plan.json --workers 2 --mode mode2
    cat .stoke/reports/latest.json

- name: Check results
  run: |
    jq '.success' .stoke/reports/latest.json | grep true
```

### Headless mode

R1's headless TUI runner works in any terminal without special requirements. All output goes to stdout/stderr as structured text.

```bash
stoke build --plan stoke-plan.json 2>&1 | tee build.log
```

The structured report at `.stoke/reports/latest.json` is the primary CI artifact.

## Build Flags Reference

### --roi (ROI filter)

Filter out low-value tasks before execution. Default: `medium`.

```bash
stoke build --plan stoke-plan.json --roi high    # only security/correctness
stoke build --plan stoke-plan.json --roi medium   # default: skip formatting only
stoke build --plan stoke-plan.json --roi skip     # run everything
```

Tasks are classified as high (security, correctness, tests), medium (refactoring, features, types), low (docs, cosmetic renames), or skip (formatting). The filter removes tasks below the threshold.

### --sqlite (SQLite session store)

Use SQLite instead of JSON files for session persistence. Better for large plans and concurrent access.

```bash
stoke build --plan stoke-plan.json --sqlite
```

Creates `.stoke/session.db` with WAL mode and 5s busy timeout. Both stores implement the same `SessionStore` interface, so crash recovery and learned patterns work identically.

### --interactive (Bubble Tea TUI)

Launch an interactive terminal UI instead of headless text output.

```bash
stoke build --plan stoke-plan.json --interactive
```

Three modes: Dashboard (task board + pool status), Focus (follow one agent's streaming output), Detail (drill into completed/failed task). Keyboard-driven: Tab switches modes, 1-9 focuses on agents, Enter drills down, q quits.
