package repl

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ericmacdougall/stoke/internal/conversation"
	"github.com/ericmacdougall/stoke/internal/interview"
	"github.com/ericmacdougall/stoke/internal/skill"
	"github.com/ericmacdougall/stoke/internal/viewport"
)

// Command is a slash command handler.
type Command struct {
	Name        string
	Description string
	Usage       string
	Run         func(args string) // args = everything after the slash command
}

// REPL is the Stoke interactive shell.
type REPL struct {
	RepoRoot     string
	Commands     map[string]Command
	OnChat       func(input string) // handler for free-text chat (dispatches to claude -p)
	OnInterview  func(scope *interview.ClarifiedScope) // handler for dispatching clarified scope
	Reader       *bufio.Scanner     // input source; defaults to os.Stdin
	Writer       *strings.Builder   // output sink; defaults to os.Stdout via fmt
	Conversation *conversation.Runtime // multi-turn conversation state (nil = no tracking)
}

// New creates a REPL with the standard slash commands registered.
func New(repoRoot string) *REPL {
	r := &REPL{
		RepoRoot: repoRoot,
		Commands: map[string]Command{},
	}
	return r
}

// RegisterBuiltins adds built-in slash commands including viewport-based file viewing.
func (r *REPL) RegisterBuiltins() {
	r.Commands["view"] = Command{
		Name:        "view",
		Description: "View a file with constrained viewport (100 lines at a time)",
		Usage:       "/view <file-path>",
		Run: func(args string) {
			file := strings.TrimSpace(args)
			if file == "" {
				r.println("  Usage: /view <file-path>")
				return
			}
			path := file
			if !filepath.IsAbs(path) {
				path = filepath.Join(r.RepoRoot, file)
			}
			vp, err := viewport.Open(path, viewport.DefaultConfig())
			if err != nil {
				r.printf("  Error: %v\n", err)
				return
			}
			r.printf("  %s\n", vp.Context())
			r.println(vp.View())
		},
	}

	r.Commands["skills"] = Command{
		Name:        "skills",
		Description: "List available skills (built-in + project + user)",
		Usage:       "/skills [search-text]",
		Run: func(args string) {
			reg := skill.DefaultRegistry(r.RepoRoot)
			_ = reg.Load()

			if query := strings.TrimSpace(args); query != "" {
				matches := reg.Match(query)
				if len(matches) == 0 {
					r.printf("  No skills matching %q\n", query)
					return
				}
				for _, s := range matches {
					r.printf("  %-30s %s [%s]\n", s.Name, s.Description, s.Source)
				}
				return
			}

			list := reg.List()
			if len(list) == 0 {
				r.println("  No skills loaded")
				return
			}
			r.printf("  %d skills available:\n\n", len(list))
			for _, s := range list {
				r.printf("  %-30s %s [%s]\n", s.Name, s.Description, s.Source)
			}
		},
	}

	r.Commands["interview"] = Command{
		Name:        "interview",
		Description: "Deep interview: clarify scope before executing a task",
		Usage:       "/interview <task description>",
		Run: func(args string) {
			task := strings.TrimSpace(args)
			if task == "" {
				r.println("  Usage: /interview <task description>")
				return
			}

			session := interview.NewSession(task)
			r.println("")
			r.printf("  Deep Interview: %s\n", task)
			r.println("  Answer each question (or press Enter to use the default).")
			r.println("  Type 'skip' to skip a question, 'done' to finish early.")
			r.println("")

			scanner := r.Reader
			if scanner == nil {
				scanner = bufio.NewScanner(os.Stdin)
			}
			for !session.IsComplete() {
				q := session.NextQuestion()
				if q == nil {
					break
				}
				r.printf("  [%s] %s\n", q.Phase, q.Question)
				if q.Default != "" {
					r.printf("  (default: %s)\n", q.Default)
				}
				r.printf("  > ")

				if !scanner.Scan() {
					break
				}
				answer := strings.TrimSpace(scanner.Text())
				switch strings.ToLower(answer) {
				case "skip", "s":
					session.Skip()
					r.println("  (skipped)")
				case "done", "d":
					// Skip remaining questions
					for !session.IsComplete() {
						session.Skip()
					}
				case "":
					session.Skip() // use default
					r.println("  (using default)")
				default:
					session.Answer(answer)
				}
				r.println("")
			}

			scope := session.Synthesize()
			r.println("  === Clarified Scope ===")
			r.println(scope.ToPrompt())
			r.printf("  Confidence: %.0f%%\n\n", scope.Confidence*100)

			if r.OnInterview != nil {
				r.OnInterview(scope)
			} else {
				r.println("  (interview handler not configured — scope printed above)")
			}
		},
	}
}

