// daemon_cmd.go — `r1 daemon` subcommand.
//
// Wraps internal/daemon.Daemon: a long-running R1 process that holds a
// persistent task queue + WAL + worker pool + HTTP control plane. The
// operator (or another agent, or R1 itself) can enqueue work, inspect
// state, resize the pool, install hooks, and pause/resume — all without
// restarting the process.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/RelayOne/r1/internal/daemon"
)

func daemonCmd(args []string) {
	if len(args) == 0 {
		daemonStartCmd(nil)
		return
	}
	switch args[0] {
	case "start", "":
		daemonStartCmd(args[1:])
	case "enqueue":
		daemonEnqueueCmd(args[1:])
	case "status":
		daemonStatusCmd(args[1:])
	case "workers":
		daemonWorkersCmd(args[1:])
	case "pause":
		daemonClientCmd("pause", args[1:])
	case "resume":
		daemonClientCmd("resume", args[1:])
	case "wal":
		daemonWALCmd(args[1:])
	case "tasks":
		daemonTasksCmd(args[1:])
	case "help", "-h", "--help":
		daemonUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown daemon subcommand: %s\n\n", args[0])
		daemonUsage()
		os.Exit(2)
	}
}

func daemonUsage() {
	fmt.Print(`r1 daemon — R1 long-running queue + WAL + workers

USAGE:
  r1 daemon start [flags]            Start the daemon (default if no subcmd)
  r1 daemon enqueue --title T --prompt P [--estimate-bytes N] [--priority N]
  r1 daemon status                   Show queue counts + worker count
  r1 daemon workers --count N        Resize worker pool
  r1 daemon pause                    Pause workers (queue keeps accepting)
  r1 daemon resume                   Resume workers at prior size
  r1 daemon wal [--n 100]            Tail recent WAL events
  r1 daemon tasks [--state S]        List tasks (queued|running|done|failed)

START FLAGS:
  --addr <host:port>      HTTP listen addr (default 127.0.0.1:9090)
  --token <s>             Bearer token for HTTP (default: empty = no auth)
  --max-parallel <n>      Initial worker count (default 10)
  --state-dir <path>      State dir for queue/wal/proofs (default ~/.stoke)
  --codex-jobs-dir <path> Codex job artifacts dir (default ~/repos/plans/codex-jobs)
  --poll-gap <ms>         Worker poll interval ms (default 250)
  --executor <name>       Executor: noop|codex|claude-code|bash (default noop)

CLIENT FLAGS (apply to enqueue/status/workers/pause/resume/wal/tasks):
  --addr <host:port>      Daemon URL host:port (default 127.0.0.1:9090)
  --token <s>             Bearer token (default empty)
`)
}

