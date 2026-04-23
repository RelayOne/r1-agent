package main

// eventlog_cmd.go — `stoke eventlog` subcommand tree.
//
// Two read-only verbs in this commit:
//
//   stoke eventlog verify        [--db PATH]
//   stoke eventlog list-sessions [--db PATH] [--json]
//
// `verify` walks the hash chain in sequence order and reports the first
// broken row. Exit codes: 0 clean, 1 chain broken, 2 IO / usage.
//
// `list-sessions` prints the distinct session / mission / loop IDs
// observed in the log so operators can pick a target for `stoke events
// --session` or the `stoke sow --resume-from=<session-id>` flow.
//
// Both verbs default --db to <cwd>/.stoke/events.db, matching the
// convention used by `stoke events`. An explicit --db wins; we do NOT
// walk up looking for a git root so behaviour stays predictable.
//
// Spec reference: specs/event-log-proper.md items 22 (list-sessions) +
// 24 (sow --resume-from=<session-id>) + 29 (verify).

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/eventlog"
)

// eventlogCmd dispatches to the `stoke eventlog <verb>` subcommands.
// Invoked from main.go's top-level switch. Prints a usage message and
// exits 2 on unknown or missing verbs.
func eventlogCmd(args []string) {
	os.Exit(runEventlogCmd(args, os.Stdout, os.Stderr))
}

// runEventlogCmd returns an exit code rather than calling os.Exit so
// it can be exercised by tests the same way runEventsCmd is.
func runEventlogCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: stoke eventlog <verb> [flags]")
		fmt.Fprintln(stderr, "verbs: verify, list-sessions")
		return 2
	}
	switch args[0] {
	case "verify":
		return runEventlogVerify(args[1:], stdout, stderr)
	case "list-sessions":
		return runEventlogListSessions(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, "usage: stoke eventlog <verb> [flags]")
		fmt.Fprintln(stdout, "verbs:")
		fmt.Fprintln(stdout, "  verify         walk the hash chain; exit 1 if broken")
		fmt.Fprintln(stdout, "  list-sessions  print distinct session / mission / loop IDs")
		return 0
	default:
		fmt.Fprintf(stderr, "eventlog: unknown verb %q\n", args[0])
		return 2
	}
}

// runEventlogVerify implements `stoke eventlog verify`. Exit codes:
//
//	0 — chain clean.
//	1 — chain broken (prints the broken sequence + expected/got hashes).
//	2 — IO error (db open, schema apply, query) or usage error.
func runEventlogVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("eventlog verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "path to events.db (default: <cwd>/.stoke/events.db)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	resolved := resolveEventsDB(*dbPath)
	if _, err := os.Stat(resolved); err != nil {
		fmt.Fprintf(stderr, "eventlog verify: db not found: %s\n", resolved)
		return 2
	}

	log, err := eventlog.Open(resolved)
	if err != nil {
		fmt.Fprintf(stderr, "eventlog verify: open %s: %v\n", resolved, err)
		return 2
	}
	defer log.Close()

	if err := log.Verify(context.Background()); err != nil {
		var broken *eventlog.ErrChainBroken
		if errors.As(err, &broken) {
			fmt.Fprintf(stderr, "eventlog verify: chain broken at sequence %d\n", broken.Sequence)
			fmt.Fprintf(stderr, "  expected parent_hash: %s\n", broken.Expected)
			fmt.Fprintf(stderr, "  got parent_hash:      %s\n", broken.Got)
			return 1
		}
		fmt.Fprintf(stderr, "eventlog verify: %v\n", err)
		return 2
	}
	fmt.Fprintf(stdout, "eventlog verify: OK (%s)\n", resolved)
	return 0
}

// runEventlogListSessions implements `stoke eventlog list-sessions`.
// Prints a grouped view of distinct session_id / mission_id / loop_id
// columns. JSON mode emits a single object so downstream tooling can
// consume the full listing without string parsing.
func runEventlogListSessions(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("eventlog list-sessions", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "path to events.db (default: <cwd>/.stoke/events.db)")
	asJSON := fs.Bool("json", false, "emit a single JSON object instead of grouped text")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	resolved := resolveEventsDB(*dbPath)
	if _, err := os.Stat(resolved); err != nil {
		fmt.Fprintf(stderr, "eventlog list-sessions: db not found: %s\n", resolved)
		return 2
	}

	log, err := eventlog.Open(resolved)
	if err != nil {
		fmt.Fprintf(stderr, "eventlog list-sessions: open %s: %v\n", resolved, err)
		return 2
	}
	defer log.Close()

	ids, err := log.ListSessions(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "eventlog list-sessions: %v\n", err)
		return 2
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(ids); err != nil {
			fmt.Fprintf(stderr, "eventlog list-sessions: encode: %v\n", err)
			return 2
		}
		return 0
	}

	printEventlogGroup(stdout, "sessions", ids.Sessions)
	printEventlogGroup(stdout, "missions", ids.Missions)
	printEventlogGroup(stdout, "loops", ids.Loops)
	if len(ids.Sessions)+len(ids.Missions)+len(ids.Loops) == 0 {
		fmt.Fprintln(stdout, "(no session, mission, or loop IDs observed)")
	}
	return 0
}

// printEventlogGroup prints a labelled group of IDs. Empty groups are
// skipped so the output stays compact on sparse logs.
func printEventlogGroup(w io.Writer, label string, ids []string) {
	if len(ids) == 0 {
		return
	}
	fmt.Fprintf(w, "%s (%d):\n", label, len(ids))
	for _, id := range ids {
		fmt.Fprintf(w, "  %s\n", id)
	}
}

// Note: --db resolution uses resolveEventsDB from ops_events.go (same
// package). --db wins; else <cwd>/.stoke/events.db; no git-root walk.

// decideEventlogResume opens <repo>/.stoke/events.db, replays every
// event scoped to sessionID, and runs the pure DecideResume classifier
// to decide what a resumed SOW run should do next. Called by sow_native
// when --resume-from=<session-id> is invoked without the "CP-" prefix
// (the checkpoint ID namespace). Returns the resume mode label and the
// anchor task ID (empty for fresh_start / already_done).
func decideEventlogResume(repo, sessionID string) (mode, taskID string, err error) {
	dbPath := filepath.Join(repo, ".stoke", "events.db")
	if _, statErr := os.Stat(dbPath); statErr != nil {
		return "", "", fmt.Errorf("events.db not found at %s", dbPath)
	}
	log, openErr := eventlog.Open(dbPath)
	if openErr != nil {
		return "", "", fmt.Errorf("open %s: %w", dbPath, openErr)
	}
	defer log.Close()
	var events []bus.Event
	for ev, iterErr := range log.ReplaySession(context.Background(), sessionID) {
		if iterErr != nil {
			return "", "", fmt.Errorf("replay %s: %w", sessionID, iterErr)
		}
		events = append(events, ev)
	}
	anchor, rm := eventlog.DecideResume(events)
	return rm.String(), anchor, nil
}
