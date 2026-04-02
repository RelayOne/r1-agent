# Project Forge: Architecture for a Better AI Coding Harness

## The Thesis

SWE-bench Pro proves it: same model, basic scaffold = 23%. Same model, optimized scaffold = 45%+. The scaffold accounts for a 22-point swing. Model swaps account for ~1 point at the frontier. **The harness IS the product.**

Five independent teams converged on the same finding in early 2026:

- **OpenAI (Harness Engineering):** 1M lines of code, 1,500 PRs, 3 engineers, 3.5 PRs/engineer/day. "The primary job of our engineering team became enabling the agents to do useful work."
- **OPENDEV (arxiv 2603.05344):** Dual-agent architecture, workload-specialized model routing, adaptive context compaction, automated memory, event-driven reminders
- **Anthropic (Carlini compiler):** 16 parallel Opus agents, 2,000 sessions, 100K-line C compiler. "Most of my effort went into designing the environment around Claude."
- **Vasilopoulos (2026):** 283 development sessions, three-tier context infrastructure on 108K-line codebase
- **Milvus code review study:** Multi-model debate raises bug detection from 53% to 80%

They all found the same four pillars: context architecture, agent specialization, persistent memory, structured execution. Performance degrades beyond ~40% context utilization. Better models make harness engineering MORE important, not less.

---

## What's Wrong With Claude Code (as a harness)

### 1. Context is disposable
Compaction throws away information mid-task. The model forgets files it read 20 turns ago. PostCompact hooks help but can't recover what's been compressed. There's no tiered memory system -- everything is conversation history or nothing.

### 2. Single-agent bottleneck
Every task runs sequentially through one model. Agent Teams exist but are bolted on. There's no native task queue, no parallel execution, no shared workspace state between agents.

### 3. All-or-nothing permissions
Either constant approval prompts or `--dangerously-skip-permissions`. No per-path, per-tool, per-operation granularity. Our enforcer built 10 hooks and 22 deny rules to approximate what should be a policy engine.

### 4. Fixed tool set
Can't add custom tools, can't modify tool behavior, can't intercept tool calls before they reach the model. Hooks are reactive (fire after events), not proactive (shape behavior before events).

### 5. No model routing
One model does everything: planning, coding, review, documentation, debugging. OPENDEV uses five model roles with fallback chains. The benchmarks show Claude dominates refactoring (4.9/5) while GPT dominates DevOps (4.7/5). A good harness routes to the right model.

### 6. No structured execution
No phases. No checkpoints. No "plan then execute then verify" loop. Our enforcer bolted this on with commands, but it's prompt instructions, not runtime enforcement. The model can skip phases because nothing structurally prevents it.

---

## Architecture: Four Layers

```
┌─────────────────────────────────────────────────────────────────┐
│                        USER INTERFACE                           │
│  Terminal TUI  │  Headless CLI  │  Web UI  │  Editor Extension  │
└────────────────────────────┬────────────────────────────────────┘
                             │
┌────────────────────────────▼────────────────────────────────────┐
│                     ORCHESTRATOR (the brain)                    │
│                                                                 │
│  ┌──────────┐  ┌───────────┐  ┌──────────┐  ┌───────────────┐  │
│  │ Workflow  │  │   Model   │  │ Context  │  │    Policy     │  │
│  │ Engine   │  │  Router   │  │ Manager  │  │    Engine     │  │
│  └──────────┘  └───────────┘  └──────────┘  └───────────────┘  │
│                                                                 │
│  ┌──────────┐  ┌───────────┐  ┌──────────┐  ┌───────────────┐  │
│  │  Agent   │  │  Memory   │  │ Artifact │  │   Provider    │  │
│  │  Pool    │  │  Store    │  │ Manager  │  │   Manager     │  │
│  └──────────┘  └───────────┘  └──────────┘  └───────────────┘  │
└────────────────────────────┬────────────────────────────────────┘
                             │
┌────────────────────────────▼────────────────────────────────────┐
│                     TOOL EXECUTION LAYER                        │
│  Sandbox │ File Ops │ Shell │ LSP │ Git │ Web │ MCP │ Custom   │
└────────────────────────────┬────────────────────────────────────┘
                             │
┌────────────────────────────▼────────────────────────────────────┐
│                     PERSISTENCE LAYER                           │
│  Session DB │ Memory Store │ Plan Files │ Git History │ Metrics │
└─────────────────────────────────────────────────────────────────┘
```

