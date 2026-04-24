package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/mcp"
	"github.com/RelayOne/r1/internal/plan"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
	"github.com/RelayOne/r1/internal/tools"
)

// MCPRegistry is the minimal surface the native runner needs from
// mcp.Registry to enumerate worker-visible tools and dispatch calls.
// *mcp.Registry satisfies this interface directly; tests inject a
// fake to avoid standing up transports or subprocesses.
//
// Kept intentionally narrow: only the two methods the wiring uses.
// If a future caller needs more (Health, Close, etc.) they should
// call the concrete type — this interface is the engine-side seam
// for test injection only.
type MCPRegistry interface {
	AllToolsForTrust(ctx context.Context, workerTrust string) ([]mcp.Tool, error)
	Call(ctx context.Context, fullName string, workerTrust string, args []byte) (mcp.ToolResult, error)
}

// NativeRunner implements CommandRunner using Stoke's own agentloop and
// the Anthropic Messages API directly. No Claude Code CLI needed.
type NativeRunner struct {
	apiKey   string
	BaseURL  string             // empty = default Anthropic URL; set for LiteLLM or custom proxy
	model    string             // e.g. "claude-sonnet-4-5"
	EventBus *hub.Bus           // optional: publishes tool use events
	// ProviderOverride, when set, is used instead of
	// constructing an AnthropicProvider from apiKey/BaseURL.
	// Used for claude-code:// mode.
	ProviderOverride provider.Provider

	// MCPRegistry, when non-nil, supplies MCP-backed tools to every
	// dispatch that doesn't override via RunSpec.MCPRegistry. Per-run
	// overrides take precedence so callers can scope tools per worker
	// without rebuilding the runner. When both this field and the
	// RunSpec field are nil the MCP integration is a no-op (backward
	// compat with every existing call site).
	MCPRegistry MCPRegistry
}

// NewNativeRunner creates a native runner using the Anthropic API directly.
func NewNativeRunner(apiKey, model string) *NativeRunner {
	return &NativeRunner{
		apiKey: apiKey,
		model:  model,
	}
}

// Prepare returns a PreparedCommand for informational/logging purposes.
// The native runner doesn't spawn a subprocess, so this is minimal.
func (n *NativeRunner) Prepare(spec RunSpec) (PreparedCommand, error) {
	if err := spec.Validate(); err != nil {
		return PreparedCommand{}, err
	}
	return PreparedCommand{
		Binary: "native",
		Args:   []string{"--model", n.model},
		Dir:    spec.WorktreeDir,
		Notes:  []string{"Using Stoke native agentloop (no CLI subprocess)"},
	}, nil
}

