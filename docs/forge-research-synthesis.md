# Forge Research Synthesis: Findings That Change the Architecture

12 deep research documents reviewed. Key findings organized by impact.

---

## ARCHITECTURE-CHANGING FINDINGS

### 1. "Forge" is DEAD as a name

Five active AI coding products already use it. ForgeCode (5,200 stars, CLI command is literally `forge`), forge-agents (universal CLI for coding agents), FORGE (forge-dev.com, patent-pending), ForgeAI (PyPI), and Mistral Forge (launched March 17, 2026 at NVIDIA GTC). Every domain (forge.dev, forge.ai, forge.sh) is taken. Every package registry has conflicts. Atlassian Forge has 25+ npm packages.

**Top alternatives validated:**
- **Smelt** -- "refining ore into pure metal." `smelt build`, `smelt refine`. Low conflict.
- **Stoke** -- "stoke the forge." Cleanest namespace of any candidate. Zero conflicts found.
- **Cadre** -- "elite specialist group." Best multi-agent metaphor. `cadre deploy`, `cadre build`.

**Decision needed before any public code/repo.**

### 2. macOS Keychain: BROKEN for multi-account pools

Claude Code on macOS stores OAuth under a fixed Keychain service name. GitHub issue #20553 (still open, no Anthropic response) reports that all CLAUDE_CONFIG_DIR profiles share the single `Claude Code-credentials` entry. Profile B's login overwrites Profile A's tokens.

**There IS evidence of SHA256-based scoping** (`Claude Code-credentials-{SHA256(config_dir)[:8]}`) in newer versions -- community plugins discovered it, and a v2.1.81 changelog fix references "concurrent sessions." BUT it's undocumented, unconfirmed by Anthropic, and has a known open bug.

**Recommended: run pool slots in Linux containers.** This eliminates the Keychain entirely. File-based credential isolation via CLAUDE_CONFIG_DIR works perfectly on Linux. Anthropic provides an official devcontainer reference. ~100-200MB overhead per container is negligible.

**Fallback: verify SHA256 scoping on your version** with `security dump-keychain -a | grep "Claude Code-credentials"`. If hash-suffixed entries appear, native macOS isolation MAY work.

### 3. Sandbox is configured via settings.json, NOT CLI flags