---

## Layer 1: Orchestrator

### 1.1 Workflow Engine

The core loop. Not a ReAct loop -- a structured phase machine with enforced transitions.

```
PLAN → EXECUTE → VERIFY → COMMIT → (next task or DONE)
```

Each phase is a discrete API call with its own system prompt, tool set, and model. The model cannot skip phases because the harness controls which phase runs next. This is the fundamental difference from Claude Code, where phases are prompt instructions the model can ignore.

```typescript
interface Phase {
  name: string;
  model: ModelSpec;                    // which model runs this phase
  tools: Tool[];                       // tools available in this phase
  systemPrompt: PromptTemplate;        // phase-specific instructions
  maxTurns: number;                    // hard turn limit
  exitCondition: (state: TaskState) => PhaseResult;  // structured exit
  onFailure: FailureHandler;           // what to do on failure
}

interface Workflow {
  phases: Phase[];
  transitions: Map<string, TransitionRule>;  // PLAN->EXECUTE requires plan artifact
  checkpoints: CheckpointPolicy;             // git commit between phases
}
```

**Why this matters:** When the PLAN phase produces a plan artifact, the EXECUTE phase receives it as structured input, not as conversation history that gets compacted away. Each phase starts with a fresh context window containing only what it needs.

### 1.2 Model Router

Route tasks to the model best suited for each workload. Based on confirmed benchmarks:

```typescript
interface ModelRouter {
  route(task: TaskSpec): ModelSpec;
}

// Routing table (from Milvus, YUV.AI, Terminal-Bench):
const ROUTES = {
  plan:          { primary: 'claude-opus',   reason: 'best at ambiguous prompts' },
  refactor:      { primary: 'claude-opus',   reason: '4.9/5 refactoring' },
  typeSafety:    { primary: 'claude-opus',   reason: '4.7/5 vs 4.2/5' },
  docs:          { primary: 'claude-opus',   reason: '4.9/5 vs 4.4/5' },
  architecture:  { primary: 'gpt-5.4',      reason: '4.8/5 vs 4.3/5' },
  devops:        { primary: 'gpt-5.4',      reason: 'Terminal-Bench 77.3% vs 65.4%' },
  concurrency:   { primary: 'gpt-5.4',      reason: 'Claude blind spot: 0/2 in Milvus' },
  review:        { primary: 'gpt-5.4',      fallback: 'claude-opus', reason: 'GPT recall + Claude precision' },
  security:      { primary: 'claude-opus',   reason: '53% vs 33% solo detection' },
};
```

With provider fallback chain: primary model → OpenRouter (built-in sub-routing) → alternate family → budget model → lint-only.

### 1.3 Context Manager

**The #1 problem in long-running AI sessions.** Performance degrades beyond ~40% context utilization. The solution is a three-tier context architecture:

```
Tier 1: ACTIVE CONTEXT (in the API call)
  - Current phase system prompt
  - Current task specification
  - Relevant file contents (loaded on demand, not accumulated)
  - Working memory from Memory Store
  - Recent tool results (last 3-5)
  Target: <40% of context window

Tier 2: SESSION STATE (on disk, loaded into Tier 1 on demand)
  - Full plan with task statuses
  - All file paths read/written this session
  - Build/test/lint results
  - Error history (what failed and why)

Tier 3: PROJECT KNOWLEDGE (persistent across sessions)
  - Project map (file inventory, structure)
  - CLAUDE.md / AGENTS.md conventions
  - Memory store (learned patterns, gotchas)
  - Decision log (why choices were made)
  - Git history summary
```