// Run executes a coding task using the native agentloop.
func (n *NativeRunner) Run(ctx context.Context, spec RunSpec, onEvent OnEventFunc) (RunResult, error) {
	if err := spec.Validate(); err != nil {
		return RunResult{IsError: true}, err
	}

	start := time.Now()

	// Create the provider — use override if set (claude-code://),
	// otherwise auto-detect format from the URL. OpenRouter and
	// OpenAI endpoints get the OpenAI-compatible provider;
	// everything else (litellm, MiniMax, Anthropic) gets the
	// Anthropic Messages API provider.
	var p provider.Provider
	if n.ProviderOverride != nil {
		p = n.ProviderOverride
	} else {
		lower := strings.ToLower(n.BaseURL)
		if strings.Contains(lower, "openrouter.ai") ||
			strings.Contains(lower, "api.openai.com") ||
			strings.Contains(lower, "api.together.xyz") ||
			strings.Contains(lower, "api.fireworks.ai") ||
			strings.Contains(lower, "api.deepseek.com") {
			base := strings.TrimRight(n.BaseURL, "/")
			base = strings.TrimSuffix(base, "/v1")
			p = provider.NewOpenAICompatProvider("openai-compat", n.apiKey, base)
		} else {
			p = provider.NewAnthropicProvider(n.apiKey, n.BaseURL)
		}
	}

	// Create the tool registry
	toolRegistry := tools.NewRegistry(spec.WorktreeDir)
	allDefs := toolRegistry.Definitions()

	// Filter tools based on phase restrictions.
	writableTools := map[string]bool{
		"edit_file":  true,
		"write_file": true,
		"bash":       true, // bash can write; restricted in read-only mode
	}
	toolDefs := make([]provider.ToolDef, 0, len(allDefs))
	for _, td := range allDefs {
		if spec.Phase.ReadOnly && writableTools[td.Name] {
			continue // exclude write-capable tools in read-only mode
		}
		toolDefs = append(toolDefs, td)
	}

	// Merge caller-supplied ExtraTools into the advertised tool list.
	// Each ExtraTool carries both its schema (advertised to the model)
	// and its handler (wired into dispatch below). This is the plug-
	// point for out-of-band tools like request_clarification that
	// don't belong in tools.Registry.
	extraHandlers := make(map[string]ExtraToolHandler, len(spec.ExtraTools))
	for _, et := range spec.ExtraTools {
		if et.Def.Name == "" || et.Handler == nil {
			continue
		}
		toolDefs = append(toolDefs, et.Def)
		extraHandlers[et.Def.Name] = et.Handler
	}

	// Merge MCP-registry-backed tools into the advertised tool list.
	// Resolution order: RunSpec.MCPRegistry (per-dispatch) wins over
	// NativeRunner.MCPRegistry (process-wide). When neither is set the
	// whole path is a no-op — no registry calls, no tool advertisements,
	// no handler wiring — preserving backward compatibility with every
	// existing NewNativeRunner caller.
	//
	// Each mcp.Tool becomes an ExtraTool whose handler routes back
	// through the same registry's Call(ctx, fullName, workerTrust, args),
	// wraps the resulting ToolResult in <mcp_result …>…</mcp_result>
	// framing so the model can visually distinguish MCP output from
	// native tool output, and propagates result.IsError as a Go error
	// so the agentloop marks the tool_result with is_error=true (see
	// agentloop/loop.go:533-540 for the translation).
	//
	// ListTools failure is NOT fatal: we log and proceed with an empty
	// MCP tool set so a broken server doesn't take down the worker.
	// The registry's internal circuit breaker will have tripped the
	// offending servers open, so subsequent Call dispatches on any
	// model-fabricated mcp_* names (should one leak through) short-
	// circuit without transport load.
	reg := spec.MCPRegistry
	if reg == nil {
		reg = n.MCPRegistry
	}
	if reg != nil {
		workerTrust := spec.WorkerTrust
		if workerTrust == "" {
			workerTrust = "untrusted"
		}
		mcpTools, listErr := reg.AllToolsForTrust(ctx, workerTrust)
		if listErr != nil {
			// Non-fatal: log and fall through without MCP tools.
			fmt.Fprintf(os.Stderr, "mcp: AllToolsForTrust(%s) failed: %v\n", workerTrust, listErr)
		}
		for _, t := range mcpTools {
			server := t.ServerName
			toolName := t.Definition.Name
			if server == "" || toolName == "" {
				continue
			}
			fullName := "mcp_" + server + "_" + toolName
			schema := t.Definition.InputSchema
			if len(schema) == 0 {
				// agentloop / Anthropic require a non-empty JSON Schema;
				// fall back to a permissive object schema so an MCP
				// server that under-specifies its tool doesn't fail
				// the whole dispatch.
				schema = json.RawMessage(`{"type":"object"}`)
			}
			toolDefs = append(toolDefs, provider.ToolDef{
				Name:        fullName,
				Description: t.Definition.Description,
				InputSchema: schema,
			})
			// Bind loop-vars for the closure.
			srvLocal := server
			toolLocal := toolName
			fullLocal := fullName
			extraHandlers[fullName] = func(ctx context.Context, input json.RawMessage) (string, error) {
				result, err := reg.Call(ctx, fullLocal, workerTrust, []byte(input))
				callID := extractCallID(result)
				body := renderMCPContent(result)
				wrapped := fmt.Sprintf(`<mcp_result server=%q tool=%q call_id=%q>%s</mcp_result>`, srvLocal, toolLocal, callID, body)
				if err != nil {
					// Transport / policy / circuit error: surface via Go
					// error so the agentloop sets is_error=true. The
					// wrapped tag is included in the error message so a
					// reviewer can still see the server / tool / call_id.
					return "", fmt.Errorf("%s: %w", wrapped, err)
				}
				if result.IsError {
					// Server-declared error result: same disposition,
					// wrapped payload carries the server's message.
					return "", fmt.Errorf("%s", wrapped)
				}
				return wrapped, nil
			}
		}
	}

	// Build allowed tool set for handler enforcement.
	allowedTools := make(map[string]bool, len(toolDefs))
	for _, td := range toolDefs {
		allowedTools[td.Name] = true
	}

	// Open the worker log (append-only JSONL) if the caller requested
	// one. Captures every tool call deterministically so reviewers
	// can verify what actually ran without depending on the worker's
	// trailing natural-language summary (which workers often omit).
	//
	// Schema (every line): {type, ts, uuid, run_id, dispatch_id,
	// session_id, task_id, attempt, depth, model, stoke_build, pid,
	// ppid, purpose, phase, tool, input, result, duration_ms,
	// result_len, err}. Empty correlation fields are elided so lines
	// stay compact.
	var workerLog *os.File
	wlc := spec.WorkerLogContext
	if wlc.DispatchID == "" {
		wlc.DispatchID = newShortID("d")
	}
	if spec.WorkerLogPath != "" {
		if f, ferr := os.OpenFile(spec.WorkerLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); ferr == nil {
			workerLog = f
			defer workerLog.Close()
			// Header: dispatch_start carries the FULL config snapshot
			// so a reviewer / post-mortem can reconstruct the run
			// without cross-referencing other state.
			hdr := map[string]any{
				"type":              "dispatch_start",
				"ts":                time.Now().UTC().Format(time.RFC3339Nano),
				"dispatch_id":       wlc.DispatchID,
				"phase":             spec.Phase.Name,
				"worktree_dir":      spec.WorktreeDir,
				"runtime_dir":       spec.RuntimeDir,
				"max_turns":         spec.Phase.MaxTurns,
				"read_only":         spec.Phase.ReadOnly,
				"max_tokens":        16000,
				"compact_threshold": spec.CompactThreshold,
				"base_url":          n.BaseURL,
				"system_prompt_len": len(spec.SystemPrompt),
				"user_prompt_len":   len(spec.Prompt),
			}
			addCtx(hdr, &wlc)
			if b, e := json.Marshal(hdr); e == nil {
				fmt.Fprintln(workerLog, string(b))
			}
		}
	}

	// Create the tool handler that bridges tools.Registry → agentloop.ToolHandler
	// and dispatches any ExtraTool calls to their attached handler.
	handler := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		if !allowedTools[name] {
			return "", fmt.Errorf("tool %q not allowed in phase %q (read_only=%v)", name, spec.Phase.Name, spec.Phase.ReadOnly)
		}
		// POL-7: fail-closed policy gate. For bash / file_read /
		// file_write / mcp_* tool names, consult the policy backend
		// BEFORE invoking the underlying side effect. A Deny verdict,
		// a client-level error, or an unavailable backend all route
		// through the same error-return path so the agentloop marks
		// the tool_result with is_error=true. Non-gated tools (grep,
		// glob, env_*, etc.) short-circuit with Allowed=true so the
		// hook is a strict subset rather than a blanket gate.
		if gate := gateToolCall(ctx, name, input); !gate.Allowed {
			return "", gate.Err
		}
		toolStart := time.Now()
		var result string
		var err error
		if h, ok := extraHandlers[name]; ok {
			result, err = h(ctx, input)
		} else {
			result, err = toolRegistry.Handle(ctx, name, input)
		}
		if workerLog != nil {
			// Every tool call gets a unique ID so a reviewer can quote
			// a specific action verbatim ("see call c-xxx") and a
			// debugger can correlate across the Anthropic API log and
			// our JSONL without guessing.
			entry := map[string]any{
				"type":        "tool_call",
				"uuid":        newShortID("c"),
				"ts":          toolStart.UTC().Format(time.RFC3339Nano),
				"tool":        name,
				"input":       truncateForWorkerLog(string(input), 4096),
				"duration_ms": time.Since(toolStart).Milliseconds(),
				"result_len":  len(result),
			}
			addCtx(entry, &wlc)
			if err != nil {
				entry["err"] = err.Error()
			} else {
				entry["result"] = truncateForWorkerLog(result, 4096)
			}
			if b, e := json.Marshal(entry); e == nil {
				fmt.Fprintln(workerLog, string(b))
			}
		}
		return result, err
	}

	// Configure the agentloop. SystemPrompt is the cacheable static
	// context (passed via RunSpec.SystemPrompt or Phase.Prompt); it's
	// wrapped in a cache_control breakpoint by agentloop.
	systemPrompt := spec.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = spec.Phase.Prompt
	}
	// Inject working directory so the model uses correct paths.
	if spec.WorktreeDir != "" {
		systemPrompt += fmt.Sprintf("\n\nPROJECT ROOT: %s\nAll file paths in tool calls (read_file, write_file, edit_file, bash) must be ABSOLUTE paths under this directory.\nBash commands should use absolute paths or cd to %s first.\nBE ACTION-ORIENTED: use tools immediately, keep text under 200 words.\n", spec.WorktreeDir, spec.WorktreeDir)
	}
	cfg := agentloop.Config{
		Model:              n.model,
		MaxTurns:           spec.Phase.MaxTurns,
		MaxConsecutiveErrs: 3,
		MaxTokens:          16000,
		SystemPrompt:       systemPrompt,
	}

	// Pre-end-turn build verification (Cline/Aider pattern).
	// When the model says "done", verify the project compiles.
	// If it doesn't, inject the errors and force the model to
	// fix them in the same conversation turn — preserving full
	// context of what was just written.
	//
	// Strategy:
	//   1. Try the Ecosystem registry (internal/plan) — proper
	//      per-language, per-package compile check (TS runs
	//      tsc in each modified file's package dir, Go runs
	//      `go vet`/`go build`, C# runs dotnet, etc.). This is
	//      correct for monorepos because it resolves each
	//      file's owning package rather than a single root
	//      command. Extensible: every Ecosystem implementation
	//      (integrity_ts.go, integrity_go.go, integrity_csharp.go,
	//      …) participates automatically.
	//   2. Fall back to detectBuildCommand's bash heuristic if
	//      no ecosystem claims any modified file (generic text
	//      projects, etc.).
	if spec.WorktreeDir != "" {
		buildChecked := false
		extraCheck := spec.ExtraPreEndTurnCheck
		cfg.PreEndTurnCheckFn = func(messages []agentloop.Message) string {
			// Run build gate once per dispatch so repeated retries
			// don't hammer tsc/go-build.
			if !buildChecked {
				buildChecked = true
				if msg := runEcosystemGate(ctx, spec.WorktreeDir); msg != "" {
					return msg
				}
				// No ecosystem match or no errors — fall back to
				// the bash-command build gate for cases where the
				// ecosystem registry doesn't cover the repo shape.
				if buildCmd := detectBuildCommand(spec.WorktreeDir); buildCmd != "" {
					cmd := exec.CommandContext(ctx, "bash", "-lc", buildCmd) // #nosec G204 -- CLI runner launches vetted provider binary with Stoke-generated args.
					cmd.Dir = spec.WorktreeDir
					out, err := cmd.CombinedOutput()
					if err != nil {
						output := string(out)
						if len(output) > 4000 {
							output = output[len(output)-4000:]
						}
						return fmt.Sprintf("Build command failed: %s\n\nErrors:\n%s", buildCmd, output)
					}
				}
			}
			// Chain to caller-provided check (descent-hardening
			// spec-1 item 3: pre_completion_gate parser). Only
			// runs AFTER build passes, because a broken build is
			// a superset signal that overrides any gate text.
			if extraCheck != nil {
				finalText := extractFinalAssistantText(messages)
				if _, reason := extraCheck(finalText); reason != "" {
					return reason
				}
			}
			return ""
		}
	}

	// Honeypot evaluation (Track A Task 3). Forwards the caller-
	// supplied HoneypotCheckFn to the agentloop. The loop runs
	// this AFTER PreEndTurnCheckFn succeeds, and — unlike build
	// errors — a firing ABORTS the turn (StopReason="honeypot_
	// fired") rather than retrying. The extractor re-uses the
	// same "find the last assistant text" walker as the spec-1
	// item 3 chain so the honeypot sees exactly the text the
	// model is attempting to finalize.
	if spec.HoneypotCheckFn != nil {
		hpCheck := spec.HoneypotCheckFn
		cfg.HoneypotCheckFn = func(messages []agentloop.Message) string {
			return hpCheck(extractFinalAssistantText(messages))
		}
	}

	// Progressive context compaction. When RunSpec.CompactThreshold is
	// set, hook a cache-preserving compactor into the agentloop so long
	// tasks don't blow past the context window. The compactor keeps the
	// first user message (task brief) + the last 6 messages verbatim
	// and summarizes older tool_results.
	if compactionEnabled(spec) {
		cfg.CompactThreshold = spec.CompactThreshold
		cfg.CompactFn = buildNativeCompactor(6, 200)
	}

	// Midturn spec-faithfulness supervisor. When RunSpec.Supervisor
	// is set, install a hook that scans the declared files every N
	// write_file/edit_file tool calls and pushes a [SUPERVISOR NOTE]
	// into the next user message if the code has drifted from the
	// canonical identifiers in the SOW.
	var supervisorFn agentloop.MidturnCheckFunc
	if spec.Supervisor != nil {
		supervisorFn = BuildNativeSupervisor(*spec.Supervisor)
	}
	// Spec-1 item 7 (ghost-write detection) and any future extra
	// hooks chain AFTER the supervisor. Both notes are concatenated
	// with a separator when both fire in the same turn.
	if supervisorFn != nil || spec.ExtraMidturnCheck != nil {
		extraCheck := spec.ExtraMidturnCheck
		cfg.MidturnCheckFn = func(messages []agentloop.Message, turn int) string {
			var notes []string
			if supervisorFn != nil {
				if n := supervisorFn(messages, turn); n != "" {
					notes = append(notes, n)
				}
			}
			if extraCheck != nil {
				tools := extractLastAssistantToolCalls(messages)
				if n := extraCheck(tools, turn); n != "" {
					notes = append(notes, n)
				}
			}
			if len(notes) == 0 {
				return ""
			}
			return strings.Join(notes, "\n\n")
		}
	}

	// Create and configure the loop
	loop := agentloop.New(p, cfg, toolDefs, handler)

	// Wire hub event bus for tool use events
	if n.EventBus != nil {
		loop.SetEventBus(n.EventBus)
	}

	// Wire streaming events if callback provided
	if onEvent != nil {
		loop.SetOnText(func(text string) {
			onEvent(stream.Event{DeltaText: text})
		})
	}

	// User message is spec.Prompt. The cacheable static context was
	// already passed as cfg.SystemPrompt above. When the caller only
	// set Phase.Prompt (legacy behavior before spec.SystemPrompt
	// existed) we still respect it: it's treated as the system prompt
	// and spec.Prompt becomes the user message.
	userMessage := spec.Prompt

	// Run the loop
	result, err := loop.Run(ctx, userMessage)

	duration := time.Since(start)
	runResult := RunResult{
		DurationMs: duration.Milliseconds(),
		Subtype:    "success",
	}

	if result != nil {
		runResult.NumTurns = result.Turns
		runResult.ResultText = result.FinalText
		runResult.Tokens = stream.TokenUsage{
			Input:  result.TotalCost.InputTokens,
			Output: result.TotalCost.OutputTokens,
		}
		runResult.CostUSD = result.TotalCost.TotalCostUSD(n.model)

		switch result.StopReason {
		case "max_turns":
			runResult.Subtype = "error_max_turns"
			runResult.IsError = true
		case "max_errors":
			runResult.Subtype = "error_during_execution"
			runResult.IsError = true
		case "cancelled":
			runResult.Subtype = "cancelled"
			runResult.IsError = true
		}
	}

	if err != nil {
		runResult.IsError = true
		if runResult.Subtype == "success" {
			runResult.Subtype = "error_during_execution"
		}
		return runResult, fmt.Errorf("native runner: %w", err)
	}

	// Emit final result event
	if onEvent != nil {
		onEvent(stream.Event{
			Type:       "result",
			CostUSD:    runResult.CostUSD,
			Tokens:     runResult.Tokens,
			DurationMs: runResult.DurationMs,
			NumTurns:   runResult.NumTurns,
			StopReason: result.StopReason,
			ResultText: result.FinalText,
		})
	}

	return runResult, nil
}

