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

type BashExecutorConfig struct {
	Shell          string
	OutBase        string
	DefaultTimeout time.Duration
}

type BashExecutor struct {
	shell          string
	outBase        string
	defaultTimeout time.Duration
}

func NewBashExecutor(cfg BashExecutorConfig) BashExecutor {
	shell := strings.TrimSpace(cfg.Shell)
	if shell == "" {
		shell = "/bin/bash"
	}
	timeout := cfg.DefaultTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return BashExecutor{
		shell:          shell,
		outBase:        cfg.OutBase,
		defaultTimeout: timeout,
	}
}

func (b BashExecutor) Type() string { return "bash" }

func (b BashExecutor) Capabilities() []string { return []string{"bash", "native", "shell"} }

func (b BashExecutor) Execute(ctx context.Context, t *Task) ExecutionResult {
	outDir := artifactDir(b.outBase, t)
	timeout := taskTimeout(t, b.defaultTimeout)
	runCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, b.shell, "-lc", t.Prompt) // #nosec G204 -- bash executor intentionally runs task-provided shell in daemon mode.
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

	stdoutPath := filepath.Join(outDir, "stdout.txt")
	stderrPath := filepath.Join(outDir, "stderr.txt")
	resultPath := filepath.Join(outDir, "result.json")
	if writeErr := writeExecutorFile(stdoutPath, stdout.Bytes()); writeErr != nil {
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
		"shell":         b.shell,
	}
	if writeErr := writeExecutorJSON(resultPath, resultDoc); writeErr != nil {
		return ExecutionResult{Err: writeErr}
	}

	proofsPath, writeProofsErr := WriteProofs(outDir, t.ID, []ProofRecord{{
		Claim:         fmt.Sprintf("bash executor finished with reason %s", finishReason),
		EvidenceType:  "file_line",
		EvidenceValue: resultPath + ":1",
		Source:        "daemon.BashExecutor",
	}, {
		Claim:         "bash executor captured stdout",
		EvidenceType:  "file_line",
		EvidenceValue: stdoutPath + ":1",
		Source:        "daemon.BashExecutor",
	}, {
		Claim:         "bash executor captured stderr",
		EvidenceType:  "file_line",
		EvidenceValue: stderrPath + ":1",
		Source:        "daemon.BashExecutor",
	}})
	if writeProofsErr != nil {
		return ExecutionResult{Err: writeProofsErr}
	}

	res := ExecutionResult{
		ActualBytes: proofsActualBytes(proofsPath, int64(stdout.Len()+stderr.Len())),
		MissionID:   fmt.Sprintf("bash-%s-attempt-%d", sanitizeExecutorID(t.ID), t.Attempts),
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
		res.Err = fmt.Errorf("bash executor %s: %s", finishReason, stderrText)
	}
	return res
}