**Key insight from OPENDEV:** Compaction should be *progressive*, not destructive. When Tier 1 is full, compress the oldest tool results first (they're the least relevant). Then compress file reads (summarize what was found). Never compress the task spec or system prompt.

```typescript
interface ContextManager {
  // Budget tracking
  currentUtilization(): number;          // 0.0 - 1.0
  budget: { target: 0.40, hard_cap: 0.70, emergency: 0.90 };

  // Progressive compaction
  compact(level: 'gentle' | 'moderate' | 'aggressive'): void;
  // gentle:     truncate tool outputs >500 lines to summary
  // moderate:   compress file reads to "read X, found Y"
  // aggressive: summarize entire conversation to structured state

  // Tier promotion/demotion
  promote(key: string): void;   // Tier 2/3 → Tier 1
  demote(key: string): void;    // Tier 1 → Tier 2
}
```

### 1.4 Policy Engine

Replaces: settings.json deny rules + 10 bash guard hooks + permissions layer.

Policies are declarative rules evaluated at the orchestrator level, BEFORE tool calls reach the model. Not regex on command strings -- structured rules on structured tool calls.

```typescript
interface Policy {
  match: ToolMatcher;       // which tool calls this applies to
  decision: 'allow' | 'deny' | 'prompt' | 'audit';
  conditions?: Condition[]; // optional: only when conditions are met
  reason: string;
}

// Examples:
const policies: Policy[] = [
  // Allow all file reads
  { match: { tool: 'read_file' }, decision: 'allow', reason: 'reads are safe' },

  // Allow writes to source, deny writes to config
  { match: { tool: 'write_file', pathGlob: 'src/**' }, decision: 'allow', reason: 'source code' },
  { match: { tool: 'write_file', pathGlob: '.claude/**' }, decision: 'deny', reason: 'enforcer files' },
  { match: { tool: 'write_file', pathGlob: '*.config.*' }, decision: 'prompt', reason: 'config change' },

  // Allow specific bash commands, deny dangerous ones
  { match: { tool: 'bash', commandPrefix: ['git', 'status'] }, decision: 'allow', reason: 'read-only git' },
  { match: { tool: 'bash', commandPrefix: ['git', 'push'] }, decision: 'deny', reason: 'orchestrator handles push' },
  { match: { tool: 'bash', commandPrefix: ['rm', '-rf'] }, decision: 'deny', reason: 'destructive' },

  // Symlink resolution happens BEFORE policy evaluation
  // No regex needed -- the policy engine resolves the real path first
];
```

**Why this is better than hooks:** Hooks fire after the tool call is dispatched. The policy engine evaluates before the API call is made. The model never sees denied tools in its available set. A model can't use `./helper.sh` to hide `git push` because the bash tool executor parses the command tree (like Codex's tree-sitter approach) and evaluates each sub-command against policy.

### 1.5 Agent Pool

Multiple agents running in parallel, each with its own context window, coordinated through shared state.

```typescript
interface AgentPool {
  // Spawn agents with specific capabilities
  spawn(spec: AgentSpec): Agent;

  // Coordination
  sharedState: SharedWorkspace;     // files, git state, build results
  taskQueue: TaskQueue;             // prioritized work items
  resultCollector: ResultCollector; // gather outputs from parallel agents

  // Supervision
  supervisor: SupervisorAgent;      // reviews agent outputs before commit
}

// Agent roles (from the enforcer's operational experience):
type AgentRole =
  | 'planner'        // reads codebase, produces structured plan
  | 'implementer'    // executes one task, commits
  | 'reviewer'       // reviews a diff, produces verdict
  | 'tester'         // writes/fixes tests
  | 'security'       // security-focused review
  | 'documenter'     // updates docs after changes
  ;
```

**Parallel execution model:** For a plan with 50 tasks, instead of running sequentially (Claude Code today), group independent tasks and run them in parallel branches (git worktrees). Each agent gets a fresh context with only its task. Supervisor merges results.

---

## Layer 2: Tool Execution

### 2.1 Sandbox

Native OS-level sandboxing, not regex guards. Options by platform:

- **macOS:** Seatbelt profiles (what Codex CLI uses)
- **Linux:** Landlock + seccomp (what Codex CLI uses)
- **Docker:** Container isolation for CI/headless

The sandbox is the FIRST enforcement layer. Regex guards become unnecessary when the OS prevents the operation.

### 2.2 File Operations

Same tools as Claude Code (read, write, edit, glob, grep) but with LSP integration:

- **find_symbol:** Jump to definition across the codebase
- **find_references:** Find all usages of a symbol
- **rename_symbol:** Workspace-wide semantic rename
- **diagnostics:** Get type errors, lint errors without running the full build

LSP gives the agent the same understanding of code structure that a human IDE user has. OPENDEV implements this with a four-layer abstraction. It's the single biggest tool improvement over Claude Code.

### 2.3 Shell Execution

