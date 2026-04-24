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

// ClaudeCode drives the claude CLI (Claude Code) with --print mode.
type ClaudeCode struct {
	BinaryPath string // path to claude binary; defaults to "claude"
	Model      string // optional model override
}

func (c *ClaudeCode) Name() string    { return "claude-code" }
func (c *ClaudeCode) Image() string   { return "" }
func (c *ClaudeCode) Version() string { return c.binPath() }

func (c *ClaudeCode) binPath() string {
	if c.BinaryPath != "" {
		return c.BinaryPath
	}
	return "claude"
}

func (c *ClaudeCode) Run(ctx context.Context, taskMount string) RunResult {
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

	args := []string{"--print", "--output-format", "text"}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	args = append(args, prompt)

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.binPath(), args...) // #nosec G204 -- benchmark harness binary with Stoke-generated args.
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
