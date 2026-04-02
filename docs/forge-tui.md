# Forge TUI: Mission Control for AI Agents

## What Claude Code's TUI Gets Right

Claude Code feels good because:
- **Streaming tokens** feel alive -- you see the model thinking
- **Tool use is visible** -- you see "Reading src/auth.ts..." → content → reasoning
- **Diffs are inline** -- you see exactly what changed, in context
- **It's fast** -- Bubble Tea renders at 60fps, no jank
- **One key to approve** -- 'y' to accept, muscle memory
- **Compact mode** -- reduce noise when you know what you're doing
- **Color is meaningful** -- green for success, red for errors, dim for context

## What Claude Code's TUI Gets Wrong

- **One agent, one view** -- you can't see what's happening in parallel
- **No progress** -- 50 tasks, no idea how far along you are
- **No rate limits** -- "rate limited" spinner, no ETA, no pool status
- **No cost** -- burning money with no visibility
- **No history** -- scroll up and it's gone after compaction
- **No retry context** -- when it fails, you manually copy the error and re-prompt
- **No verification** -- you have to manually check "did it actually work?"

## Forge's TUI: Two Modes

### Mode 1: Focus Mode (Default)

Looks and feels like Claude Code, but smarter. This is what you see 90% of the time.

```
┌─────────────────────────────────────────────────────────────────────┐
│ ⚡ FORGE  main ⟫ genads  ◆ 12/47 tasks  ● 3 agents  $4.23       │
│ ▸ Claude①②③ ● GPT④ ● idle⑤⑥⑦  │ rate: ████░░ 62%  ◷ 2h left  │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│ ▸ TASK-12: Add rate limiting middleware              [attempt 1/3] │
│   Agent: Claude ① (Max 20x, pool 1)                                │
│   Phase: EXECUTE ▸▸▸▸░ (4/5 phases)                               │
│                                                                     │
│   Reading src/middleware/index.ts...                                │
│   ┌─ src/middleware/rateLimit.ts (new) ────────────────────────┐   │
│   │  import rateLimit from 'express-rate-limit';               │   │
│   │  import RedisStore from 'rate-limit-redis';                │   │
│   │                                                             │   │
│   │  export const apiLimiter = rateLimit({                      │   │
│   │    store: new RedisStore({ client: redis }),                │   │
│   │    windowMs: 15 * 60 * 1000,                                │   │
│   │    max: 100,                                                │   │
│   │+   standardHeaders: true,                                   │   │
│   │+   legacyHeaders: false,                                    │   │
│   │  });                                                        │   │
│   └─────────────────────────────────────────────────────────────┘   │
│                                                                     │
│   Running npm test...  ████████░░ 80%  (12/15 suites)              │
│                                                                     │
│ ┌─ Parallel ──────────────────────────────────────────────────────┐ │
│ │ TASK-13: Add CORS config          Claude② VERIFY ✓             │ │
│ │ TASK-14: Fix auth token refresh   GPT④    EXECUTE ▸▸░          │ │
│ └──────────────────────────────────────────────────────────────────┘ │
│                                                                     │
├─────────────────────────────────────────────────────────────────────┤
│ ❯ Type a message, or: Tab=dashboard  ?=help  p=pause  s=skip     │
└─────────────────────────────────────────────────────────────────────┘
```

**What's happening here:**

The top bar is always visible: branch, project, task progress (12/47), active agents (3 of 7 subs), session cost, rate limit across all pools.

The main area shows the ACTIVE task with streaming output -- same feel as Claude Code. You see the file being created, the diff, the test run. This is where your eyes go.

The parallel bar at the bottom shows other agents working simultaneously. Just one line each: task, agent, phase, status. Expand with ↓ to see details.

The input bar works like Claude Code: type a message to intervene, or use keyboard shortcuts.

### Mode 2: Dashboard Mode (Tab to toggle)

The orchestration view. See everything at once.