Six-stage execution (from OPENDEV):
1. Policy check (parsed command tree, not string regex)
2. Approval (if policy says 'prompt')
3. Sandbox enforcement (OS-level)
4. Execution with timeout
5. Output capture + truncation
6. Background task management (long-running builds, test suites)

Background tasks are critical. `npm test` might take 60 seconds. The agent shouldn't block -- it should start the test, continue working, and check results when needed.

---

## Layer 3: Memory and Persistence

### 3.1 Dual Memory Architecture (from OPENDEV)

**Episodic Memory:** Project-specific knowledge accumulated across sessions.
- "Last time we changed auth.ts, we also needed to update middleware.ts"
- "The test suite takes 45 seconds to run"
- "API rate limits hit at ~50 requests/minute"

Stored as structured entries with relevance scores. Injected into context when relevant (not always).

**Working Memory:** Current session state.
- What files have been read/modified
- What build/test results showed
- What tasks are complete/pending
- What errors have occurred and how they were resolved

Persisted to disk every N turns. Survives crashes and rate limits.

### 3.2 Event-Driven Reminders (from OPENDEV)

The model forgets instructions over long sessions ("instruction fade-out"). Reminders inject critical rules when specific events are detected:

```typescript
interface Reminder {
  trigger: EventDetector;     // when to fire
  template: string;           // what to inject
  priority: number;           // context budget priority
  maxFirings: number;         // don't spam
}

// Examples:
{ trigger: 'file_write_to_test', template: 'TEST RULES: never weaken assertions...', priority: 90 },
{ trigger: 'context_above_60pct', template: 'COMPACT: context is filling up...', priority: 95 },
{ trigger: 'error_repeated_3x', template: 'STUCK: you have hit this error 3 times...', priority: 100 },
{ trigger: 'task_running_20min', template: 'TIMEOUT: this task is taking too long...', priority: 85 },
```

### 3.3 Plan as First-Class Artifact

Not conversation history. Not a markdown file the model might forget to update. A structured data store that the harness manages:

```typescript
interface Plan {
  tasks: Task[];
  metadata: {
    source: string;           // which spec generated this
    created: Date;
    lastCheckpoint: string;   // git commit hash
  };
}

interface Task {
  id: string;
  description: string;
  status: 'pending' | 'active' | 'done' | 'failed' | 'blocked';
  commit?: string;            // hash when done
  dependencies: string[];     // task IDs that must complete first
  files: string[];            // files this task will modify (for parallel scheduling)
  attempts: Attempt[];        // history of tries
}
```

The harness updates task status, not the model. When a task commit succeeds, the harness marks it done. When it fails, the harness marks it failed with the error. The model can't fabricate completion because it doesn't control the plan state.

---

## Layer 4: Quality Control

### 4.1 ROI Filter (from enforcer)

Applied automatically at every layer. Users rubber-stamp everything, so the system must filter.

```typescript
interface ROIFilter {
  classify(finding: Finding): 'fix' | 'fix_if_easy' | 'drop';

  // Tier 1 AUTO-FIX: security, data loss, breaking bugs, scaling blockers
  // Tier 2 FIX IF REASONABLE: reliability, test gaps on critical paths
  // Tier 3 AUTO-DROP: style, theory, pattern migrations, marginal gains

  // "Nothing to fix" is valid. Offer to lower threshold.
}
```

### 4.2 Cross-Model Verification

Every agent output is reviewed by a different model family before commit. The Milvus data: solo detection 53%, cross-model debate 80%.

```
Implementer (Claude) → Reviewer (GPT) → Commit
Implementer (GPT)    → Reviewer (Claude) → Commit
```

### 4.3 Deterministic Checks

Always run. Can't be fooled by clever phrasing.
- Build passes
- Tests pass
- Lint passes
- Commit hash exists and modifies the claimed files
- No TODO/FIXME/placeholder introduced
- No type bypasses (@ts-ignore, `as any`)

---

## Implementation Strategy

### Phase 1: Minimum Viable Harness (2-3 weeks)

Build the orchestrator core in TypeScript/Node.js. Target: run a structured plan against the Anthropic API with phase enforcement.

