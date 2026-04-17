// Package provider — codex.go
//
// Provider that delegates to OpenAI Codex CLI headless
// (`codex exec -p <prompt> --print`). Same pattern as
// ClaudeCodeProvider but for Codex.
package provider

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/stream"
)

type CodexProvider struct {
	Binary  string
	WorkDir string
	Model   string
	Timeout time.Duration
}

func NewCodexProvider(binary, workDir, model string) *CodexProvider {
	if binary == "" {
		binary = "codex"
	}
	return &CodexProvider{
		Binary:  binary,
		WorkDir: workDir,
		Model:   model,
		Timeout: 20 * time.Minute,
	}
}

func (p *CodexProvider) Name() string { return "codex" }

func (p *CodexProvider) Chat(req ChatRequest) (*ChatResponse, error) {
	cliPrompt, stdinContent := splitCodexPrompt(req)
	if cliPrompt == "" && stdinContent != "" {
		cliPrompt = "Process the input piped via stdin and produce the requested output. Output ONLY the requested format — no prose wrapper."
	}

	// codex exec doesn't have --print. Use -o to write
	// the last message to a temp file, then read it back.
	tmpOut := fmt.Sprintf("/tmp/codex-out-%d.txt", time.Now().UnixNano())
	args := []string{"exec",
		"--dangerously-bypass-approvals-and-sandbox",
		"-o", tmpOut,
		cliPrompt}
	if p.Model != "" {
		args = append(args, "-m", p.Model)
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

	// No hard timeout — codex can take 15+ min on big prompts.
	// Monitor stdout growth: if no output for 5 min, the
	// process is hung (codex is known to hang). Kill + error.
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
				return nil, fmt.Errorf("codex: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
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
					return nil, fmt.Errorf("codex: process hung (no output for %ds)", maxStale*30)
				}
			} else {
				staleChecks = 0
				lastSize = cur
			}
		}
	}
output:

	// Read output from temp file (codex writes last message there)
	outData, readErr := os.ReadFile(tmpOut)
	os.Remove(tmpOut)
	if readErr != nil {
		// Fallback to stdout if file wasn't created
		outData = stdout.Bytes()
	}
	text := strings.TrimSpace(string(outData))
	text = stripMarkdownFences(text)
	return &ChatResponse{
		Content: []ResponseContent{{Type: "text", Text: text}},
	}, nil
}

func (p *CodexProvider) ChatStream(req ChatRequest, onEvent func(stream.Event)) (*ChatResponse, error) {
	resp, err := p.Chat(req)
	if err != nil {
		return nil, err
	}
	if onEvent != nil && resp != nil && len(resp.Content) > 0 {
		onEvent(stream.Event{DeltaText: resp.Content[0].Text})
	}
	return resp, nil
}

func splitCodexPrompt(req ChatRequest) (string, string) {
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
