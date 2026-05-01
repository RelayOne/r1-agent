package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type CodexExecutorConfig struct {
	Binary         string
	JobsDir        string
	DefaultEffort  string
	PollInterval   time.Duration
	StartTimeout   time.Duration
	DefaultTimeout time.Duration
}

type CodexExecutor struct {
	binary         string
	jobsDir        string
	defaultEffort  string
	pollInterval   time.Duration
	startTimeout   time.Duration
	defaultTimeout time.Duration
}

type codexJobState struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	EstimateBytes  int64  `json:"estimate_bytes"`
	ActualBytes    *int64 `json:"actual_bytes"`
	DeltaPct       *int   `json:"delta_pct"`
	Underdelivered bool   `json:"underdelivered"`
	Exit           *int   `json:"exit"`
	PID            *int   `json:"pid"`
	Started        any    `json:"started"`
	Finished       any    `json:"finished"`
}

func NewCodexExecutor(cfg CodexExecutorConfig) CodexExecutor {
	binary := strings.TrimSpace(cfg.Binary)
	if binary == "" {
		binary = "/home/eric/.local/bin/codexjob"
	}
	jobsDir := strings.TrimSpace(cfg.JobsDir)
	if jobsDir == "" {
		home, err := os.UserHomeDir()
		if err == nil && strings.TrimSpace(home) != "" {
			jobsDir = filepath.Join(home, "repos", "plans", "codex-jobs")
		}
	}
	effort := strings.TrimSpace(cfg.DefaultEffort)
	if effort == "" {
		effort = "medium"
	}
	pollInterval := cfg.PollInterval
	if pollInterval <= 0 {
		pollInterval = 750 * time.Millisecond
	}
	startTimeout := cfg.StartTimeout
	if startTimeout <= 0 {
		startTimeout = 15 * time.Second
	}
	defaultTimeout := cfg.DefaultTimeout
	if defaultTimeout <= 0 {
		defaultTimeout = time.Hour
	}
	return CodexExecutor{
		binary:         binary,
		jobsDir:        jobsDir,
		defaultEffort:  effort,
		pollInterval:   pollInterval,
		startTimeout:   startTimeout,
		defaultTimeout: defaultTimeout,
	}
}

func (c CodexExecutor) Type() string { return "codex" }

func (c CodexExecutor) Capabilities() []string { return []string{"codex"} }

func (c CodexExecutor) Execute(ctx context.Context, t *Task) ExecutionResult {
	timeout := taskTimeout(t, c.defaultTimeout)
	runCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	jobID := c.selectJobID(t)
	if state, ok, err := c.readState(jobID); err == nil && ok {
		switch state.Status {
		case "done":
			return c.resultFromState(jobID, state, nil)
		case "running", "queued":
			return c.waitForJob(runCtx, jobID)
		}
	}

	if err := c.startJob(runCtx, jobID, t); err != nil {
		return ExecutionResult{MissionID: jobID, Err: err}
	}
	return c.waitForJob(runCtx, jobID)
}

func (c CodexExecutor) selectJobID(t *Task) string {
	attempt := 1
	if t != nil && t.Attempts > 0 {
		attempt = t.Attempts
	}
	if t != nil && isRecoveredFromWAL(t) && t.Attempts > 1 {
		previousID := c.jobID(t, t.Attempts-1)
		if _, ok, err := c.readState(previousID); err == nil && ok {
			return previousID
		}
	}
	return c.jobID(t, attempt)
}

func (c CodexExecutor) jobID(t *Task, attempt int) string {
	if attempt < 1 {
		attempt = 1
	}
	taskID := "task-unknown"
	if t != nil {
		taskID = sanitizeExecutorID(t.ID)
	}
	return fmt.Sprintf("%s-attempt-%d", taskID, attempt)
}

func (c CodexExecutor) jobDir(jobID string) string {
	return filepath.Join(c.jobsDir, jobID)
}