```
Week 1: Workflow engine + context manager + file tools
         Can execute: read files → plan → execute tasks → verify
Week 2: Policy engine + model router + Codex integration
         Can route: Claude for code, GPT for review
Week 3: Memory store + session persistence + parallel agents
         Can survive: rate limits, crashes, multi-session work
```

### Phase 2: Production Hardening (2-3 weeks)

Sandbox integration, LSP tools, background tasks, cleanup system, web UI.

### Phase 3: Self-Improvement Loop

Use the harness to build the harness. Benchmark against Claude Code on real tasks. Instrument everything. Iterate on the scaffold until the numbers prove it's better.

---

## Technology Choices

Applying the enforcer's own principle: **LTS/stable over hype.**

- **Language:** Go (single static binary, trivial cross-compilation, goroutines for parallel agents)
- **Why Go:** OpenCode is Go. Same language means fork/contribute/share code without a language boundary. Go is the language of developer tools (Docker, K8s, Terraform, Codex CLI is Rust but same philosophy). `GOOS=darwin GOARCH=arm64 go build` and you have a macOS binary. No runtime, no npm, no node_modules.
- **API clients:** net/http with structured request/response types (Anthropic, OpenAI, OpenRouter all REST)
- **Sandbox:** OS-native (Seatbelt/Landlock) via exec.Command with platform detection
- **LSP:** go-lsp (or fork OpenCode's existing LSP client)
- **Shell parsing:** tree-sitter Go bindings (smacker/go-tree-sitter) for command decomposition
- **Storage:** SQLite via modernc.org/sqlite (pure Go, no CGo, zero external deps)
- **TUI:** Bubble Tea (what OpenCode uses, mature, battle-tested)
- **Git:** go-git for in-process operations, exec.Command to native git for complex ops

Ships as one binary. `curl -fsSL install.sh | bash` and you're done.

---

## What This Replaces From the Enforcer

| Enforcer (5,077 lines of bash) | Forge (native Go) |
|---|---|
| 10 bash guard hooks | Policy engine with tree-sitter command parsing |
| Regex command matching | Structured rules on parsed command trees |
| settings.json deny list | Declarative policy files (YAML) |
| PostCompact hook to restore state | Three-tier context with progressive compaction |
| /build command (3000 lines of markdown) | Workflow engine with enforced phase machine |
| /scope + /scan-and-repair commands | Built-in workflows as Go code |
| Codex review/execute bash scripts | Native multi-model routing with goroutines |
| cleanup.sh | Artifact manager built into session lifecycle |
| runclaude + env parsing | Native config with OS sandbox integration |
| Subagent supervision hooks | Parallel agent pool with typed supervisor |
| Cross-verify bash script | Native cross-model verification pipeline |
| detect-self-skip keyword/semantic grep | Plan state managed by harness (model can't fabricate) |
| ROI filter in prompt instructions | ROI filter in workflow engine (findings never reach model) |

The enforcer was 5,000+ lines of bash patching over someone else's runtime. Forge is a purpose-built Go runtime where every enforcer lesson is a compiled, tested function.

---

## Fork vs Build: The Decision

### Option A: Fork OpenCode (MIT Licensed, Go)

**What it is:** 90K+ GitHub stars, 640+ contributors, client-server architecture, MIT licensed. Built by the SST team. Go backend, Bubble Tea TUI, Tauri desktop app, SolidJS web UI.

**What it already has:**
- Multi-provider support (75+ providers including Anthropic, OpenAI, Gemini, local models)
- Client-server architecture (Go backend serves HTTP/SSE, multiple frontends connect)
- LSP integration (diagnostics exposed to AI, full protocol support underneath)
- Plugin system (custom tools, auth providers, hooks)
- Session management with SQLite persistence
- Dual agents: "build" (full access) and "plan" (read-only)
- Git-backed session review (uncommitted changes, branch diffs)
- Desktop app (Tauri v2), VS Code extension, web interface, TUI (Bubble Tea)
- MCP server support, skills system, custom commands
- OpenAPI spec with auto-generated SDK

**What it's missing (what we'd add):**
- Workflow engine (structured phases, not just ReAct loop)
- Model routing by task type (it has multi-model, but no smart routing)
- Progressive context compaction (it has basic compaction)
- Policy engine (structured rules on parsed command trees, not string matching)
- Cross-model verification pipeline
- ROI filter on findings
- Parallel agent pool with supervisor
- Event-driven reminders (instruction fade-out prevention)
- Deterministic checks layer (build/test/lint enforcement)

**Risk:** OpenCode is a fast-moving project (90K stars, active development). Maintaining a fork means merging upstream changes. But the MIT license means we can take what we need.

### Option B: Fork Aider (Apache 2.0, Python)

**Verdict:** Wrong language, wrong architecture, wrong scope. Aider is a mature interactive pair-programmer, not an autonomous orchestration system. Python, single-process, no plugin system, no client-server split.

### Option C: Build from Scratch (Go)

**Pros:** Total control, clean architecture, no legacy constraints.
**Cons:** 8-10 weeks to feature parity with OpenCode's basics. Rebuilding solved problems.

### Option D: Fork OpenCode + Graft Forge Orchestrator (RECOMMENDED)

**The play:** Fork OpenCode. Add the Forge orchestrator as a new package inside the monorepo. Replace the agent loop with the workflow engine. Keep everything else.

```
opencode/               (forked)
  packages/
    opencode/            ← existing Go backend (tools, providers, LSP, git, session DB)
      src/
        agent/           ← REPLACE: swap ReAct loop for Forge workflow engine
        tool/            ← KEEP: file ops, shell, LSP, MCP, web
        provider/        ← KEEP + EXTEND: add model router layer
        session/         ← KEEP + EXTEND: add three-tier context
        server/          ← KEEP: HTTP/SSE API
    forge/               ← NEW Go package
      workflow/          # Phase machine with enforced transitions
      context/           # Three-tier context manager with budget tracking
      policy/            # Declarative policy engine with tree-sitter
      router/            # Model routing by task type + fallback chains
      pool/              # Parallel agent pool with supervisor
      memory/            # Episodic + working memory
      verify/            # Cross-model verification pipeline
      roi/               # Impact-effort filter
      reminders/         # Event-driven instruction refresh
    app/                 ← KEEP: SolidJS shared UI components
    desktop/             ← KEEP: Tauri desktop app
    web/                 ← KEEP: documentation site
    sdk/                 ← KEEP: TypeScript SDK for external integrations
```

**Why fork, not orchestrate externally:**
- No language boundary. Forge is Go calling Go functions, not TypeScript calling HTTP.
- We replace the agent loop at the source, not by wrapping it.
- Policy enforcement happens INSIDE the tool execution path, not outside it.
- Context management controls what goes into the API call, not what comes back.
- Single binary. `go build` produces one executable with everything.

**What we keep from OpenCode (70% of the codebase):**
- Tool system (file ops, shell execution, LSP, MCP, web)
- Provider management (auth, streaming, rate limit tracking)
- Session persistence (SQLite, message history)
- Git operations (worktrees, diff, commit, review)
- TUI (Bubble Tea), desktop (Tauri), VS Code extension
- Plugin system, skills, custom commands
- HTTP/SSE server for frontend communication

**What we replace (the agent core, ~15% of the codebase):**
- Agent loop → Workflow engine (phase machine)
- Simple compaction → Three-tier progressive context manager
- Permission prompts → Declarative policy engine
- Single model → Model router with task-type routing
- Sequential execution → Parallel agent pool

**What we add (Forge packages, ~15% new code):**
- Cross-model verification pipeline
- ROI filter
- Event-driven reminders
- Episodic memory system
- Deterministic checks layer

**Effort estimate:**
- Week 1-2: Fork OpenCode, build forge/ package, replace agent loop with workflow engine
- Week 3: Policy engine + model router + context manager
- Week 4: Agent pool (parallel via goroutines + git worktrees) + cross-verify
- Week 5: Memory system + event-driven reminders + ROI filter
- Week 6: Port enforcer commands/personas as Forge workflows + benchmark vs Claude Code

---

## Implementation Plan: Phase 1 (Weeks 1-2)

### Week 1: Fork + Forge Core

**Day 1: Fork and build**
```bash
git clone https://github.com/sst/opencode.git forge
cd forge
go build -o forge ./packages/opencode
./forge  # verify it runs
```

**Day 2-3: Workflow engine** (`forge/workflow/`)
```go
package workflow

type Phase struct {
    Name          string
    Model         ModelSpec           // which model runs this phase
    Tools         []string            // tool names available in this phase
    SystemPrompt  string              // phase-specific instructions
    MaxTurns      int                 // hard turn limit
    ExitCondition func(TaskState) PhaseResult
    OnFailure     FailureHandler
}

type Engine struct {
    phases      []Phase
    transitions map[string]TransitionRule  // PLAN->EXECUTE requires plan artifact
    current     int
    state       *TaskState
}

// Run executes the workflow: PLAN → EXECUTE → VERIFY → COMMIT
// The model cannot skip phases because the engine controls which phase runs next.
func (e *Engine) Run(ctx context.Context, task TaskSpec) error {
    for e.current < len(e.phases) {
        phase := e.phases[e.current]
        result, err := e.runPhase(ctx, phase)
        if err != nil { return e.handleFailure(phase, err) }

        next, ok := e.transitions[phase.Name]
        if !ok { break }
        if !next.Satisfied(result) {
            return fmt.Errorf("phase %s did not produce required artifact", phase.Name)
        }
        e.current++
    }
    return nil
}
```

**Day 4-5: Context manager** (`forge/context/`)
```go
package context

type Tier int
const (
    Active  Tier = iota  // In the API call (<40% of window)
    Session              // On disk, promoted on demand
    Project              // Persistent across sessions
)

type Manager struct {
    budget   Budget        // target: 0.40, hard_cap: 0.70
    active   []ContextItem // currently in the API call
    session  *SessionStore // disk-backed session state
    project  *ProjectStore // persistent memory
}

// Compact progressively reduces context when budget is exceeded.
// Gentle: truncate tool outputs. Moderate: summarize file reads. Aggressive: full state summary.
func (m *Manager) Compact(level CompactLevel) {
    switch level {
    case Gentle:
        m.truncateToolOutputs(500) // lines
    case Moderate:
        m.summarizeFileReads()
    case Aggressive:
        m.summarizeToState()
    }
}

// PrepareContext assembles the API call with only what this phase needs.
// Each phase starts with a fresh context -- no accumulated history.
func (m *Manager) PrepareContext(phase Phase, task TaskSpec) []Message {
    msgs := []Message{
        {Role: "system", Content: phase.SystemPrompt},
    }
    // Add task spec
    msgs = append(msgs, Message{Role: "user", Content: task.Render()})
    // Add relevant memory from project store
    for _, mem := range m.project.Relevant(task) {
        msgs = append(msgs, Message{Role: "system", Content: mem.Render()})
    }
    // Add working memory from session
    msgs = append(msgs, m.session.WorkingMemory()...)
    return msgs
}
```

### Week 2: Policy + Router

**Day 1-2: Policy engine** (`forge/policy/`)
```go
package policy

type Engine struct {
    rules  []Rule
    parser *treesitter.Parser  // for bash command decomposition
}

// Evaluate checks a tool call against all rules BEFORE it reaches the model.
// Returns deny with reason, or allow.
func (e *Engine) Evaluate(call ToolCall) Decision {
    // For bash commands: parse the command tree, evaluate each sub-command
    if call.Tool == "bash" {
        commands := e.parser.Parse(call.Input.Command)
        for _, cmd := range commands {
            if d := e.evaluateCommand(cmd); d.Action == Deny {
                return d
            }
        }
    }
    // For file operations: resolve symlinks first, then match path rules
    if call.Tool == "write_file" || call.Tool == "edit_file" {
        realPath := resolveSymlink(call.Input.Path) // fail-closed
        call.Input.Path = realPath
    }
    return e.matchRules(call)
}
```

**Day 3-4: Model router** (`forge/router/`)
```go
package router

type Router struct {
    routes    map[TaskType]ModelSpec
    fallbacks map[string][]ModelSpec  // provider → fallback chain
    circuits  map[string]*CircuitBreaker
}

// Route selects the best model for a task type, with fallback.
func (r *Router) Route(taskType TaskType) (ModelSpec, error) {
    primary := r.routes[taskType]
    if r.circuits[primary.Provider].IsOpen() {
        return r.fallback(primary.Provider)
    }
    return primary, nil
}
```

**Day 5: Integration test**
- Fork builds and runs
- Workflow engine executes PLAN → EXECUTE → VERIFY → COMMIT
- Policy engine blocks dangerous commands via tree-sitter
- Model router selects Claude for implementation, GPT for review
- Single binary: `go build -o forge && ./forge`

