package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"stoke/internal/config"
	"stoke/internal/hooks"
	"stoke/internal/stream"
)

type ClaudeRunner struct {
	Binary string
	Parser *stream.Parser
}

func NewClaudeRunner(binary string) *ClaudeRunner {
	if strings.TrimSpace(binary) == "" {
		binary = "claude"
	}
	return &ClaudeRunner{Binary: binary, Parser: stream.DefaultParser()}
}

func (r *ClaudeRunner) Prepare(spec RunSpec) (PreparedCommand, error) {
	if strings.TrimSpace(spec.WorktreeDir) == "" {
		return PreparedCommand{}, fmt.Errorf("missing worktree dir")
	}
	// All harness-owned files go to RuntimeDir (outside worktree, trusted).
	// This prevents symlink attacks from the agent-writable worktree.
	runtimeDir := spec.RuntimeDir
	if runtimeDir == "" {
		return PreparedCommand{}, fmt.Errorf("missing runtime dir (harness files must not be in worktree)")
	}
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
		json.Unmarshal(raw, &merged)
		for k, v := range hooksConf { merged[k] = v }
		raw, _ = json.MarshalIndent(merged, "", "  ")
	}

	if err := os.WriteFile(settingsPath, raw, 0o644); err != nil {
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
		if err := os.WriteFile(emptyMCPPath, []byte("{}"), 0o644); err != nil {
			return PreparedCommand{}, err
		}
		args = append(args, "--strict-mcp-config", "--mcp-config", emptyMCPPath)
		notes = append(notes, "MCP disabled via strict empty config + mcp__* deny")
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

	cmd := exec.CommandContext(ctx, prepared.Binary, prepared.Args...)
	cmd.Dir = prepared.Dir
	cmd.Env = prepared.Env

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
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
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
