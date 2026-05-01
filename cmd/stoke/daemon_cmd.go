// daemon_cmd.go — `stoke daemon` subcommand.
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
	"syscall"

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
	fmt.Println(`stoke daemon — R1 long-running queue + WAL + workers

USAGE:
  stoke daemon start [flags]            Start the daemon (default if no subcmd)
  stoke daemon enqueue --title T --prompt P [--estimate-bytes N] [--priority N]
  stoke daemon status                   Show queue counts + worker count
  stoke daemon workers --count N        Resize worker pool
  stoke daemon pause                    Pause workers (queue keeps accepting)
  stoke daemon resume                   Resume workers at prior size
  stoke daemon wal [--n 100]            Tail recent WAL events
  stoke daemon tasks [--state S]        List tasks (queued|running|done|failed)

START FLAGS:
  --addr <host:port>      HTTP listen addr (default 127.0.0.1:9090)
  --token <s>             Bearer token for HTTP (default: empty = no auth)
  --max-parallel <n>      Initial worker count (default 10)
  --state-dir <path>      State dir for queue/wal/proofs (default ~/.stoke)
  --poll-gap <ms>         Worker poll interval ms (default 250)
  --executor <name>       Executor: noop (default; safe smoke run)

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
	pollGap := fs.Int("poll-gap", 250, "")
	executor := fs.String("executor", "noop", "")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	if *stateDir == "" {
		home, _ := os.UserHomeDir()
		*stateDir = filepath.Join(home, ".stoke")
	}

	var exec daemon.Executor
	switch *executor {
	case "noop":
		exec = daemon.NoopExecutor{OutBase: filepath.Join(*stateDir, "proofs")}
	default:
		fmt.Fprintf(os.Stderr, "unknown executor %q (only 'noop' supported in this build)\n", *executor)
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
	fmt.Printf("stoke daemon listening on %s (state=%s, workers=%d, executor=%s)\n",
		*addr, *stateDir, *max, *executor)
	fmt.Println("Ctrl-C to stop.")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Println("\nshutting down...")
	d.Stop()
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