// runEcosystemGate discovers recently-modified files via git,
// groups them by registered Ecosystem (plan package), and runs
// each ecosystem's CompileErrors() over its claimed files. The
// result is a concatenated error message (truncated to 4 KB)
// suitable for injection into the agentloop, or "" if no
// errors were found or no ecosystem claimed any file.
//
// Per-language checks come from internal/plan/integrity_*.go —
// each file self-registers via RegisterEcosystem() in its init.
// The TS ecosystem, for example, finds each file's owning
// package and runs `pnpm exec tsc --noEmit` in THAT directory,
// so per-package tsconfigs are respected in monorepos — a case
// the old flat-bash build command used to miss.
func runEcosystemGate(ctx context.Context, repoDir string) string {
	// Discover modified files. We prefer `git status --porcelain`
	// because it includes both staged and unstaged changes as
	// well as untracked files — all of which the worker may
	// have just produced.
	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	statusCmd.Dir = repoDir
	out, err := statusCmd.Output()
	if err != nil {
		return "" // not a git repo or git unavailable
	}
	lines := strings.Split(string(out), "\n")
	files := make([]string, 0, len(lines))
	for _, line := range lines {
		if len(line) < 4 {
			continue
		}
		// porcelain v1: XY filename (XY is 2 chars + space)
		path := strings.TrimSpace(line[3:])
		// Strip rename arrows: "old -> new"
		if idx := strings.Index(path, " -> "); idx >= 0 {
			path = strings.TrimSpace(path[idx+4:])
		}
		// Strip surrounding quotes for paths with special chars
		path = strings.Trim(path, `"`)
		if path == "" {
			continue
		}
		absPath := filepath.Join(repoDir, path)
		if st, err := os.Stat(absPath); err != nil || st.IsDir() {
			continue
		}
		files = append(files, absPath)
	}
	if len(files) == 0 {
		return ""
	}

	// Bucket files by ecosystem (first match wins).
	byEco := map[string][]string{}
	ecoLookup := map[string]plan.Ecosystem{}
	for _, f := range files {
		eco := plan.EcosystemFor(f)
		if eco == nil {
			continue
		}
		byEco[eco.Name()] = append(byEco[eco.Name()], f)
		ecoLookup[eco.Name()] = eco
	}
	if len(byEco) == 0 {
		return ""
	}

	// Run each ecosystem's CompileErrors and concatenate.
	var sb strings.Builder
	for name, ecoFiles := range byEco {
		eco := ecoLookup[name]
		errs, err := eco.CompileErrors(ctx, repoDir, ecoFiles)
		if err != nil {
			// Gate itself failed (e.g. tsc missing). Surface as
			// hint but do not block — other ecosystems still run.
			sb.WriteString(fmt.Sprintf("\n[%s] gate error: %v\n", name, err))
			continue
		}
		if len(errs) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("\n[%s] compile errors (%d):\n", name, len(errs)))
		for i, e := range errs {
			if i >= 30 {
				sb.WriteString(fmt.Sprintf("  … and %d more\n", len(errs)-30))
				break
			}
			sb.WriteString(fmt.Sprintf("  %s:%d:%d %s %s\n",
				e.File, e.Line, e.Column, e.Code, e.Message))
		}
	}
	msg := sb.String()
	if msg == "" {
		return ""
	}
	if len(msg) > 4000 {
		msg = msg[:4000] + "\n… (truncated)"
	}
	return "Build gate failed — the following compile errors must be fixed before ending the turn:\n" + msg
}

