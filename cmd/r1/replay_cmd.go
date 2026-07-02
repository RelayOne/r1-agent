package main

// replay_cmd.go — O4: a read side for the session replay recordings that
// internal/workflow persists once per task attempt under
// r1dir.JoinFor(repo, "replays")/<id>.json.
//
// Before this the recordings were write-only: internal/replay ships the whole
// reader (Load, Player.Seek/SeekToType, Recording.Summary/Errors/EventCounts)
// but grep found zero consumers of .stoke/replays/ anywhere, so the package
// doc's promise of post-mortem debugging never had an entry point. These verbs
// are that entry point:
//
//	r1 replay list  [--repo path] [--json]
//	r1 replay show  <id> [--repo path] [--type error|tool_call|...] [--json]
//	r1 replay errors <id> [--repo path] [--json]
//
// All three resolve recordings under r1dir.JoinFor(repo, "replays") so they
// read exactly what the workflow writer produced (canonical .r1/ preferred,
// legacy .stoke/ fallback, matching the writer's JoinFor resolution). The
// --json output follows the emitEventsJSON NDJSON convention (one object per
// line, nested payload) so `r1 replay show <id> --json | jq -c .` composes.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/RelayOne/r1/internal/r1dir"
	"github.com/RelayOne/r1/internal/replay"
)

const replayUsage = `r1 replay — inspect session replay recordings (post-mortem debugging)

USAGE:
  r1 replay list   [--repo path] [--json]
  r1 replay show   <id> [--repo path] [--type TYPE] [--json]
  r1 replay errors <id> [--repo path] [--json]

VERBS:
  list     List recordings under <repo>/.r1/replays (or .stoke/replays):
           id, outcome, duration, event counts.
  show     Print the event trace of one recording. --type filters to a
           single event type (message|tool_call|tool_result|decision|
           error|phase|metric). --json emits NDJSON.
  errors   Print only the error events of one recording.

<id> may be the full recording id, the on-disk filename, or a unique prefix.`

// runReplayCmd dispatches the `r1 replay` verb family. It returns an exit
// code rather than calling os.Exit so it composes the same way as
// runEventsCmd / runCtlCmd.
//
// Exit codes:
//
//	0 — success (zero recordings/events is still success)
//	1 — runtime failure (dir read, load, decode)
//	2 — usage error (missing verb/id, bad flag)
func runReplayCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, replayUsage)
		return 2
	}
	switch args[0] {
	case "list":
		return runReplayList(args[1:], stdout, stderr)
	case "show":
		return runReplayShow(args[1:], stdout, stderr)
	case "errors":
		return runReplayErrors(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, replayUsage)
		return 0
	default:
		fmt.Fprintf(stderr, "replay: unknown verb %q\n\n%s\n", args[0], replayUsage)
		return 2
	}
}

// runReplayList scans the replays directory and prints a compact table of
// every recording (newest first).
func runReplayList(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("replay list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repo := flags.String("repo", ".", "repository root (default: .)")
	asJSON := flags.Bool("json", false, "emit one JSON object per recording (NDJSON) instead of a table")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	dir := r1dir.JoinFor(*repo, "replays")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(stdout, "no replay recordings found under %s\n", dir)
			return 0
		}
		fmt.Fprintf(stderr, "replay list: read %s: %v\n", dir, err)
		return 1
	}

	type row struct {
		rec  *replay.Recording
		file string
	}
	var rows []row
	var loadErrs int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		rec, lerr := replay.Load(filepath.Join(dir, e.Name()))
		if lerr != nil {
			fmt.Fprintf(stderr, "replay list: skip %s: %v\n", e.Name(), lerr)
			loadErrs++
			continue
		}
		rows = append(rows, row{rec: rec, file: e.Name()})
	}
	if len(rows) == 0 {
		if loadErrs > 0 {
			return 1
		}
		fmt.Fprintf(stdout, "no replay recordings found under %s\n", dir)
		return 0
	}
	// Newest first (by start time).
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].rec.StartTime.After(rows[j].rec.StartTime)
	})

	if *asJSON {
		enc := json.NewEncoder(stdout)
		for _, r := range rows {
			counts := map[string]int{}
			for et, c := range r.rec.EventCounts() {
				counts[string(et)] = c
			}
			obj := map[string]any{
				"id":           r.rec.ID,
				"task_id":      r.rec.TaskID,
				"outcome":      r.rec.Outcome,
				"start_time":   r.rec.StartTime.UTC().Format(time.RFC3339Nano),
				"duration_ms":  r.rec.Duration().Milliseconds(),
				"events":       len(r.rec.Events),
				"errors":       len(r.rec.Errors()),
				"event_counts": counts,
			}
			if err := enc.Encode(obj); err != nil {
				fmt.Fprintf(stderr, "replay list: encode: %v\n", err)
				return 1
			}
		}
		return 0
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tOUTCOME\tDURATION\tEVENTS\tERRORS\tSTARTED")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\n",
			r.rec.ID,
			orDash(r.rec.Outcome),
			r.rec.Duration().Round(time.Millisecond),
			len(r.rec.Events),
			len(r.rec.Errors()),
			r.rec.StartTime.UTC().Format(time.RFC3339),
		)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(stderr, "replay list: tabwriter flush: %v\n", err)
		return 1
	}
	if loadErrs > 0 {
		return 1
	}
	return 0
}

