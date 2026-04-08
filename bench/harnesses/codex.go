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

// Codex drives the codex CLI.
type Codex struct {
	BinaryPath string // path to codex binary; defaults to "codex"
	Model      string // optional model override
}

func (c *Codex) Name() string    { return "codex" }
func (c *Codex) Image() string   { return "" }
func (c *Codex) Version() string { return c.binPath() }

func (c *Codex) binPath() string {
	if c.BinaryPath != "" {
		return c.BinaryPath
	}
	return "codex"
}

func (c *Codex) Run(ctx context.Context, taskMount string) RunResult {
	started := time.Now()
	res := RunResult{
		HarnessName: c.Name(),
		TaskID:      filepath.Base(taskMount),
		Started:     started,
	}

	// Read the task prompt.
	prompt := "Complete the task in this directory."
	promptPath := filepath.Join(taskMount, "prompt.md")
	if data, err := os.ReadFile(promptPath); err == nil {
		prompt = string(data)
	}

	args := []string{"--quiet", "--approval-mode", "full-auto"}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	args = append(args, prompt)

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.binPath(), args...)
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