// Register adds a slash command.
func (r *REPL) Register(cmd Command) {
	r.Commands[cmd.Name] = cmd
}

// printf writes to the REPL's output (Writer if set, else stdout).
func (r *REPL) printf(format string, args ...interface{}) {
	if r.Writer != nil {
		fmt.Fprintf(r.Writer, format, args...)
	} else {
		fmt.Printf(format, args...)
	}
}

// println writes a line to the REPL's output.
func (r *REPL) println(s string) {
	r.printf("%s\n", s)
}

// Run starts the interactive loop.
func (r *REPL) Run() {
	absRepo, _ := filepath.Abs(r.RepoRoot)
	repoName := filepath.Base(absRepo)

	r.println("⚡ STOKE")
	r.printf("  repo: %s\n", absRepo)
	r.println("")
	r.println("  Type naturally to chat. Stoke dispatches to Claude Code behind the scenes.")
	r.println("  Slash commands trigger orchestrated workflows:")
	r.println("")

	// Print available commands
	order := []string{"ship", "build", "scope", "repair", "scan", "audit", "plan", "run", "interview", "skills", "yolo", "findings", "add-claude", "add-codex", "pools", "remove-pool", "status", "pool", "help", "quit"}
	for _, name := range order {
		if cmd, ok := r.Commands[name]; ok {
			r.printf("    /%-10s %s\n", cmd.Name, cmd.Description)
		}
	}
	// Print any commands not in the ordered list
	for name, cmd := range r.Commands {
		found := false
		for _, o := range order {
			if o == name { found = true; break }
		}
		if !found {
			r.printf("    /%-10s %s\n", cmd.Name, cmd.Description)
		}
	}
	r.println("")

	scanner := r.Reader
	if scanner == nil {
		scanner = bufio.NewScanner(os.Stdin)
	}
	for {
		r.printf("\033[1;36m%s>\033[0m ", repoName)

		if !scanner.Scan() {
			break
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Slash command
		if strings.HasPrefix(line, "/") {
			parts := strings.SplitN(line[1:], " ", 2)
			cmdName := strings.ToLower(parts[0])
			cmdArgs := ""
			if len(parts) > 1 {
				cmdArgs = strings.TrimSpace(parts[1])
			}

			if cmdName == "quit" || cmdName == "exit" || cmdName == "q" {
				r.println("  Bye.")
				return
			}

			if cmdName == "help" || cmdName == "?" {
				r.println("")
				for _, name := range order {
					if cmd, ok := r.Commands[name]; ok {
						r.printf("  /%-10s %s\n", cmd.Name, cmd.Description)
						if cmd.Usage != "" {
							r.printf("  %-12s %s\n", "", cmd.Usage)
						}
					}
				}
				r.println("  /quit       Exit Stoke")
				r.println("")
				continue
			}

			cmd, ok := r.Commands[cmdName]
			if !ok {
				r.printf("  Unknown command: /%s (try /help)\n", cmdName)
				continue
			}

			cmd.Run(cmdArgs)
			continue
		}

		// Free text -> dispatch to chat handler
		if r.Conversation != nil {
			r.Conversation.AddMessage(conversation.TextMessage(conversation.RoleUser, line))
		}
		if r.OnChat != nil {
			r.OnChat(line)
		} else {
			r.println("  (chat not configured -- use slash commands)")
		}
	}
}
