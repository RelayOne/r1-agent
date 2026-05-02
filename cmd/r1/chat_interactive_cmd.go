package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/RelayOne/r1/internal/app"
	"github.com/RelayOne/r1/internal/conversation"
	"github.com/RelayOne/r1/internal/interview"
	"github.com/RelayOne/r1/internal/r1dir"
	"github.com/RelayOne/r1/internal/taskstate"
	"github.com/RelayOne/r1/internal/workflow"
)

var builtinCommands = map[string]func([]string) error{
	"chat-interactive": runChatInteractiveCmd,
}

func runBuiltinCommand(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	run, ok := builtinCommands[args[0]]
	if !ok {
		return false, nil
	}
	return true, run(args[1:])
}

type chatInteractiveConfig struct {
	RepoRoot        string
	AuthMode        app.AuthMode
	RunnerMode      string
	ClaudeBinary    string
	CodexBinary     string
	ClaudeConfigDir string
	CodexHome       string
	NativeAPIKey    string
	NativeModel     string
	NativeBaseURL   string
	// CortexEnabled toggles parallel-cognition Lobes (cortex-core spec 1).
	// Default off; spec 2 (cortex-concerns) owns the actual Cortex
	// construction + wire-up that consumes this flag.
	CortexEnabled bool
}

type chatInteractiveSession struct {
	cfg       chatInteractiveConfig
	in        *bufio.Scanner
	out       io.Writer
	storePath string
	conv      *conversation.Runtime
	planFn    func(context.Context, string) (workflow.Result, error)
	execFn    func(context.Context, string) (workflow.Result, error)
}

var (
	chatTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
	chatPromptStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
	chatPlanStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	chatOKStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("35"))
	chatWarnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	chatErrStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	chatDimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
)

func runChatInteractiveCmd(args []string) error {
	fs := flag.NewFlagSet("chat-interactive", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	repo := fs.String("repo", ".", "Git repository root")
	authMode := fs.String("mode", "mode1", "Auth mode: mode1 or mode2")
	runnerMode := fs.String("runner", "claude", "Runner backend: claude, codex, native, hybrid")
	claudeBin := fs.String("claude-bin", "claude", "Claude Code binary")
	codexBin := fs.String("codex-bin", "codex", "Codex CLI binary")
	claudeConfigDir := fs.String("claude-config-dir", "", "CLAUDE_CONFIG_DIR")
	codexHome := fs.String("codex-home", "", "CODEX_HOME")
	nativeAPIKey := fs.String("native-api-key", "", "Anthropic API key for native runner")
	nativeModel := fs.String("native-model", "claude-sonnet-4-6", "Model for native runner")
	nativeBaseURL := fs.String("native-base-url", "", "Base URL for native runner")
	cortexEnabled := fs.Bool("cortex", false, "Enable parallel-cognition Lobes (cortex-core spec 1; off by default)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		return fmt.Errorf("resolve repo: %w", err)
	}

	storePath := r1dir.JoinFor(absRepo, "conversation", "chat-interactive.json")
	conv := conversation.NewRuntime("R1 chat-interactive session", 200000)
	if err := loadConversation(storePath, conv); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load conversation: %w", err)
	}

	cfg := chatInteractiveConfig{
		RepoRoot:        absRepo,
		AuthMode:        app.AuthMode(*authMode),
		RunnerMode:      *runnerMode,
		ClaudeBinary:    *claudeBin,
		CodexBinary:     *codexBin,
		ClaudeConfigDir: *claudeConfigDir,
		CodexHome:       *codexHome,
		NativeAPIKey:    *nativeAPIKey,
		NativeModel:     *nativeModel,
		NativeBaseURL:   *nativeBaseURL,
		CortexEnabled:   *cortexEnabled,
	}

	session := &chatInteractiveSession{
		cfg:       cfg,
		in:        bufio.NewScanner(os.Stdin),
		out:       os.Stdout,
		storePath: storePath,
		conv:      conv,
	}
	session.planFn = func(ctx context.Context, task string) (workflow.Result, error) {
		return session.runWorkflow(ctx, task, true)
	}
	session.execFn = func(ctx context.Context, task string) (workflow.Result, error) {
		return session.runWorkflow(ctx, task, false)
	}
	return session.run(context.Background())
}

