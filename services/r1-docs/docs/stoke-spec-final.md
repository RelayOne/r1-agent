# Forge: Complete Specification

**An AI coding agent orchestrator that drives Claude Code and Codex CLI as execution engines, adds structured workflow enforcement, multi-model routing, intelligent retry with failure analysis, and parallel agent coordination across multiple subscription pools.**

Version: 1.0 Draft
Date: March 29, 2026
Author: Eric (VP Engineering, Good Ventures)

---

## Table of Contents

1. [Thesis & Evidence](#1-thesis--evidence)
2. [Problem Statement](#2-problem-statement)
3. [Prior Art & Research](#3-prior-art--research)
4. [Architecture Overview](#4-architecture-overview)
5. [OAuth & Subscription Strategy](#5-oauth--subscription-strategy)
6. [Execution Model](#6-execution-model)
7. [Feedback Loop & Intelligent Retry](#7-feedback-loop--intelligent-retry)
8. [Context Architecture](#8-context-architecture)
9. [Policy Engine](#9-policy-engine)
10. [Model Routing](#10-model-routing)
11. [TUI Design](#11-tui-design)
12. [Technology Stack](#12-technology-stack)
13. [Implementation Plan](#13-implementation-plan)
14. [Functional Comparison vs Claude Code](#14-functional-comparison-vs-claude-code)
15. [Cost Model](#15-cost-model)
16. [Open Questions & Risks](#16-open-questions--risks)

---

## 1. Thesis & Evidence

**The scaffold accounts for a 22-point swing on SWE-bench. Model swaps account for ~1 point at the frontier. The harness IS the product.**

Evidence:

- SWE-bench Pro: same model, basic SWE-Agent scaffold = 23%. Same model, 250-turn optimized scaffold = 45%+. (Scale AI, standardized testing)
- Six frontier models within 1.3% of each other on SWE-bench Verified (~78-80%): Claude Opus 4.6, GPT-5.4, Gemini 3.1 Pro, MiniMax M2.5, DeepSeek V3.2, Kimi K2.5
- Milvus code review study: single-model detection 53%, multi-model debate 80% (15 real PRs, known bugs)
- OpenAI Harness Engineering paper: 1M lines, 1,500 PRs, 3 engineers, 3.5 PRs/engineer/day. "The primary job of our engineering team became enabling the agents to do useful work."
- OPENDEV paper (arxiv 2603.05344): dual-agent architecture, workload-specialized model routing, adaptive context compaction, automated memory
- Anthropic (Carlini compiler project): 16 parallel Opus agents, 2,000 sessions, 100K-line C compiler. "Most of my effort went into designing the environment around Claude."
- Five independent teams converged on four pillars: context architecture, agent specialization, persistent memory, structured execution
- Performance degrades beyond ~40% context utilization
- Better models make harness engineering MORE important, not less

**Conclusion:** Building a better scaffold is the highest-leverage work in AI coding. The model is a commodity. The harness is the differentiator.

---

## 2. Problem Statement

### What Claude Code does well (as of March 2026)

Claude Code is the most capable single-agent coding tool available. It has:

- **Agent Teams** with shared task lists, direct teammate messaging, task dependencies, file-locking for claiming, and explicit parallel-work practices (`CLAUDE_CODE_TASK_LIST_ID`)
- **Parallel worktree sessions** via `--worktree` (each in its own isolated git worktree under `.claude/worktrees/`)
- **Auto mode** (research preview, requires Team plan) with a Sonnet 4.6 classifier that evaluates each action, replacing the binary choice between approval prompts and bypass-permissions. Agent Teams are experimental and disabled by default.
- **Plugins, skills, hooks, MCP servers, LSP servers** as official extension surfaces. Hooks include command, HTTP, prompt, and MCP-tool types that receive structured event data and can return decisions (not just passive logging)
- **Auto memory** that persists project-specific knowledge across sessions, plus full session resume that restores message history, tool state, and context
- **An agentic loop** described by Anthropic as "gather context, take action, verify results"
- **Managed settings** for enterprise, including server-pushed permission rules and the ability to disable bypass-permissions centrally (though still client-side enforcement)

### What Claude Code still can't do (where Forge differentiates)

**Deterministic phase enforcement.** Claude Code's agentic loop is model-driven: the model decides what to do next. It can skip verification, it can declare a task complete without running tests, it can ignore instructions from 40 turns ago. Forge makes the workflow deterministic: PLAN runs, then EXECUTE runs, then VERIFY runs, then COMMIT runs. The harness controls the sequence. The model is a function called at each phase, not the driver.

**Cross-model execution.** Claude Code uses Claude for everything. There is no mechanism to route architecture tasks to GPT-5.4 (4.8/5 on architecture) while keeping refactoring on Claude (4.9/5). There is no mechanism to have a different model family review every commit (which raises bug detection from 53% to 80% per the Milvus study).

**Intelligent failure recovery.** When a task fails in Claude Code, the user re-prompts. There is no automatic failure classification, no extraction of exact errors from build/test/lint output, no generation of targeted retry instructions, no cross-task learning where patterns from task 3 prevent the same failure in task 12.

**Multi-subscription pool scheduling.** Claude Code uses one subscription per session. With 7 Max subscriptions ($1,400/month total), 6 sit idle while 1 works. There is no automatic pool rotation, utilization tracking, or load balancing across independent rate limit pools.

**Structured verification pipeline.** Claude Code's verify step is model-driven: Claude decides whether to run tests. Forge's verification is deterministic: build MUST pass, tests MUST pass, lint MUST pass, cross-model review MUST return clean. The model cannot skip verification because the harness doesn't call COMMIT until all checks return green.

**Session-level cost and throughput visibility.** No per-task cost tracking, no subscription savings calculation, no rate limit pool visualization across multiple accounts.

**The honest framing:** Forge is not "Claude Code plus missing basics." Forge is a deterministic cross-engine orchestration layer that drives Claude Code (and Codex) as execution engines, adding enforced workflows, multi-model routing, intelligent retry, parallel scheduling, and structured verification that the model-driven approach fundamentally cannot provide.

---

## 3. Prior Art & Research

### Open-source alternatives evaluated

| Project | License | Language | Stars | Verdict |
|---|---|---|---|---|
| OpenCode (anomalyco/opencode) | MIT | Go | 120K+ | Best runtime. Multi-provider, LSP, plugins, TUI, multi-session parallel. But: ReAct loop, no deterministic phases, no cross-model routing |
| Aider | Apache 2.0 | Python | 30K+ | Wrong language, wrong architecture. Interactive pair programmer, not orchestrator |
| Cline | Apache 2.0 | TypeScript | VS Code | IDE-locked, no headless, no parallel |
| OPENDEV | MIT | TypeScript | New | Academic reference architecture. Dual-agent, context compaction, memory. Not production-ready |
| Codex CLI | Proprietary | Rust | N/A | Excellent review/exec. But single-model (GPT), no orchestration |

### Key research papers and findings

**OPENDEV (arxiv 2603.05344):** Six-phase ReAct loop (pre-check, thinking, self-critique, action, tool execution, post-processing). Seven supporting subsystems. Dual-memory architecture (episodic + working). Event-driven reminders counter instruction fade-out. Lazy tool discovery. Adaptive context compaction that progressively reduces older observations.

**OpenAI Harness Engineering:** Architecture documentation as first-class artifacts. Plans treated as versioned, co-located files. Dependency layers enforced mechanically (Types → Config → Repo → Service → Runtime → UI). Dedicated linters validate knowledge base consistency.

**Milvus Code Review Study:** Five models tested against 15 real PRs with known bugs. Claude Opus 4.6: 53% solo, perfect on L3 (hardest) bugs. GPT-5.2-Codex: 33% solo but fewer false positives. Five-model debate: 80%. Claude + Codex pair: ~73% of five-model ceiling. Different models have different blind spots: this is WHY cross-model review works.

**YUV.AI Benchmarks / Task Routing Data:**

| Task | Claude (Opus 4.6) | GPT-5.4 | Winner |
|---|---|---|---|
| Refactoring | 4.9/5 | 4.5/5 | Claude |
| Type safety | 4.7/5 | 4.2/5 | Claude |
| Documentation | 4.9/5 | 4.4/5 | Claude |
| Security review | 4.8/5 | 4.6/5 | Claude |
| Architecture | 4.3/5 | 4.8/5 | GPT |
| DevOps/infra | 4.3/5 | 4.7/5 | GPT |
| Terminal tasks | 65.4% | 77.3% | GPT (Terminal-Bench) |
| Concurrency bugs | 0/2 detected | Catches | GPT (Claude blind spot) |
| Code review precision | 100% (0 FP) | 86.7% (3 FP) | Claude |
| Code review recall | Lower | Higher | GPT |

**BSWEN Review Pipeline Recommendation:** Run GPT first for high-recall triage, then Claude for high-precision validation.

### Architecture decisions influenced by prior art

From OPENDEV: dual-memory (episodic + working), event-driven reminders, progressive compaction
From OpenAI Harness: plans as first-class artifacts, mechanical enforcement of boundaries
From Milvus: cross-model review, model-specific routing
From enforcer (our own 8 rounds): ROI filter, fail-closed symlink resolution, snapshot-based cleanup, session learning, "nothing to fix" as valid outcome

---

## 4. Architecture Overview

### The Core Insight

Claude Code is a conversation with tool use. Forge is a phase machine that dispatches conversations.

In Claude Code, the model decides what to do next. It can skip phases, forget the plan, fabricate completion.

In Forge, the harness decides what phase runs next. The model is a function called by each phase with scoped inputs. It can't skip VERIFY because the harness doesn't call COMMIT until VERIFY passes.

### System Diagram

```
┌─────────────────────────────────────────────────────────────────────┐
│                           FORGE TUI                                 │
│   Focus Mode │ Dashboard Mode │ Detail Mode │ Headless CLI          │
└──────────────────────────────┬──────────────────────────────────────┘
                               │
┌──────────────────────────────▼──────────────────────────────────────┐
│                        ORCHESTRATOR                                 │
│                                                                     │
│  ┌────────────┐  ┌────────────┐  ┌────────────┐  ┌──────────────┐ │
│  │  Workflow   │  │   Model    │  │  Context   │  │   Policy     │ │
│  │  Engine     │  │   Router   │  │  Manager   │  │   Engine     │ │
│  └────────────┘  └────────────┘  └────────────┘  └──────────────┘ │
│                                                                     │
│  ┌────────────┐  ┌────────────┐  ┌────────────┐  ┌──────────────┐ │
│  │  Agent     │  │  Feedback  │  │  Memory    │  │ Subscription │ │
│  │  Pool      │  │  Loop      │  │  Store     │  │ Manager      │ │
│  └────────────┘  └────────────┘  └────────────┘  └──────────────┘ │
└──────────────────────────────┬──────────────────────────────────────┘
                               │
              ┌────────────────┼────────────────┐
              │                │                │
              ▼                ▼                ▼
┌──────────────────┐ ┌──────────────┐ ┌──────────────────────┐
│  CLAUDE CODE     │ │  CODEX CLI   │ │  OPENROUTER /        │
│  HEADLESS        │ │              │ │  DIRECT API          │
│                  │ │              │ │                      │
│  claude -p ...   │ │  codex exec  │ │  HTTP POST           │
│  (cwd=worktree)  │ │  --sandbox   │ │  /v1/messages        │
│  --stream-json   │ │  --ephemeral │ │                      │
│  --tools         │ │  --profile   │ │  (fallback only)     │
│  --max-turns     │ │  --json      │ │                      │
│                  │ │              │ │                      │
│  7 Max subs      │ │  Codex OAuth │ │  Pay-per-token       │
│  (independent    │ │  (ChatGPT    │ │  (budget/emergency)  │
│   rate pools)    │ │   Pro sub)   │ │                      │
└──────────────────┘ └──────────────┘ └──────────────────────┘
```

### What Forge Controls vs What Execution Engines Control

**Forge controls:**
- WHICH task runs next (workflow engine)
- WHICH model/subscription runs it (model router + subscription manager)
- WHAT context the model sees (context manager)
- WHAT tools the model can use (policy engine → --tools restriction + --disallowedTools deny)
- HOW LONG it can run (--max-turns)
- WHERE it runs (git worktree = isolated sandbox)
- WHETHER the result is accepted (verification pipeline)
- WHAT to do on failure (feedback loop)
- WHAT the retry attempt knows (failure analysis → retry brief)
- WHEN to commit to main (Forge owns git, models don't)

**Execution engines control:**
- HOW to read/write files (Claude Code's tool implementations)
- HOW to run shell commands (Claude Code's bash execution)
- HOW to stream to the API (Claude Code's provider management)
- HOW to auth with Anthropic/OpenAI (their respective OAuth/API flows)

**Key safety property:** Nothing reaches `main` unless Forge's verification pipeline passes. Claude Code can do whatever it wants inside a disposable worktree. If it messes up, the worktree is discarded and the retry attempt gets better instructions.

---

## 5. OAuth & Subscription Strategy

### The OAuth Landscape (March 2026)

**Anthropic Claude:** Subscription OAuth (Pro/Max) is restricted to official Anthropic clients. Using OAuth tokens in third-party tools violates ToS and has resulted in account bans. However, `claude -p` (headless mode) IS the official client. It uses subscription auth by default. Forge drives Claude Code itself -- it doesn't need third-party OAuth.

**OpenAI Codex:** Subscription OAuth (ChatGPT Plus/Pro) IS officially supported in third-party tools. Codex CLI works with subscription auth natively. OpenAI explicitly partners with tools like OpenCode for this.

### Two Auth Modes

**Mode 1: Internal/Private Subscription Mode (primary -- personal and private team use only)**

For individual developers and private teams using their own subscriptions on their own machines. This is Forge's default mode. **This is explicitly an internal/private operating mode, not a public-product posture.** Pooling consumer subscriptions to build a hosted service would violate both providers' terms.

```
Claude: Forge drives `claude -p` which uses your logged-in subscription (Pro/Max).
        Each pool = separate CLAUDE_CONFIG_DIR = separate login = separate rate limits.
        This is within ToS: you're using Claude Code, the official client, on your machine.

Codex:  Forge drives `codex exec` which uses your ChatGPT login (Plus/Pro).
        CODEX_HOME isolation for multiple accounts if needed.
        OpenAI explicitly supports ChatGPT sign-in for Codex CLI.
        OpenAI docs recommend API keys for programmatic/CI automation and
        say not to expose Codex execution in untrusted or public environments.
```

**CRITICAL SPIKE (Week 1): macOS Keychain credential isolation.**

Anthropic's auth docs state that on macOS, credentials are stored in the encrypted macOS Keychain, NOT under `CLAUDE_CONFIG_DIR`. On Linux/Windows, credential files live under `CLAUDE_CONFIG_DIR` and per-pool isolation is documented.

On macOS (the likely development environment), `CLAUDE_CONFIG_DIR` customizes config and data storage, but auth tokens may still route through the shared Keychain. This means per-pool login isolation may not work as expected on macOS without additional workarounds (e.g., separate Keychain entries, `security` CLI manipulation, or running pools in containers).

**Validate before building the pool scheduler.** If macOS Keychain blocks per-pool isolation, options include:
- Linux/container-based pool runners (each in its own namespace)
- macOS Keychain API to create per-pool credential entries
- `--apiKeyHelper` in Claude Code settings to provide per-pool auth programmatically
- Fallback to API-key mode for macOS (Mode 2)

**Mode 2: Enterprise/API Mode (for teams, CI/CD, public products)**

For teams, CI/CD pipelines, and any context where subscription pooling is inappropriate.

```
Claude: Anthropic API key (pay-per-token) or Claude Teams/Enterprise workspace.
        Claude Teams ($25/user/month for Teams, custom for Enterprise).
        Managed settings can centrally push permission rules.

Codex:  OpenAI API key (pay-per-token) or Codex Cloud.
        Standard OpenAI tier-based rate limits.

Router: OpenRouter API key for multi-model fallback.
        Built-in sub-routing across providers.
```

**Enterprise controls:** Claude Teams/Enterprise provides managed-settings.json that cannot be overridden by user or project settings, including the ability to disable bypass-permissions. These are client-side controls (not hard boundaries), but they compose with Forge's worktree isolation and verification pipeline for defense-in-depth.

**Important caveat from GPT's review:** Both Anthropic and OpenAI reserve the right to restrict subscription auth for automation use cases. Anthropic says third-party developers "generally may not offer claude.ai login" without approval. OpenAI recommends API keys for "programmatic/CI automation." Forge as a private tool using your own subscriptions on your own machine is very defensible. Forge as a public product whose throughput story depends on pooling consumer subscriptions is riskier. The spec should be honest about this boundary.

### Why CLI Subprocesses, Not the Agent SDK

GPT's review correctly flagged this as a missing rationale.

**The Agent SDK** (`@anthropic-ai/claude-agent-sdk`) is "Claude Code as a library" -- same tools, same agent loop, same context management, callable from TypeScript/Python. It provides: structured query/response, hook callbacks, tool approval callbacks, streaming message objects, and native embedding.

**Why Forge uses CLI (`claude -p`) instead:**

1. **Subscription auth.** The Agent SDK explicitly says third-party tools should use API-key auth. The CLI uses subscription auth by default. For Mode 1 (the primary mode), this is the difference between $0/task and $5-25/MTok.

2. **Go binary, not Node.js.** The Agent SDK is TypeScript/Python. Forge is Go. Calling `claude -p` from Go via `exec.Command` is trivial. Embedding the SDK would require a Node.js runtime or Python subprocess, adding complexity and breaking the single-binary promise.

3. **Isolation.** Each `claude -p` call is a fresh process with its own memory. No leaked state between tasks. No accumulated conversation history. This IS the three-tier context architecture: each phase starts clean.

4. **Hooks still fire.** `claude -p` (without `--bare`) loads hooks, skills, plugins, and CLAUDE.md. Forge installs enforcer hooks in each worktree, and they fire on every tool call during headless execution.

**When to reconsider:** If Anthropic changes the SDK to support subscription auth, or if Forge needs tighter integration (e.g., intercepting tool calls mid-execution rather than post-hoc), the SDK becomes more attractive. The architecture should make the execution engine swappable -- CLI today, SDK tomorrow, same orchestrator.

---

## 6. Execution Model

### The Worktree Sandbox

Every task dispatched to Claude Code runs in a disposable git worktree that FORGE creates and owns:

```bash
# FORGE creates the worktree (Forge owns worktree lifecycle)
git worktree add .forge/worktrees/task-12 -b forge/task-12 main
```

```go
// Forge sets the subprocess working directory -- no --cd flag needed.
// Claude Code docs don't expose a --cd flag; the documented directory
// flags are --add-dir and --worktree (which creates its own worktree).
// We control cwd at the OS level via exec.Command.Dir.
cmd := exec.CommandContext(ctx, "claude", "-p", taskPrompt,
    "--output-format", "stream-json",
    "--tools", "Read,Edit,Bash,Glob,Grep",
    "--disallowedTools", "Bash(rm -rf *),Bash(git push *),Bash(git reset --hard *)",
    "--allowedTools", "Read,Edit,Bash(npm test:*),Bash(npm run build:*)",
    "--max-turns", "20",
)

// If MCP is disabled for this phase, generate an empty config and use --strict-mcp-config.
// This prevents Claude from loading MCP servers from ~/.claude.json, .mcp.json, or plugins.
if !phase.MCPEnabled {
    emptyMCPPath := filepath.Join(worktreeDir, ".forge", "empty-mcp.json")
    os.MkdirAll(filepath.Dir(emptyMCPPath), 0755)
    os.WriteFile(emptyMCPPath, []byte("{}"), 0644)
    cmd.Args = append(cmd.Args, "--strict-mcp-config", "--mcp-config", emptyMCPPath)
}
cmd.Dir = ".forge/worktrees/task-12"  // OS-level cwd, not a CLI flag

// CRITICAL: Do NOT inherit os.Environ() wholesale in Mode 1.
// Claude's auth precedence: ANTHROPIC_API_KEY and ANTHROPIC_AUTH_TOKEN
// override subscription OAuth. If the parent shell has an exported API key,
// it silently bypasses the subscription pool scheduler and cost model.
// Build a clean env with only what Claude Code needs.
cmd.Env = safeEnvForMode1(poolConfigDir)

// safeEnvForMode1 builds a subprocess environment that:
//   - Sets CLAUDE_CONFIG_DIR to the pool's config directory
//   - Passes through PATH, HOME, TERM, LANG, SHELL, TMPDIR, USER (basic OS)
//   - Passes through NODE_PATH, npm_config_* (Node.js runtime)
//   - Passes through GIT_* (git operations)
//   - STRIPS: ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN, OPENAI_API_KEY,
//     CODEX_API_KEY, AWS_*, GOOGLE_*, AZURE_* (auth vars that bypass subscription)
//   - In Mode 2 (enterprise/API), these are explicitly set instead of stripped
func safeEnvForMode1(configDir string) []string {
    passthrough := []string{
        "PATH", "HOME", "TERM", "LANG", "SHELL", "TMPDIR", "USER",
        "NODE_PATH", "XDG_CONFIG_HOME", "XDG_DATA_HOME",
    }
    passthroughPrefixes := []string{"npm_config_", "GIT_"}
    stripExact := map[string]bool{
        "ANTHROPIC_API_KEY": true, "ANTHROPIC_AUTH_TOKEN": true,
        "OPENAI_API_KEY": true, "CODEX_API_KEY": true,
    }
    stripPrefixes := []string{"AWS_", "GOOGLE_", "AZURE_", "BEDROCK_", "VERTEX_"}

    env := []string{"CLAUDE_CONFIG_DIR=" + configDir}
    for _, e := range os.Environ() {
        k, _, _ := strings.Cut(e, "=")
        if stripExact[k] { continue }
        if hasAnyPrefix(k, stripPrefixes) { continue }
        if slices.Contains(passthrough, k) || hasAnyPrefix(k, passthroughPrefixes) {
            env = append(env, e)
        }
    }
    return env
}

// MODE 1 BASELINE: Reject repo-supplied apiKeyHelper.
// Claude's auth precedence: cloud-provider → ANTHROPIC_AUTH_TOKEN → ANTHROPIC_API_KEY
// → apiKeyHelper → subscription OAuth. A project's .claude/settings.json can define
// an apiKeyHelper script that runs before OAuth and returns an API key. In Mode 1,
// Forge MUST write a settings.json into each worktree that explicitly sets
// apiKeyHelper to null, preventing repo-supplied helpers from bypassing subscription
// auth. This is not optional hardening -- it is required for Mode 1 cost model integrity.
```

```bash
# On success: Forge verifies, then merges to main
git checkout main
git merge --no-ff forge/task-12 -m "feat(TASK-12): rate limiting middleware"

# On failure: Forge discards
git worktree remove .forge/worktrees/task-12
git branch -D forge/task-12
```

**Important:** Forge sets `cmd.Dir` to the worktree path, NOT `--worktree` (which creates Claude's own worktree under `.claude/worktrees/`). Forge must own the worktree lifecycle. Never use `--bare` for dispatched tasks -- Anthropic documents that `--bare` skips hooks, skills, plugins, MCP servers, auto memory, and CLAUDE.md. Forge needs hooks to fire inside each worktree.

### The Phase Machine

```
PLAN → EXECUTE → VERIFY → COMMIT → next task
```

Each phase is a discrete `claude -p` or `codex exec` call:

```go
type Phase struct {
    Name          string
    Engine        string              // "claude" or "codex"
    BuiltinTools  []string            // --tools: built-in tool NAMES only (Read, Edit, Write, Bash, Glob, Grep)
    AllowedRules  []string            // --allowedTools: pattern rules for auto-approve (e.g. "Bash(npm test:*)")
    DeniedRules   []string            // --disallowedTools: pattern rules for hard deny (e.g. "Bash(rm -rf *)")
    MCPEnabled    bool                // whether MCP servers are available in this phase
    // --tools does NOT control MCP. Claude loads MCP from multiple scopes:
    // user/local entries in ~/.claude.json, project .mcp.json, and plugins.
    // MCPEnabled=false means: launch with --strict-mcp-config <empty-config>
    // which tells Claude to ignore ALL other MCP sources.
    // MCPEnabled=true means: let Claude discover MCP servers normally.
    MaxTurns      int
    Prompt        PromptTemplate      // phase-specific system prompt
    Verify        func(WorktreeState) VerifyResult
}

// IMPORTANT: --tools accepts ONLY built-in tool names: Read, Edit, Write, Bash, Glob, Grep, WebFetch
// Pattern syntax like Bash(npm test:*) belongs in --allowedTools or --disallowedTools (permission rules)
// MCP tools are a separate extension layer -- --tools does not disable them.
// To disable MCP in a phase, launch with --strict-mcp-config --mcp-config <empty>.
// Omitting MCP from worktree settings alone is NOT sufficient (Claude loads MCP
// from ~/.claude.json, .mcp.json, and plugin auto-start).

var DefaultWorkflow = []Phase{
    {
        Name:         "plan",
        Engine:       "claude",
        BuiltinTools: []string{"Read", "Glob", "Grep"},      // read-only built-ins
        AllowedRules: []string{"Read", "Glob", "Grep"},       // auto-approve all (no prompts)
        DeniedRules:  nil,                                     // nothing to deny in read-only
        MCPEnabled:   false,                                   // no external tools during planning
        MaxTurns:     10,
        Prompt:       planPrompt,
    },
    {
        Name:         "execute",
        Engine:       "claude",         // or "codex" depending on task type routing
        BuiltinTools: []string{"Read", "Edit", "Write", "Bash", "Glob", "Grep"},
        AllowedRules: []string{         // auto-approve safe patterns
            "Read", "Edit",
            "Bash(npm test:*)", "Bash(npm run lint:*)", "Bash(npm run build:*)",
            "Bash(cat *)", "Bash(grep *)", "Bash(git status)", "Bash(git diff *)",
        },
        DeniedRules: []string{          // hard deny dangerous patterns
            "Bash(rm -rf *)", "Bash(git push *)", "Bash(git reset --hard *)",
            "Bash(git rebase *)", "Bash(sudo *)", "Bash(curl *)", "Bash(wget *)",
        },
        MCPEnabled:   true,             // project MCP servers available if configured
        MaxTurns:     20,
        Prompt:       executePrompt,    // task + retry brief + session learning
    },
    {
        Name:         "verify",
        Engine:       "codex",          // DIFFERENT model family reviews
        BuiltinTools: []string{"Read", "Glob", "Grep", "Bash"},  // built-in names only
        AllowedRules: []string{         // auto-approve test/lint only
            "Read", "Glob", "Grep",
            "Bash(npm test:*)", "Bash(npm run lint:*)",
        },
        DeniedRules: []string{          // deny writes and destructive ops
            "Edit", "Write",
            "Bash(rm *)", "Bash(git *)",
        },
        MCPEnabled:   false,            // no external tools during verification
        MaxTurns:     5,
        Prompt:       verifyPrompt,
    },
}
```

**The model cannot skip phases because the harness controls which phase runs next.** This is the fundamental difference from Claude Code, where phases are prompt instructions the model can ignore.

### Parallel Execution

Independent tasks (no shared file dependencies) run in parallel:

```go
func (e *Engine) ExecuteParallel(tasks []Task) {
    // Build dependency graph
    independent := tasks.WithNoDependencies()

    // Dispatch to available pools
    var wg sync.WaitGroup
    results := make(chan TaskResult, len(independent))

    for _, task := range independent {
        pool := e.subs.LeastLoaded(e.router.ProviderFor(task))
        if pool == nil { continue } // all pools busy, queue for later

        wg.Add(1)
        go func(t Task, p *SubscriptionPool) {
            defer wg.Done()
            result := e.executeTask(t, p)
            results <- result
        }(task, pool)
    }

    // Collect results, update plan, dispatch next batch
    go func() { wg.Wait(); close(results) }()
    for result := range results {
        e.plan.Update(result)
        e.tui.Notify(result)
    }
}
```

With 7 Max subscriptions + 1 Codex subscription, up to 8 agents can run simultaneously in separate worktrees. For 50 tasks with ~10 parallel-safe groups, this is 5-10x faster than sequential.

---

## 7. Feedback Loop & Intelligent Retry

### The Problem With Dumb Retries

"Try again" with the same instructions = same failure. Three retries of identical instructions is not a strategy.

### Forge's Five-Stage Pipeline

**Stage 1: Classify the failure.**
BuildFailed, TestsFailed, LintFailed, PolicyViolation, ReviewRejected, Timeout, WrongFiles, Incomplete, Regression. Each class gets a different analysis strategy.

**Stage 2: Extract specifics from the failed worktree.**
Before discarding, run `npm run build 2>&1`, `npm test 2>&1`, `npm run lint 2>&1`. Parse EXACT errors: file, line, error message, suggested fix. For policy violations (@ts-ignore), also extract the type errors the agent was trying to bypass.

```go
type FailureAnalysis struct {
    Class       FailureClass
    Summary     string            // "3 type errors bypassed with @ts-ignore"
    RootCause   string            // what the agent actually did wrong
    Missing     []string          // what the instructions didn't say
    Specifics   []FailureDetail   // file:line:error:fix for each issue
    DiffSummary string            // compressed diff of what the agent changed
}
```

**Stage 3: Generate the retry brief.**
Original task + failure context. "Previous attempt failed because X. The actual errors are Y. The fixes are Z. DO NOT use @ts-ignore."

**Stage 4: Decide retry vs escalate.**
Same error twice = the TASK is wrong, not the agent. Escalate to user with full analysis and options. Don't burn a third attempt on the same wall.

**Stage 5: Cross-task session learning.**
If task 3 fails because the agent doesn't know the codebase uses declaration merging for Request types, inject that pattern into task 7's instructions preemptively. The agent gets it right first try.

### Key Design Constraint

**Each retry starts from a clean worktree (fresh copy of main).** No accumulated garbage, no "fix the fix" chains. The learning is in the INSTRUCTIONS, not in the code state.

---

## 8. Context Architecture

### Three-Tier Context Management

```
Tier 1: ACTIVE CONTEXT (in the API call)
  - Phase-specific system prompt
  - Task specification + retry brief
  - Relevant file contents (loaded on demand)
  - Working memory (current session state)
  - Session learning (codebase patterns)
  - Recent tool results (last 3-5 only)
  Target: <40% of context window

Tier 2: SESSION STATE (on disk, promoted to Tier 1 on demand)
  - Full plan with all task statuses
  - All file paths read/written this session
  - Build/test/lint results history
  - Error history (what failed and why)
  - Failure analyses from retries

Tier 3: PROJECT KNOWLEDGE (persistent across sessions)
  - Project map (file inventory, structure)
  - CLAUDE.md / project conventions
  - Episodic memory (learned patterns across sessions)
  - Decision log (why choices were made)
  - Git history summary
```

### Progressive Compaction

Each phase starts with a FRESH context containing only what it needs. No accumulated conversation history. This alone eliminates the #1 cause of degradation in long Claude Code sessions.

When a phase's context approaches the budget:
- **Gentle:** truncate tool outputs over 500 lines to summaries
- **Moderate:** compress file reads to "read X, found Y"
- **Aggressive:** summarize entire phase state to structured data

### Event-Driven Reminders (from OPENDEV)

Inject critical rules when patterns are detected:

```go
var reminders = []Reminder{
    {Trigger: FileWriteToTest, Message: "TEST RULES: never weaken assertions..."},
    {Trigger: ContextAbove60Pct, Message: "COMPACT: context filling, focus on current task..."},
    {Trigger: ErrorRepeated3x, Message: "STUCK: you hit this error 3 times. Try a different approach."},
    {Trigger: TaskRunning20Min, Message: "TIMEOUT: this task is taking too long. Finish or report blockers."},
}
```

---

## 9. Policy Engine

### The Critical Distinction: --tools vs --allowedTools

GPT's review caught a spec-breaking bug: `--allowedTools` does NOT restrict which tools are available. It only marks tools as auto-approved (no prompt needed). Claude still has access to all tools.

To actually restrict which built-in tools appear in Claude's context, use `--tools`. Omitting a tool from `--tools` removes it entirely -- Claude never sees it and never attempts it. `--disallowedTools` blocks specific tools but leaves them visible (Claude may waste a turn trying).

**Forge uses `--tools` for hard restriction + `--disallowedTools` as backup + settings.json deny rules for pattern-level control:**

```bash
# CORRECT: restrict to ONLY these tools (hard restriction)
claude -p "task" \
  --tools "Read,Edit,Bash" \
  --disallowedTools "Bash(rm -rf *),Bash(git push *),Bash(git reset --hard *)" \
  --allowedTools "Read,Edit,Bash(npm test:*),Bash(npm run lint:*)"
```

### Forge's Layered Policy

```
Layer 1: --tools (Claude Code native, HARD restriction on BUILT-IN tools only)
  Controls which built-in tools EXIST in Claude's context.
  Accepts ONLY built-in tool names: Read, Edit, Write, Bash, Glob, Grep, WebFetch.
  Does NOT control MCP tools -- MCP is a separate extension layer.
  If a built-in tool isn't listed, Claude cannot attempt it.

Layer 2: MCP isolation (--strict-mcp-config with generated empty config)
  --tools does NOT control MCP. Claude loads MCP servers from multiple scopes:
  user/local entries in ~/.claude.json, project .mcp.json, and plugin-provided
  servers that auto-start when a plugin is enabled. Omitting MCP from the
  worktree settings.json alone is NOT sufficient.
  To fully disable MCP in a phase, Forge launches Claude with:
    --strict-mcp-config --mcp-config .forge/empty-mcp.json
  where empty-mcp.json is a generated file containing {}. The --strict-mcp-config
  flag tells Claude to ignore ALL other MCP sources.
  MCPEnabled=true phases omit these flags, allowing normal MCP discovery.

Layer 3: --disallowedTools (Claude Code native, HARD deny patterns)
  Blocks specific tool+argument patterns.
  "Bash(rm -rf *)" = blocked even though Bash is in --tools.
  Deny rules always win over allow rules at every level.

Layer 4: --allowedTools (Claude Code native, auto-approve convenience)
  Tools listed here run without prompting. Does NOT restrict access.
  Only used to prevent approval prompts on known-safe operations.

Layer 5: settings.json deny rules (per-worktree)
  Forge writes a settings.json into each worktree with pattern-level deny rules.
  Deny at any level cannot be overridden by any other level.

Layer 6: Worktree isolation (repo integrity)
  Each task runs in a disposable git worktree copy.
  Cannot damage main. Cannot escape to parent directories.
  Protects REPO INTEGRITY: bad code never reaches main without verification.

Layer 7: Claude Code sandbox (Bash subprocess filesystem/network control)
  Worktrees protect integrity but do NOT prevent exfiltration via Bash.
  Claude Code's native sandbox provides OS-level filesystem and network
  restriction for the Bash tool and its child processes specifically.
  IMPORTANT: The sandbox applies to Bash subprocesses only. Built-in Read,
  Edit, and Write tools stay under the permission system (Layers 1-5), not
  the sandbox. So "sandbox enabled" does NOT mean full confidentiality for
  every tool path -- it means Bash commands can't escape the allowed filesystem
  or make unauthorized network requests.
  IMPORTANT: Claude docs say if sandboxing cannot start, Claude warns and runs
  commands WITHOUT sandboxing by default. Forge MUST set sandbox.failIfUnavailable
  = true in per-worktree settings so sandbox failure is a hard error, not a silent
  fallback to unsandboxed execution.
  Forge enables sandboxing in execute/verify phases to constrain:
    - Bash filesystem: restrict to worktree + node_modules + system deps
    - Bash network: block outbound by default, whitelist npm registry if needed
  NOTE: Sandbox surface is still evolving. Spike during Week 1 implementation.

Layer 8: --max-turns (execution cap)
  Prevents infinite loops and runaway sessions.

Layer 9: Enforcer hooks (fast filter, installed in worktree)
  Same hooks from setup.sh. Catch obvious patterns DURING execution.
  Efficiency optimization, not the safety boundary.

Layer 10: Verification pipeline (the real gate)
  Build, tests, lint, cross-model review, scope check.
  Runs AFTER Claude finishes, BEFORE Forge commits to main.
  Nothing reaches main unless ALL checks pass.

Layer 11: Forge owns git
  The model cannot commit, push, merge, or modify main.
  Only Forge can. And only after verification passes.
```

### Policy Configuration

```yaml
# forge.policy.yaml
# Three distinct concerns:
#   builtin_tools → --tools (which built-in tools EXIST in context)
#   denied_rules  → --disallowedTools (pattern-level hard deny)
#   allowed_rules → --allowedTools (pattern-level auto-approve, no prompting)
#   mcp_enabled   → when false: launch with --strict-mcp-config --mcp-config <empty>
#                   (ignores ALL other MCP sources: ~/.claude.json, .mcp.json, plugins)
#                   when true: omit these flags, allow normal MCP discovery

phases:
  plan:
    builtin_tools: [Read, Glob, Grep]
    denied_rules: []
    allowed_rules: [Read, Glob, Grep]
    mcp_enabled: false

  execute:
    builtin_tools: [Read, Edit, Write, Bash, Glob, Grep]
    denied_rules:
      - "Bash(rm -rf *)"
      - "Bash(git push *)"
      - "Bash(git reset --hard *)"
      - "Bash(git rebase *)"
      - "Bash(sudo *)"
      - "Bash(curl *)"
      - "Bash(wget *)"
    allowed_rules:
      - "Read"
      - "Edit"
      - "Bash(npm test:*)"
      - "Bash(npm run lint:*)"
      - "Bash(npm run build:*)"
      - "Bash(cat *)"
      - "Bash(grep *)"
      - "Bash(git status)"
      - "Bash(git diff *)"
    mcp_enabled: true

  verify:
    builtin_tools: [Read, Glob, Grep, Bash]
    denied_rules:
      - "Edit"
      - "Write"
      - "Bash(rm *)"
      - "Bash(git *)"
    allowed_rules:
      - "Read"
      - "Bash(npm test:*)"
      - "Bash(npm run lint:*)"
    mcp_enabled: false

files:
  protected: [".claude/", ".forge/", "CLAUDE.md", ".env*", "forge.policy.yaml"]

verification:
  build: required
  tests: required
  lint: required
  cross_model_review: required
  scope_check: required
```

### Why This Is Stronger Than the Enforcer

The enforcer used regex guards on command strings (`echo "$CMD" | grep -qE 'git stash'`). The reviewer correctly identified this as bypassable by wrapper indirection (`bash helper.sh` hides the command).

Forge's policy works at three levels that compose:
1. `--tools` removes tools from Claude's context entirely (not string matching -- tool schema removal)
2. `--disallowedTools` blocks patterns at Claude Code's native enforcement layer
3. Worktree isolation means even if both are somehow bypassed, damage is contained

The enforcer's architectural ceiling ("regex guards on command strings, bypassable by wrapper indirection") is eliminated because Forge doesn't need to inspect command strings -- it controls which tools exist in the first place.

---

## 10. Model Routing

### Task-Type Routing (Benchmark-Backed)

```go
var routes = map[TaskType]RouteConfig{
    Plan:          {Primary: "claude", Reason: "best at ambiguous prompts"},
    Refactor:      {Primary: "claude", Reason: "4.9/5 refactoring"},
    TypeSafety:    {Primary: "claude", Reason: "4.7/5 vs 4.2/5"},
    Docs:          {Primary: "claude", Reason: "4.9/5 vs 4.4/5"},
    Security:      {Primary: "claude", Reason: "53% vs 33% solo detection"},
    Architecture:  {Primary: "codex",  Reason: "4.8/5 vs 4.3/5"},
    DevOps:        {Primary: "codex",  Reason: "Terminal-Bench 77.3% vs 65.4%"},
    Concurrency:   {Primary: "codex",  Reason: "Claude blind spot: 0/2 in Milvus"},
    Review:        {Primary: "codex",  Fallback: "claude", Reason: "GPT recall + Claude precision"},
}
```

### Cross-Model Verification

Every task commit is reviewed by a DIFFERENT model family:

```
Claude implements → GPT reviews → Forge commits
GPT implements    → Claude reviews → Forge commits
```

The Milvus data: cross-model raises detection from 53% to 80%. Different training data = different blind spots = complementary coverage.

### Fallback Chain

```
Primary: Claude Code headless (subscription pool, $0 marginal)
    ↓ pool exhausted
Fallback 1: Codex CLI (subscription OAuth, $0 marginal)
    ↓ pool exhausted
Fallback 2: OpenRouter (API key, built-in sub-routing, broadest coverage)
    ↓ credits exhausted
Fallback 3: Direct API (Anthropic or OpenAI, pay-per-token)
    ↓ budget cap hit
Fallback 4: Lint-only + "manual review recommended" flag
```

---

## 11. TUI Design

### Philosophy: Mission Control, Not Chat

Claude Code is a chat with one agent. Forge is mission control for multiple agents.

### Three Modes

**Focus Mode (default):** Feels like Claude Code. Streaming tokens, inline diffs, tool use visibility. BUT with a status bar showing: branch, project, task progress (12/47), active agents (3 of 7), session cost, aggregate rate limit. Parallel agents get a summary line at the bottom.

**Dashboard Mode (Tab):** The orchestration view. Task list with live status (✓▸○✗). All subscription pools with utilization bars and reset timers. Session learning panel. Cost tracker with subscription savings.

**Detail Mode (Enter on task):** Drill into a specific task. See attempt history, failure analyses, retry briefs, and the current attempt's streaming output. See exactly where learning kicked in.

### Key UX Features

- Streaming tokens at same speed/feel as Claude Code (stream-json parsing)
- Keyboard-first: every action has a key. Mouse optional.
- Rate limit pools are first-class citizens (the #1 operational concern with 7 subs)
- Cost tracking: "Session cost $4.23, would have cost $67 via API, subs saved 93%"
- Sound notifications: chime on task complete, alert on failure, notification on escalation
- Session persistence: crash-safe. SQLite + git. Just run `forge` again to resume.
- Intervention without interruption: type at any time to inject instructions into active agent

### Technical Implementation

- Bubble Tea (Go TUI framework, Elm Architecture, 60fps, same as OpenCode)
- Lip Gloss (styling)
- Each agent streams through a goroutine + channel to the TUI
- Focus mode renders active agent's stream in real-time, others buffer
- Switch focus with 1-7 number keys -- buffered output renders instantly

---

## 12. Technology Stack

Applying the enforcer's own principle: **LTS/stable over hype.**

| Component | Choice | Rationale |
|---|---|---|
| Language | Go | Single static binary. Cross-compiles everywhere. Goroutines = parallel agents. Channels = task queues. OpenCode already proved Go works for this. |
| TUI | Bubble Tea + Lip Gloss | Same framework as OpenCode. Elm Architecture. 60fps. Battle-tested. |
| Storage | SQLite (modernc.org/sqlite) | Pure Go, no CGo, zero external deps. Session state, memory, metrics. |
| Shell parsing | tree-sitter Go bindings | Command decomposition for policy engine. Same approach as Codex CLI. |
| Git | go-git + native git | go-git for in-process ops (status, diff). Native git for worktrees, merge. |
| API clients | net/http | Anthropic, OpenAI, OpenRouter are all REST. No SDK dependency needed. |
| Config | YAML | Human-readable policy files, model routing, workflow definitions. |

**What Forge does NOT include (delegates to execution engines):**
- File operation tools (Claude Code has these)
- Provider auth/OAuth (Claude Code and Codex handle their own)
- LSP integration (Claude Code and Codex may add this)
- MCP servers (Claude Code supports these natively)

**Distribution:** Single Go binary. `curl -fsSL https://forge.dev/install | bash`. Same install experience as Claude Code.

---

## 13. Implementation Plan

### Phase 1: Core Orchestrator (Weeks 1-2)

**Week 1:**
- Project setup, CI, test harness
- **SPIKE: macOS Keychain credential isolation (Claude).** Validate that `CLAUDE_CONFIG_DIR` actually isolates auth on macOS (Keychain may share credentials). Test with 2 accounts. If blocked, design the Linux container fallback or `--apiKeyHelper` workaround. This gates the pool scheduler.
- **SPIKE: Codex credential-store isolation.** OpenAI docs say Codex caches credentials either in `auth.json` under `CODEX_HOME` or in the OS credential store. `CODEX_HOME` only isolates file-based storage. If the OS keyring is used, multi-account isolation may fail on macOS. Test with `cli_auth_credentials_store = "file"` in config to force file-based auth, or validate keyring behavior. Same class of issue as the Claude Keychain spike.
- **SPIKE: Claude Code sandbox surface.** Test sandbox flags for Bash filesystem/network restriction in worktrees. Document what's available, what's research-preview, what's stable. Note: sandbox applies to Bash subprocesses only, not built-in Read/Edit/Write.
- **SPIKE: MCP isolation via --strict-mcp-config.** Validate that `--strict-mcp-config --mcp-config empty.json` actually prevents MCP server discovery from all scopes (~/.claude.json, .mcp.json, plugins). Test that plugin-provided MCP servers don't auto-start under a clean config dir. This gates the security model for plan/verify phases.
- Workflow engine (phase machine with enforced transitions)
- Subscription manager (pool tracking, rotation, utilization)
- Claude Code headless integration (spawn via `exec.Command` with `cmd.Dir`, stream, parse JSON output)
- Worktree lifecycle (create, verify, merge/discard)

**Week 2:**
- Policy engine (YAML policies → --tools restriction + --disallowedTools deny + subprocess cwd isolation)
- Model router (task-type routing, provider selection)
- Codex CLI integration (spawn, stream, parse JSONL output)
- Cross-model verification pipeline (Claude implements → GPT reviews)
- Context manager (three-tier, prompt assembly per phase)

**Milestone:** End of week 2, Forge can: read a plan, dispatch tasks to Claude Code headless across multiple pools, verify results, commit to main or retry with failure analysis.

### Phase 2: Intelligence Layer (Weeks 3-4)

**Week 3:**
- Feedback loop (failure classification, specific extraction, retry brief generation)
- Session learning (cross-task pattern accumulation)
- Escalation logic (retry vs escalate decision tree)
- Memory store (SQLite: episodic + working memory)

**Week 4:**
- TUI: Focus mode (streaming, diffs, tool use, status bar)
- TUI: Dashboard mode (task board, pool status, cost tracking, learning panel)
- TUI: Detail mode (task history, retry briefs, attempt comparison)
- Event-driven reminders (instruction fade-out prevention)

**Milestone:** End of week 4, Forge has a complete TUI with intelligent retry. Users can watch parallel agents work, see failure analyses, and see session learning accumulate.

### Phase 3: Production Hardening (Weeks 5-6)

**Week 5:**
- Port enforcer workflows (scope, build, scan-and-repair as Forge workflows)
- Port enforcer hooks (as fast-filter layer inside worktrees)
- ROI filter integration
- Cleanup system (artifact management)
- Port 17 audit personas as workflow definitions

**Week 6:**
- Benchmark vs Claude Code on real tasks (same repo, same tasks, measure time/quality/cost)
- Crash recovery testing (kill at every stage, verify resume)
- Rate limit edge cases (all pools exhausted, mid-task rate limit)
- Cross-platform testing (macOS ARM, macOS Intel, Linux x64, Linux ARM)
- Documentation, install script, release

### Phase 4: Ongoing

- Contribute useful patches upstream to OpenCode if we forked anything
- SWE-bench evaluation (measure the scaffold swing)
- Community feedback loop
- Desktop app (Tauri, same approach as OpenCode -- optional, after CLI is solid)

---

## 14. Functional Comparison: Forge as Cross-Engine Orchestrator

### What Forge IS

Forge is a deterministic orchestration layer that drives Claude Code and Codex CLI as execution engines. It does not replace Claude Code's capabilities -- it adds a structured control plane on top.

### What Forge Adds Over Claude Code Alone

| Capability | Claude Code (native) | Forge (orchestration layer) |
|---|---|---|
| **Workflow control** | Model-driven ("gather, act, verify" -- model decides sequence) | Harness-driven (PLAN→EXECUTE→VERIFY→COMMIT -- harness controls sequence) |
| **Phase skipping** | Model can skip verification or fabricate completion | Impossible: harness doesn't call COMMIT until VERIFY passes |
| **Model selection** | Claude only | Claude for refactoring + GPT for DevOps + cross-model review |
| **Cross-model review** | Not available | Every commit reviewed by a different model family |
| **Subscription pools** | 1 session = 1 pool | 7+ pools, auto-rotated by utilization |
| **Parallel scheduling** | Agent Teams (model-coordinated, same pool) | Goroutine pool (harness-coordinated, independent pools) |
| **Failure recovery** | User re-prompts manually | 5-stage analysis: classify → extract specifics → generate retry brief → decide retry/escalate → session learning |
| **Cross-task learning** | Auto memory (general) | Specific: "this codebase uses declaration merging in src/types/" injected into every subsequent task |
| **Verification** | Model decides whether to run tests | Deterministic: build + tests + lint + cross-model review MUST all pass |
| **Cost visibility** | None | Per-task cost tracking + subscription savings calculation |
| **Rate limit management** | "Rate limited" message | Per-pool utilization bars, reset timers, auto-rotation |
| **Plan management** | Model manages its own plan in conversation | Harness manages typed plan data: model can't fabricate completion |
| **TUI** | Single-agent chat | Mission control: parallel agents + dashboard + task drill-down |

### What Claude Code Does That Forge Inherits (Not Replaces)

| Claude Code capability | Forge's relationship |
|---|---|
| Tool execution (Read, Edit, Write, Bash, Glob, Grep) | Drives via `claude -p` |
| Provider auth (OAuth for subscription) | Uses via `CLAUDE_CONFIG_DIR` |
| Auto mode with safety classifier | Plan-gated (Team plan required); not a Forge dependency |
| Hooks (PreToolUse, PostToolUse, etc.) | Installs enforcer hooks in each worktree |
| Plugins, skills, MCP servers | Available inside each worktree session |
| Auto memory | Supplements (doesn't replace) Forge's session learning |
| Session resume | Forge has its own persistence (SQLite + git state) |
| Agent Teams | Replaced by Forge's parallel pool scheduler (more control) |
| Managed settings (enterprise) | Compatible -- Forge can deploy managed settings per worktree |

### The Honest Assessment

**Forge is BETTER when:**
- You need deterministic workflow enforcement (model can't skip verify)
- You need multi-model routing (different models for different task types)
- You need cross-model verification (53% → 80% bug detection)
- You have multiple subscriptions and want to maximize throughput
- You need intelligent retry with failure analysis
- You need visibility into cost, rate limits, and progress
- You need structured plans that the model can't fabricate

**Claude Code alone is BETTER when:**
- You want interactive pair programming (conversation, not orchestration)
- You're doing exploratory work (no predefined plan)
- You want the simplest possible setup (just run `claude`)
- You only have one subscription
- You trust the model to self-direct (and it usually does fine)

---

## 15. Cost Model

### Scenario A: Solo developer, moderate usage

```
Claude Code (Max 5x):  $100/month (Claude only, 1 pool)
Forge:                 $20/month ChatGPT Plus (GPT-5.4 via Codex, included in Plus)
                     + $20/month Claude Pro (1 Claude pool via claude -p)
                     = $40/month (two model families, 2 pools)
```
Winner: Forge ($40 vs $100, plus multi-model)

### Scenario B: Power user (Eric's actual setup)

```
Claude Code (Max 20x): $200/month (1 pool, ~900 prompts/5h*)
Forge:                 7 × $200 Claude Max 20x ($1,400/month, 7 pools, ~6,300 prompts/5h*)
                     + $200 ChatGPT Pro (1 Codex pool, ~1,500 msgs/5h*)
                     = $1,600/month (8 pools, ~7,800 prompts/5h*)
```
*Estimates. Actual throughput varies by message length, model, and features.
Forge costs more in subscriptions but delivers 7x the Claude throughput + cross-model review. Equivalent API cost at those volumes: $5,000-15,000/month.

### Scenario C: Team of 5

```
Claude Code (Max 5x × 5): $500/month (5 separate pools)
Forge:                     $500/month in subs (mix of Max + Plus plans)
                         = Same cost, but Forge gets multi-model + orchestration + parallel
```

### Pricing Reference (March 2026)

| Plan | Price | Throughput* |
|---|---|---|
| ChatGPT Plus | $20/month | Includes Codex CLI access |
| ChatGPT Pro | $200/month | Higher rate limits, Codex Cloud |
| Claude Pro | $20/month | ~45 prompts/5h* |
| Claude Max 5x | $100/month | ~225 prompts/5h* |
| Claude Max 20x | $200/month | ~900 prompts/5h* |

*Throughput numbers are observed community estimates, not contractual guarantees. Anthropic states that actual limits vary by message length, attached files, conversation length, model, and feature use. Check your Usage page for real-time 5-hour progress bars. OpenAI Codex limits similarly vary by task complexity and model choice.

---

## 16. Open Questions & Risks

### Technical Risks

1. **Claude Code headless reliability.** Known issues: missing final result events (~8% of CI runs), processes that don't exit, stream disconnections. Mitigation: external timeout, retry logic, JSON validation. We already solved these in the enforcer's codex-execute.sh.

2. **Rate limit tracking accuracy.** Claude Code doesn't expose utilization in headless JSON output cleanly. May need to track cumulative `cost_usd` and estimate. Codex has similar gaps. Mitigation: conservative estimates, proactive pool rotation at 70% rather than waiting for 429s.

3. **Worktree merge conflicts.** Parallel agents modifying the same file will conflict at merge time. Mitigation: task dependency graph prevents parallel execution of tasks with overlapping file scopes. Fall back to sequential for dependent tasks.

4. **Session learning quality.** Patterns learned from failures might be overly specific or wrong. Mitigation: patterns expire after N tasks without reinforcement. User can view and clear learned patterns.

4b. **macOS Keychain credential isolation -- Claude (HIGH PRIORITY).** On macOS, Claude Code stores auth in the encrypted Keychain, not under `CLAUDE_CONFIG_DIR`. Per-pool isolation of auth tokens may require Keychain API manipulation, separate Keychain entries, or Linux container-based pools. This gates the entire multi-pool scheduler for macOS users. SPIKE IN WEEK 1.

4c. **Codex credential-store isolation (MEDIUM PRIORITY).** OpenAI docs say Codex caches credentials in `auth.json` under `CODEX_HOME` OR in the OS credential store. `CODEX_HOME` only isolates file-based storage. If the OS keyring is used, multi-account Codex isolation on macOS has the same class of problem. Mitigation: force file-based auth via `cli_auth_credentials_store = "file"` in Codex config. SPIKE IN WEEK 1.

### Business Risks

5. **Anthropic tightens headless mode.** If Anthropic restricts `claude -p` to API keys only (blocking subscription auth), the cost model breaks for Mode 1. Mitigation: this would break their own CI/CD documentation and enterprise workflows. Unlikely but possible. Fallback: Mode 2 (API keys) with OpenRouter for cost management. Note from GPT's review: Anthropic's docs say third-party developers "generally may not offer claude.ai login" without approval. Forge as a private tool is defensible. Forge as a public product with subscription pooling is riskier.

6. **OpenAI restricts Codex OAuth.** Currently supported and officially partnered, but OpenAI's docs recommend API keys for "programmatic/CI automation." Lower risk than Anthropic but nonzero. Mitigation: same fallback to API-key mode.

7. **Competition from Claude Code itself.** Claude Code already has Agent Teams, auto mode, plugins, managed settings. If Anthropic adds deterministic workflow phases, cross-model review, or pool scheduling natively, Forge's moat shrinks. Mitigation: Forge's moat is the COMPOSITION of features (workflow + routing + retry + verification + scheduling), not any single feature. Individual features are copyable; the integrated system is hard to replicate.

8. **Competition from OpenCode.** OpenCode has 132K+ stars (up from 90K in the research phase), multi-session parallel work, any-provider support, and a similar curl installer. Forge can still beat OpenCode on deterministic workflows, failure recovery, cross-model verification, and pool scheduling -- but not by positioning against a strawman version of the competition.

### Open Design Questions

8. **How to detect task dependencies automatically?** Currently requires manual specification in the plan. Could use file-scope analysis (which files does each task touch?) to infer dependencies.

9. **How to handle shared build state?** Test suites often depend on the full repo. Running tests in a worktree requires `npm install` in each worktree. Mitigation: symlink `node_modules` from main, or use a shared install.

10. **How to measure scaffold quality?** SWE-bench is the standard but requires specific infrastructure. Need a lighter benchmark for iteration. Candidate: run 20 real tasks from an enforcer scan-and-repair, measure success rate and time vs Claude Code solo.

11. **Naming and branding.** "Forge" is a working name. May conflict with existing tools. Research needed before launch.

12. **Agent SDK migration path.** The spec uses CLI (`claude -p`) for subscription auth reasons (Section 5). If Anthropic adds subscription support to the Agent SDK, Forge should switch. The execution engine interface should be abstract enough to swap CLI for SDK without changing the orchestrator. Design the `ExecutionEngine` interface now.

13. **Auto mode vs manual permissions per worktree.** Claude Code's auto mode uses a Sonnet-based classifier to approve/deny actions. However, Anthropic documents `--enable-auto-mode` as a research preview requiring a Team plan and Sonnet 4.6 or Opus 4.6. Treat auto mode as an optional optimization for teams that have it, not a core dependency. Forge's foundation should work with explicit `--tools` + `--disallowedTools` on any plan tier. Auto mode can be an acceleration layer on top.

14. **Never use `--bare` for task dispatch.** Anthropic documents that `--bare` skips hooks, skills, plugins, MCP servers, auto memory, and CLAUDE.md. Forge relies on hooks firing inside worktrees (enforcer guards) and CLAUDE.md for project context. `--bare` would silently disable both. The only legitimate use of `--bare` might be for ultra-lightweight verification queries where no project context is needed.

---

## Appendix: What Forge Adds Beyond the Enforcer and Claude Code

| Need | Enforcer (bash on Claude Code) | Claude Code (native, March 2026) | Forge (orchestration layer) |
|---|---|---|---|
| Phase enforcement | Prompt instructions in /build command | Model-driven "gather, act, verify" | Harness-driven PLAN→EXECUTE→VERIFY→COMMIT |
| Multi-model | Codex bash scripts, fragile | Claude only | Native routing: Claude for code, GPT for review |
| Cross-model review | cross-verify.sh, 4-provider chain | Not available | Every commit, different family, structured |
| Parallel execution | runclaude × 7 (manual) | Agent Teams (model-coordinated) | Goroutine pool × 8 (harness-coordinated) |
| Pool management | 7 CLAUDE_CONFIG_DIRs (manual) | 1 session = 1 pool | Auto-rotation by utilization, all pools |
| Failure recovery | PostToolUseFailure hook | User re-prompts | 5-stage: classify → extract → retry brief → escalate → learn |
| Policy | 10 regex guard hooks + deny list | Permissions + auto mode classifier | `--tools` restriction + `--disallowedTools` + worktree isolation |
| Verification | verify-completion.sh (keyword+semantic) | Model decides whether to test | Deterministic: build+tests+lint+review MUST pass |
| Plan state | Markdown files, model can fabricate | Conversation history | Typed Go structs, harness-managed |
| Context management | PostCompact hook (partial restore) | Auto memory + resume | Three-tier: active/session/project per phase |
| Session learning | None | Auto memory (general) | Specific patterns injected per task |
| Cost visibility | None | None | Per-task tracking + subscription savings |
| TUI | N/A (uses Claude Code's) | Single-agent chat | Mission control: parallel + dashboard |
| Install | `bash setup.sh` (5,077 lines) | `npm install -g` | `curl \| bash` (single Go binary) |
