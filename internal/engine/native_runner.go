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

	"github.com/ericmacdougall/stoke/internal/agentloop"
	"github.com/ericmacdougall/stoke/internal/hub"
	"github.com/ericmacdougall/stoke/internal/plan"
	"github.com/ericmacdougall/stoke/internal/provider"
	"github.com/ericmacdougall/stoke/internal/stream"
	"github.com/ericmacdougall/stoke/internal/tools"
)

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
	var toolDefs []provider.ToolDef
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

	// Build allowed tool set for handler enforcement.
	allowedTools := make(map[string]bool, len(toolDefs))
	for _, td := range toolDefs {
		allowedTools[td.Name] = true
	}

	// Create the tool handler that bridges tools.Registry → agentloop.ToolHandler
	// and dispatches any ExtraTool calls to their attached handler.
	handler := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		if !allowedTools[name] {
			return "", fmt.Errorf("tool %q not allowed in phase %q (read_only=%v)", name, spec.Phase.Name, spec.Phase.ReadOnly)
		}
		if h, ok := extraHandlers[name]; ok {
			return h(ctx, input)
		}
		return toolRegistry.Handle(ctx, name, input)
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
		cfg.PreEndTurnCheckFn = func(_ []agentloop.Message) string {
			if buildChecked {
				return ""
			}
			buildChecked = true
			if msg := runEcosystemGate(ctx, spec.WorktreeDir); msg != "" {
				return msg
			}
			// No ecosystem match or no errors — fall back to
			// the bash-command build gate for cases where the
			// ecosystem registry doesn't cover the repo shape.
			buildCmd := detectBuildCommand(spec.WorktreeDir)
			if buildCmd == "" {
				return ""
			}
			cmd := exec.CommandContext(ctx, "bash", "-lc", buildCmd)
			cmd.Dir = spec.WorktreeDir
			out, err := cmd.CombinedOutput()
			if err != nil {
				output := string(out)
				if len(output) > 4000 {
					output = output[len(output)-4000:]
				}
				return fmt.Sprintf("Build command failed: %s\n\nErrors:\n%s", buildCmd, output)
			}
			return ""
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
	if spec.Supervisor != nil {
		if fn := BuildNativeSupervisor(*spec.Supervisor); fn != nil {
			cfg.MidturnCheckFn = fn
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
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
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
