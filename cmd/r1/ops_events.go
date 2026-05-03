package main

// ops_events.go — OPSUX-events: thin read-only operator surface for
// the eventlog. Lets operators see what an in-flight or finished
// session actually did without firing up a SQL prompt.
//
//	r1 events [--db PATH] [--session SID] [--last N]
//	             [--since SEQ] [--type PREFIX] [--json]
//
// (spec-deviation: spec 21 is massive; this commit ships ONE high-value
// verb; remaining verbs queued for follow-up)
//
// Substantial overlap with CDC-13/14's ctl_cmd.go (status/approve/...).
// Those verbs talk to a *running* session over a Unix socket. The
// `events` verb is complementary: it reads the durable, hash-chained
// `.stoke/events.db` and works for any session past or present (and for
// sessions that were never sock-attached at all). It also unblocks the
// follow-up `tasks` / `cost` / `logs` verbs, which can all be derived
// by filtering the same eventlog.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/eventlog"
)

// runEventsCmd implements `r1 events`. Returns an exit code rather
// than calling os.Exit so it composes the same way as runCtlCmd.
//
// Exit codes:
//
//	0 — success (zero events is still success)
//	1 — runtime failure (db open, query)
//	2 — usage error (bad flag, --last < 0)
func runEventsCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("events", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "path to events.db (default: <repo>/.stoke/events.db)")
	session := fs.String("session", "", "filter to events scoped to this session/mission/task/loop id")
	last := fs.Int("last", 20, "show only the last N events (use 0 for unlimited)")
	since := fs.Uint64("since", 0, "start from sequence number (>=)")
	typePrefix := fs.String("type", "", "filter to events whose type starts with this prefix")
	asJSON := fs.Bool("json", false, "emit one JSON object per event (NDJSON) instead of a table")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *last < 0 {
		fmt.Fprintln(stderr, "events: --last must be >= 0")
		return 2
	}

	resolved := resolveEventsDB(*dbPath)
	if _, err := os.Stat(resolved); err != nil {
		fmt.Fprintf(stderr, "events: db not found: %s\n", resolved)
		return 1
	}

	log, err := eventlog.Open(resolved)
	if err != nil {
		fmt.Fprintf(stderr, "events: open %s: %v\n", resolved, err)
		return 1
	}
	defer log.Close()

	ctx := context.Background()
	collected, err := collectEvents(ctx, log, *session, *since, *typePrefix, *last)
	if err != nil {
		fmt.Fprintf(stderr, "events: %v\n", err)
		return 1
	}

	if *asJSON {
		return emitEventsJSON(stdout, stderr, collected)
	}
	return renderEventsTable(stdout, stderr, collected)
}

// resolveEventsDB returns the absolute path to the events.db a verb
// invocation should read. Explicit --db wins; otherwise default to
// <cwd>/.stoke/events.db. We do NOT walk up looking for a git root —
// keep behaviour predictable and easy to override.
func resolveEventsDB(explicit string) string {
	if explicit != "" {
		return explicit
	}
	// LINT-ALLOW chdir-cli-entry: r1 ops events subcommand; cwd is the events.db discovery anchor when --explicit is unset.
	cwd, err := os.Getwd()
	if err != nil {
		// Fall through to a relative path; Stat will surface the failure.
		cwd = "."
	}
	return filepath.Join(cwd, ".stoke", "events.db")
}

