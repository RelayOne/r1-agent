package harnesses

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Aider drives the aider CLI.
type Aider struct {
	BinaryPath string // path to aider binary; defaults to "aider"
	Model      string // optional model override, e.g. "claude-sonnet-4-20250514"
}

func (a *Aider) Name() string    { return "aider" }
func (a *Aider) Image() string   { return "" }
func (a *Aider) Version() string { return a.binPath() }

func (a *Aider) binPath() string {
	if a.BinaryPath != "" {
		return a.BinaryPath
	}
	return "aider"
}

func (a *Aider) Run(ctx context.Context, taskMount string) RunResult {
	started := time.Now()
	res := RunResult{
		HarnessName: a.Name(),
		TaskID:      filepath.Base(taskMount),
		Started:     started,
	}

	// Read the task prompt.
	prompt := "Complete the task in this directory."
	promptPath := filepath.Join(taskMount, "prompt.md")
	if data, err := os.ReadFile(promptPath); err == nil {
		prompt = string(data)
	}

	args := []string{
		"--yes-always",  // non-interactive: auto-accept all changes
		"--no-auto-commits",
		"--message", prompt,
	}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, a.binPath(), args...) // #nosec G204 -- benchmark harness binary with Stoke-generated args.
	cmd.Dir = taskMount
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	res.Ended = time.Now()

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
		}
		if ctx.Err() != nil {
			res.TimedOut = true
		}
		res.Error = err.Error()
	}

	if text := strings.TrimSpace(stdout.String()); text != "" {
		res.AssistantTexts = []string{text}
	}

	res.OutputFiles = collectModifiedFiles(taskMount)

	return res
}