// detectBuildCommand returns the right build/typecheck
// command for the project at dir, or "" if none detected.
// IMPORTANT: pnpm workspaces are checked BEFORE root-tsconfig
// because per-package tsconfigs (not a root tsconfig) are the
// norm — a monorepo with no root tsconfig but broken per-package
// typecheck scripts used to escape the gate via the `|| true`
// JS-project fallthrough. Now we run workspace-wide build or
// typecheck so every package's script is actually exercised.
func detectBuildCommand(dir string) string {
	// pnpm monorepo — highest priority. Run the project's own
	// scripts across all packages so broken per-package scripts
	// (missing tsconfig, malformed typecheck, etc.) surface.
	if _, err := os.Stat(filepath.Join(dir, "pnpm-workspace.yaml")); err == nil {
		return "pnpm -r build 2>&1 || pnpm -r typecheck 2>&1 || pnpm -r tsc --noEmit 2>&1"
	}
	// yarn / npm workspaces
	if pkgHasWorkspaces(dir) {
		return "(pnpm -r build 2>&1 || yarn workspaces run build 2>&1 || npm run build --workspaces 2>&1)"
	}
	// Single-package TypeScript / Node
	if _, err := os.Stat(filepath.Join(dir, "tsconfig.json")); err == nil {
		return "npx tsc --noEmit 2>&1"
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
		return "npx tsc --noEmit 2>&1 || true" // JS project, no tsconfig, skip
	}
	// Go
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		return "go build ./... 2>&1"
	}
	// Rust
	if _, err := os.Stat(filepath.Join(dir, "Cargo.toml")); err == nil {
		return "cargo check 2>&1"
	}
	// Python
	if _, err := os.Stat(filepath.Join(dir, "pyproject.toml")); err == nil {
		return "python -m py_compile $(find . -name '*.py' -not -path '*/venv/*' | head -20) 2>&1"
	}
	// C# / .NET — solution or project files
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.sln")); len(matches) > 0 {
		return "dotnet build --nologo 2>&1"
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.csproj")); len(matches) > 0 {
		return "dotnet build --nologo 2>&1"
	}
	// Java / Kotlin — pom.xml or Gradle
	if _, err := os.Stat(filepath.Join(dir, "pom.xml")); err == nil {
		return "mvn -q compile 2>&1"
	}
	if _, err := os.Stat(filepath.Join(dir, "build.gradle")); err == nil {
		return "./gradlew compileJava compileKotlin 2>&1 || gradle compileJava compileKotlin 2>&1"
	}
	if _, err := os.Stat(filepath.Join(dir, "build.gradle.kts")); err == nil {
		return "./gradlew compileJava compileKotlin 2>&1 || gradle compileJava compileKotlin 2>&1"
	}
	// Elixir
	if _, err := os.Stat(filepath.Join(dir, "mix.exs")); err == nil {
		return "mix compile --warnings-as-errors 2>&1"
	}
	// Swift (SwiftPM) — Xcode projects have their own build system
	if _, err := os.Stat(filepath.Join(dir, "Package.swift")); err == nil {
		return "swift build 2>&1"
	}
	// Ruby — bundler + ruby -c syntax sweep
	if _, err := os.Stat(filepath.Join(dir, "Gemfile")); err == nil {
		return "bundle exec ruby -c $(find . -name '*.rb' -not -path '*/vendor/*' | head -50) 2>&1"
	}
	return ""
}

