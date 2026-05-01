package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type ClaudeCodeExecutorConfig struct {
	Binary         string
	OutBase        string
	DefaultTimeout time.Duration
}

type ClaudeCodeExecutor struct {
	binary         string
	outBase        string
	defaultTimeout time.Duration
}

func NewClaudeCodeExecutor(cfg ClaudeCodeExecutorConfig) ClaudeCodeExecutor {
	binary := strings.TrimSpace(cfg.Binary)
	if binary == "" {
		binary = "claude"
	}
	timeout := cfg.DefaultTimeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	return ClaudeCodeExecutor{
		binary:         binary,
		outBase:        cfg.OutBase,
		defaultTimeout: timeout,
	}
}

func (c ClaudeCodeExecutor) Type() string { return "claude-code" }

func (c ClaudeCodeExecutor) Capabilities() []string { return []string{"claude", "claude-code"} }

func (c ClaudeCodeExecutor) Execute(ctx context.Context, t *Task) ExecutionResult {
	outDir := artifactDir(c.outBase, t)
	timeout := taskTimeout(t, c.defaultTimeout)
	runCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	prompt := claudePrompt(t)
	args := []string{"--print", "--no-session-persistence", prompt}
	if t != nil && t.Meta != nil {
		if model := strings.TrimSpace(t.Meta["claude_model"]); model != "" {
			args = append(args, "--model", model)
		}
	}
	cmd := exec.CommandContext(runCtx, c.binary, args...) // #nosec G204 -- claude-code executor launches a configured local CLI with daemon-managed args.
	if t != nil && strings.TrimSpace(t.Repo) != "" {
		cmd.Dir = t.Repo
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	startedAt := time.Now().UTC()
	err := cmd.Run()
	finishedAt := time.Now().UTC()

	exitCode := 0
	finishReason := "completed"
	if err != nil {
		var exitErr *exec.ExitError
		switch {
		case errors.Is(runCtx.Err(), context.DeadlineExceeded):
			finishReason = "timeout"
		case errors.Is(runCtx.Err(), context.Canceled):
			finishReason = "cancelled"
		case errors.As(err, &exitErr):
			exitCode = exitErr.ExitCode()
			finishReason = fmt.Sprintf("exit-%d", exitCode)
		default:
			exitCode = -1
			finishReason = "start-error"
		}
	}

	lastMessage := strings.TrimSpace(stdout.String())
	lastMessagePath := filepath.Join(outDir, "last-message.txt")
	stderrPath := filepath.Join(outDir, "stderr.txt")
	resultPath := filepath.Join(outDir, "result.json")
	if writeErr := writeExecutorFile(lastMessagePath, []byte(lastMessage)); writeErr != nil {
		return ExecutionResult{Err: writeErr}
	}
	if writeErr := writeExecutorFile(stderrPath, stderr.Bytes()); writeErr != nil {
		return ExecutionResult{Err: writeErr}
	}
	resultDoc := map[string]any{
		"task_id":       t.ID,
		"title":         t.Title,
		"repo":          t.Repo,
		"started_at":    startedAt.Format(time.RFC3339Nano),
		"finished_at":   finishedAt.Format(time.RFC3339Nano),
		"exit_code":     exitCode,
		"finish_reason": finishReason,
		"binary":        c.binary,
	}
	if writeErr := writeExecutorJSON(resultPath, resultDoc); writeErr != nil {
		return ExecutionResult{Err: writeErr}
	}

	proofsPath, writeProofsErr := WriteProofs(outDir, t.ID, []ProofRecord{{
		Claim:         fmt.Sprintf("claude-code executor finished with reason %s", finishReason),
		EvidenceType:  "file_line",
		EvidenceValue: resultPath + ":1",
		Source:        "daemon.ClaudeCodeExecutor",
	}, {
		Claim:         "claude-code executor captured the final assistant message",
		EvidenceType:  "file_line",
		EvidenceValue: lastMessagePath + ":1",
		Source:        "daemon.ClaudeCodeExecutor",
	}, {
		Claim:         "claude-code executor captured stderr output",
		EvidenceType:  "file_line",
		EvidenceValue: stderrPath + ":1",
		Source:        "daemon.ClaudeCodeExecutor",
	}})
	if writeProofsErr != nil {
		return ExecutionResult{Err: writeProofsErr}
	}

	actual := int64(len(lastMessage))
	if actual == 0 {
		actual = proofsActualBytes(proofsPath, int64(stderr.Len()))
	}
	res := ExecutionResult{
		ActualBytes: actual,
		MissionID:   fmt.Sprintf("claude-code-%s-attempt-%d", sanitizeExecutorID(t.ID), t.Attempts),
		ProofsPath:  proofsPath,
	}
	if err != nil {
		if runErr := runCtx.Err(); runErr != nil {
			res.Err = runErr
			return res
		}
		stderrText := strings.TrimSpace(stderr.String())
		if stderrText == "" {
			stderrText = strings.TrimSpace(err.Error())
		}
		res.Err = fmt.Errorf("claude-code executor %s: %s", finishReason, stderrText)
	}
	return res
}

func claudePrompt(t *Task) string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	if t.Title != "" {
		fmt.Fprintf(&b, "Task: %s\n", t.Title)
	}
	if t.Repo != "" {
		fmt.Fprintf(&b, "Repo: %s\n", t.Repo)
	}
	if t.ResumeCheckpoint != "" {
		fmt.Fprintf(&b, "Resume checkpoint: %s\n", t.ResumeCheckpoint)
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString(strings.TrimSpace(t.Prompt))
	return b.String()
}
