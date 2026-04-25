// Package ember implements the env.Environment interface for Ember burst workers.
//
// Ember workers are Fly machines provisioned via Ember's /v1/workers API with
// credit-based billing. Command execution is performed via SSH to the worker's
// hostname since the underlying Fly machines don't expose an exec API.
//
// Ember also provides managed AI at /v1/ai/chat which can be used as a
// model.Provider — see the EmberAIClient in this package.
package ember

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/RelayOne/r1-agent/internal/env"
	"github.com/RelayOne/r1-agent/internal/logging"
)

// Backend implements env.Environment for Ember burst workers.
type Backend struct {
	apiURL  string
	token   string
	http    *http.Client
	sshKey  string
	sshUser string
}

// Config configures the Ember backend.
type Config struct {
	// APIURL is the Ember API base URL (e.g. "https://ember.dev").
	APIURL string

	// Token is the Ember API key (Bearer token).
	Token string

	// SSHKeyPath is the path to the SSH private key for command execution.
	SSHKeyPath string

	// SSHUser is the SSH user on the worker. Default: "root".
	SSHUser string
}

// New creates an Ember environment backend.
func New(cfg Config) *Backend {
	user := cfg.SSHUser
	if user == "" {
		user = "root"
	}
	return &Backend{
		apiURL:  cfg.APIURL,
		token:   cfg.Token,
		http:    &http.Client{Timeout: 60 * time.Second},
		sshKey:  cfg.SSHKeyPath,
		sshUser: user,
	}
}

// --- Ember API types ---

