// Package engine provides Claude Code and Codex CLI runners with process group isolation and streaming output.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/RelayOne/r1-agent/internal/config"
	"github.com/RelayOne/r1-agent/internal/hooks"
	"github.com/RelayOne/r1-agent/internal/stream"
)

// ClaudeRunner executes tasks via the Claude Code CLI with NDJSON streaming and 3-tier timeout handling.
type ClaudeRunner struct {
	Binary     string
	Parser     *stream.Parser
	CacheStats *stream.PromptCacheStats // shared, records cache hits per execution
}

// NewClaudeRunner creates a ClaudeRunner using the given binary path, defaulting to "claude" if empty.
func NewClaudeRunner(binary string) *ClaudeRunner {
	if strings.TrimSpace(binary) == "" {
		binary = "claude"
	}
	return &ClaudeRunner{Binary: binary, Parser: stream.DefaultParser()}
}

// Prepare builds the CLI command, settings file, and environment for a Claude Code invocation without executing it.
func (r *ClaudeRunner) Prepare(spec RunSpec) (PreparedCommand, error) {
	if err := spec.Validate(); err != nil {
		return PreparedCommand{}, err
	}
	runtimeDir := spec.RuntimeDir
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return PreparedCommand{}, err
	}

	settingsPath := filepath.Join(runtimeDir, fmt.Sprintf("claude-settings-%s.json", spec.Phase.Name))
	settings := config.BuildClaudeSettings(config.ClaudeSettingsOptions{
		Mode:                  string(spec.Mode),
		Phase:                 config.PhasePolicy{BuiltinTools: spec.Phase.BuiltinTools, AllowedRules: spec.Phase.AllowedRules, DeniedRules: spec.Phase.DeniedRules, MCPEnabled: spec.Phase.MCPEnabled},
		SandboxEnabled:        spec.SandboxEnabled,
		SandboxAllowedDomains: append([]string(nil), spec.SandboxDomains...),
		SandboxAllowRead:      append([]string(nil), spec.SandboxAllowRead...),
		SandboxAllowWrite:     append([]string(nil), spec.SandboxAllowWrite...),
	})
	raw, err := config.MarshalClaudeSettings(settings)
	if err != nil {
		return PreparedCommand{}, err
	}

	// Merge enforcer hooks config into settings if hooks are installed
	hooksConf := hooks.HooksConfig(runtimeDir)
	if len(hooksConf) > 0 {
		var merged map[string]interface{}
		if err = json.Unmarshal(raw, &merged); err != nil {
			return PreparedCommand{}, fmt.Errorf("unmarshal settings for hooks merge: %w", err)
		}
		for k, v := range hooksConf {
			merged[k] = v
		}
		raw, err = json.MarshalIndent(merged, "", "  ")
		if err != nil {
			return PreparedCommand{}, fmt.Errorf("marshal merged settings: %w", err)
		}
	}

	if err := os.WriteFile(settingsPath, raw, 0o644); err != nil { // #nosec G306 -- MCP config stub for Claude Code subprocess; 0644 is standard.
		return PreparedCommand{}, err
	}

	args := []string{"-p", spec.Prompt, "--output-format", "stream-json", "--tools", strings.Join(spec.Phase.BuiltinTools, ","), "--max-turns", strconv.Itoa(spec.Phase.MaxTurns), "--settings", settingsPath}
	if len(spec.Phase.DeniedRules) > 0 {
		denied := append([]string{}, spec.Phase.DeniedRules...)
		if !spec.Phase.MCPEnabled {
			denied = append(denied, "mcp__*")
		}
		args = append(args, "--disallowedTools")
		args = append(args, strings.Join(denied, ","))
	} else if !spec.Phase.MCPEnabled {
		args = append(args, "--disallowedTools", "mcp__*")
	}
	if len(spec.Phase.AllowedRules) > 0 {
		args = append(args, "--allowedTools")
		args = append(args, strings.Join(spec.Phase.AllowedRules, ","))
	}
	notes := []string{"Claude Code runner"}
	if !spec.Phase.MCPEnabled {
		emptyMCPPath := filepath.Join(runtimeDir, fmt.Sprintf("empty-mcp-%s.json", spec.Phase.Name))
		if err := os.WriteFile(emptyMCPPath, []byte("{}"), 0o644); err != nil { // #nosec G306 -- MCP config stub for Claude Code subprocess; 0644 is standard.
			return PreparedCommand{}, err
		}
		args = append(args, "--strict-mcp-config", "--mcp-config", emptyMCPPath)
		notes = append(notes, "MCP disabled via strict empty config + mcp__* deny")
	} else if spec.MCPConfigPath != "" {
		// MCP enabled with a custom config (e.g., codebase analysis tools for discovery)
		args = append(args, "--mcp-config", spec.MCPConfigPath)
		notes = append(notes, "MCP enabled with custom config: "+spec.MCPConfigPath)
	}

	env := safeEnvMode2(nil)
	if spec.Mode == AuthModeMode1 {
		env = safeEnvForClaudeMode1(spec.PoolConfigDir)
	}

	return PreparedCommand{Binary: r.Binary, Args: args, Dir: spec.WorktreeDir, Env: env, Notes: notes}, nil
}

