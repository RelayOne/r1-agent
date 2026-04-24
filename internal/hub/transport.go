package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// scriptInput is the JSON payload sent to script hooks via stdin.
type scriptInput struct {
	EventID    string         `json:"event_id"`
	EventType  EventType      `json:"event_type"`
	Timestamp  time.Time      `json:"timestamp"`
	TaskID     string         `json:"task_id,omitempty"`
	WorktreeID string         `json:"worktree_id,omitempty"`
	Phase      string         `json:"phase,omitempty"`
	Tool       *ToolEvent     `json:"tool,omitempty"`
	File       *FileEvent     `json:"file,omitempty"`
	Git        *GitEvent      `json:"git,omitempty"`
	Security   *SecurityEvent `json:"security,omitempty"`
	Cost       *CostEvent     `json:"cost,omitempty"`
	Custom     map[string]any `json:"custom,omitempty"`
}

// scriptOutput is the JSON response expected from script hooks via stdout.
type scriptOutput struct {
	Decision   string         `json:"decision,omitempty"`
	Reason     string         `json:"reason,omitempty"`
	Mutations  map[string]any `json:"mutations,omitempty"`
	Injections []Injection    `json:"injections,omitempty"`
	Suppress   bool           `json:"suppress,omitempty"`
}

// invokeScript runs a script hook as a subprocess, piping the event as JSON
// to stdin and parsing the JSON response from stdout.
func (b *Bus) invokeScript(ctx context.Context, sub *Subscriber, ev *Event) *HookResponse {
	cfg := sub.Script
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if cfg.Command == "" {
		b.log.Error("script hook: empty command", "subscriber", sub.ID)
		b.recordFailure(sub.ID)
		return &HookResponse{Decision: Abstain, Reason: "empty command"}
	}

	// Run via shell to support pipes, quoting, etc.
	cmd := exec.CommandContext(ctx, "sh", "-c", cfg.Command) // #nosec G204 -- binary name is hardcoded; args come from Stoke-internal orchestration, not external input.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Kill entire process group on timeout
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	// Pipe event as JSON to stdin
	if cfg.InputJSON {
		input := scriptInput{
			EventID:    ev.ID,
			EventType:  ev.Type,
			Timestamp:  ev.Timestamp,
			TaskID:     ev.TaskID,
			WorktreeID: ev.WorktreeID,
			Phase:      ev.Phase,
			Tool:       ev.Tool,
			File:       ev.File,
			Git:        ev.Git,
			Security:   ev.Security,
			Cost:       ev.Cost,
			Custom:     ev.Custom,
		}
		data, err := json.Marshal(input)
		if err != nil {
			b.log.Error("script hook: marshal input", "subscriber", sub.ID, "error", err)
			b.recordFailure(sub.ID)
			return &HookResponse{Decision: Abstain, Reason: "marshal error"}
		}
		cmd.Stdin = bytes.NewReader(data)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			b.log.Warn("script hook: timeout", "subscriber", sub.ID, "timeout", timeout)
			b.recordFailure(sub.ID)
			return &HookResponse{Decision: Abstain, Reason: "timeout"}
		}
		b.log.Error("script hook: exec failed", "subscriber", sub.ID, "error", err, "stderr", stderr.String())
		b.recordFailure(sub.ID)
		// Non-zero exit in gate mode = deny
		if sub.Mode == ModeGate {
			return &HookResponse{Decision: Deny, Reason: fmt.Sprintf("script failed: %s", stderr.String())}
		}
		return &HookResponse{Decision: Abstain, Reason: err.Error()}
	}

	// Parse JSON output if configured
	if cfg.OutputJSON && stdout.Len() > 0 {
		var out scriptOutput
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			b.log.Warn("script hook: invalid JSON output", "subscriber", sub.ID, "error", err)
			b.recordSuccess(sub.ID)
			return &HookResponse{Decision: Allow}
		}
		b.recordSuccess(sub.ID)
		decision := Allow
		switch strings.ToLower(out.Decision) {
		case "deny":
			decision = Deny
		case "abstain":
			decision = Abstain
		}
		return &HookResponse{
			Decision:   decision,
			Reason:     out.Reason,
			Mutations:  out.Mutations,
			Injections: out.Injections,
			Suppress:   out.Suppress,
		}
	}

	b.recordSuccess(sub.ID)
	return &HookResponse{Decision: Allow}
}

// invokeWebhook sends the event as an HTTP POST to the configured URL
// and parses the JSON response.
func (b *Bus) invokeWebhook(ctx context.Context, sub *Subscriber, ev *Event) *HookResponse {
	cfg := sub.Webhook
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payload, err := json.Marshal(ev)
	if err != nil {
		b.log.Error("webhook hook: marshal", "subscriber", sub.ID, "error", err)
		b.recordFailure(sub.ID)
		return &HookResponse{Decision: Abstain}
	}

	var lastErr error
	maxAttempts := cfg.Retries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt) * time.Second):
			case <-ctx.Done():
				b.recordFailure(sub.ID)
				return &HookResponse{Decision: Abstain, Reason: "timeout"}
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(payload))
		if err != nil {
			b.log.Error("webhook hook: create request", "subscriber", sub.ID, "error", err)
			b.recordFailure(sub.ID)
			return &HookResponse{Decision: Abstain}
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range cfg.Headers {
			req.Header.Set(k, v)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}

		var out scriptOutput
		decodeErr := json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()

		if decodeErr != nil {
			// Non-JSON response: treat 2xx as allow, 4xx as deny
			b.recordSuccess(sub.ID)
			if resp.StatusCode >= 400 {
				return &HookResponse{Decision: Deny, Reason: fmt.Sprintf("HTTP %d", resp.StatusCode)}
			}
			return &HookResponse{Decision: Allow}
		}

		b.recordSuccess(sub.ID)
		decision := Allow
		switch strings.ToLower(out.Decision) {
		case "deny":
			decision = Deny
		case "abstain":
			decision = Abstain
		}
		return &HookResponse{
			Decision:   decision,
			Reason:     out.Reason,
			Mutations:  out.Mutations,
			Injections: out.Injections,
			Suppress:   out.Suppress,
		}
	}

	b.log.Error("webhook hook: all attempts failed", "subscriber", sub.ID, "error", lastErr)
	b.recordFailure(sub.ID)
	return &HookResponse{Decision: Abstain, Reason: "webhook unreachable"}
}