```
┌─────────────────────────────────────────────────────────────────────┐
│ ⚡ FORGE DASHBOARD  main ⟫ genads             ◷ Session: 1h 42m   │
├──────────────────────────┬──────────────────────────────────────────┤
│ TASKS              12/47 │ AGENTS                                   │
│                          │                                          │
│ ✓ TASK-1  Auth setup     │ Claude ①  TASK-12  EXECUTE   ██░ 62%   │
│ ✓ TASK-2  DB migration   │ Claude ②  TASK-13  VERIFY    ███ 100%  │
│ ✓ TASK-3  User model     │ Claude ③  idle     ─                    │
│ ✓ TASK-4  Auth routes    │ GPT    ④  TASK-14  EXECUTE   █░░ 40%   │
│ ✓ TASK-5  Tests          │ Claude ⑤  idle     ─                    │
│ ...                      │ Claude ⑥  idle     ─                    │
│ ✓ TASK-11 Logging        │ Claude ⑦  idle     ─                    │
│ ▸ TASK-12 Rate limit  ① │                                          │
│ ▸ TASK-13 CORS        ② │ POOLS                                    │
│ ▸ TASK-14 Token fix   ④ │ ① Max20x  ████████░░  78%  resets 1:22 │
│ ○ TASK-15 Error handle   │ ② Max20x  ████░░░░░░  42%  resets 3:01 │
│ ○ TASK-16 Validation     │ ③ Max20x  ██░░░░░░░░  18%  resets 4:15 │
│ ○ TASK-17 Pagination     │ ④ GPT Pro ██████░░░░  58%  resets 2:40 │
│ ...                      │ ⑤ Max5x   ░░░░░░░░░░   0%  full       │
│ ✗ TASK-8  Cache (2/3)    │ ⑥ Max5x   ░░░░░░░░░░   0%  full       │
│                          │ ⑦ Max5x   ░░░░░░░░░░   0%  full       │
├──────────────────────────┼──────────────────────────────────────────┤
│ LEARNED THIS SESSION     │ COST & SPEED                            │
│                          │                                          │
│ ● Request type: use      │ Tasks completed: 12                      │
│   declaration merging    │ Tasks failed:     1 (TASK-8, retrying)  │
│   in src/types/          │ Avg time/task:    3.2 min                │
│ ● Redis client: import   │ Session cost:     $4.23 (API calls)     │
│   from src/lib/redis.ts  │ Sub cost equiv:   ~$67 (if API-only)    │
│ ● Test runner: vitest    │ Savings:          $62.77 (93% off)      │
│   not jest               │                                          │
│                          │ Cross-model reviews: 12                  │
│ RECENT EVENTS            │ Bugs caught by review: 3                 │
│ 14:23 TASK-13 verified ✓ │ Retries: 4 (3 succeeded)                │
│ 14:22 TASK-12 tests run  │                                          │
│ 14:21 TASK-8  retry 2/3  │                                          │
│ 14:20 TASK-11 committed  │                                          │
│ 14:18 Learning: redis    │                                          │
└──────────────────────────┴──────────────────────────────────────────┘
```

**What's happening here:**

Left column: task list with status. ✓ done, ▸ active, ○ pending, ✗ failed. One glance shows progress.

Right top: agents and their current assignments. Which subscription is doing what. Which pools are filling up, when they reset. Forge uses this to route: when pool ① hits 80%, shift new tasks to pool ③ (18%).

Right bottom: cost tracking. Not just "you spent $4.23" but "this would have cost $67 via API, your subscriptions saved you 93%." That's the number that justifies the setup.

Left bottom: session learning (what Forge has learned about this codebase) and recent events (chronological log).

### Mode 3: Detail View (Enter on any task)

Drill into a specific task to see its full history.

