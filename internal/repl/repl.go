package repl

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	RepoRoot string
	Commands map[string]Command
	OnChat   func(input string) // handler for free-text chat (dispatches to claude -p)
	Reader   *bufio.Scanner     // input source; defaults to os.Stdin
	Writer   *strings.Builder   // output sink; defaults to os.Stdout via fmt
}

// New creates a REPL with the standard slash commands registered.
func New(repoRoot string) *REPL {
	r := &REPL{
		RepoRoot: repoRoot,
		Commands: map[string]Command{},
	}
	return r
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
	order := []string{"ship", "build", "scope", "repair", "scan", "audit", "plan", "run", "yolo", "add-claude", "add-codex", "pools", "remove-pool", "status", "pool", "help", "quit"}
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
		if r.OnChat != nil {
			r.OnChat(line)
		} else {
			r.println("  (chat not configured -- use slash commands)")
		}
	}
}
