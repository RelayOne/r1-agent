package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"stoke/internal/stream"
)

type CodexRunner struct {
	Binary string
}

func NewCodexRunner(binary string) *CodexRunner {
	if strings.TrimSpace(binary) == "" {
		binary = "codex"
	}
	return &CodexRunner{Binary: binary}
}

func (r *CodexRunner) Prepare(spec RunSpec) (PreparedCommand, error) {
	if strings.TrimSpace(spec.WorktreeDir) == "" {
		return PreparedCommand{}, fmt.Errorf("missing worktree dir")
	}
	runtimeDir := spec.RuntimeDir
	if runtimeDir == "" {
		return PreparedCommand{}, fmt.Errorf("missing runtime dir")
	}
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return PreparedCommand{}, err
	}

	sandbox := "workspace-write"
	profile := "task"
	if spec.Phase.ReadOnly {
		sandbox = "read-only"
		profile = "review"
	}

	lastMessagePath := filepath.Join(runtimeDir, fmt.Sprintf("codex-%s-last-message.txt", spec.Phase.Name))
	args := []string{"exec", "--cd", spec.WorktreeDir, "--sandbox", sandbox, "--json", "--output-last-message", lastMessagePath, "--profile", profile, spec.Prompt}

	env := safeEnvMode2(nil)
	if spec.Mode == AuthModeMode1 {
		env = safeEnvForCodexMode1(spec.PoolConfigDir)
	}

	return PreparedCommand{Binary: r.Binary, Args: args, Dir: spec.WorktreeDir, Env: env, Notes: []string{"Codex CLI runner"}, LastMessagePath: lastMessagePath}, nil
}

// Run spawns codex exec with process group isolation and stderr rate limit detection.
func (r *CodexRunner) Run(ctx context.Context, spec RunSpec, onEvent OnEventFunc) (RunResult, error) {
	prepared, err := r.Prepare(spec)
	if err != nil {
		return RunResult{}, err
	}

	cmd := exec.CommandContext(ctx, prepared.Binary, prepared.Args...)
	cmd.Dir = prepared.Dir
	cmd.Env = prepared.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return RunResult{}, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return RunResult{}, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return RunResult{}, fmt.Errorf("start codex: %w", err)
	}

	// Read stderr for rate limit detection (Codex prints "429 Too Many Requests" there)
	stderrDone := make(chan string, 1)
	go func() {
		var sb strings.Builder
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			sb.WriteString(scanner.Text() + "\n")
		}
		stderrDone <- sb.String()
	}()

	// Parse stdout JSONL
	result := RunResult{Prepared: prepared}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 512*1024), 2*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] != '{' {
			continue
		}
		if !json.Valid([]byte(line)) {
			continue
		}

		// Parse codex events for token accumulation
		var raw struct {
			Type  string `json:"type"`
			Usage *struct {
				InputTokens       int `json:"input_tokens"`
				CachedInputTokens int `json:"cached_input_tokens"`
				OutputTokens      int `json:"output_tokens"`
			} `json:"usage,omitempty"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}

		if raw.Usage != nil {
			result.Tokens.Input += raw.Usage.InputTokens
			result.Tokens.Output += raw.Usage.OutputTokens
			result.Tokens.CacheRead += raw.Usage.CachedInputTokens
		}

		if onEvent != nil {
			onEvent(stream.Event{Type: raw.Type, Raw: []byte(line)})
		}
	}

	// Wait for process
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case waitErr := <-waitDone:
		stderrText := <-stderrDone
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				result.ExitCode = exitErr.ExitCode()
			}
			result.IsError = true
			result.Subtype = "error_during_execution"
		} else {
			result.Subtype = "success"
		}
		// Check stderr for rate limits
		if strings.Contains(stderrText, "429") || strings.Contains(stderrText, "usage limit") {
			result.IsError = true
			result.Subtype = "rate_limited"
		}

		// Read actual review output from last-message file (not stderr)
		lastMsg, readErr := os.ReadFile(prepared.LastMessagePath)
		if readErr == nil && len(strings.TrimSpace(string(lastMsg))) > 0 {
			result.ResultText = strings.TrimSpace(string(lastMsg))
		} else {
			// Fallback: if no last-message file, use stderr but flag it
			result.ResultText = stderrText
			if !result.IsError && result.Subtype == "success" {
				result.IsError = true
				result.Subtype = "missing_review_output"
			}
		}

	case <-time.After(60 * time.Second):
		killProcessGroup(cmd)
		result.IsError = true
		result.Subtype = "timeout_after_result"
		// Drain stderrDone to prevent goroutine leak
		select {
		case <-stderrDone:
		default:
		}
	}

	return result, nil
}