// runReplayShow prints the full (or type-filtered) event trace of one
// recording.
func runReplayShow(args []string, stdout, stderr io.Writer) int {
	id, rest := splitReplayArgs(args)
	flags := flag.NewFlagSet("replay show", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repo := flags.String("repo", ".", "repository root (default: .)")
	typeFilter := flags.String("type", "", "only show events of this type (message|tool_call|tool_result|decision|error|phase|metric)")
	asJSON := flags.Bool("json", false, "emit one JSON object per event (NDJSON) instead of a table")
	if err := flags.Parse(rest); err != nil {
		return 2
	}
	if id == "" {
		fmt.Fprintln(stderr, "replay show: missing <id>\n\n"+replayUsage)
		return 2
	}

	rec, path, err := loadReplayByID(*repo, id)
	if err != nil {
		fmt.Fprintf(stderr, "replay show: %v\n", err)
		return 1
	}

	// Collect the events to show. --type walks via Player.SeekToType so the
	// filter is exercised through the reader API; otherwise take them all.
	player := replay.NewPlayer(rec)
	var events []replay.Event
	if *typeFilter != "" {
		et := replay.EventType(*typeFilter)
		for {
			e := player.SeekToType(et)
			if e == nil {
				break
			}
			events = append(events, *e)
		}
	} else {
		for {
			e := player.Next()
			if e == nil {
				break
			}
			events = append(events, *e)
		}
	}

	if *asJSON {
		return emitReplayEventsJSON(stdout, stderr, events)
	}

	// Human output: the Recording.Summary header, then a per-event table.
	fmt.Fprintf(stdout, "%s\n", path)
	fmt.Fprint(stdout, rec.Summary())
	if len(events) == 0 {
		if *typeFilter != "" {
			fmt.Fprintf(stdout, "\nno %q events\n", *typeFilter)
		} else {
			fmt.Fprintln(stdout, "\nno events")
		}
		return 0
	}
	fmt.Fprintln(stdout)
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEQ\tELAPSED\tTYPE\tDETAIL")
	for _, ev := range events {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n",
			ev.Seq,
			ev.Elapsed.Round(time.Millisecond),
			string(ev.Type),
			replayEventDetail(ev),
		)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(stderr, "replay show: tabwriter flush: %v\n", err)
		return 1
	}
	return 0
}