// renderMCPContent flattens an mcp.ToolResult's content blocks into a
// single string suitable for embedding in the <mcp_result> wrapper that
// goes back to the model. Text blocks are appended verbatim; non-text
// blocks (image / data) are summarized to a "[<type>:<mime>] N bytes"
// marker so the wrapper shape stays text-friendly for the LLM without
// losing provenance. An empty result yields "".
func renderMCPContent(r mcp.ToolResult) string {
	var sb strings.Builder
	for i, c := range r.Content {
		if i > 0 {
			sb.WriteString("\n")
		}
		if c.Type == "text" || c.Text != "" {
			sb.WriteString(c.Text)
			continue
		}
		if len(c.Data) > 0 {
			mime := c.MIME
			if mime == "" {
				mime = "application/octet-stream"
			}
			sb.WriteString(fmt.Sprintf("[%s:%s] %d bytes", c.Type, mime, len(c.Data)))
		}
	}
	return sb.String()
}

// extractCallID is a best-effort scan for a call_id hint inside the
// tool result. The mcp.ToolResult struct doesn't carry call_id in a
// first-class field today (registry emits it on the bus, not in the
// body), so this returns "" unless the server echoed it in a text
// block with a conventional `call_id=...` prefix. Kept as a seam so
// a future MCP-9 change (adding ToolResult.CallID) needs only one edit.
func extractCallID(r mcp.ToolResult) string {
	for _, c := range r.Content {
		if c.Type != "text" {
			continue
		}
		if idx := strings.Index(c.Text, "call_id="); idx >= 0 {
			rest := c.Text[idx+len("call_id="):]
			end := strings.IndexAny(rest, " \t\n\r,;")
			if end < 0 {
				return strings.TrimSpace(rest)
			}
			return strings.TrimSpace(rest[:end])
		}
	}
	return ""
}