func (c CodexExecutor) startJob(ctx context.Context, jobID string, t *Task) error {
	startCtx, cancel := context.WithTimeout(ctx, c.startTimeout)
	defer cancel()

	effort := c.defaultEffort
	if t != nil && t.Meta != nil {
		if value := strings.TrimSpace(t.Meta["codex_reasoning_effort"]); value != "" {
			effort = value
		}
	}

	estimate := int64(0)
	if t != nil && t.EstimateBytes > 0 {
		estimate = t.EstimateBytes
	}
	prompt := codexPrompt(t)
	args := []string{
		"start",
		jobID,
		"worker",
		"--effort",
		effort,
		"--estimate-bytes",
		strconv.FormatInt(estimate, 10),
		prompt,
	}
	cmd := exec.CommandContext(startCtx, c.binary, args...) // #nosec G204 -- codex executor launches the configured codexjob wrapper with daemon-built args.
	cmd.Env = append(os.Environ(), "JOBS_DIR="+c.jobsDir)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if _, ok, stateErr := c.readState(jobID); ok && stateErr == nil {
			return nil
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("codexjob start %s: %s", jobID, msg)
	}
	return nil
}

func (c CodexExecutor) waitForJob(ctx context.Context, jobID string) ExecutionResult {
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		state, ok, err := c.readState(jobID)
		if err != nil {
			return ExecutionResult{MissionID: jobID, Err: err}
		}
		if ok {
			switch state.Status {
			case "done", "failed":
				return c.resultFromState(jobID, state, nil)
			}
		}

		select {
		case <-ctx.Done():
			killErr := c.killJob(jobID)
			if killErr != nil && !errors.Is(killErr, os.ErrNotExist) {
				return ExecutionResult{MissionID: jobID, Err: fmt.Errorf("cancel codex job %s: %w", jobID, killErr)}
			}
			return ExecutionResult{MissionID: jobID, Err: ctx.Err()}
		case <-ticker.C:
		}
	}
}

func (c CodexExecutor) killJob(jobID string) error {
	cmd := exec.Command(c.binary, "kill", jobID) // #nosec G204 -- codex executor only invokes its configured job wrapper.
	cmd.Env = append(os.Environ(), "JOBS_DIR="+c.jobsDir)
	return cmd.Run()
}

func (c CodexExecutor) readState(jobID string) (codexJobState, bool, error) {
	path := filepath.Join(c.jobDir(jobID), "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return codexJobState{}, false, nil
		}
		return codexJobState{}, false, err
	}
	var state codexJobState
	if err := json.Unmarshal(data, &state); err != nil {
		return codexJobState{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if state.ID == "" {
		state.ID = jobID
	}
	return state, true, nil
}

func (c CodexExecutor) resultFromState(jobID string, state codexJobState, stateErr error) ExecutionResult {
	jobDir := c.jobDir(jobID)
	proofsPath := filepath.Join(jobDir, "PROOFS.md")
	actualBytes := int64(0)
	if state.ActualBytes != nil {
		actualBytes = *state.ActualBytes
	}
	if actualBytes == 0 {
		actualBytes = proofsActualBytes(proofsPath, 0)
	}
	res := ExecutionResult{
		ActualBytes: actualBytes,
		MissionID:   jobID,
		ProofsPath:  proofsPath,
		Err:         stateErr,
	}
	if state.Status == "failed" {
		errText := c.readJobMessage(jobDir)
		exitCode := 0
		if state.Exit != nil {
			exitCode = *state.Exit
		}
		if errText == "" {
			errText = fmt.Sprintf("codex job failed with exit %d", exitCode)
		}
		res.Err = fmt.Errorf("codex job %s failed: %s", jobID, strings.TrimSpace(errText))
	}
	return res
}

func (c CodexExecutor) readJobMessage(jobDir string) string {
	for _, name := range []string{"last-message.txt", "stderr.log", "stdout.log"} {
		data, err := os.ReadFile(filepath.Join(jobDir, name))
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(data))
		if text != "" {
			return text
		}
	}
	return ""
}

func codexPrompt(t *Task) string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	if t.Title != "" {
		fmt.Fprintf(&b, "Task: %s\n", t.Title)
	}
	if t.Repo != "" {
		fmt.Fprintf(&b, "Repo: %s\nWork inside that repository before changing code or running git commands.\n", t.Repo)
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
