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
	"encoding/json"
	"fmt"
	"os"
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
		Timeout: 20 * time.Minute,
	}
}

func (p *ClaudeCodeProvider) Name() string { return "claude-code" }

// Chat sends the prompt to Claude Code headless and returns
// the response. System prompt + user messages are
// concatenated into a single prompt string for --print mode.
func (p *ClaudeCodeProvider) Chat(req ChatRequest) (*ChatResponse, error) {
	// Split: system prompt (instructions) as the CLI argument,
	// user content (the actual data — SOW prose, task spec,
	// etc.) piped via stdin. Claude Code treats stdin as
	// additional context appended to the prompt argument.
	// This handles 55KB+ prose without hitting arg-length
	// limits and ensures Claude Code sees the instructions
	// as a TASK, not a conversation.
	cliPrompt, stdinContent := splitForClaudeCode(req)
	fmt.Fprintf(os.Stderr, "[claude-code] cli=%d bytes, stdin=%d bytes\n",
		len(cliPrompt), len(stdinContent))
	// When there's no CLI prompt (all content is large and
	// went to stdin), provide a default instruction so Claude
	// Code knows what to do with the piped data.
	if cliPrompt == "" && stdinContent != "" {
		cliPrompt = "Process the input piped via stdin and produce the requested output. Output ONLY the requested format (typically JSON) — no prose, no markdown fences."
	}
	args := []string{"--print", cliPrompt}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}

	cmd := exec.Command(p.Binary, args...)
	if stdinContent != "" {
		cmd.Stdin = strings.NewReader(stdinContent)
	}
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
	// Strip markdown fences — Claude Code often wraps JSON
	// output in ```json ... ``` blocks even when asked not to.
	text = stripMarkdownFences(text)
	return &ChatResponse{
		Content: []ResponseContent{{Type: "text", Text: text}},
	}, nil
}

// stripMarkdownFences removes ```lang\n...\n``` wrappers
// from Claude Code output so downstream JSON parsers see
// clean content.
func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Find the end of the opening fence line.
	nl := strings.IndexByte(s, '\n')
	if nl < 0 {
		return s
	}
	s = s[nl+1:]
	// Strip trailing fence.
	if idx := strings.LastIndex(s, "```"); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
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

// splitForClaudeCode separates the request into a CLI prompt
// argument (system prompt + instructions) and stdin content
// (the bulk data — SOW prose, task spec, code excerpts).
// The CLI argument is capped at ~8KB to avoid arg-length
// limits; everything else goes via stdin.
func splitForClaudeCode(req ChatRequest) (cliPrompt, stdinContent string) {
	var cli, stdin strings.Builder
	if req.System != "" {
		cli.WriteString(req.System)
		cli.WriteString("\n\n")
	}
	for _, msg := range req.Messages {
		if msg.Role == "user" {
			text := extractTextFromContent(msg.Content)
			if text == "" {
				continue
			}
			// If the text is large (>4KB), route it to stdin.
			// Small messages stay in the CLI prompt for context.
			if len(text) > 4096 {
				stdin.WriteString(text)
				stdin.WriteString("\n\n")
			} else {
				cli.WriteString(text)
				cli.WriteString("\n\n")
			}
		}
	}
	return strings.TrimSpace(cli.String()), strings.TrimSpace(stdin.String())
}

// buildClaudeCodePrompt concatenates system + user messages
// into a single string for --print mode. Used by ChatStream.
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
// Uses proper JSON unmarshaling to handle escaped quotes,
// newlines, and other special characters in large content
// (e.g., 55KB SOW prose).
func extractTextFromContent(raw []byte) string {
	s := bytes.TrimSpace(raw)
	if len(s) == 0 {
		return ""
	}
	// Try JSON array of content blocks first.
	if s[0] == '[' {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(s, &blocks); err == nil {
			var texts []string
			for _, b := range blocks {
				if b.Text != "" {
					texts = append(texts, b.Text)
				}
			}
			return strings.Join(texts, "\n")
		}
	}
	// Try JSON string.
	var str string
	if json.Unmarshal(s, &str) == nil {
		return str
	}
	// Fallback: raw text.
	return string(s)
}