```
┌─────────────────────────────────────────────────────────────────────┐
│ TASK-8: Implement Redis caching layer                    attempt 2 │
├─────────────────────────────────────────────────────────────────────┤
│ ATTEMPT 1 ✗ (3 min ago)                                           │
│ Agent: Claude ③  Phase reached: VERIFY                             │
│ Failure: TestsFailed                                                │
│ Root cause: Agent created new Redis client instead of using         │
│   existing singleton from src/lib/redis.ts                         │
│ Tests broken: cache.test.ts — "expected mock calls: 1, received: 0"│
│   (new client bypasses the mock)                                    │
│ Learned: "import redis client from src/lib/redis.ts, don't create" │
│                                                                     │
│ ATTEMPT 2 ▸ (in progress)                                          │
│ Agent: Claude ①  Phase: EXECUTE                                    │
│ Retry brief: "Previous attempt created a new Redis client which    │
│   bypassed test mocks. Use the existing singleton from              │
│   src/lib/redis.ts. The mock is set up in tests/setup.ts."        │
│                                                                     │
│ Streaming:                                                          │
│   Reading src/lib/redis.ts... ✓                                    │
│   Reading tests/setup.ts... ✓ (checking mock configuration)       │
│   Writing src/services/cache.ts...                                  │
│   ┌─ src/services/cache.ts ──────────────────────────────────────┐ │
│   │  import { redis } from '../lib/redis';  // ← using singleton │ │
│   │                                                               │ │
│   │  export class CacheService {                                  │ │
│   │    async get<T>(key: string): Promise<T | null> {             │ │
│   │      const data = await redis.get(key);                       │ │
│   │      return data ? JSON.parse(data) : null;                   │ │
│   │    }                                                          │ │
│   └───────────────────────────────────────────────────────────────┘ │
│                                                                     │
├─────────────────────────────────────────────────────────────────────┤
│ Esc=back  r=retry  s=skip  e=escalate  d=diff  Enter=intervene    │
└─────────────────────────────────────────────────────────────────────┘
```

You can see the retry brief that was generated from the failure analysis. You can see the agent is now reading the mock setup file (it learned). You can intervene at any point.

## Key UX Principles

### 1. Streaming Is Non-Negotiable

The model's output must stream token-by-token in Focus mode. `claude -p --output-format stream-json` gives us this. Parse the SSE events, render incrementally. If the TUI feels laggy or batched, it's dead.

```go
// Parse Claude Code's stream-json output
scanner := bufio.NewScanner(stdout)
for scanner.Scan() {
    var event StreamEvent
    json.Unmarshal(scanner.Bytes(), &event)

    switch event.Type {
    case "stream_event":
        if event.Event.Delta.Type == "text_delta" {
            tui.AppendText(event.Event.Delta.Text) // render immediately
        }
    case "tool_use":
        tui.ShowToolUse(event.Tool, event.Input)
    case "tool_result":
        tui.ShowToolResult(event.Result)
    }
}
```

### 2. Parallel Agents Are Visible But Not Noisy

Focus mode shows ONE agent in detail (the most interesting one). Parallel agents get a single summary line each. You can arrow-down to switch focus. Dashboard shows everything.

The "most interesting" agent is selected by: currently failing > currently in VERIFY > longest running > most recent.

### 3. Rate Limits Are a First-Class Citizen

With 7 subscriptions, rate limit management IS the user experience. The top bar always shows aggregate utilization. Dashboard shows per-pool status with reset timers.

```go
type PoolStatus struct {
    ID          int
    Provider    string    // "Claude Max 20x" or "ChatGPT Pro"
    Utilization float64   // 0.0 - 1.0
    ResetsIn    time.Duration
    Active      bool      // currently running a task
    TaskID      string    // which task is using this pool
}
```

Forge automatically shifts load to the freshest pool. If pool ① is at 78%, new tasks go to pool ③ at 18%. The user never has to think about this -- they just see it working.

### 4. Intervention Without Interruption

