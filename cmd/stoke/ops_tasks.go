package main

// ops_tasks.go — OPSUX-tail: `stoke tasks` read-only verb. Groups events
// from .stoke/events.db by Scope.TaskID and renders one row per task
// with first/last timestamps, event count, and last-seen event type.
// Mirrors the ops_events.go pattern (resolve db → open → iterate →
// render table | NDJSON).
//
//	stoke tasks [--db PATH] [--session SID] [--json]
//
// "session" follows the same heuristic as `stoke events --session`:
// it matches any event whose session_id, mission_id, task_id, or
// loop_id equals the provided value (see eventlog.Log.ReplaySession).
// Events without a task_id are ignored — we only surface things the
// scheduler actually dispatched as tasks.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/eventlog"
)

// taskSummary collects per-task aggregates produced while iterating the
// eventlog. The fields are intentionally small — we do not retain full
// event payloads here, only the headline stats operators ask for when
// answering "what tasks ran / are running in this mission?".
type taskSummary struct {
	TaskID    string    `json:"task_id"`
	MissionID string    `json:"mission_id,omitempty"`
	LoopID    string    `json:"loop_id,omitempty"`
	Count     int       `json:"event_count"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	LastType  string    `json:"last_type"`
}

// runTasksCmd implements `stoke tasks`. Exit-code convention matches
// runEventsCmd (0 ok, 1 runtime, 2 usage).
func runTasksCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tasks", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "path to events.db (default: <cwd>/.stoke/events.db)")
	session := fs.String("session", "", "filter to events scoped to this session/mission/task/loop id")
	asJSON := fs.Bool("json", false, "emit one JSON object per task (NDJSON) instead of a table")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	resolved := resolveEventsDB(*dbPath)
	if _, err := os.Stat(resolved); err != nil {
		fmt.Fprintf(stderr, "tasks: db not found: %s\n", resolved)
		return 1
	}

	log, err := eventlog.Open(resolved)
	if err != nil {
		fmt.Fprintf(stderr, "tasks: open %s: %v\n", resolved, err)
		return 1
	}
	defer log.Close()

	summaries, err := collectTaskSummaries(context.Background(), log, *session)
	if err != nil {
		fmt.Fprintf(stderr, "tasks: %v\n", err)
		return 1
	}

	if *asJSON {
		return emitTasksJSON(stdout, stderr, summaries)
	}
	return renderTasksTable(stdout, summaries)
}

// collectTaskSummaries walks every relevant event and groups by
// Scope.TaskID. Events without a TaskID are skipped — the verb is
// called "tasks" for a reason. Output is sorted by FirstSeen so the
// oldest task lands at the top (matches operators' "what started this
// run?" mental model).
func collectTaskSummaries(ctx context.Context, log *eventlog.Log, session string) ([]taskSummary, error) {
	var seq func(yield func(bus.Event, error) bool)
	if session != "" {
		seq = log.ReplaySession(ctx, session)
	} else {
		seq = log.ReadFrom(ctx, 0)
	}

	byTask := map[string]*taskSummary{}
	for ev, err := range seq {
		if err != nil {
			return nil, err
		}
		tid := ev.Scope.TaskID
		if tid == "" {
			continue
		}
		s, ok := byTask[tid]
		if !ok {
			s = &taskSummary{
				TaskID:    tid,
				MissionID: ev.Scope.MissionID,
				LoopID:    ev.Scope.LoopID,
				FirstSeen: ev.Timestamp,
			}
			byTask[tid] = s
		}
		// Backfill mission/loop if the first event for the task lacked
		// them but a later one supplies them. (Rare, but keeps the table
		// more useful than dropping the non-empty value we saw later.)
		if s.MissionID == "" && ev.Scope.MissionID != "" {
			s.MissionID = ev.Scope.MissionID
		}
		if s.LoopID == "" && ev.Scope.LoopID != "" {
			s.LoopID = ev.Scope.LoopID
		}
		s.Count++
		if ev.Timestamp.Before(s.FirstSeen) {
			s.FirstSeen = ev.Timestamp
		}
		if !ev.Timestamp.Before(s.LastSeen) {
			s.LastSeen = ev.Timestamp
			s.LastType = string(ev.Type)
		}
	}

	out := make([]taskSummary, 0, len(byTask))
	for _, s := range byTask {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FirstSeen.Equal(out[j].FirstSeen) {
			return out[i].TaskID < out[j].TaskID
		}
		return out[i].FirstSeen.Before(out[j].FirstSeen)
	})
	return out, nil
}

func renderTasksTable(stdout io.Writer, tasks []taskSummary) int {
	if len(tasks) == 0 {
		fmt.Fprintln(stdout, "no tasks")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TASK\tMISSION\tLOOP\tEVENTS\tFIRST\tLAST\tLAST_TYPE")
	for _, s := range tasks {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			s.TaskID,
			orDash(s.MissionID),
			orDash(s.LoopID),
			s.Count,
			s.FirstSeen.UTC().Format(time.RFC3339),
			s.LastSeen.UTC().Format(time.RFC3339),
			orDash(s.LastType),
		)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(stdout, "tasks: tabwriter flush: %v\n", err)
		return 1
	}
	return 0
}

func emitTasksJSON(stdout, stderr io.Writer, tasks []taskSummary) int {
	enc := json.NewEncoder(stdout)
	for _, s := range tasks {
		if err := enc.Encode(s); err != nil {
			fmt.Fprintf(stderr, "tasks: encode: %v\n", err)
			return 1
		}
	}
	return 0
}
