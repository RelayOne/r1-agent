package engine

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// GeminiRunner drives the Gemini CLI or API.
type GeminiRunner struct {
	Binary  string
	Timeout time.Duration
}

func NewGeminiRunner(binary string) *GeminiRunner {
	if strings.TrimSpace(binary) == "" {
		binary = "gemini" // default binary name
	}
	return &GeminiRunner{Binary: binary, Timeout: 10 * time.Minute}
}

func (r *GeminiRunner) Prepare(spec RunSpec) (PreparedCommand, error) {
	if strings.TrimSpace(spec.WorktreeDir) == "" {
		return PreparedCommand{}, fmt.Errorf("missing worktree dir")
	}
	// Build args for the gemini CLI: --prompt delivers the task, --dir sets the working directory.
	args := []string{"--prompt", spec.Prompt, "--dir", spec.WorktreeDir}
	env := safeEnvMode2(nil)
	if apiKey := spec.PoolAPIKey; apiKey != "" {
		env = append(env, "GEMINI_API_KEY="+apiKey)
	}
	return PreparedCommand{
		Binary: r.Binary, Args: args, Dir: spec.WorktreeDir, Env: env,
		Notes: []string{"Gemini runner (CLI mode)"},
	}, nil
}

func (r *GeminiRunner) Run(ctx context.Context, spec RunSpec, onEvent OnEventFunc) (RunResult, error) {
	prepared, err := r.Prepare(spec)
	if err != nil {
		return RunResult{}, err
	}

	// Check if binary exists
	if _, lookErr := exec.LookPath(prepared.Binary); lookErr != nil {
		return RunResult{}, fmt.Errorf("gemini binary not found: %s (install gemini CLI or set --gemini-bin)", prepared.Binary)
	}

	cmd := exec.CommandContext(ctx, prepared.Binary, prepared.Args...)
	cmd.Dir = prepared.Dir
	cmd.Env = prepared.Env

	out, err := cmd.CombinedOutput()
	result := RunResult{Prepared: prepared, ResultText: string(out)}
	if err != nil {
		result.IsError = true
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		}
	}
	return result, nil
}
