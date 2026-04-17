// Package provider — claudecode.go
//
// Provider implementation that delegates to Claude Code
// headless (`claude --print -p <prompt>`) instead of
// making HTTP API calls. Every call that would normally go
// to litellm/MiniMax/OpenRouter goes through the local
// Claude Code binary, which has access to the filesystem
// and its own tool-use loop.
//
// Benefits over API calls for planning:
//   - Claude Code can READ the repo during planning (grep,
//     read_file, ls) — the API-based planner only sees
//     what's in the prompt
//   - Claude Code manages its own retries + context
//   - No rate limiting from external providers
//   - Model selection follows Claude Code's own config
//
// Trade-offs:
//   - Slower per-call (spawns a process)
//   - Not parallelizable (single Claude Code instance)
//   - Output format must be text (no streaming SSE)
//
// Use via: --runner native --native-base-url claude-code://
// or --builder-source claude-code
package provider

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/stream"
)

// ClaudeCodeProvider shells out to `claude --print -p` for
// each Chat call. Implements Provider.
type ClaudeCodeProvider struct {
	// Binary is the path to the claude CLI. Default: "claude".
	Binary string

	// WorkDir is the working directory for claude calls.
	// Set to the repo root so Claude Code can use tools.
	WorkDir string

	// Model override for Claude Code. Empty = use claude's
	// default from its config.
	Model string

	// Timeout per call. Default 10 min.
	Timeout time.Duration
}

// NewClaudeCodeProvider returns a provider backed by the
// local Claude Code CLI.
func NewClaudeCodeProvider(binary, workDir, model string) *ClaudeCodeProvider {
	if binary == "" {
		binary = "claude"
	}
	return &ClaudeCodeProvider{
		Binary:  binary,
		WorkDir: workDir,
		Model:   model,
		Timeout: 10 * time.Minute,
	}
}

func (p *ClaudeCodeProvider) Name() string { return "claude-code" }

// Chat sends the prompt to Claude Code headless and returns
// the response. System prompt + user messages are
// concatenated into a single prompt string for --print mode.
func (p *ClaudeCodeProvider) Chat(req ChatRequest) (*ChatResponse, error) {
	prompt := buildClaudeCodePrompt(req)

	args := []string{"--print", "-p", prompt}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}

	cmd := exec.Command(p.Binary, args...)
	if p.WorkDir != "" {
		cmd.Dir = p.WorkDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	timeout := p.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	select {
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf("claude-code: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
		}
	case <-time.After(timeout):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return nil, fmt.Errorf("claude-code: timed out after %s", timeout)
	}

	text := strings.TrimSpace(stdout.String())
	return &ChatResponse{
		Content: []ResponseContent{{Type: "text", Text: text}},
	}, nil
}

// ChatStream is the same as Chat for Claude Code — there's
// no streaming from the CLI. We emit a single text event
// with the full response.
func (p *ClaudeCodeProvider) ChatStream(req ChatRequest, onEvent func(stream.Event)) (*ChatResponse, error) {
	resp, err := p.Chat(req)
	if err != nil {
		return nil, err
	}
	if onEvent != nil && resp != nil && len(resp.Content) > 0 {
		onEvent(stream.Event{
			DeltaText: resp.Content[0].Text,
		})
	}
	return resp, nil
}

// buildClaudeCodePrompt concatenates system + user messages
// into a single string for --print mode.
func buildClaudeCodePrompt(req ChatRequest) string {
	var b strings.Builder
	if req.System != "" {
		b.WriteString(req.System)
		b.WriteString("\n\n")
	}
	for _, msg := range req.Messages {
		if msg.Role == "user" {
			// Content can be a JSON array of content blocks
			// or a plain string. Extract text from both.
			text := extractTextFromContent(msg.Content)
			if text != "" {
				b.WriteString(text)
				b.WriteString("\n\n")
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// extractTextFromContent pulls text from either a plain
// string or a JSON-encoded array of content blocks.
func extractTextFromContent(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) == 0 {
		return ""
	}
	// If it starts with [, it's a JSON array of content blocks.
	if s[0] == '[' {
		// Quick extraction: find "text":"..." values.
		var texts []string
		for {
			idx := strings.Index(s, `"text":"`)
			if idx < 0 {
				idx = strings.Index(s, `"text": "`)
				if idx < 0 {
					break
				}
				idx += 9
			} else {
				idx += 8
			}
			s = s[idx:]
			end := strings.Index(s, `"`)
			if end < 0 {
				// Find the end allowing escaped quotes
				end = findUnescapedQuote(s)
			}
			if end > 0 {
				texts = append(texts, s[:end])
				s = s[end:]
			} else {
				break
			}
		}
		return strings.Join(texts, "\n")
	}
	// Plain string (possibly JSON-quoted).
	if s[0] == '"' {
		s = strings.Trim(s, `"`)
	}
	return s
}

func findUnescapedQuote(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '"' && (i == 0 || s[i-1] != '\\') {
			return i
		}
	}
	return -1
}