// truncateForWorkerLog shortens s to max bytes with a "... <truncated N bytes>"
// marker so reviewers see that truncation happened without scanning for length.
func truncateForWorkerLog(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + fmt.Sprintf("... <truncated %d bytes>", len(s)-maxBytes)
}

// addCtx stamps the non-empty correlation fields from wlc onto the
// given JSONL entry map. Empty strings / zero ints are elided so
// lines stay compact when a field isn't meaningful for the caller.
func addCtx(entry map[string]any, wlc *WorkerLogContext) {
	if wlc == nil {
		return
	}
	if wlc.RunID != "" {
		entry["run_id"] = wlc.RunID
	}
	if wlc.DispatchID != "" {
		entry["dispatch_id"] = wlc.DispatchID
	}
	if wlc.SessionID != "" {
		entry["session_id"] = wlc.SessionID
	}
	if wlc.TaskID != "" {
		entry["task_id"] = wlc.TaskID
	}
	if wlc.Attempt > 0 {
		entry["attempt"] = wlc.Attempt
	}
	if wlc.Depth > 0 {
		entry["depth"] = wlc.Depth
	}
	if wlc.Model != "" {
		entry["model"] = wlc.Model
	}
	if wlc.StokeBuild != "" {
		// S3-3 dual-emit: worker-log entries carry both the legacy
		// `stoke_build` key and the canonical `r1_build` key with
		// identical values during the 30-day rename window
		// (work-r1-rename.md §S3-3).
		entry["stoke_build"] = wlc.StokeBuild
		entry["r1_build"] = wlc.StokeBuild
	}
	if wlc.SOWPath != "" {
		entry["sow_path"] = wlc.SOWPath
	}
	if wlc.PID > 0 {
		entry["pid"] = wlc.PID
	}
	if wlc.PPID > 0 {
		entry["ppid"] = wlc.PPID
	}
	if wlc.PurposeTag != "" {
		entry["purpose"] = wlc.PurposeTag
	}
}

