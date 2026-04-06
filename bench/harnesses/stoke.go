package harnesses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Stoke drives the stoke CLI in --runner native mode.
type Stoke struct {
	BinaryPath string // path to stoke binary; defaults to "stoke"
	Model      string // model name, e.g. "claude-sonnet-4-20250514"
}

func (s *Stoke) Name() string    { return "stoke" }
func (s *Stoke) Image() string   { return "" } // native execution, no container
func (s *Stoke) Version() string { return s.binPath() + " (native)" }

func (s *Stoke) binPath() string {
	if s.BinaryPath != "" {
		return s.BinaryPath
	}
	return "stoke"
}

// stokeOutput is the subset of stoke JSON output we parse for metrics.
type stokeOutput struct {
	CostUSD          float64 `json:"cost_usd"`
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	APICallCount     int     `json:"api_call_count"`
}

func (s *Stoke) Run(ctx context.Context, taskMount string) RunResult {
	started := time.Now()
	res := RunResult{
		HarnessName: s.Name(),
		TaskID:      filepath.Base(taskMount),
		Started:     started,
	}

	args := []string{"--runner", "native", "--dir", taskMount}
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	// Expect a prompt.md in the task directory.
	promptPath := filepath.Join(taskMount, "prompt.md")
	if data, err := os.ReadFile(promptPath); err == nil {
		args = append(args, "--message", string(data))
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, s.binPath(), args...)
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

	// Capture assistant output.
	if text := strings.TrimSpace(stdout.String()); text != "" {
		res.AssistantTexts = []string{text}
	}

	// Try to parse structured metrics from a results file stoke may emit.
	resultsPath := filepath.Join(taskMount, ".stoke", "results.json")
	if data, err := os.ReadFile(resultsPath); err == nil {
		var so stokeOutput
		if json.Unmarshal(data, &so) == nil {
			res.CostUSD = so.CostUSD
			res.InputTokens = so.InputTokens
			res.OutputTokens = so.OutputTokens
			res.CacheReadTokens = so.CacheReadTokens
			res.CacheWriteTokens = so.CacheWriteTokens
			res.APICallCount = so.APICallCount
		}
	}

	// Collect output files (everything modified under taskMount).
	res.OutputFiles = collectModifiedFiles(taskMount)

	return res
}

// collectModifiedFiles walks the task directory and returns files that are
// not hidden directories (skips .git, .stoke, etc.) as a simple heuristic
// for files the harness may have created or modified.
func collectModifiedFiles(root string) []string {
	var files []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		// Skip hidden directories.
		if info.IsDir() && strings.HasPrefix(info.Name(), ".") && rel != "." {
			return filepath.SkipDir
		}
		if !info.IsDir() {
			files = append(files, rel)
		}
		return nil
	})
	return files
}