// collectEvents pulls events from the log applying the requested
// filters. The contract is:
//
//   - session != "" → use Log.ReplaySession (hits the indexed columns).
//   - session == "" → use Log.ReadFrom(since).
//   - typePrefix != "" → drop events whose Type does not have the prefix.
//   - last > 0 → keep only the trailing N events (ring-buffer style).
//
// Returned slice is in ascending sequence order, ready for rendering.
func collectEvents(ctx context.Context, log *eventlog.Log, session string, since uint64, typePrefix string, last int) ([]bus.Event, error) {
	var seq func(yield func(bus.Event, error) bool)
	if session != "" {
		seq = log.ReplaySession(ctx, session)
	} else {
		seq = log.ReadFrom(ctx, since)
	}

	// Ring-buffer the trailing N events when --last > 0; otherwise
	// accumulate everything. We avoid materialising the whole table when
	// the operator only wants the tail.
	var ring []bus.Event
	if last > 0 {
		ring = make([]bus.Event, 0, last)
	}
	for ev, err := range seq {
		if err != nil {
			return nil, err
		}
		if typePrefix != "" && !strings.HasPrefix(string(ev.Type), typePrefix) {
			continue
		}
		// session != "" already filtered upstream; --since only applies
		// in the ReadFrom branch (ReplaySession ignores it).
		if session == "" && ev.Sequence < since {
			continue
		}
		if last == 0 {
			ring = append(ring, ev)
			continue
		}
		if len(ring) < last {
			ring = append(ring, ev)
			continue
		}
		// Drop oldest. cap == last so this is constant memory.
		copy(ring, ring[1:])
		ring[len(ring)-1] = ev
	}
	return ring, nil
}

// renderEventsTable prints a tabwriter table:
//
//	SEQ  TIMESTAMP             TYPE                MISSION  TASK   LOOP
//	14   2026-04-21T12:00:01Z  task.dispatch       M1       T1     -
//
// Empty scope columns render as "-" so the table stays aligned even on
// rows that lack a particular scope tag.
func renderEventsTable(stdout, stderr io.Writer, events []bus.Event) int {
	if len(events) == 0 {
		fmt.Fprintln(stdout, "no events")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEQ\tTIMESTAMP\tTYPE\tMISSION\tTASK\tLOOP")
	for _, ev := range events {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n",
			ev.Sequence,
			ev.Timestamp.UTC().Format(time.RFC3339),
			string(ev.Type),
			orDash(ev.Scope.MissionID),
			orDash(ev.Scope.TaskID),
			orDash(ev.Scope.LoopID),
		)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(stderr, "events: tabwriter flush: %v\n", err)
		return 1
	}
	return 0
}

// emitEventsJSON writes one JSON object per event (NDJSON), with the
// payload re-parsed so the output is a real nested object rather than a
// stringified blob. NDJSON (vs a JSON array) keeps `r1 events |
// jq -c .` ergonomic and lets pipelines stream.
func emitEventsJSON(stdout, stderr io.Writer, events []bus.Event) int {
	enc := json.NewEncoder(stdout)
	for _, ev := range events {
		obj := map[string]any{
			"id":         ev.ID,
			"sequence":   ev.Sequence,
			"type":       string(ev.Type),
			"timestamp":  ev.Timestamp.UTC().Format(time.RFC3339Nano),
			"emitter_id": ev.EmitterID,
		}
		// Scope is flat (mission/task/loop/...); only include non-empty
		// fields so callers don't see noise.
		scope := map[string]any{}
		if ev.Scope.MissionID != "" {
			scope["mission_id"] = ev.Scope.MissionID
		}
		if ev.Scope.TaskID != "" {
			scope["task_id"] = ev.Scope.TaskID
		}
		if ev.Scope.LoopID != "" {
			scope["loop_id"] = ev.Scope.LoopID
		}
		if ev.Scope.BranchID != "" {
			scope["branch_id"] = ev.Scope.BranchID
		}
		if ev.Scope.StanceID != "" {
			scope["stance_id"] = ev.Scope.StanceID
		}
		if len(scope) > 0 {
			obj["scope"] = scope
		}
		if ev.CausalRef != "" {
			obj["causal_ref"] = ev.CausalRef
		}
		// Re-parse payload into a generic value so the JSON encoder
		// emits it as nested JSON, not an escaped string. On parse
		// failure (shouldn't happen — eventlog stores canonical JSON)
		// fall back to the raw bytes so we never silently drop data.
		if len(ev.Payload) > 0 {
			var parsed any
			if err := json.Unmarshal(ev.Payload, &parsed); err == nil {
				obj["payload"] = parsed
			} else {
				obj["payload_raw"] = string(ev.Payload)
			}
		}
		if err := enc.Encode(obj); err != nil {
			fmt.Fprintf(stderr, "events: encode: %v\n", err)
			return 1
		}
	}
	return 0
}
