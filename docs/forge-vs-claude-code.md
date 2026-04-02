# Forge vs Claude Code: Honest Functional Comparison

## The OAuth Situation (Deal-Defining)

**Anthropic (Claude): Subscription OAuth is BLOCKED for third-party tools.**

Anthropic sent lawyers to OpenCode in March 2026. PR #18186 removed all Claude OAuth code. The ToS now explicitly states: "Using OAuth tokens obtained through Claude Free, Pro, or Max accounts in any other product, tool, or service -- including the Agent SDK -- is not permitted."

Community workaround plugins exist (opencode-claude-auth reads Claude Code's keychain credentials), but they violate ToS and are a ticking clock.

**To use Claude models in Forge, you MUST use the Anthropic API with an API key.** That means pay-per-token pricing:
- Opus 4.6: $5/$25 per MTok (input/output)
- Sonnet 4.6: $3/$15 per MTok
- No flat-rate subscription ceiling
- No rate-limit-based "free" usage like Claude Code Pro/Max

**OpenAI (Codex/GPT): Subscription OAuth is SUPPORTED in third-party tools.**

OpenAI officially partnered with OpenCode. ChatGPT Plus/Pro subscribers can use their subscriptions in OpenCode, OpenHands, and RooCode. OpenAI explicitly supports this flow. Codex OAuth works natively.

**What this means for Forge:**
- GPT models (GPT-5.4, GPT-5.2-Codex): free with ChatGPT subscription via OAuth ✅
- Claude models (Opus 4.6, Sonnet 4.6): API key only, pay-per-token ✅ but expensive
- OpenRouter: API key, pay-per-token + 5.5% fee, but accesses both families ✅
- Local models (Ollama, LM Studio): free, no auth needed ✅

**Bottom line:** You can't replicate Claude Code's $20/month flat-rate Claude access. But you CAN get $20/month flat-rate GPT-5.4 access (which matches Claude on benchmarks) and use Claude API for the tasks where it's specifically better (refactoring, type safety, security review).

---

## Feature-by-Feature Comparison

### Core Agent Loop

| Capability | Claude Code | OpenCode (current) | Forge (what we'd build) |
|---|---|---|---|
| Execution model | ReAct loop, single conversation | ReAct loop, client-server | **Phase machine: PLAN→EXECUTE→VERIFY→COMMIT** |
| Model skipping phases | Model can ignore prompt instructions | Model can ignore prompt instructions | **Harness controls phases -- model can't skip** |
| Plan enforcement | Prompt instructions only | Prompt instructions only | **Harness manages plan state as typed data** |
| Completion verification | None (model self-reports) | None | **Deterministic: build/test/lint + cross-model review** |
| Doom loop detection | Basic (max turns) | Basic | **Pattern detection: same error 3x, escalate** |

**Verdict: Forge wins.** This is the 22-point scaffold swing. Claude Code and OpenCode both let the model drive. Forge makes the harness drive.

### Subagent / Multi-Agent

| Capability | Claude Code | OpenCode | Forge |
|---|---|---|---|
| Subagents | Agent Teams (Task tool) | Subagent via @general | **Typed agent pool with goroutines** |
| Parallel execution | Sequential (Agent Teams are serial) | Sequential | **Parallel via goroutines + git worktrees** |
| Agent specialization | Prompt-based only | "build" + "plan" agents | **Typed roles: planner, implementer, reviewer, tester, security, documenter** |
| Supervisor | None (model self-supervises) | None | **Supervisor agent reviews all output before commit** |
| Cross-model review | None | None | **Native: different model family reviews every commit** |

**Verdict: Forge wins.** Claude Code's Agent Teams are sequential single-model. Forge runs multiple models in parallel with typed supervision.

### Context Management

| Capability | Claude Code | OpenCode | Forge |
|---|---|---|---|
| Compaction | Destructive (throws away context) | Basic compaction | **Three-tier: active (<40%) / session / project** |
| File reads | Accumulated in conversation | Accumulated | **On-demand: loaded per-phase, not accumulated** |
| Memory across sessions | Claude Memory (limited, no code context) | SQLite persistence | **Episodic memory: "last time we changed auth.ts, also needed middleware.ts"** |
| Context budget | No tracking | No tracking | **Real-time utilization tracking, auto-compact at thresholds** |
| Instruction fade-out | Real problem, no solution | No solution | **Event-driven reminders inject critical rules when patterns detected** |

**Verdict: Forge wins.** Context architecture is the biggest gap in both current tools. Each phase starting fresh with only what it needs is transformative for long sessions.

### Policy / Security

| Capability | Claude Code | OpenCode | Forge |
|---|---|---|---|
| Permission model | Allow/deny prompts or skip-all | Approval prompts | **Declarative YAML policies evaluated before API call** |
| Command blocking | Prompt-based (model can try anything) | Prompt-based | **Tree-sitter parsed command trees, structured rules** |
| Wrapper evasion | Possible (bash helper.sh hides commands) | Possible | **Parsed: bash -c "git push && rm -rf /" decomposed into sub-commands** |
| Symlink protection | None | None | **Fail-closed resolution before policy evaluation** |
| File path rules | None (or regex hooks bolted on) | None | **Per-path, per-tool, per-operation policies** |
| Sandbox | macOS sandbox-exec (opt-in) | None built-in | **OS-native: Seatbelt (macOS), Landlock (Linux)** |

**Verdict: Forge wins.** Policy is the second-biggest gap. The enforcer proved that bolted-on regex guards hit a ceiling. Native policy is the fix.

### Multi-Model / Provider

| Capability | Claude Code | OpenCode | Forge |
|---|---|---|---|
| Models available | Claude only | 75+ providers | **75+ providers (inherited from OpenCode)** |
| Smart routing | None (one model does everything) | Manual model selection | **Task-type routing: Claude for refactoring, GPT for DevOps** |
| Fallback chain | None (rate limit = wait) | Basic retry | **Circuit breakers + provider fallback: Codex→OpenRouter→Claude API→budget model** |
| Cross-model review | None | None | **Different model family reviews every change** |
| Cost tracking | None (subscription) | None | **Per-task cost tracking, budget caps** |
| OAuth (subscriptions) | Claude only ($20-200/mo) | OpenAI Codex OAuth ✅ | **OpenAI Codex OAuth ✅, Claude = API key only** |

**Verdict: Mixed.** Forge gets better routing and cross-model review. But Claude Code gets flat-rate Claude access we can't match. The question is whether GPT-5.4 via subscription + Claude API for specific tasks is cost-competitive.

### Developer Experience

| Capability | Claude Code | OpenCode | Forge |
|---|---|---|---|
| Install | `npm install -g @anthropic-ai/claude-code` | `curl \| bash` or brew/npm | **`curl \| bash` (single Go binary)** |
| TUI | Custom terminal UI | Bubble Tea (polished) | **Inherited from OpenCode** |
| Desktop app | None | Tauri v2 (macOS/Win/Linux) | **Inherited** |
| VS Code extension | None (terminal only) | Yes | **Inherited** |
| Web UI | None | Yes | **Inherited** |
| Hooks/plugins | Hooks (lifecycle events) | Plugins (tools, auth, hooks) | **Workflows + plugins** |
| Custom commands | .claude/commands/*.md | Skills + commands | **Typed workflows (Go code, not markdown)** |
| LSP integration | None | Diagnostics only | **Full: find_symbol, find_references, rename_symbol** |
| MCP servers | Yes | Yes | **Inherited** |
| Git integration | Good | Good + git-backed review | **Inherited + parallel worktrees** |

**Verdict: Forge wins on structure, ties on DX.** OpenCode's frontend ecosystem is mature. We inherit it all.

---

## The Honest "Will It Be As Good?" Assessment

### Where Forge will be BETTER than Claude Code:

1. **Structured execution.** The model can't skip phases, fabricate completion, or ignore instructions. This alone is worth the effort -- it's the #1 problem in long sessions.

2. **Multi-model.** Claude for refactoring, GPT for DevOps, cross-model review on every commit. The Milvus data: 53% solo → 80% cross-model. That's not marginal.

3. **Context management.** Each phase starts fresh. No more "I forgot what file I was editing because compaction threw it away." Progressive compaction with budget tracking.

4. **Parallel agents.** 50 tasks don't have to be sequential. Goroutines + worktrees = real parallelism.

5. **Policy enforcement.** OS-level sandbox + tree-sitter parsed policies. Not regex guards on command strings.

6. **Cost control.** GPT-5.4 via subscription ($20/mo) for most work. Claude API only where Claude is measurably better. One user tracked $5,623 equivalent API cost covered by a $100 Max subscription -- that math applies to Forge via OpenAI subscription too.

### Where Forge will be WORSE than Claude Code:

1. **Claude model quality for complex reasoning.** Claude Opus 4.6 is the best model for complex multi-file refactoring, deep security review, and documentation. Using it via API at $5/$25 per MTok is 10-50x more expensive than Claude Code Max subscription per equivalent work. For heavy Claude usage, Claude Code is simply cheaper.

2. **No extended thinking at subscription rates.** Claude Code Pro/Max subscribers get extended thinking (Opus with deep reasoning) at flat rate. Via API, extended thinking consumes 5-20x more output tokens. A single complex refactoring task could cost $2-5 via API vs pennies via subscription.

3. **Day-one polish.** Claude Code has years of iteration on prompt engineering, tool schemas, edit formats, and error handling. Our phase machine will be structurally superior but the individual prompts will need iteration.

4. **Anthropic's internal optimizations.** Claude Code likely uses prompt caching, batching, and internal routing that aren't available to API users. We don't know what we don't know here.

5. **Community and support.** Claude Code is backed by Anthropic. Forge is a fork of an open-source project. Bugs are on us.

### Where it's a WASH:

1. **SWE-bench performance.** GPT-5.4 and Claude Opus 4.6 are tied at 78.2% on SWE-bench Verified. The scaffold accounts for 22 points, the model for ~1. Forge's better scaffold should close any model gap.

2. **Terminal experience.** OpenCode's Bubble Tea TUI is excellent. Desktop app, VS Code extension, web UI are all mature.

3. **Git integration.** Both handle git well. Forge adds worktree-based parallelism.

---

## The Cost Model: When Does Forge Win?

### Scenario A: Solo developer, moderate usage (20-30 tasks/day)

```
Claude Code Max 5x:  $100/month (flat rate, Claude only)
Forge:               $20/month ChatGPT Pro (GPT-5.4 via OAuth)
                   + ~$15-30/month Claude API (for review/refactoring tasks only)
                   = $35-50/month
```
**Forge wins on cost AND gets multi-model.**

### Scenario B: Heavy user, 100+ tasks/day

```
Claude Code Max 20x: $200/month (but rate limits hit at ~900 prompts/5h)
Forge:               $200/month ChatGPT Pro (GPT-5.4 via OAuth, 1500 msgs/5h)
                   + ~$50-100/month Claude API (heavy review/refactoring)
                   = $250-300/month
```
**Claude Code wins on cost if you only need Claude. Forge wins if you need multi-model throughput.**

### Scenario C: Team of 5

```
Claude Code Max 5x × 5: $500/month
Forge:                   $100/month ChatGPT Pro × 5
                       + ~$100/month Claude API (shared, pooled)
                       + free OpenRouter for budget tasks
                       = $200/month
```
**Forge wins on cost and flexibility.**

### Scenario D: CI/CD pipeline (headless, high volume)

```
Claude Code: headless mode shares subscription limits, burns through fast
Forge:       API keys with pay-per-token, scales linearly, no rate limit walls
```
**Forge wins on scalability.**

---

## The Real Question: Is the Scaffold Worth the Model Trade-Off?

The data says yes:
- Same model, basic scaffold = 23%. Optimized scaffold = 45%+. That's a 22-point swing.
- The model swap from Claude to GPT is ~1 point on SWE-bench at the frontier.
- Cross-model review raises bug detection from 53% to 80%.
- Parallel execution on 50 tasks = 5-10x faster throughput.

The only scenario where Claude Code definitively wins is: you need extended thinking on Opus for complex reasoning tasks, at high volume, and cost matters. For that specific workload, $100-200/month flat-rate is unbeatable.

For everything else -- structured execution, multi-model routing, parallel agents, policy enforcement, context architecture -- the scaffold wins.

---

## Recommendation

**Build Forge.** Fork OpenCode. Use OpenAI Codex OAuth for flat-rate GPT-5.4 access. Use Claude API for the 20% of tasks where Claude is measurably better (refactoring, security, docs). Use the saved money to fund the API calls.

The scaffold is the product. The model is a commodity.