// Run spawns claude -p with streaming output, process group isolation,
// and 3-tier timeout handling via the stream parser.
func (r *ClaudeRunner) Run(ctx context.Context, spec RunSpec, onEvent OnEventFunc) (RunResult, error) {
	prepared, err := r.Prepare(spec)
	if err != nil {
		return RunResult{}, err
	}

	var cmd *exec.Cmd
	if spec.ContainerImage != "" && spec.ContainerVol != "" {
		// Wrap in docker run for container pool execution
		cmd = wrapInDocker(ctx, prepared, spec)
	} else {
		cmd = exec.CommandContext(ctx, prepared.Binary, prepared.Args...) // #nosec G204 -- CLI runner launches vetted provider binary with Stoke-generated args.
		cmd.Dir = prepared.Dir
		cmd.Env = prepared.Env
	}

	// Process group isolation: prevents orphaned claude/node subprocesses (#33979)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return RunResult{}, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = nil // discard stderr (sandbox debug noise on Linux: #12007)

	if err := cmd.Start(); err != nil {
		return RunResult{}, fmt.Errorf("start claude: %w", err)
	}

	// Parse the NDJSON stream with 3-tier timeouts
	done := make(chan struct{})
	events := r.Parser.Parse(stdout, done)

	result := RunResult{Prepared: prepared}
	for ev := range events {
		if onEvent != nil {
			onEvent(ev)
		}
		if ev.Type == "result" {
			result.CostUSD = ev.CostUSD
			result.DurationMs = ev.DurationMs
			result.NumTurns = ev.NumTurns
			result.Tokens = ev.Tokens
			result.Subtype = ev.Subtype
			result.IsError = ev.IsError
			result.ResultText = ev.ResultText
			// Track prompt cache stats for cost optimization reporting
			if r.CacheStats != nil {
				r.CacheStats.Record(ev.Tokens)
			}
		}
		if ev.Type == "rate_limit_event" {
			result.IsError = true
			result.Subtype = "rate_limited"
		}
	}

	// Wait for process exit (with timeout for the hang-after-result bug #25629)
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case waitErr := <-waitDone:
		if waitErr != nil {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				result.ExitCode = exitErr.ExitCode()
			}
			// Don't return error for exit code 1 -- parse subtype instead (#15685: exit 0 on rate limit)
		}
	case <-time.After(r.Parser.PostResultTimeout + 5*time.Second):
		killProcessGroup(cmd)
		result.IsError = true
		result.Subtype = "timeout_after_result"
	}

	<-done // wait for parser goroutine to finish
	return result, nil
}

// killProcessGroup sends SIGTERM then SIGKILL to the process group.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		cmd.Process.Kill()
		return
	}
	syscall.Kill(-pgid, syscall.SIGTERM)
	time.Sleep(3 * time.Second)
	syscall.Kill(-pgid, syscall.SIGKILL)
}