type createWorkerRequest struct {
	Name            string            `json:"name,omitempty"`
	Size            string            `json:"size,omitempty"`
	TTLMinutes      int               `json:"ttl_minutes,omitempty"`
	TaskID          string            `json:"task_id,omitempty"`
	TaskDescription string            `json:"task_description,omitempty"`
	RepoURL         string            `json:"repo_url,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	IdempotencyKey  string            `json:"idempotency_key,omitempty"`
}

type workerResponse struct {
	ID               string `json:"id"`
	Hostname         string `json:"hostname"`
	Status           string `json:"status"`
	ExpiresAt        string `json:"expires_at"`
	CostCents        int    `json:"cost_cents"`
	CreditsRemaining int    `json:"credits_remaining"`
}

type workerStatus struct {
	ID               string            `json:"id"`
	Status           string            `json:"status"`
	WorkerType       string            `json:"workerType"`
	CostCents        int               `json:"costCents"`
	ExpiresAt        string            `json:"expiresAt"`
	Error            *string           `json:"error"`
	WorkerInstanceID string            `json:"workerInstanceId"`
	StartedAt        string            `json:"startedAt"`
	CompletedAt      *string           `json:"completedAt"`
	Metadata         map[string]string `json:"metadata"`
}

// --- Environment interface ---

func (b *Backend) Provision(ctx context.Context, spec env.Spec) (*env.Handle, error) {
	size := spec.Size
	if size == "" {
		size = "performance-4x"
	}
	ttl := spec.TTLMinutes
	if ttl == 0 {
		ttl = 30
	}

	reqBody := createWorkerRequest{
		Size:       size,
		TTLMinutes: ttl,
		Metadata:   spec.Env,
	}
	if spec.RepoRoot != "" {
		reqBody.RepoURL = spec.RepoRoot
	}

	var resp workerResponse
	if err := b.post(ctx, "/v1/workers", reqBody, &resp); err != nil {
		return nil, fmt.Errorf("ember provision: %w", err)
	}

	// Wait for worker to become active.
	if err := b.waitForActive(ctx, resp.ID); err != nil {
		_ = b.stopWorker(ctx, resp.ID)
		return nil, fmt.Errorf("ember wait: %w", err)
	}

	// Wait for SSH access.
	if err := b.waitForSSH(ctx, resp.Hostname); err != nil {
		_ = b.stopWorker(ctx, resp.ID)
		return nil, fmt.Errorf("ember ssh wait: %w", err)
	}

	workDir := spec.WorkDir
	if workDir == "" {
		workDir = "/workspace"
	}

	// Run setup commands.
	for _, cmd := range spec.SetupCommands {
		result, err := b.sshExec(ctx, resp.Hostname, workDir, cmd, nil, nil, 5*time.Minute)
		if err != nil {
			_ = b.stopWorker(ctx, resp.ID)
			return nil, fmt.Errorf("ember setup %q: %w", cmd, err)
		}
		if !result.Success() {
			_ = b.stopWorker(ctx, resp.ID)
			return nil, fmt.Errorf("ember setup %q failed (exit %d): %s", cmd, result.ExitCode, result.CombinedOutput())
		}
	}

	return &env.Handle{
		ID:      resp.ID,
		Backend: env.BackendEmber,
		WorkDir: workDir,
		Meta: map[string]string{
			"hostname":          resp.Hostname,
			"size":              size,
			"cost_cents":        fmt.Sprintf("%d", resp.CostCents),
			"credits_remaining": fmt.Sprintf("%d", resp.CreditsRemaining),
		},
		CreatedAt: time.Now(),
	}, nil
}

func (b *Backend) Exec(ctx context.Context, h *env.Handle, cmd []string, opts env.ExecOpts) (*env.ExecResult, error) {
	if h == nil {
		return nil, env.ErrNotProvisioned
	}
	hostname := h.Meta["hostname"]
	if hostname == "" {
		return nil, fmt.Errorf("ember exec: no hostname in handle")
	}
	dir := h.WorkDir
	if opts.Dir != "" {
		dir = opts.Dir
	}
	timeout := 10 * time.Minute
	if opts.Timeout > 0 {
		timeout = opts.Timeout
	}
	return b.sshExec(ctx, hostname, dir, strings.Join(cmd, " "), opts.Env, opts.Stdin, timeout)
}

func (b *Backend) CopyIn(ctx context.Context, h *env.Handle, srcLocal, dstRemote string) error {
	if h == nil {
		return env.ErrNotProvisioned
	}
	hostname := h.Meta["hostname"]
	cmd := exec.CommandContext(ctx, "scp", b.scpBaseArgs(srcLocal, fmt.Sprintf("%s@%s:%s", b.sshUser, hostname, dstRemote))...) // #nosec G204 -- sandbox/container binary invoked with Stoke-generated args.
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ember copy-in: %w: %s", err, out)
	}
	return nil
}

func (b *Backend) CopyOut(ctx context.Context, h *env.Handle, srcRemote, dstLocal string) error {
	if h == nil {
		return env.ErrNotProvisioned
	}
	hostname := h.Meta["hostname"]
	cmd := exec.CommandContext(ctx, "scp", b.scpBaseArgs(fmt.Sprintf("%s@%s:%s", b.sshUser, hostname, srcRemote), dstLocal)...) // #nosec G204 -- sandbox/container binary invoked with Stoke-generated args.
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ember copy-out: %w: %s", err, out)
	}
	return nil
}

func (b *Backend) Service(ctx context.Context, h *env.Handle, name string) (env.ServiceAddr, error) {
	if h == nil {
		return env.ServiceAddr{}, env.ErrNotProvisioned
	}
	hostname := h.Meta["hostname"]
	if hostname == "" {
		return env.ServiceAddr{}, env.ErrServiceNotFound
	}
	return env.ServiceAddr{
		Protocol: "https",
		Host:     hostname,
		Port:     443,
	}, nil
}

func (b *Backend) Teardown(ctx context.Context, h *env.Handle) error {
	if h == nil {
		return nil
	}
	return b.stopWorker(ctx, h.ID)
}

func (b *Backend) Cost(ctx context.Context, h *env.Handle) (env.CostEstimate, error) {
	if h == nil {
		return env.CostEstimate{}, env.ErrNotProvisioned
	}
	// Query current status to get real cost.
	status, err := b.getWorkerStatus(ctx, h.ID)
	if err != nil {
		// Fall back to zero cost when the status endpoint is
		// unreachable: Cost() must not fail the whole teardown
		// pipeline just because we can't fetch a live price. Log the
		// error so operators can see transient API failures.
		logging.Global().Warn("ember: worker status unavailable, falling back to zero cost",
			"worker_id", h.ID, "err", err)
		return env.CostEstimate{
			ComputeUSD: 0,
			TotalUSD:   0,
			Elapsed:    time.Since(h.CreatedAt),
		}, nil
	}
	costUSD := float64(status.CostCents) / 100.0
	return env.CostEstimate{
		ComputeUSD: costUSD,
		TotalUSD:   costUSD,
		Elapsed:    time.Since(h.CreatedAt),
	}, nil
}

// --- Ember API helpers ---

func (b *Backend) stopWorker(ctx context.Context, id string) error {
	return b.postEmpty(ctx, fmt.Sprintf("/v1/workers/%s/stop", id))
}

func (b *Backend) getWorkerStatus(ctx context.Context, id string) (*workerStatus, error) {
	var status workerStatus
	if err := b.getJSON(ctx, fmt.Sprintf("/v1/workers/%s/status", id), &status); err != nil {
		return nil, err
	}
	return &status, nil
}

func (b *Backend) waitForActive(ctx context.Context, workerID string) error {
	deadline := time.After(120 * time.Second)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for worker %s to become active", workerID)
		case <-ticker.C:
			status, err := b.getWorkerStatus(ctx, workerID)
			if err != nil {
				continue
			}
			switch status.Status {
			case "active":
				return nil
			case "failed", "expired":
				msg := "unknown error"
				if status.Error != nil {
					msg = *status.Error
				}
				return fmt.Errorf("worker %s: %s", status.Status, msg)
			}
		}
	}
}

// --- SSH helpers (shared pattern with fly backend) ---

func (b *Backend) sshExec(ctx context.Context, host, dir, cmdStr string, envVars map[string]string, stdin []byte, timeout time.Duration) (*env.ExecResult, error) {
	start := time.Now()
	var remote strings.Builder
	if dir != "" {
		fmt.Fprintf(&remote, "cd %s && ", dir)
	}
	for k, v := range envVars {
		fmt.Fprintf(&remote, "%s=%s ", k, v)
	}
	remote.WriteString(cmdStr)

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := b.sshBaseArgs(host, remote.String())
	cmd := exec.CommandContext(execCtx, "ssh", args...) // #nosec G204 -- sandbox/container binary invoked with Stoke-generated args.
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, err
		}
	}

	return &env.ExecResult{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(start),
	}, nil
}

func (b *Backend) sshBaseArgs(host, command string) []string {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=10",
	}
	if b.sshKey != "" {
		args = append(args, "-i", b.sshKey)
	}
	args = append(args, fmt.Sprintf("%s@%s", b.sshUser, host), command)
	return args
}

func (b *Backend) scpBaseArgs(srcdst ...string) []string {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-r",
	}
	if b.sshKey != "" {
		args = append(args, "-i", b.sshKey)
	}
	args = append(args, srcdst...)
	return args
}

func (b *Backend) waitForSSH(ctx context.Context, host string) error {
	deadline := time.After(90 * time.Second)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for SSH on %s", host)
		case <-ticker.C:
			result, err := b.sshExec(ctx, host, "/", "echo ok", nil, nil, 5*time.Second)
			if err == nil && result.Success() {
				return nil
			}
		}
	}
}

// --- HTTP helpers ---

func (b *Backend) doReq(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, b.apiURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return b.http.Do(req)
}

func (b *Backend) getJSON(ctx context.Context, path string, out interface{}) error {
	resp, err := b.doReq(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return parseErr(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (b *Backend) post(ctx context.Context, path string, body, out interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := b.doReq(ctx, http.MethodPost, path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return parseErr(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (b *Backend) postEmpty(ctx context.Context, path string) error {
	resp, err := b.doReq(ctx, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return parseErr(resp)
	}
	return nil
}

func parseErr(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var errResp struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		return fmt.Errorf("ember api %d: %s", resp.StatusCode, errResp.Error)
	}
	return fmt.Errorf("ember api %d: %s", resp.StatusCode, body)
}