There is no stable `--sandbox` CLI flag. The feature request for `--sandbox <mode>` (GitHub #7097) remains unimplemented. Configure entirely through `.claude/settings.json` in each worktree.

Key settings confirmed:
```json
{
  "sandbox": {
    "enabled": true,
    "failIfUnavailable": true,
    "autoAllowBashIfSandboxed": true,
    "allowUnsandboxedCommands": false
  }
}
```

**Platform details:**
- macOS: Seatbelt (`sandbox-exec`), kernel-level network enforcement. Works out of box.
- Linux: bubblewrap + socat + seccomp BPF. Must install `bubblewrap socat`.
- **Claude Code does NOT use Landlock** (that's Codex CLI). Common confusion.
- Linux proxy bypass: programs ignoring HTTP_PROXY env vars can escape network restrictions.
- Beta/research preview feature, but available on ALL plan tiers.

**Critical for spec:** Sandbox settings go in the per-worktree settings.json that Forge writes, not in CLI flags on the `claude -p` command line.

### 4. MCP isolation: --strict-mcp-config works, but plugins bypass it

Confirmed: `--strict-mcp-config --mcp-config '{}'` suppresses user, local, project, and cloud MCP sources. Both `{}` and `{"mcpServers": {}}` work for zero servers.

**CRITICAL GAP: plugins bypass --strict-mcp-config entirely.** No `--no-plugins` flag exists. The `--bare` flag suppresses plugins but requires ANTHROPIC_API_KEY (disables OAuth).

**The definitive zero-MCP recipe for Mode 1 (subscription auth):**
```bash
CLAUDE_CONFIG_DIR="/tmp/claude-clean-$$" \
claude \
  --strict-mcp-config \
  --mcp-config '{}' \
  --disallowedTools "mcp__*" \
  -p "prompt"
```
Using a clean CLAUDE_CONFIG_DIR ensures no plugins are configured. The `--disallowedTools "mcp__*"` is belt-and-suspenders -- removes MCP tools from model context even if one somehow loads.

**For Mode 2 (API key):** add `--bare` for maximum isolation.

**Known bug #14490:** if a server name appears in `disabledMcpServers` in ~/.claude.json, passing it via --strict-mcp-config won't re-enable it. Irrelevant for the zero-server case.

### 5. Undocumented OAuth usage endpoint solves rate limit tracking

**This is the biggest discovery.** An undocumented endpoint returns utilization percentage:

```
GET https://api.anthropic.com/api/oauth/usage
Authorization: Bearer sk-ant-oat01-...
anthropic-beta: oauth-2025-04-20
```

Response:
```json
{
  "five_hour": {"utilization": 42.0, "resets_at": "2026-03-29T15:00:00Z"},
  "seven_day": {"utilization": 35.0, "resets_at": "2026-04-02T00:00:00Z"}
}
```

**No quota consumed. Server-authoritative. Pollable every 30-60 seconds.** This is the pool rotation signal. OAuth token retrievable from macOS Keychain via `security find-generic-password -s "Claude Code-credentials" -w` or from `~/.claude/.credentials.json` on Linux.

Existing tool `pi-multi-pass` (195 stars) already implements this for multi-subscription rotation.

### 6. Headless mode has 14 known bugs your parser must handle

The stream-json format has **6 event types**: system, assistant, user, result, stream_event, and the undocumented `rate_limit_event` (crashes parsers that don't expect it).

**Critical bugs:**
- Process hangs after result event (#25629) -- need 30s post-result timeout
- Missing result event (#1920, #8126) -- need session-level timeout
- Exit code 0 on rate limit failure (#15685) -- never trust exit codes
- Orphaned subprocess accumulation (#33979) -- "top-5 reported bug category"
- stdout block-buffering on macOS when piped (#25670)
- JSON stream corruption from sandbox debug messages on Linux (#12007)
- OAuth tokens expire in headless mode and don't refresh (#28827)
- Headless sessions randomly receive SIGTERM after 3-10 minutes (#29642)
- SIGINT/SIGTERM kills immediately with no graceful shutdown (#29096)

**Required: three-tier timeout strategy** -- stream idle (90s), post-result exit (30s), global session timeout. Spawn in process groups. Always validate JSON before parsing.

---

## CONFIRMED (spec is correct)

### Codex credential isolation: WORKS perfectly

`cli_auth_credentials_store` defaults to `"file"`. CODEX_HOME fully isolates auth.json. Separate directories = separate credentials = zero cross-contamination. Keyring entries also scoped by CODEX_HOME path.

**One catch:** OAuth refresh token rotation means concurrent processes sharing the same credentials will race (#10332). Solution: separate CODEX_HOME per instance (which we already planned).

Setup:
```bash
mkdir -p /pool/codex-1 /pool/codex-2
cat > /pool/codex-1/config.toml << 'EOF'
cli_auth_credentials_store = "file"
EOF
CODEX_HOME=/pool/codex-1 codex login
```

### Agent SDK is a CLI subprocess wrapper

The SDK spawns the Claude Code CLI as a subprocess, communicating over JSON-lines via stdin/stdout. Same wire protocol as our `claude -p` approach. ~300MB per session. No official Go SDK exists (8+ community implementations).

**Key insight for ExecutionEngine interface:** The SDK adds `tools`, `allowedTools`, `disallowedTools`, `strictMcpConfig`, `sandbox: SandboxSettings`, and `canUseTool` callback -- all features we can access via CLI flags + settings.json. Migration would be architectural (moving from exec.Command to SDK client), not protocol-level.

**Design now:** Build an `AgentSession` abstraction with CLIBackend and future SDKBackend. The message types are identical.

### Task scheduling: RCPSP with event-driven dispatch

The research confirms:
- Tree-sitter + PageRank for file prediction (Aider's approach, 130+ languages)
- GRPW priority queue for scheduling (best general RCPSP heuristic)
- `git merge-tree --write-tree` for zero-side-effect conflict validation (Git 2.38+)
- Event-driven reactive dispatch, not static scheduling (AI task durations too uncertain)
- OCC (optimistic concurrency control) pattern from databases maps to worktree merges

**go-git does NOT support merge operations.** Use `git merge-tree` via os/exec or git2go.

### Benchmarking: 50 tasks minimum, weekend protocol

- 20 tasks gives ~45% statistical power (coin flip). 50 tasks gives ~80% (credible).
- Run each task 3-5x per agent for mixed-effects regression.
- McNemar's test on discordant pairs for paired binary outcomes.
- SWE-bench Pro (1,865 tasks, multi-language) has replaced Verified as the standard.
- The CCA paper confirms: cheaper model + better scaffold beats expensive model + basic scaffold.

---

## SPEC UPDATES REQUIRED

### 1. Rename the project (blocking)
"Forge" cannot ship. Pick from: Smelt, Stoke, Cadre. Domain/registry verification needed.

### 2. Pool architecture: Linux containers for Claude, not native macOS
The spec assumed CLAUDE_CONFIG_DIR isolates on macOS. It doesn't reliably. Change:
- Pool slots run in Docker/Colima containers with file-based credentials
- OR: verify SHA256 Keychain scoping on latest version as a lighter option
- Codex pools: native macOS is fine (file-based default, CODEX_HOME isolates)

### 3. Sandbox via settings.json, not CLI flags
The spec's policy layer assumed sandbox CLI flags. Fix: Forge writes sandbox config into each worktree's `.claude/settings.json`. The `--sandbox` flag doesn't exist.

### 4. MCP isolation needs --disallowedTools "mcp__*" belt-and-suspenders
The spec assumed --strict-mcp-config alone was sufficient. Plugins bypass it. Add:
- Clean CLAUDE_CONFIG_DIR (no plugins configured)
- `--disallowedTools "mcp__*"` as fallback
- `--bare` for Mode 2 (API key) phases

### 5. Headless parser must handle rate_limit_event and 14 bugs
The spec assumed clean stream parsing. Reality: need JSON validation per line, undocumented event types, three-tier timeouts, process group management, orphan cleanup.

### 6. Rate limit tracking via undocumented OAuth endpoint
The spec's risk section said "Claude Code doesn't expose utilization cleanly." The research found an exact endpoint returning utilization percentage. Integrate into pool scheduler.

### 7. Stream idle timeout: set CLAUDE_STREAM_IDLE_TIMEOUT_MS
Default 90 seconds. The spec didn't mention this environment variable.

### 8. OAuth tokens expire in headless mode without refresh
GitHub #28827: OAuth access tokens expire after ~10-15 minutes and are NOT refreshed in non-interactive mode. This is a critical issue for long-running pool sessions. Mitigation: keep sessions under 10 minutes (which our per-task worktree approach already does).

---

## IMPLEMENTATION PRIORITY (revised)

### Week 1 (spikes are now answered -- go straight to building)

All four spikes have answers. No need to spend time validating:

- **macOS Keychain:** Use Linux containers (or verify SHA256 on day 1)
- **Codex credentials:** `cli_auth_credentials_store = "file"` + CODEX_HOME. Done.
- **Sandbox:** settings.json config, not CLI flag. Schema documented.
- **MCP:** `--strict-mcp-config --mcp-config '{}' --disallowedTools "mcp__*"` + clean config dir.

**Start building immediately:**
1. Name decision (30 minutes, do it now)
2. Workflow engine (phase machine)
3. Subscription manager (poll OAuth usage endpoint for Claude, file-based for Codex)
4. Stream parser (6 event types, 3-tier timeouts, process group management)
5. Worktree lifecycle

### Week 2
1. Policy engine (write sandbox + permissions into per-worktree settings.json)
2. Model router + cross-model verification
3. Context manager
4. Feedback loop (failure analysis, retry briefs)

### Week 3-4
1. TUI (Focus/Dashboard/Detail)
2. Session learning
3. Task scheduler (GRPW priority, file-scope conflict detection)
4. Benchmarking setup

---

## COST: What we now know is possible

The undocumented OAuth usage endpoint + Linux container pool slots means:
- 7 Claude Max 20x subs: each polled every 30s for utilization %
- Auto-rotate at 80% utilization to freshest pool
- Per-task cost tracked from headless JSON `total_cost_usd` field
- Subscription savings: track cumulative `total_cost_usd` vs equivalent API pricing
- The pi-multi-pass project (195 stars) already proves this works at scale
