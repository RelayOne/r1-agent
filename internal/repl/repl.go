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

// Run starts the interactive loop.
func (r *REPL) Run() {
	absRepo, _ := filepath.Abs(r.RepoRoot)
	repoName := filepath.Base(absRepo)

	fmt.Println("⚡ STOKE")
	fmt.Printf("  repo: %s\n", absRepo)
	fmt.Println()
	fmt.Println("  Type naturally to chat. Stoke dispatches to Claude Code behind the scenes.")
	fmt.Println("  Slash commands trigger orchestrated workflows:")
	fmt.Println()

	// Print available commands
	order := []string{"ship", "build", "scope", "repair", "scan", "audit", "plan", "run", "yolo", "add-claude", "add-codex", "pools", "remove-pool", "status", "pool", "help", "quit"}
	for _, name := range order {
		if cmd, ok := r.Commands[name]; ok {
			fmt.Printf("    /%-10s %s\n", cmd.Name, cmd.Description)
		}
	}
	// Print any commands not in the ordered list
	for name, cmd := range r.Commands {
		found := false
		for _, o := range order {
			if o == name { found = true; break }
		}
		if !found {
			fmt.Printf("    /%-10s %s\n", cmd.Name, cmd.Description)
		}
	}
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Printf("\033[1;36m%s>\033[0m ", repoName)

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
				fmt.Println("  Bye.")
				return
			}

			if cmdName == "help" || cmdName == "?" {
				fmt.Println()
				for _, name := range order {
					if cmd, ok := r.Commands[name]; ok {
						fmt.Printf("  /%-10s %s\n", cmd.Name, cmd.Description)
						if cmd.Usage != "" {
							fmt.Printf("  %-12s %s\n", "", cmd.Usage)
						}
					}
				}
				fmt.Println("  /quit       Exit Stoke")
				fmt.Println()
				continue
			}

			cmd, ok := r.Commands[cmdName]
			if !ok {
				fmt.Printf("  Unknown command: /%s (try /help)\n", cmdName)
				continue
			}

			cmd.Run(cmdArgs)
			continue
		}

		// Free text -> dispatch to chat handler
		if r.OnChat != nil {
			r.OnChat(line)
		} else {
			fmt.Println("  (chat not configured -- use slash commands)")
		}
	}
}