func (s *chatInteractiveSession) run(ctx context.Context) error {
	fmt.Fprintln(s.out, chatTitleStyle.Render("⚡ R1 chat-interactive"))
	fmt.Fprintln(s.out, chatDimStyle.Render("Type a task, then approve the generated plan. /quit exits."))
	if turns := s.conv.TurnCount(); turns > 0 {
		fmt.Fprintf(s.out, "%s\n", chatDimStyle.Render(fmt.Sprintf("Loaded %d prior conversation messages.", turns)))
	}
	fmt.Fprintln(s.out)

	for {
		task, err := s.prompt("task> ")
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		task = strings.TrimSpace(task)
		if task == "" {
			continue
		}
		if isQuitCommand(task) {
			fmt.Fprintln(s.out, chatDimStyle.Render("Bye."))
			return nil
		}

		currentTask := task
		for {
			if err := s.record(conversation.TextMessage(conversation.RoleUser, currentTask)); err != nil {
				return err
			}

			planResult, err := s.planFn(ctx, currentTask)
			if err != nil {
				fmt.Fprintf(s.out, "%s\n\n", chatErrStyle.Render("plan failed: "+err.Error()))
				if recErr := s.record(conversation.TextMessage(conversation.RoleAssistant, "Plan failed: "+err.Error())); recErr != nil {
					return recErr
				}
				break
			}

			planText := renderPlanResult(planResult)
			fmt.Fprintf(s.out, "%s\n%s\n\n", chatPlanStyle.Render("Plan"), planText)
			if err := s.record(conversation.TextMessage(conversation.RoleAssistant, planText)); err != nil {
				return err
			}

			decision, err := s.prompt("execute? [y/n/edit] ")
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			decision = strings.ToLower(strings.TrimSpace(decision))
			if err := s.record(conversation.TextMessage(conversation.RoleUser, "decision: "+decision)); err != nil {
				return err
			}

			switch decision {
			case "y", "yes":
				fmt.Fprintln(s.out, chatWarnStyle.Render("Executing..."))
				result, execErr := s.execFn(ctx, currentTask)
				if execErr != nil {
					msg := "execute failed: " + execErr.Error()
					fmt.Fprintf(s.out, "%s\n\n", chatErrStyle.Render(msg))
					if err := s.record(conversation.TextMessage(conversation.RoleAssistant, msg)); err != nil {
						return err
					}
					break
				}
				summary := strings.TrimSpace(result.Render())
				fmt.Fprintf(s.out, "%s\n%s\n\n", chatOKStyle.Render("Execution complete"), summary)
				if err := s.record(conversation.TextMessage(conversation.RoleAssistant, summary)); err != nil {
					return err
				}
				goto nextTask
			case "n", "no":
				fmt.Fprintln(s.out, chatDimStyle.Render("Plan cleared."))
				fmt.Fprintln(s.out)
				if err := s.record(conversation.TextMessage(conversation.RoleAssistant, "Plan cleared by operator.")); err != nil {
					return err
				}
				goto nextTask
			case "edit":
				refined, promptErr := s.prompt("refined task (or /clarify): ")
				if promptErr != nil {
					if errors.Is(promptErr, io.EOF) {
						return nil
					}
					return promptErr
				}
				refined = strings.TrimSpace(refined)
				if strings.HasPrefix(refined, "/clarify") {
					clarified, clarifyErr := s.runClarification(currentTask, refined)
					if clarifyErr != nil {
						return clarifyErr
					}
					currentTask = clarified
					fmt.Fprintln(s.out)
					continue
				}
				if refined != "" {
					currentTask = refined
				}
				fmt.Fprintln(s.out)
				continue
			default:
				fmt.Fprintf(s.out, "%s\n\n", chatWarnStyle.Render("Answer with y, n, or edit."))
				continue
			}

			break
		}

	nextTask:
	}
}

func (s *chatInteractiveSession) runClarification(currentTask, command string) (string, error) {
	task := currentTask
	if rest := strings.TrimSpace(strings.TrimPrefix(command, "/clarify")); rest != "" {
		task = rest
	}

	session := interview.NewSession(task)
	fmt.Fprintf(s.out, "%s\n", chatPlanStyle.Render("Clarification"))
	fmt.Fprintln(s.out, chatDimStyle.Render("Answer each question. Enter uses the suggested default."))
	for !session.IsComplete() {
		question := session.NextQuestion()
		if question == nil {
			break
		}
		fmt.Fprintf(s.out, "%s %s\n", chatDimStyle.Render("["+string(question.Phase)+"]"), question.Question)
		if question.Default != "" {
			fmt.Fprintf(s.out, "%s\n", chatDimStyle.Render("default: "+question.Default))
		}
		answer, err := s.prompt("clarify> ")
		if err != nil {
			return "", err
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "skip", "s":
			session.Skip()
		case "done", "d":
			for !session.IsComplete() {
				session.Skip()
			}
		default:
			session.Answer(strings.TrimSpace(answer))
		}
	}

	scope := session.Synthesize()
	clarified := strings.TrimSpace(scope.ToPrompt())
	fmt.Fprintf(s.out, "%s\n%s\n", chatPlanStyle.Render("Clarified scope"), clarified)
	if err := s.record(conversation.TextMessage(conversation.RoleAssistant, clarified)); err != nil {
		return "", err
	}
	return clarified, nil
}

