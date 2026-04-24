package main

// ops_logs.go — OPSUX-tail: `stoke logs` read-only verb. Tails the
// newest N lines of .stoke/stream.jsonl (the streamjson emitter's
// disk mirror) when present, otherwise falls back to the eventlog at
// .stoke/events.db. The fallback is what makes this verb useful on
// sessions that pre-date the stream.jsonl tee.
//
//	stoke logs [--db PATH] [--stream PATH] [--session SID] [--last N]
//
// --last defaults to 50 (a useful tail without flooding). --last 0
// means "unlimited". When --session is set we always use the
// eventlog branch — stream.jsonl is mission-scoped today and has no
// cheap session filter.

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ericmacdougall/stoke/internal/eventlog"
	"github.com/ericmacdougall/stoke/internal/r1dir"
)

// runLogsCmd implements `stoke logs`. Exit-code convention matches
// runEventsCmd.
func runLogsCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "path to events.db (default: <cwd>/.stoke/events.db)")
	streamPath := fs.String("stream", "", "path to stream.jsonl (default: <cwd>/.stoke/stream.jsonl)")
	session := fs.String("session", "", "filter to events scoped to this session/mission/task/loop id (forces eventlog source)")
	last := fs.Int("last", 50, "tail the last N lines (0 = unlimited)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *last < 0 {
		fmt.Fprintln(stderr, "logs: --last must be >= 0")
		return 2
	}

	resolvedStream := resolveStreamJSONL(*streamPath)
	// Session filter requires the eventlog (stream.jsonl has no cheap
	// session filter). Otherwise prefer the stream.jsonl tee if it
	// exists — it carries the already-rendered JSON the emitter wrote,
	// which is what humans ran `tail -f` against.
	if *session == "" {
		if _, err := os.Stat(resolvedStream); err == nil {
			return tailStreamJSONL(resolvedStream, *last, stdout, stderr)
		}
	}

	resolvedDB := resolveEventsDB(*dbPath)
	if _, err := os.Stat(resolvedDB); err != nil {
		fmt.Fprintf(stderr, "logs: no log sources found (stream=%s db=%s)\n", resolvedStream, resolvedDB)
		return 1
	}
	log, err := eventlog.Open(resolvedDB)
	if err != nil {
		fmt.Fprintf(stderr, "logs: open %s: %v\n", resolvedDB, err)
		return 1
	}
	defer log.Close()

	// Reuse ops_events.go's collector: session, no `since`, no type
	// prefix, last-N ring. Then re-emit via the same NDJSON path as
	// `stoke events --json` so operators get a uniform line format
	// whichever source we ended up reading.
	events, err := collectEvents(context.Background(), log, *session, 0, "", *last)
	if err != nil {
		fmt.Fprintf(stderr, "logs: %v\n", err)
		return 1
	}
	return emitEventsJSON(stdout, stderr, events)
}

// resolveStreamJSONL mirrors resolveEventsDB: explicit wins, otherwise
// default to <cwd>/<resolved-dir>/stream.jsonl. We do NOT walk up to a
// git root — behaviour stays predictable and easy to override.
//
// Dual-resolve (§S1-5): when both `.r1/stream.jsonl` and
// `.stoke/stream.jsonl` exist the canonical path wins; when only one
// exists we return it; when neither exists we return the canonical path
// so "file not found" messages point at the post-rename layout.
func resolveStreamJSONL(explicit string) string {
	if explicit != "" {
		return explicit
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	canonical := filepath.Join(cwd, r1dir.Canonical, "stream.jsonl")
	if _, err := os.Stat(canonical); err == nil {
		return canonical
	}
	legacy := filepath.Join(cwd, r1dir.Legacy, "stream.jsonl")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return canonical
}

// tailStreamJSONL emits the last N lines of a JSONL file to stdout.
// When N == 0 it emits the whole file. The implementation keeps a
// ring buffer of size N to bound memory independent of file size —
// streaming a 1GB file with --last 50 is O(50) memory.
//
// Blank lines are preserved as-is; we do not attempt to re-parse the
// JSON (the stream.jsonl emitter already guarantees one event per
// line). This keeps the verb honest: "logs" means "show the raw log".
func tailStreamJSONL(path string, last int, stdout, stderr io.Writer) int {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(stderr, "logs: open %s: %v\n", path, err)
		return 1
	}
	defer f.Close()

	// bufio.Scanner's default 64k line limit is too small for rich
	// streamjson payloads (tool-use blocks can exceed it). Grow the
	// buffer to 1MiB to match the streamjson emitter's practical
	// worst case.
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	if last == 0 {
		for sc.Scan() {
			if _, err := fmt.Fprintln(stdout, sc.Text()); err != nil {
				fmt.Fprintf(stderr, "logs: write: %v\n", err)
				return 1
			}
		}
		if err := sc.Err(); err != nil {
			fmt.Fprintf(stderr, "logs: read %s: %v\n", path, err)
			return 1
		}
		return 0
	}

	ring := make([]string, 0, last)
	for sc.Scan() {
		line := sc.Text()
		if len(ring) < last {
			ring = append(ring, line)
			continue
		}
		copy(ring, ring[1:])
		ring[len(ring)-1] = line
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(stderr, "logs: read %s: %v\n", path, err)
		return 1
	}
	for _, line := range ring {
		if _, err := fmt.Fprintln(stdout, line); err != nil {
			fmt.Fprintf(stderr, "logs: write: %v\n", err)
			return 1
		}
	}
	return 0
}

