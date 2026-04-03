package compute

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EmberBackend spawns Flare microVMs via the Ember API.
type EmberBackend struct {
	endpoint    string
	apiKey      string
	defaultSize string
	httpClient  *http.Client
}

func NewEmberBackend(endpoint, apiKey string) *EmberBackend {
	return &EmberBackend{
		endpoint:    strings.TrimRight(endpoint, "/"),
		apiKey:      apiKey,
		defaultSize: "4x",
		httpClient:  &http.Client{Timeout: 60 * time.Second},
	}
}

func (b *EmberBackend) Name() string { return "flare" }

func (b *EmberBackend) Spawn(ctx context.Context, opts SpawnOpts) (Worker, error) {
	if opts.Size == "" {
		opts.Size = b.defaultSize
	}

	body, err := json.Marshal(map[string]any{
		"name":     fmt.Sprintf("stoke-%s", opts.TaskID),
		"size":     opts.Size,
		"repo_url": opts.RepoURL,
		"branch":   opts.Branch,
		"env":      opts.Env,
		"metadata": map[string]any{
			"stoke":             true,
			"parent_machine_id": opts.ParentID,
			"task_id":           opts.TaskID,
			"auto_destroy":      opts.AutoDestroy,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal spawn request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", b.endpoint+"/v1/workers", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("spawn worker: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("spawn worker: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		ID       string `json:"id"`
		Hostname string `json:"hostname"`
		State    string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parse spawn response: %w", err)
	}

	w := &flareWorker{
		id:       result.ID,
		hostname: result.Hostname,
		endpoint: b.endpoint,
		apiKey:   b.apiKey,
		client:   b.httpClient,
	}

	// Poll until running (max 60s), respecting context cancellation
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context cancelled while waiting for worker: %w", ctx.Err())
		default:
		}
		state, err := w.getState(ctx)
		if err != nil {
			return nil, err
		}
		if state == "running" {
			return w, nil
		}
		if state == "error" || state == "destroyed" {
			return nil, fmt.Errorf("worker failed to start: state=%s", state)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context cancelled while waiting for worker: %w", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("worker did not start within 60s")
}

type flareWorker struct {
	id       string
	hostname string
	endpoint string
	apiKey   string
	client   *http.Client
}

func (w *flareWorker) ID() string        { return w.id }
func (w *flareWorker) Hostname() string  { return w.hostname }
func (w *flareWorker) Stdout() io.Reader { return strings.NewReader("") }

func (w *flareWorker) doReq(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, w.endpoint+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+w.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return w.client.Do(req)
}

func (w *flareWorker) getState(ctx context.Context) (string, error) {
	resp, err := w.doReq(ctx, "GET", "/v1/workers/"+w.id+"/status", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("get state: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var result struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode worker state: %w", err)
	}
	return result.State, nil
}

func (w *flareWorker) Exec(ctx context.Context, cmd string, args ...string) (ExecResult, error) {
	resp, err := w.doReq(ctx, "POST", "/v1/workers/"+w.id+"/exec", map[string]any{
		"command": cmd, "args": args,
	})
	if err != nil {
		return ExecResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return ExecResult{}, fmt.Errorf("exec: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var result struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		Duration int64  `json:"duration_ms"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ExecResult{}, fmt.Errorf("decode exec result: %w", err)
	}
	return ExecResult{
		ExitCode: result.ExitCode, Stdout: result.Stdout,
		Stderr: result.Stderr, Duration: result.Duration,
	}, nil
}

func (w *flareWorker) Upload(ctx context.Context, localPath, remotePath string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filepath.Base(localPath))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return fmt.Errorf("copy file to upload: %w", err)
	}
	if err := mw.WriteField("path", remotePath); err != nil {
		return fmt.Errorf("write multipart field: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", w.endpoint+"/v1/workers/"+w.id+"/upload", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+w.apiKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (w *flareWorker) Download(ctx context.Context, remotePath, localPath string) error {
	resp, err := w.doReq(ctx, "GET", fmt.Sprintf("/v1/workers/%s/download?path=%s", w.id, url.QueryEscape(remotePath)), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	out, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

func (w *flareWorker) Stop(ctx context.Context) error {
	resp, err := w.doReq(ctx, "POST", "/v1/workers/"+w.id+"/stop", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (w *flareWorker) Destroy(ctx context.Context) error {
	resp, err := w.doReq(ctx, "DELETE", "/v1/workers/"+w.id, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