func (s *chatInteractiveSession) runWorkflow(ctx context.Context, task string, planOnly bool) (workflow.Result, error) {
	runID := "chat-interactive-execute"
	if planOnly {
		runID = "chat-interactive-plan"
	}

	orchestrator, err := app.New(app.RunConfig{
		RepoRoot:        s.cfg.RepoRoot,
		Task:            task,
		PlanOnly:        planOnly,
		AuthMode:        s.cfg.AuthMode,
		RunnerMode:      s.cfg.RunnerMode,
		ClaudeBinary:    s.cfg.ClaudeBinary,
		CodexBinary:     s.cfg.CodexBinary,
		ClaudeConfigDir: s.cfg.ClaudeConfigDir,
		CodexHome:       s.cfg.CodexHome,
		NativeAPIKey:    s.cfg.NativeAPIKey,
		NativeModel:     s.cfg.NativeModel,
		NativeBaseURL:   s.cfg.NativeBaseURL,
		State:           taskstate.NewTaskState(runID),
	})
	if err != nil {
		return workflow.Result{}, err
	}

	runCtx := ctx
	cancel := func() {}
	if planOnly {
		runCtx, cancel = context.WithTimeout(ctx, 10*time.Minute)
	}
	defer cancel()

	return orchestrator.Run(runCtx)
}

func (s *chatInteractiveSession) prompt(label string) (string, error) {
	fmt.Fprint(s.out, chatPromptStyle.Render(label))
	if !s.in.Scan() {
		if err := s.in.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return s.in.Text(), nil
}

func (s *chatInteractiveSession) record(msg conversation.Message) error {
	s.conv.AddMessage(msg)
	if s.conv.EstimatedTokens() > 160000 && s.conv.TurnCount() > 12 {
		s.conv.Compact(12)
	}
	return saveConversation(s.storePath, s.conv)
}

func renderPlanResult(result workflow.Result) string {
	text := strings.TrimSpace(result.PlanOutput)
	if text != "" {
		return text
	}
	return strings.TrimSpace(result.Render())
}

func isQuitCommand(input string) bool {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "/quit", "/exit", "quit", "exit":
		return true
	default:
		return false
	}
}

func loadConversation(path string, runtime *conversation.Runtime) error {
	repoRoot := safeRepoRoot(path)
	relPath, err := filepath.Rel(repoRoot, path)
	if err != nil || strings.HasPrefix(relPath, "..") {
		return runtime.LoadFrom(path)
	}
	data, err := r1dir.ReadFileFor(repoRoot, relPath)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "r1-chat-interactive-load-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if closeErr := tmp.Close(); closeErr != nil {
		_ = os.Remove(tmpPath)
		return closeErr
	}
	defer os.Remove(tmpPath)
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return err
	}
	return runtime.LoadFrom(tmpPath)
}

func saveConversation(path string, runtime *conversation.Runtime) error {
	tmp, err := os.CreateTemp("", "r1-chat-interactive-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if closeErr := tmp.Close(); closeErr != nil {
		_ = os.Remove(tmpPath)
		return closeErr
	}
	defer os.Remove(tmpPath)

	if err := runtime.SaveTo(tmpPath); err != nil {
		return err
	}
	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return err
	}
	relPath, err := filepath.Rel(safeRepoRoot(path), path)
	if err != nil || strings.HasPrefix(relPath, "..") {
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
			return mkErr
		}
		return os.WriteFile(path, data, 0o644)
	}
	return r1dir.WriteFileFor(safeRepoRoot(path), relPath, data, 0o644)
}

func safeRepoRoot(path string) string {
	dir := filepath.Dir(path)
	for dir != filepath.Dir(dir) {
		base := filepath.Base(dir)
		if base == r1dir.Canonical || base == r1dir.Legacy {
			return filepath.Dir(dir)
		}
		dir = filepath.Dir(dir)
	}
	return filepath.Dir(path)
}