// daemonStartCmd starts the daemon in the foreground. Ctrl-C triggers
// graceful shutdown.
func daemonStartCmd(args []string) {
	fs := flag.NewFlagSet("daemon start", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:9090", "")
	token := fs.String("token", "", "")
	max := fs.Int("max-parallel", 10, "")
	stateDir := fs.String("state-dir", "", "")
	codexJobsDir := fs.String("codex-jobs-dir", "", "")
	pollGap := fs.Int("poll-gap", 250, "")
	executor := fs.String("executor", "noop", "")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	if *stateDir == "" {
		home, _ := os.UserHomeDir()
		*stateDir = filepath.Join(home, ".stoke")
	}

	exec, err := loadDaemonExecutor(*executor, *stateDir, *codexJobsDir, time.Duration(*pollGap)*time.Millisecond)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	d, err := daemon.New(daemon.Config{
		StateDir:    *stateDir,
		Addr:        *addr,
		Token:       *token,
		MaxParallel: *max,
		PollGap:     *pollGap,
	}, exec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon new: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := d.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "daemon start: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("r1 daemon listening on %s (state=%s, workers=%d, executor=%s)\n",
		*addr, *stateDir, *max, *executor)
	fmt.Println("Ctrl-C to stop.")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Println("\nshutting down...")
	d.Stop()
}

func loadDaemonExecutor(name, stateDir, codexJobsDir string, pollGap time.Duration) (daemon.Executor, error) {
	switch strings.TrimSpace(name) {
	case "", "noop":
		return daemon.NoopExecutor{OutBase: filepath.Join(stateDir, "proofs")}, nil
	case "codex":
		return daemon.NewCodexExecutor(daemon.CodexExecutorConfig{
			Binary:         os.Getenv("STOKE_CODEXJOB_BIN"),
			JobsDir:        firstNonEmpty(codexJobsDir, os.Getenv("STOKE_CODEXJOB_JOBS_DIR"), defaultCodexJobsDir()),
			DefaultEffort:  os.Getenv("STOKE_CODEXJOB_EFFORT"),
			PollInterval:   maxDuration(pollGap, 100*time.Millisecond),
			StartTimeout:   15 * time.Second,
			DefaultTimeout: time.Hour,
		}), nil
	case "claude-code":
		return daemon.NewClaudeCodeExecutor(daemon.ClaudeCodeExecutorConfig{
			Binary:         firstNonEmpty(os.Getenv("STOKE_CLAUDE_BIN"), "claude"),
			OutBase:        filepath.Join(stateDir, "proofs", "claude-code"),
			DefaultTimeout: 20 * time.Minute,
		}), nil
	case "bash":
		return daemon.NewBashExecutor(daemon.BashExecutorConfig{
			Shell:          firstNonEmpty(os.Getenv("STOKE_BASH_SHELL"), "/bin/bash"),
			OutBase:        filepath.Join(stateDir, "proofs", "bash"),
			DefaultTimeout: 10 * time.Minute,
		}), nil
	default:
		return nil, fmt.Errorf("unknown executor %q (supported: noop, codex, claude-code, bash)", name)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func defaultCodexJobsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join("~", "repos", "plans", "codex-jobs")
	}
	return filepath.Join(home, "repos", "plans", "codex-jobs")
}

// ---- client subcommands (use HTTP to talk to a running daemon) ----

func daemonEnqueueCmd(args []string) {
	fs := flag.NewFlagSet("daemon enqueue", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:9090", "")
	token := fs.String("token", "", "")
	id := fs.String("id", "", "task id (default auto)")
	title := fs.String("title", "", "task title (required)")
	prompt := fs.String("prompt", "", "task prompt (required)")
	repo := fs.String("repo", "", "")
	runner := fs.String("runner", "hybrid", "")
	estimate := fs.Int64("estimate-bytes", 0, "")
	priority := fs.Int("priority", 0, "")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *title == "" || *prompt == "" {
		fmt.Fprintln(os.Stderr, "--title and --prompt required")
		os.Exit(2)
	}
	body := map[string]any{
		"id": *id, "title": *title, "prompt": *prompt,
		"repo": *repo, "runner": *runner,
		"estimate_bytes": *estimate, "priority": *priority,
	}
	out, err := daemonHTTP("POST", *addr, *token, "/enqueue", body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(out)
}

func daemonStatusCmd(args []string) {
	fs := flag.NewFlagSet("daemon status", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:9090", "")
	token := fs.String("token", "", "")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	out, err := daemonHTTP("GET", *addr, *token, "/status", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(out)
}

func daemonWorkersCmd(args []string) {
	fs := flag.NewFlagSet("daemon workers", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:9090", "")
	token := fs.String("token", "", "")
	count := fs.Int("count", -1, "new worker count (required)")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *count < 0 {
		fmt.Fprintln(os.Stderr, "--count required (>=0)")
		os.Exit(2)
	}
	out, err := daemonHTTP("POST", *addr, *token, "/workers", map[string]any{"count": *count})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(out)
}

func daemonClientCmd(action string, args []string) {
	fs := flag.NewFlagSet("daemon "+action, flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:9090", "")
	token := fs.String("token", "", "")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	out, err := daemonHTTP("POST", *addr, *token, "/"+action, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(out)
}

func daemonWALCmd(args []string) {
	fs := flag.NewFlagSet("daemon wal", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:9090", "")
	token := fs.String("token", "", "")
	n := fs.Int("n", 50, "")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	out, err := daemonHTTP("GET", *addr, *token, fmt.Sprintf("/wal?n=%d", *n), nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(out)
}

func daemonTasksCmd(args []string) {
	fs := flag.NewFlagSet("daemon tasks", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:9090", "")
	token := fs.String("token", "", "")
	state := fs.String("state", "", "")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	path := "/tasks"
	if *state != "" {
		path = "/tasks?state=" + *state
	}
	out, err := daemonHTTP("GET", *addr, *token, path, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(out)
}
