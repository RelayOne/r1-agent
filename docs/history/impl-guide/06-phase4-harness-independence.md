# 06 — Phase 4: Harness Independence

This phase makes Stoke its own AI coding harness. After this phase, Stoke can execute coding tasks end-to-end using only the Anthropic Messages API directly — no Claude Code CLI or Codex CLI subprocess needed. The CLI runners stay supported (they're useful when Eric wants to use his Claude Max subscriptions instead of API keys), but Stoke is no longer dependent on them.

## Why this matters

Three concrete reasons this phase exists:

1. **Caching:** Spawning Claude Code CLI as a subprocess **destroys prompt caching benefits** because the cache is per-process [P69]. A native runner can maintain a single warm cache across many turns within a session, cutting cost 70–82%.

2. **Hook integration:** When Stoke runs through Claude Code CLI, Stoke's hub events for tool use happen *inside* a black box. The native runner publishes `EvtToolPreUse` and `EvtToolPostUse` to the hub for every tool invocation, making honesty enforcement deterministic.

3. **Convergence with Stoke's positioning:** Stoke's selling point is verifiable, anti-deception coding. That requires owning the execution layer, not delegating to a tool that might silently retry or rewrite tests behind your back.

## Three sub-phases

This is the largest phase. Do it in order and validate at each sub-phase boundary.

| Sub-phase | What | Validates with |
|---|---|---|
| **4.1** | `internal/tools/` — tool definitions and executor | Unit tests + standalone CLI that takes a tool call JSON and runs it |
| **4.2** | `internal/agentloop/` — Anthropic Messages API agentic loop | Integration test against real API with a "create file" task |
| **4.3** | `internal/engine/native_runner.go` — drop-in `CommandRunner` | Run an existing test mission with `--runner native` and verify it produces the same output as Claude Code mode |

---

## Sub-phase 4.1: `internal/tools/`

### Package structure

```
internal/tools/
  tool.go          — Tool interface, Definition struct, registry
  registry.go      — central registry of all built-in tools
  read.go          — Read tool (file → numbered lines)
  write.go         — Write tool (create new file)
  edit.go          — Edit tool with str_replace cascade
  glob.go          — Glob tool (file pattern search)
  grep.go          — Grep tool (ripgrep wrapper)
  bash.go          — Bash tool with sandboxing
  list.go          — Ls tool (directory listing)
  str_replace.go   — the cascading replace algorithm
  sandbox.go       — bash sandboxing helpers (firejail / gVisor / no sandbox)
  schemas.go       — JSON schemas as Go structs for the Anthropic API
  tools_test.go
```

### `internal/tools/tool.go`

```go
// Package tools implements Stoke's native tool execution layer. Tools are
// defined with JSON schemas compatible with the Anthropic Messages API
// tool_use mechanism and executed in a sandboxed environment.
package tools

import (
    "context"
    "encoding/json"
    "fmt"
)

// Tool is the interface every executable tool implements.
type Tool interface {
    // Name is the unique tool identifier (must match Anthropic regex ^[a-zA-Z0-9_-]{1,64}$).
    Name() string
    // Definition returns the tool definition for the Anthropic API tools array.
    Definition() Definition
    // Execute runs the tool with the given input. Returns a string result
    // formatted for the model (typically structured text or JSON).
    // Returns isError=true if the tool execution failed; the result string
    // becomes the error message for the model.
    Execute(ctx context.Context, input json.RawMessage) (result string, isError bool)
}

// Definition is the Anthropic API tool definition format.
type Definition struct {
    Name        string          `json:"name"`
    Description string          `json:"description"`
    InputSchema InputSchema     `json:"input_schema"`
}

// InputSchema is a JSON Schema 2020-12 subset.
type InputSchema struct {
    Type       string                 `json:"type"`        // always "object"
    Properties map[string]Property    `json:"properties"`
    Required   []string               `json:"required"`
}

type Property struct {
    Type        string      `json:"type"`
    Description string      `json:"description"`
    Enum        []string    `json:"enum,omitempty"`
    Items       *Property   `json:"items,omitempty"`
    Default     interface{} `json:"default,omitempty"`
}

// Registry holds all available tools and dispatches Execute calls by name.
type Registry struct {
    tools map[string]Tool
}

func NewRegistry() *Registry {
    return &Registry{tools: make(map[string]Tool)}
}

func (r *Registry) Register(t Tool) {
    r.tools[t.Name()] = t
}

func (r *Registry) Get(name string) Tool {
    return r.tools[name]
}

// Definitions returns all tool definitions sorted alphabetically.
// CRITICAL: alphabetical sorting is required for Anthropic prompt caching to
// work correctly — unsorted tool arrays bust the cache on every turn.
func (r *Registry) Definitions() []Definition {
    names := make([]string, 0, len(r.tools))
    for name := range r.tools {
        names = append(names, name)
    }
    // Sort alphabetically (use sort.Strings)
    sortStrings(names)
    out := make([]Definition, 0, len(names))
    for _, name := range names {
        out = append(out, r.tools[name].Definition())
    }
    return out
}

// Execute dispatches a tool call by name.
func (r *Registry) Execute(ctx context.Context, name string, input json.RawMessage) (string, bool) {
    t := r.tools[name]
    if t == nil {
        return fmt.Sprintf("unknown tool: %s", name), true
    }
    return t.Execute(ctx, input)
}

func sortStrings(s []string) {
    for i := 0; i < len(s); i++ {
        for j := i + 1; j < len(s); j++ {
            if s[j] < s[i] {
                s[i], s[j] = s[j], s[i]
            }
        }
    }
}
```

### `internal/tools/read.go`

```go
package tools

import (
    "context"
    "encoding/json"
    "fmt"
    "os"
    "strings"
    "sync"
)

// Read returns file contents with line numbers in cat -n format.
type Read struct {
    // ReadTracker tracks which files have been read in the current session.
    // Edit will refuse to operate on files that haven't been Read first.
    Tracker *ReadTracker
}

type ReadTracker struct {
    mu sync.Mutex
    files map[string]bool
}

func NewReadTracker() *ReadTracker {
    return &ReadTracker{files: make(map[string]bool)}
}

func (t *ReadTracker) MarkRead(path string) {
    t.mu.Lock()
    defer t.mu.Unlock()
    t.files[path] = true
}

func (t *ReadTracker) HasBeenRead(path string) bool {
    t.mu.Lock()
    defer t.mu.Unlock()
    return t.files[path]
}

func (r *Read) Name() string { return "Read" }

func (r *Read) Definition() Definition {
    return Definition{
        Name:        "Read",
        Description: "Reads a file from the filesystem and returns its contents with line numbers (cat -n format). The line number prefix is for display only — do not include it when using Edit. Always Read a file before Editing it.",
        InputSchema: InputSchema{
            Type: "object",
            Properties: map[string]Property{
                "file_path": {Type: "string", Description: "Absolute or repo-relative path to the file"},
                "offset":    {Type: "integer", Description: "Line number to start reading from (1-indexed). Optional."},
                "limit":     {Type: "integer", Description: "Maximum number of lines to return. Optional."},
            },
            Required: []string{"file_path"},
        },
    }
}

type readInput struct {
    FilePath string `json:"file_path"`
    Offset   int    `json:"offset"`
    Limit    int    `json:"limit"`
}

func (r *Read) Execute(ctx context.Context, raw json.RawMessage) (string, bool) {
    var in readInput
    if err := json.Unmarshal(raw, &in); err != nil {
        return fmt.Sprintf("invalid input: %v", err), true
    }

    data, err := os.ReadFile(in.FilePath)
    if err != nil {
        return fmt.Sprintf("read failed: %v", err), true
    }

    lines := strings.Split(string(data), "\n")
    start := 0
    if in.Offset > 0 {
        start = in.Offset - 1
    }
    if start >= len(lines) {
        return "", false
    }
    end := len(lines)
    if in.Limit > 0 && start+in.Limit < end {
        end = start + in.Limit
    }

    var sb strings.Builder
    for i := start; i < end; i++ {
        fmt.Fprintf(&sb, "%6d\t%s\n", i+1, lines[i])
    }

    if r.Tracker != nil {
        r.Tracker.MarkRead(in.FilePath)
    }

    return sb.String(), false
}
```

### `internal/tools/str_replace.go`

```go
package tools

import (
    "fmt"
    "strings"
)

// ReplaceResult is the outcome of a str_replace attempt.
type ReplaceResult struct {
    NewContent  string
    Replacements int
    Method      string  // exact|whitespace|ellipsis|fuzzy
    Confidence  float64 // 0.0 - 1.0
}

// StrReplace performs the cascading str_replace algorithm:
//   1. Exact match
//   2. Whitespace-normalized match
//   3. Ellipsis expansion (handle "..." markers)
//   4. Fuzzy match (line-by-line similarity scoring)
//
// If oldStr appears multiple times in content and replaceAll is false, returns
// an error so the caller can ask for more context.
func StrReplace(content, oldStr, newStr string, replaceAll bool) (*ReplaceResult, error) {
    if oldStr == "" {
        return nil, fmt.Errorf("old_string cannot be empty")
    }

    // Method 1: exact match
    count := strings.Count(content, oldStr)
    if count > 0 {
        if count > 1 && !replaceAll {
            return nil, fmt.Errorf("old_string appears %d times in file; provide more context to make it unique, or set replace_all=true", count)
        }
        replacements := 1
        if replaceAll {
            replacements = count
            return &ReplaceResult{
                NewContent:  strings.ReplaceAll(content, oldStr, newStr),
                Replacements: replacements,
                Method:      "exact",
                Confidence:  1.0,
            }, nil
        }
        return &ReplaceResult{
            NewContent:  strings.Replace(content, oldStr, newStr, 1),
            Replacements: 1,
            Method:      "exact",
            Confidence:  1.0,
        }, nil
    }

    // Method 2: whitespace-normalized match
    if r := whitespaceNormalizedReplace(content, oldStr, newStr); r != nil {
        return r, nil
    }

    // Method 3: ellipsis expansion
    if strings.Contains(oldStr, "...") {
        if r := ellipsisReplace(content, oldStr, newStr); r != nil {
            return r, nil
        }
    }

    // Method 4: fuzzy match
    if r := fuzzyReplace(content, oldStr, newStr); r != nil {
        return r, nil
    }

    return nil, fmt.Errorf("old_string not found in content (tried exact, whitespace-normalized, ellipsis, fuzzy)")
}

func whitespaceNormalizedReplace(content, oldStr, newStr string) *ReplaceResult {
    normalize := func(s string) string {
        return strings.Join(strings.Fields(s), " ")
    }
    normContent := normalize(content)
    normOld := normalize(oldStr)
    if !strings.Contains(normContent, normOld) {
        return nil
    }
    // Find the original location by walking the content
    // This is approximate — we look for the first line of oldStr
    oldFirstLine := firstNonEmptyLine(oldStr)
    if oldFirstLine == "" {
        return nil
    }
    idx := strings.Index(content, oldFirstLine)
    if idx < 0 {
        return nil
    }
    // Extract the matching block from content based on line count of oldStr
    oldLines := len(strings.Split(oldStr, "\n"))
    contentLines := strings.Split(content[idx:], "\n")
    if len(contentLines) < oldLines {
        return nil
    }
    matched := strings.Join(contentLines[:oldLines], "\n")
    if normalize(matched) != normOld {
        return nil
    }
    return &ReplaceResult{
        NewContent:   strings.Replace(content, matched, newStr, 1),
        Replacements: 1,
        Method:       "whitespace",
        Confidence:   0.85,
    }
}

func ellipsisReplace(content, oldStr, newStr string) *ReplaceResult {
    // Split oldStr on ... and use each segment as an anchor
    segments := strings.Split(oldStr, "...")
    if len(segments) < 2 {
        return nil
    }
    first := strings.TrimSpace(segments[0])
    last := strings.TrimSpace(segments[len(segments)-1])
    if first == "" || last == "" {
        return nil
    }
    startIdx := strings.Index(content, first)
    if startIdx < 0 {
        return nil
    }
    endStart := startIdx + len(first)
    endIdx := strings.Index(content[endStart:], last)
    if endIdx < 0 {
        return nil
    }
    matched := content[startIdx : endStart+endIdx+len(last)]
    return &ReplaceResult{
        NewContent:   strings.Replace(content, matched, newStr, 1),
        Replacements: 1,
        Method:       "ellipsis",
        Confidence:   0.75,
    }
}

func fuzzyReplace(content, oldStr, newStr string) *ReplaceResult {
    // Simple fuzzy: find a line range in content that has highest similarity to oldStr
    // using line-by-line cosine of token sets.
    contentLines := strings.Split(content, "\n")
    oldLines := strings.Split(oldStr, "\n")
    if len(oldLines) > len(contentLines) {
        return nil
    }

    bestStart := -1
    bestScore := 0.0
    for i := 0; i <= len(contentLines)-len(oldLines); i++ {
        score := lineBlockSimilarity(contentLines[i:i+len(oldLines)], oldLines)
        if score > bestScore {
            bestScore = score
            bestStart = i
        }
    }
    if bestStart < 0 || bestScore < 0.7 {
        return nil
    }
    matched := strings.Join(contentLines[bestStart:bestStart+len(oldLines)], "\n")
    return &ReplaceResult{
        NewContent:   strings.Replace(content, matched, newStr, 1),
        Replacements: 1,
        Method:       "fuzzy",
        Confidence:   bestScore,
    }
}

func lineBlockSimilarity(a, b []string) float64 {
    if len(a) != len(b) {
        return 0
    }
    matches := 0
    for i := range a {
        if normalizedEqual(a[i], b[i]) {
            matches++
        }
    }
    return float64(matches) / float64(len(a))
}

func normalizedEqual(a, b string) bool {
    return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}

func firstNonEmptyLine(s string) string {
    for _, line := range strings.Split(s, "\n") {
        if strings.TrimSpace(line) != "" {
            return line
        }
    }
    return ""
}
```

### `internal/tools/edit.go`

```go
package tools

import (
    "context"
    "encoding/json"
    "fmt"
    "os"
)

// Edit modifies an existing file using str_replace.
type Edit struct {
    Tracker *ReadTracker
}

func (e *Edit) Name() string { return "Edit" }

func (e *Edit) Definition() Definition {
    return Definition{
        Name:        "Edit",
        Description: "Edits a file by replacing exactly one occurrence of old_string with new_string. The file MUST have been read first via the Read tool. The old_string must be unique in the file unless replace_all is set. If old_string appears multiple times, expand it with surrounding context until it is unique.",
        InputSchema: InputSchema{
            Type: "object",
            Properties: map[string]Property{
                "file_path":   {Type: "string", Description: "Absolute or repo-relative path"},
                "old_string":  {Type: "string", Description: "The text to replace. Must match exactly (whitespace-sensitive)"},
                "new_string":  {Type: "string", Description: "The text to replace it with"},
                "replace_all": {Type: "boolean", Description: "Replace all occurrences instead of just one. Default false."},
            },
            Required: []string{"file_path", "old_string", "new_string"},
        },
    }
}

type editInput struct {
    FilePath   string `json:"file_path"`
    OldString  string `json:"old_string"`
    NewString  string `json:"new_string"`
    ReplaceAll bool   `json:"replace_all"`
}

func (e *Edit) Execute(ctx context.Context, raw json.RawMessage) (string, bool) {
    var in editInput
    if err := json.Unmarshal(raw, &in); err != nil {
        return fmt.Sprintf("invalid input: %v", err), true
    }
    if e.Tracker != nil && !e.Tracker.HasBeenRead(in.FilePath) {
        return fmt.Sprintf("file %s has not been read yet — call Read first", in.FilePath), true
    }

    data, err := os.ReadFile(in.FilePath)
    if err != nil {
        return fmt.Sprintf("read failed: %v", err), true
    }
    result, err := StrReplace(string(data), in.OldString, in.NewString, in.ReplaceAll)
    if err != nil {
        return err.Error(), true
    }
    if err := os.WriteFile(in.FilePath, []byte(result.NewContent), 0644); err != nil {
        return fmt.Sprintf("write failed: %v", err), true
    }
    return fmt.Sprintf("edited %s (%s match, %d replacement(s), confidence %.2f)",
        in.FilePath, result.Method, result.Replacements, result.Confidence), false
}
```

### `internal/tools/write.go`, `bash.go`, `glob.go`, `grep.go`

These follow the same pattern. The full code is long but the structure is identical: define a `Definition()`, parse input JSON, execute, return result+isError.

**Write tool input:** `file_path`, `content`. Refuses to overwrite existing files unless `--force` flag is set on the agent (configurable).

**Bash tool:** Wraps `exec.CommandContext` with sandboxing. On Linux, optionally use `firejail` or `bwrap` if available. Default behavior: no sandbox (preserve current Stoke behavior). Always use a configurable timeout (default 30s).

**Glob:** Uses Go's `filepath.Glob` or `doublestar` for `**` patterns.

**Grep:** Shells out to `rg` (ripgrep) if available, falls back to a Go regexp walker.

### Tool registry initialization

**File:** `internal/tools/registry.go`

```go
package tools

// DefaultRegistry returns a registry with all built-in tools registered.
// The same ReadTracker is shared between Read and Edit so that read-before-edit
// is enforced.
func DefaultRegistry() *Registry {
    tracker := NewReadTracker()
    r := NewRegistry()
    r.Register(&Read{Tracker: tracker})
    r.Register(&Write{})
    r.Register(&Edit{Tracker: tracker})
    r.Register(&Glob{})
    r.Register(&Grep{})
    r.Register(&Bash{Timeout: 30})  // seconds
    r.Register(&Ls{})
    return r
}
```

---

## Sub-phase 4.2: `internal/agentloop/`

### `internal/agentloop/loop.go`

```go
// Package agentloop implements the multi-turn tool-use loop against the
// Anthropic Messages API. It manages conversation state, prompt caching
// alignment, error handling with consecutive failure limits, and integrates
// with internal/tools for execution.
package agentloop

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "time"

    "github.com/ericmacdougall/stoke/internal/hub"
    "github.com/ericmacdougall/stoke/internal/provider"
    "github.com/ericmacdougall/stoke/internal/tools"
)

// Config controls a Loop's behavior.
type Config struct {
    Provider           provider.Provider
    Model              string
    Tools              *tools.Registry
    Bus                *hub.Bus  // optional, for hub events on tool use
    SystemPrompt       string
    SkillBlock         string  // pre-rendered skill XML block (from skill registry)
    MaxTurns           int     // hard limit; default 50
    MaxConsecutiveErrs int     // default 3
    EnableThinking     bool
    ThinkingBudget     int     // tokens for extended thinking; default 16000
}

// Loop is a single multi-turn conversation.
type Loop struct {
    cfg      Config
    messages []provider.Message
    turn     int
    consecutiveErrs int
}

// New creates a new Loop.
func New(cfg Config) *Loop {
    if cfg.MaxTurns == 0 {
        cfg.MaxTurns = 50
    }
    if cfg.MaxConsecutiveErrs == 0 {
        cfg.MaxConsecutiveErrs = 3
    }
    return &Loop{cfg: cfg}
}

// Run executes the loop until completion or limit.
//
// Termination conditions:
//   - stop_reason = "end_turn" → Done = true, success
//   - stop_reason = "max_tokens" → continue
//   - stop_reason = "tool_use" → execute tools, send results
//   - turn count >= MaxTurns → return error
//   - consecutive errors >= MaxConsecutiveErrs → return error
func (l *Loop) Run(ctx context.Context, userMessage string) (*Result, error) {
    // Initial user message
    l.messages = append(l.messages, provider.Message{
        Role: "user",
        Content: []provider.ContentBlock{
            {Type: "text", Text: userMessage},
        },
    })

    for l.turn = 0; l.turn < l.cfg.MaxTurns; l.turn++ {
        if err := ctx.Err(); err != nil {
            return nil, err
        }

        // Build the request with cache-aligned structure
        req := l.buildRequest()

        // Publish pre-call event
        if l.cfg.Bus != nil {
            evt, _ := hub.NewEvent(hub.EvtModelPreCall, map[string]interface{}{
                "model": l.cfg.Model,
                "turn":  l.turn,
            })
            l.cfg.Bus.PublishAsync(evt)
        }

        resp, err := l.cfg.Provider.SendMessages(ctx, req)
        if err != nil {
            l.consecutiveErrs++
            if l.consecutiveErrs >= l.cfg.MaxConsecutiveErrs {
                return nil, fmt.Errorf("too many consecutive errors: %w", err)
            }
            time.Sleep(backoff(l.consecutiveErrs))
            continue
        }
        l.consecutiveErrs = 0

        // Publish post-call event with usage
        if l.cfg.Bus != nil {
            evt, _ := hub.NewEvent(hub.EvtModelPostCall, map[string]interface{}{
                "model":              l.cfg.Model,
                "input_tokens":       resp.Usage.InputTokens,
                "output_tokens":      resp.Usage.OutputTokens,
                "cache_read_tokens":  resp.Usage.CacheReadInputTokens,
                "cache_write_tokens": resp.Usage.CacheCreationInputTokens,
            })
            l.cfg.Bus.PublishAsync(evt)
        }

        // Append the assistant message to the conversation
        l.messages = append(l.messages, provider.Message{
            Role:    "assistant",
            Content: resp.Content,
        })

        switch resp.StopReason {
        case "end_turn":
            return &Result{Messages: l.messages, Turns: l.turn + 1, Done: true}, nil
        case "max_tokens":
            // Continue the conversation
            continue
        case "tool_use":
            // Execute every tool_use block and send results
            toolResults := l.executeToolCalls(ctx, resp.Content)
            l.messages = append(l.messages, provider.Message{
                Role:    "user",
                Content: toolResults,
            })
            continue
        case "stop_sequence":
            return &Result{Messages: l.messages, Turns: l.turn + 1, Done: true}, nil
        default:
            return nil, fmt.Errorf("unexpected stop_reason: %s", resp.StopReason)
        }
    }
    return nil, errors.New("max turns exceeded")
}

func (l *Loop) buildRequest() provider.MessagesRequest {
    // Tools are sorted alphabetically (handled in the tools registry)
    toolDefs := l.cfg.Tools.Definitions()

    // Build cache-aligned system prompt with skill block
    systemBlocks := []provider.SystemBlock{
        {
            Type: "text",
            Text: l.cfg.SystemPrompt,
            CacheControl: &provider.CacheControl{Type: "ephemeral"},
        },
    }
    if l.cfg.SkillBlock != "" {
        systemBlocks = append(systemBlocks, provider.SystemBlock{
            Type: "text",
            Text: l.cfg.SkillBlock,
            CacheControl: &provider.CacheControl{Type: "ephemeral"},
        })
    }

    // Build messages with cache breakpoint on the most recent user message
    msgs := make([]provider.Message, len(l.messages))
    copy(msgs, l.messages)
    if len(msgs) > 0 {
        // Mark the last block of the second-to-last message as cacheable
        // (incremental caching of conversation history)
        if len(msgs) >= 2 {
            for i := range msgs[len(msgs)-2].Content {
                msgs[len(msgs)-2].Content[i].CacheControl = &provider.CacheControl{Type: "ephemeral"}
                break
            }
        }
    }

    req := provider.MessagesRequest{
        Model:     l.cfg.Model,
        MaxTokens: 8192,
        System:    systemBlocks,
        Messages:  msgs,
        Tools:     toolDefs,
    }

    if l.cfg.EnableThinking {
        req.Thinking = &provider.Thinking{
            Type:         "enabled",
            BudgetTokens: l.cfg.ThinkingBudget,
        }
    }
    return req
}

func (l *Loop) executeToolCalls(ctx context.Context, content []provider.ContentBlock) []provider.ContentBlock {
    var results []provider.ContentBlock
    for _, block := range content {
        if block.Type != "tool_use" {
            continue
        }

        // Publish tool pre-use to hub (gates can deny here)
        var decision hub.Decision = hub.DecisionAllow
        if l.cfg.Bus != nil {
            evt, _ := hub.NewEvent(hub.EvtToolPreUse, map[string]interface{}{
                "tool_name":   block.Name,
                "tool_input":  block.Input,
                "tool_use_id": block.ID,
            })
            decision, _, _ = l.cfg.Bus.Publish(ctx, evt)
        }
        if decision == hub.DecisionDeny {
            results = append(results, provider.ContentBlock{
                Type:      "tool_result",
                ToolUseID: block.ID,
                Content:   "tool use denied by hub gate",
                IsError:   true,
            })
            continue
        }

        result, isError := l.cfg.Tools.Execute(ctx, block.Name, block.Input)

        // Publish post-use
        if l.cfg.Bus != nil {
            evt, _ := hub.NewEvent(hub.EvtToolPostUse, map[string]interface{}{
                "tool_name":   block.Name,
                "tool_input":  block.Input,
                "tool_use_id": block.ID,
                "result":      result,
                "is_error":    isError,
            })
            l.cfg.Bus.PublishAsync(evt)
        }

        results = append(results, provider.ContentBlock{
            Type:      "tool_result",
            ToolUseID: block.ID,
            Content:   result,
            IsError:   isError,
        })
    }
    return results
}

func backoff(attempt int) time.Duration {
    base := 500 * time.Millisecond
    for i := 0; i < attempt; i++ {
        base *= 2
    }
    if base > 30*time.Second {
        base = 30 * time.Second
    }
    return base
}

// Result is the outcome of a Loop.Run call.
type Result struct {
    Messages []provider.Message
    Turns    int
    Done     bool
}

// AssistantText returns the concatenated text from the final assistant message.
func (r *Result) AssistantText() string {
    if len(r.Messages) == 0 {
        return ""
    }
    last := r.Messages[len(r.Messages)-1]
    if last.Role != "assistant" {
        return ""
    }
    var s string
    for _, b := range last.Content {
        if b.Type == "text" {
            s += b.Text
        }
    }
    return s
}
```

### `internal/provider/anthropic.go` — extend with tool_use support

The existing `AnthropicProvider` likely has `Chat(ctx, system, user)` returning a string. You need to add a richer method `SendMessages(ctx, MessagesRequest) (*MessagesResponse, error)` that handles the full Messages API including tool_use.

**Add to `internal/provider/types.go`:**

```go
package provider

import "encoding/json"

// MessagesRequest is the full Anthropic Messages API request.
type MessagesRequest struct {
    Model       string         `json:"model"`
    MaxTokens   int            `json:"max_tokens"`
    System      []SystemBlock  `json:"system,omitempty"`
    Messages    []Message      `json:"messages"`
    Tools       []interface{}  `json:"tools,omitempty"`
    ToolChoice  *ToolChoice    `json:"tool_choice,omitempty"`
    Thinking    *Thinking      `json:"thinking,omitempty"`
    Temperature float64        `json:"temperature,omitempty"`
    StopSequences []string     `json:"stop_sequences,omitempty"`
}

type SystemBlock struct {
    Type         string        `json:"type"`
    Text         string        `json:"text"`
    CacheControl *CacheControl `json:"cache_control,omitempty"`
}

type CacheControl struct {
    Type string `json:"type"`  // "ephemeral"
    TTL  string `json:"ttl,omitempty"`  // "5m" or "1h"
}

type Message struct {
    Role    string         `json:"role"`
    Content []ContentBlock `json:"content"`
}

type ContentBlock struct {
    // Common
    Type string `json:"type"`

    // text
    Text string `json:"text,omitempty"`

    // tool_use (assistant)
    ID    string          `json:"id,omitempty"`
    Name  string          `json:"name,omitempty"`
    Input json.RawMessage `json:"input,omitempty"`

    // tool_result (user)
    ToolUseID string `json:"tool_use_id,omitempty"`
    Content   string `json:"content,omitempty"`
    IsError   bool   `json:"is_error,omitempty"`

    // thinking (assistant, when extended thinking enabled)
    Thinking  string `json:"thinking,omitempty"`
    Signature string `json:"signature,omitempty"`

    // cache control on this block
    CacheControl *CacheControl `json:"cache_control,omitempty"`
}

type ToolChoice struct {
    Type string `json:"type"`  // auto|any|tool
    Name string `json:"name,omitempty"`
}

type Thinking struct {
    Type         string `json:"type"`  // "enabled"
    BudgetTokens int    `json:"budget_tokens"`
}

// MessagesResponse is the full Anthropic Messages API response.
type MessagesResponse struct {
    ID         string         `json:"id"`
    Type       string         `json:"type"`
    Role       string         `json:"role"`
    Model      string         `json:"model"`
    Content    []ContentBlock `json:"content"`
    StopReason string         `json:"stop_reason"`
    Usage      Usage          `json:"usage"`
}

type Usage struct {
    InputTokens              int `json:"input_tokens"`
    OutputTokens             int `json:"output_tokens"`
    CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
    CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// Provider is the interface every LLM provider implements.
type Provider interface {
    Chat(ctx context.Context, system, user string) (string, error)
    SendMessages(ctx context.Context, req MessagesRequest) (*MessagesResponse, error)
}
```

**In `internal/provider/anthropic.go`,** implement `SendMessages` by POSTing to `https://api.anthropic.com/v1/messages` with these headers:

```
x-api-key: {key}
anthropic-version: 2023-06-01
content-type: application/json
```

If `req.Thinking` is non-nil, also add:
```
anthropic-beta: extended-thinking-2025-04-29
```

If any system block uses cache control, add:
```
anthropic-beta: prompt-caching-2024-07-31
```

Marshal `req` to JSON, POST it, decode the response into `MessagesResponse`. Handle 429 (rate limit) and 529 (overloaded) by emitting `EvtModelRateLimit` to the bus and returning a typed error.

---

## Sub-phase 4.3: `internal/engine/native_runner.go`

### Drop-in replacement for `ClaudeRunner`

```go
package engine

import (
    "context"

    "github.com/ericmacdougall/stoke/internal/agentloop"
    "github.com/ericmacdougall/stoke/internal/hub"
    "github.com/ericmacdougall/stoke/internal/provider"
    "github.com/ericmacdougall/stoke/internal/tools"
)

// NativeRunner implements CommandRunner by driving the Anthropic Messages API
// directly via internal/agentloop. It does not spawn any subprocess.
type NativeRunner struct {
    Provider     provider.Provider
    Model        string
    Bus          *hub.Bus
    Tools        *tools.Registry
    SystemPrompt string
}

func NewNativeRunner(p provider.Provider, model string, bus *hub.Bus) *NativeRunner {
    return &NativeRunner{
        Provider:     p,
        Model:        model,
        Bus:          bus,
        Tools:        tools.DefaultRegistry(),
        SystemPrompt: defaultStokeSystemPrompt(),
    }
}

// Run satisfies the existing CommandRunner interface (assuming it's
// (ctx, prompt) → (output, error)). Adapt the signature to whatever the
// real interface is — the key change is that it uses agentloop instead
// of exec.Command for "claude" or "codex".
func (n *NativeRunner) Run(ctx context.Context, prompt string) (string, error) {
    loop := agentloop.New(agentloop.Config{
        Provider:     n.Provider,
        Model:        n.Model,
        Tools:        n.Tools,
        Bus:          n.Bus,
        SystemPrompt: n.SystemPrompt,
        MaxTurns:     50,
    })
    result, err := loop.Run(ctx, prompt)
    if err != nil {
        return "", err
    }
    return result.AssistantText(), nil
}

func defaultStokeSystemPrompt() string {
    return `You are a coding assistant working inside Stoke, an AI coding orchestrator that emphasizes verifiable, honest engineering.

Your tools are: Read, Write, Edit, Glob, Grep, Bash, Ls.

Hard rules:
- Always Read a file before Editing it.
- When editing, your old_string must match exactly (whitespace-sensitive). If it appears more than once, expand with surrounding context until unique.
- Never write placeholder code, TODO/FIXME comments, panic("not implemented"), or empty function bodies.
- Never use @ts-ignore, "as any", eslint-disable, or //nolint directives.
- Never modify or weaken existing tests to make them pass. If a test fails, fix the implementation.
- If you cannot complete a task, explicitly say so. Do not claim completion of work you didn't do.
- Run any tests/lints/builds you have available before claiming completion.
`
}
```

**Add a flag to select runner:** in `cmd/stoke/main.go`, accept `--runner native|claude|codex|hybrid` and route accordingly. Default stays `claude` (no behavior change for existing users) until Phase 4 is fully validated.

---

## Validation gate for Phase 4

Run these in order. Each must pass before moving on.

### After 4.1 (tools)

1. `go test ./internal/tools/...` passes with >70% coverage
2. `go run ./cmd/stoke tool exec Read '{"file_path":"go.mod"}'` returns the file content with line numbers
3. `go run ./cmd/stoke tool exec Edit '{"file_path":"/tmp/test.txt","old_string":"foo","new_string":"bar"}'` after creating `/tmp/test.txt` with "foo" returns success
4. Edit on a file that hasn't been Read returns the read-first error
5. StrReplace fuzzy match handles a deliberately whitespace-mangled `old_string`

### After 4.2 (agentloop)

1. `go test ./internal/agentloop/...` passes (mock provider)
2. Integration test against the real API: a Loop with prompt "create a file /tmp/hello.go that prints hello world, then run it" succeeds end-to-end. The file exists and contains valid Go.
3. Cost tracker shows cache hits on the second turn (cache_read_tokens > 0)
4. Bus receives `EvtToolPreUse` and `EvtToolPostUse` for every tool invocation
5. Honesty gate denies a write that contains `panic("not implemented")` and the loop continues with the deny message

### After 4.3 (native runner)

1. Run an existing test mission with `--runner native` and verify it produces output
2. Compare the output to `--runner claude` mode for the same task — the file changes should be roughly equivalent
3. Cost tracker shows actual API spend matching the observed cost in the Anthropic console
4. Hub audit log shows complete tool use trace
5. Append phase 4 entry to `STOKE-IMPL-NOTES.md`

## Now go to `07-phase5-wisdom-and-fixes.md`.