You can type at any time in Focus mode. Your message goes to the ACTIVE task's agent as a user turn injected into the conversation. The agent sees it as a mid-task instruction.

But you can also:
- `p` pause all agents (finish current tool call, then wait)
- `s` skip current task (mark BLOCKED, move on)
- `r` force retry (discard current attempt, start fresh)
- `e` escalate (stop task, present full analysis, ask for decision)
- `Tab` toggle Dashboard
- `?` help
- `1-7` switch focus to agent by pool number
- `/` command mode (like Claude Code's slash commands)

### 5. Diffs Are Beautiful

Same diff rendering as Claude Code but with two additions:

**Review annotations:** When cross-model review finds issues, they appear as inline comments in the diff view:

```diff
+ export const apiLimiter = rateLimit({
+   windowMs: 15 * 60 * 1000,
+   max: 100,                      // ⚠ GPT: should this be configurable?
+   standardHeaders: true,
+ });
```

**Before/after comparison:** When a retry succeeds after a failure, show what changed between attempt 1 and attempt 2:

```
Attempt 1 (failed):  import Redis from 'ioredis'; const redis = new Redis();
Attempt 2 (passed):  import { redis } from '../lib/redis';
                     ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^ learned from failure
```

### 6. Sound and Notifications

Optional but delightful:
- Quiet chime when a task completes ✓
- Distinct sound when verification fails ✗
- System notification when session completes or hits escalation
- Rate limit warning sound when pools are getting low

```go
// Desktop notifications via OS APIs
func notify(title, body string) {
    switch runtime.GOOS {
    case "darwin":
        exec.Command("osascript", "-e",
            fmt.Sprintf(`display notification "%s" with title "%s"`, body, title)).Run()
    case "linux":
        exec.Command("notify-send", title, body).Run()
    }
}
```

### 7. Session Persistence Is Invisible

If Forge crashes, the terminal closes, or you Ctrl+C:
- All task state is in SQLite (survives instantly)
- All completed work is committed to git (permanent)
- Active worktrees are preserved on disk
- `forge` restarts and shows: "Resuming session: 12/47 tasks done. 2 tasks were in progress -- re-verifying..."

No `/resume` command needed. Just run `forge` again.

## The Keyboard-First Philosophy

Every action has a key. Mouse is optional. This matches how Claude Code works and how terminal developers expect to interact.

```
FOCUS MODE:
  Enter     Send message to active agent
  Tab       Toggle Dashboard
  Esc       Cancel current input / back
  ↑↓        Scroll agent output
  ←→        Switch between parallel agents
  1-7       Jump to agent by pool number
  p         Pause all
  s         Skip task
  r         Retry task
  e         Escalate task
  d         Show full diff
  l         Show session learning
  c         Show cost breakdown
  /         Command mode
  ?         Help
  q         Quit (confirms if tasks are active)
  Ctrl+C    Quit (commits in-progress, safe)

DASHBOARD MODE:
  Tab       Toggle Focus
  ↑↓        Navigate task list
  Enter     Detail view for selected task
  Space     Toggle task selection (for bulk skip/retry)
  a         Select all pending
  1-7       Jump to agent pool detail

DETAIL MODE:
  Esc       Back to Dashboard
  r         Retry this task
  s         Skip this task
  e         Escalate this task
  d         Full diff view
  h         Show attempt history
```

## Technical Implementation

### Bubble Tea (Go TUI framework)

Same framework OpenCode uses. Elm Architecture: Model → Update → View.

```go
type ForgeModel struct {
    mode        ViewMode          // Focus, Dashboard, Detail
    tasks       []Task
    agents      []AgentState
    pools       []PoolStatus
    activeAgent int               // which agent is in focus
    learning    SessionLearning
    cost        CostTracker
    events      []Event
    input       textinput.Model   // Bubble Tea text input
    viewport    viewport.Model    // scrollable content area
}

func (m ForgeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.KeyMsg:
        switch msg.String() {
        case "tab":
            m.mode = (m.mode + 1) % 3
        case "p":
            return m, m.pauseAll()
        case "1", "2", "3", "4", "5", "6", "7":
            m.activeAgent = int(msg.String()[0] - '0') - 1
        }
    case AgentOutputMsg:
        m.agents[msg.AgentID].appendOutput(msg.Text)
    case TaskCompleteMsg:
        m.tasks[msg.TaskID].Status = Done
        m.cost.Add(msg.Cost)
    case VerifyFailMsg:
        m.tasks[msg.TaskID].Status = Failed
        m.tasks[msg.TaskID].FailureAnalysis = msg.Analysis
    case PoolUpdateMsg:
        m.pools[msg.PoolID] = msg.Status
    }
    return m, nil
}
```

### Lip Gloss (styling)

```go
var (
    titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
    successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
    failStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
    dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
    activeStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
                       BorderForeground(lipgloss.Color("39"))
    barFull      = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).SetString("█")
    barEmpty     = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).SetString("░")
)

func renderPoolBar(utilization float64) string {
    full := int(utilization * 10)
    return strings.Repeat(barFull.String(), full) +
           strings.Repeat(barEmpty.String(), 10-full) +
           fmt.Sprintf(" %d%%", int(utilization*100))
}
```

### Agent Output Multiplexing

Each `claude -p` runs in a goroutine. Output streams through channels to the TUI:

```go
type AgentOutput struct {
    AgentID int
    Type    string // "text", "tool_use", "tool_result", "error", "complete"
    Data    string
}

func runAgent(ctx context.Context, pool int, task string, out chan<- AgentOutput) {
    cmd := exec.CommandContext(ctx, "claude", "-p", task,
        "--output-format", "stream-json",
        "--worktree",
        "--allowedTools", "Read,Edit,Bash",
        "--max-turns", "20",
    )
    cmd.Env = append(os.Environ(),
        "CLAUDE_CONFIG_DIR="+poolConfigDir(pool),
    )

    stdout, _ := cmd.StdoutPipe()
    cmd.Start()

    scanner := bufio.NewScanner(stdout)
    for scanner.Scan() {
        var event StreamEvent
        if json.Unmarshal(scanner.Bytes(), &event) == nil {
            out <- AgentOutput{
                AgentID: pool,
                Type:    event.Type,
                Data:    extractText(event),
            }
        }
    }
    cmd.Wait()
    out <- AgentOutput{AgentID: pool, Type: "complete"}
}
```

All 7 agents stream simultaneously. The TUI multiplexes: Focus mode renders the active agent's stream in real-time, others buffer silently. Switch focus with arrow keys or number keys -- buffered output renders instantly.

## What Makes This Better Than Claude Code's TUI

| Claude Code | Forge |
|---|---|
| One conversation | Multiple parallel agents, one focus + summary bar |
| No progress tracking | Task board with live status |
| "Rate limited" spinner | Per-pool utilization with reset timers, auto-rotation |
| No cost visibility | Live cost tracking + subscription savings calculation |
| Manual retry (re-type prompt) | Smart retry with failure analysis injected |
| No verification visibility | Phase progress bar: PLAN▸EXECUTE▸VERIFY▸COMMIT |
| No learning | Session learning panel shows codebase patterns |
| Scroll up for history | Dashboard with full event log + task detail drill-down |
| Single subscription | 7 pools visualized, load balanced automatically |

## What's The Same (On Purpose)

- Token streaming (same feel, same speed)
- Inline diffs (same rendering)
- Keyboard-first (same muscle memory)
- Color language (green=good, red=bad, dim=context)
- Chat input at the bottom (same interaction pattern)
- Tool use visibility (same "Reading file..." → content pattern)
- Single binary (`forge` instead of `claude`)

The goal: a Claude Code user should feel at home in 30 seconds, then discover the orchestration features naturally.
