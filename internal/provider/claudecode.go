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

	// WorkerMode controls invocation mode.
	//   false (default) → reviewer: `--print` text-only, no tools.
	//     Fast, cheap, can't edit files — correct for judges,
	//     reviewers, plan critics, classifiers.
	//   true → worker: `-p <prompt> --dangerously-skip-permissions
	//     --output-format json --max-turns 100`. CC uses its full
	//     tool suite (Read, Edit, Write, Bash, etc.) and produces
	//     a structured JSON result. Correct for tasks that need
	//     to create/modify files or run builds.
	WorkerMode bool
}

// NewClaudeCodeProvider returns a reviewer-mode provider
// (text-only --print). For worker-mode use NewClaudeCodeWorker.
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

// NewClaudeCodeWorker returns a worker-mode provider that
// invokes CC with tool access + JSON output.
func NewClaudeCodeWorker(binary, workDir, model string) *ClaudeCodeProvider {
	p := NewClaudeCodeProvider(binary, workDir, model)
	p.WorkerMode = true
	return p
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
	// went to stdin), synthesize an instruction from the
	// content so Claude Code knows what to do. A generic
	// "process stdin" causes CC to refuse ("no code provided,
	// won't rubber-stamp"). Instead, tell CC exactly what
	// the stdin contains and what output format is expected.
	if cliPrompt == "" && stdinContent != "" {
		// Detect the task type from the content and tailor
		// the instruction accordingly.
		lower := strings.ToLower(stdinContent[:min(len(stdinContent), 2000)])
		switch {
		case strings.Contains(lower, "review") && strings.Contains(lower, "task"):
			cliPrompt = "You are reviewing code for a specific task. The full review request (task spec, code excerpts, acceptance criteria) is piped via stdin. Read it carefully and output ONLY a JSON verdict matching the schema described in the input. No prose outside the JSON."
		case strings.Contains(lower, "skeleton") || strings.Contains(lower, "statement of work"):
			cliPrompt = "You are converting a project specification into a structured JSON document. The full specification is piped via stdin. Read it and output ONLY the JSON — no prose, no markdown fences. Start with { and end with }."
		case strings.Contains(lower, "decompose") || strings.Contains(lower, "sub-directive"):
			cliPrompt = "You are decomposing a task gap into sub-directives. The full context is piped via stdin. Output ONLY JSON matching the schema in the input."
		case strings.Contains(lower, "briefing") || strings.Contains(lower, "lead-dev"):
			cliPrompt = "You are a lead developer producing per-task briefings. The full session context is piped via stdin. Output ONLY JSON matching the schema in the input."
		case strings.Contains(lower, "judge") || strings.Contains(lower, "verdict"):
			cliPrompt = "You are an expert judge evaluating code or acceptance criteria. The full evaluation context is piped via stdin. Output ONLY JSON matching the schema in the input."
		default:
			cliPrompt = "The full prompt is piped via stdin. Read it completely, then produce ONLY the output format it requests (typically JSON). No commentary outside the requested format."
		}
	}

	// Also: if stdin content has the system prompt embedded
	// (common when system is empty but the user message
	// contains instructions), extract the first sentence
	// as a CLI hint so CC has immediate context.
	if len(cliPrompt) < 50 && len(stdinContent) > 100 {
		firstLine := stdinContent
		if nl := strings.IndexByte(firstLine, '\n'); nl > 0 && nl < 200 {
			firstLine = firstLine[:nl]
		} else if len(firstLine) > 200 {
			firstLine = firstLine[:200]
		}
		cliPrompt = firstLine + "\n\n" + cliPrompt
	}
	var args []string
	if p.WorkerMode {
		// Worker: full tool access + JSON-structured output so
		// we can extract the final text from .result.
		args = []string{
			"-p", cliPrompt,
			"--dangerously-skip-permissions",
			"--output-format", "json",
			"--no-session-persistence",
			"--max-turns", "100",
		}
	} else {
		// Reviewer: text-only, no tools, no permissions.
		args = []string{
			"--print",
			"--no-session-persistence",
			cliPrompt,
		}
	}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}

	cmd := exec.Command(p.Binary, args...) // #nosec G204 -- binary name is hardcoded; args come from Stoke-internal orchestration, not external input.
	if stdinContent != "" {
		cmd.Stdin = strings.NewReader(stdinContent)
	}
	if p.WorkDir != "" {
		cmd.Dir = p.WorkDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// No hard timeout — CLI providers can legitimately take
	// 15+ min on large SOW conversions. Instead, monitor
	// stdout growth: if no output for 5 min, the process
	// is hung (codex is known to hang). Kill and return error.
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	watchdog := time.NewTicker(30 * time.Second)
	defer watchdog.Stop()
	lastSize := 0
	staleChecks := 0
	const maxStale = 10 // 10 × 30s = 5 min of no output

	for {
		select {
		case err := <-done:
			if err != nil {
				return nil, fmt.Errorf("claude-code: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
			}
			goto output
		case <-watchdog.C:
			cur := stdout.Len() + stderr.Len()
			if cur == lastSize {
				staleChecks++
				if staleChecks >= maxStale {
					if cmd.Process != nil {
						cmd.Process.Kill()
					}
					return nil, fmt.Errorf("claude-code: process hung (no output for %ds)", maxStale*30)
				}
			} else {
				staleChecks = 0
				lastSize = cur
			}
		}
	}
output:

	text := strings.TrimSpace(stdout.String())
	if p.WorkerMode {
		// Worker mode: output is a JSON object with .result
		// holding the final assistant text.
		var r struct {
			Result   string  `json:"result"`
			NumTurns int     `json:"num_turns"`
			Cost     float64 `json:"total_cost_usd"`
		}
		if json.Unmarshal([]byte(text), &r) == nil && r.Result != "" {
			text = r.Result
		}
	}
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