// newShortID returns a compact probabilistically-unique ID with the
// given prefix, used for run/dispatch/call correlation in the worker
// JSONL log. Format: "<prefix>-<12 hex chars>" from crypto/rand.
// Collision probability across a whole run is negligible.
func newShortID(prefix string) string {
	var buf [6]byte
	if _, err := cryptoRandRead(buf[:]); err != nil {
		// Fall back to time-based so we never write an empty ID.
		return fmt.Sprintf("%s-t%x", prefix, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s-%x", prefix, buf[:])
}

// cryptoRandRead is a package-level indirection so tests can stub it.
// Default implementation reads from crypto/rand.
var cryptoRandRead = randReadImpl

// extractLastAssistantToolCalls returns the (tool_use, tool_result)
// pairs from the most recent assistant turn. Used by ExtraMidturnCheck
// hooks (descent-hardening spec-1 item 7) so the hook sees the tools
// the model just called and can act on file_path inputs.
//
// Algorithm: walk from the tail backwards, find the last assistant
// message, collect its tool_use blocks, then look at the IMMEDIATELY
// following user message for tool_result blocks and correlate by id.
// Returns empty slice when the tail isn't assistant-with-tools.
func extractLastAssistantToolCalls(messages []agentloop.Message) []MidturnToolCall {
	if len(messages) < 2 {
		return nil
	}
	// The midturn hook runs AFTER tool results are appended, so the
	// tail is the user message with tool_results and the prior is
	// the assistant with tool_uses.
	last := messages[len(messages)-1]
	if last.Role != "user" {
		return nil
	}
	prev := messages[len(messages)-2]
	if prev.Role != "assistant" {
		return nil
	}
	resultByID := map[string]agentloop.ContentBlock{}
	for _, b := range last.Content {
		if b.Type == "tool_result" {
			resultByID[b.ToolUseID] = b
		}
	}
	out := make([]MidturnToolCall, 0, len(prev.Content))
	for _, b := range prev.Content {
		if b.Type != "tool_use" {
			continue
		}
		call := MidturnToolCall{Name: b.Name, Input: []byte(b.Input)}
		if r, ok := resultByID[b.ID]; ok {
			call.Result = r.Content
			call.IsError = r.IsError
		}
		out = append(out, call)
	}
	return out
}

// extractFinalAssistantText walks the message history from the tail
// forwards to pluck the most recent assistant text. Used by the
// pre-end-turn gate chain (descent-hardening spec-1 item 3) so the
// caller-supplied check can see the model's claimed completion text.
// Returns "" when no assistant message is present.
func extractFinalAssistantText(messages []agentloop.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "assistant" {
			continue
		}
		var b strings.Builder
		for _, c := range messages[i].Content {
			if c.Type == "text" {
				b.WriteString(c.Text)
				b.WriteString("\n")
			}
		}
		return b.String()
	}
	return ""
}

// pkgHasWorkspaces returns true when the root package.json
// declares a `workspaces` array (npm/yarn monorepo).
func pkgHasWorkspaces(dir string) bool {
	pj, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	// minimal parse — just detect the presence of the "workspaces"
	// key at the top level without pulling in json deps here.
	// We're OK with a false positive in edge cases (a `"workspaces"`
	// string inside a description); the resulting command is still
	// a safe fallback.
	return strings.Contains(string(pj), "\"workspaces\"")
}