// runReplayErrors prints only the error events of one recording via
// Recording.Errors.
func runReplayErrors(args []string, stdout, stderr io.Writer) int {
	id, rest := splitReplayArgs(args)
	flags := flag.NewFlagSet("replay errors", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repo := flags.String("repo", ".", "repository root (default: .)")
	asJSON := flags.Bool("json", false, "emit one JSON object per error event (NDJSON) instead of a table")
	if err := flags.Parse(rest); err != nil {
		return 2
	}
	if id == "" {
		fmt.Fprintln(stderr, "replay errors: missing <id>\n\n"+replayUsage)
		return 2
	}

	rec, _, err := loadReplayByID(*repo, id)
	if err != nil {
		fmt.Fprintf(stderr, "replay errors: %v\n", err)
		return 1
	}

	errEvents := rec.Errors()
	if *asJSON {
		return emitReplayEventsJSON(stdout, stderr, errEvents)
	}
	if len(errEvents) == 0 {
		fmt.Fprintf(stdout, "no error events in recording %s\n", rec.ID)
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEQ\tELAPSED\tERROR")
	for _, ev := range errEvents {
		msg, _ := ev.Data["error"].(string)
		fmt.Fprintf(tw, "%d\t%s\t%s\n",
			ev.Seq,
			ev.Elapsed.Round(time.Millisecond),
			orDash(strings.TrimSpace(msg)),
		)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(stderr, "replay errors: tabwriter flush: %v\n", err)
		return 1
	}
	return 0
}

// emitReplayEventsJSON writes one JSON object per replay event (NDJSON),
// mirroring emitEventsJSON: nested payload (Data is already a generic map so
// it encodes as real JSON, not a stringified blob) and RFC3339Nano timestamps.
func emitReplayEventsJSON(stdout, stderr io.Writer, events []replay.Event) int {
	enc := json.NewEncoder(stdout)
	for _, ev := range events {
		obj := map[string]any{
			"seq":        ev.Seq,
			"type":       string(ev.Type),
			"timestamp":  ev.Timestamp.UTC().Format(time.RFC3339Nano),
			"elapsed_ms": ev.Elapsed.Milliseconds(),
		}
		if len(ev.Data) > 0 {
			obj["data"] = ev.Data
		}
		if err := enc.Encode(obj); err != nil {
			fmt.Fprintf(stderr, "replay: encode event %d: %v\n", ev.Seq, err)
			return 1
		}
	}
	return 0
}

// loadReplayByID resolves id to a recording under r1dir.JoinFor(repo,
// "replays"). id may be the exact recording id, the on-disk filename, or a
// unique filename-stem prefix. A prefix that matches more than one recording
// is an error so the operator gets a deterministic result.
func loadReplayByID(repo, id string) (*replay.Recording, string, error) {
	dir := r1dir.JoinFor(repo, "replays")
	base := strings.TrimSuffix(id, ".json")

	// Exact hit wins — no directory scan needed.
	exact := filepath.Join(dir, base+".json")
	if _, err := os.Stat(exact); err == nil {
		rec, lerr := replay.Load(exact)
		if lerr != nil {
			return nil, exact, lerr
		}
		return rec, exact, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", fmt.Errorf("no replays directory at %s", dir)
		}
		return nil, "", fmt.Errorf("read %s: %w", dir, err)
	}
	var matches []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), ".json")
		if strings.HasPrefix(stem, base) {
			matches = append(matches, e.Name())
		}
	}
	switch len(matches) {
	case 0:
		return nil, "", fmt.Errorf("no replay recording for id %q under %s", id, dir)
	case 1:
		p := filepath.Join(dir, matches[0])
		rec, lerr := replay.Load(p)
		if lerr != nil {
			return nil, p, lerr
		}
		return rec, p, nil
	default:
		sort.Strings(matches)
		return nil, "", fmt.Errorf("ambiguous id %q matches %d recordings: %s", id, len(matches), strings.Join(matches, ", "))
	}
}

// splitReplayArgs separates the leading <id> positional from the flag tokens
// so an operator can write the id before or after flags (Go's flag package
// otherwise stops parsing at the first non-flag argument). Only --type and
// --repo take a value in the `--flag value` form; --json is boolean. The
// `--flag=value` form is a single token and needs no special handling.
func splitReplayArgs(args []string) (id string, flags []string) {
	valueFlags := map[string]bool{
		"--type": true, "-type": true,
		"--repo": true, "-repo": true,
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if valueFlags[a] && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		if id == "" {
			id = a
			continue
		}
		// A second bare positional is unexpected; hand it to flag.Parse so it
		// surfaces as a usage error rather than being silently dropped.
		flags = append(flags, a)
	}
	return id, flags
}

// replayEventDetail renders a compact one-line summary of an event's payload
// for the human table. It surfaces the most useful field per event type and
// falls back to a truncated key list.
func replayEventDetail(ev replay.Event) string {
	if len(ev.Data) == 0 {
		return "-"
	}
	switch ev.Type {
	case replay.EventError:
		if s, ok := ev.Data["error"].(string); ok {
			return truncOneLine(s, 100)
		}
	case replay.EventToolCall:
		if s, ok := ev.Data["tool"].(string); ok {
			return truncOneLine(s, 100)
		}
	case replay.EventMessage:
		role, _ := ev.Data["role"].(string)
		content, _ := ev.Data["content"].(string)
		if role != "" || content != "" {
			return truncOneLine(strings.TrimSpace(role+": "+content), 100)
		}
	case replay.EventPhase:
		if s, ok := ev.Data["phase"].(string); ok {
			return truncOneLine(s, 100)
		}
	}
	if s, ok := ev.Data["delta"].(string); ok && s != "" {
		return truncOneLine(s, 100)
	}
	keys := make([]string, 0, len(ev.Data))
	for k := range ev.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return truncOneLine(strings.Join(keys, ","), 100)
}

// truncOneLine collapses newlines/tabs to spaces and truncates to n runes so
// a payload preview never breaks the tabwriter grid.
func truncOneLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
