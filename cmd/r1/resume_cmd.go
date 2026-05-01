package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/bus"
)

// resumeCmd implements Task 18 — read the event log for a given
// session and reconstruct the last-known in-progress state. MVP
// scope is reporting: the operator sees what the session was doing
// when it stopped, which task was mid-dispatch, and how much
// budget had been spent. Mid-session restart (actually resuming
// execution) requires wiring into the SOW runner — tracked as a
// follow-up and documented in the --help output.
//
// Usage:
//   r1 resume --session <id> [--repo PATH] [--json]
//   r1 resume --list [--repo PATH]
func resumeCmd(args []string) {
	fs := flag.NewFlagSet("resume", flag.ExitOnError)
	sessionID := fs.String("session", "", "session ID to resume from the event log")
	repo := fs.String("repo", ".", "repository root (looks for .stoke/bus)")
	listAll := fs.Bool("list", false, "list known sessions instead of resuming a specific one")
	asJSON := fs.Bool("json", false, "emit JSON reconstruction instead of pretty text")
	fs.Parse(args)

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fatal("resolve repo: %v", err)
	}
	walDir := filepath.Join(absRepo, ".stoke", "bus")

	if _, err := os.Stat(walDir); err != nil {
		fmt.Fprintf(os.Stderr, "no event log at %s (nothing to resume)\n", walDir)
		os.Exit(1)
	}

	w, err := bus.OpenWAL(walDir)
	if err != nil {
		fatal("open WAL: %v", err)
	}
	events, err := w.ReadFrom(0)
	if err != nil {
		fatal("read events: %v", err)
	}

	if *listAll {
		listSessions(events)
		return
	}
	if *sessionID == "" {
		fmt.Fprintln(os.Stderr, "usage: r1 resume --session <id> [--repo PATH] [--json]")
		fmt.Fprintln(os.Stderr, "   or: r1 resume --list [--repo PATH]")
		os.Exit(1)
	}
	printReconstruction(*sessionID, events, *asJSON)
}

// sessionState is the reconstructed view produced from the event
// log. Kept flat so JSON serialization is trivial for downstream
// tooling.
type sessionState struct {
	SessionID      string    `json:"session_id"`
	FirstSeenAt    time.Time `json:"first_seen_at"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	EventCount     int       `json:"event_count"`
	EventsByType   []typeCount `json:"events_by_type"`
	LastEventType  string    `json:"last_event_type"`
	LastEventID    string    `json:"last_event_id"`
	LastSequence   uint64    `json:"last_sequence"`
	MissionIDs     []string  `json:"mission_ids,omitempty"`
	TaskIDs        []string  `json:"task_ids,omitempty"`
	ResumeHint     string    `json:"resume_hint"`
}

type typeCount struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// reconstructSession walks the events and produces a session-scoped
// summary. Scope.LoopID matches the session for SOW runs; we match
// on that field first and fall back to MissionID for looser
// matching.
func reconstructSession(sessionID string, events []bus.Event) sessionState {
	st := sessionState{SessionID: sessionID}
	typeCounts := map[string]int{}
	missionSet := map[string]struct{}{}
	taskSet := map[string]struct{}{}
	matched := make([]bus.Event, 0, len(events))

	for _, e := range events {
		if !eventMatchesSession(e, sessionID) {
			continue
		}
		matched = append(matched, e)
	}
	st.EventCount = len(matched)
	if len(matched) == 0 {
		st.ResumeHint = "no events found for this session ID"
		return st
	}
	st.FirstSeenAt = matched[0].Timestamp
	st.LastSeenAt = matched[len(matched)-1].Timestamp
	last := matched[len(matched)-1]
	st.LastEventID = last.ID
	st.LastEventType = string(last.Type)
	st.LastSequence = last.Sequence
	for _, e := range matched {
		typeCounts[string(e.Type)]++
		if e.Scope.MissionID != "" {
			missionSet[e.Scope.MissionID] = struct{}{}
		}
		if e.Scope.TaskID != "" {
			taskSet[e.Scope.TaskID] = struct{}{}
		}
	}
	for t, c := range typeCounts {
		st.EventsByType = append(st.EventsByType, typeCount{Type: t, Count: c})
	}
	sort.Slice(st.EventsByType, func(i, j int) bool {
		return st.EventsByType[i].Count > st.EventsByType[j].Count
	})
	for m := range missionSet {
		st.MissionIDs = append(st.MissionIDs, m)
	}
	sort.Strings(st.MissionIDs)
	for t := range taskSet {
		st.TaskIDs = append(st.TaskIDs, t)
	}
	sort.Strings(st.TaskIDs)
	st.ResumeHint = resumeHint(last)
	return st
}

// eventMatchesSession returns true when the event's scope or ID
// carries the given session identifier. SOW runs populate
// Scope.LoopID; TrustPlane emits Scope.MissionID; some emitters
// stash the session ID in the event ID prefix.
func eventMatchesSession(e bus.Event, sessionID string) bool {
	if e.Scope.LoopID == sessionID {
		return true
	}
	if e.Scope.MissionID == sessionID {
		return true
	}
	if e.Scope.BranchID == sessionID {
		return true
	}
	if strings.Contains(e.ID, sessionID) {
		return true
	}
	return false
}

// resumeHint translates the last observed event into a one-line
// pointer for the operator. Best-effort; maps a handful of known
// event types to a concrete next step.
func resumeHint(e bus.Event) string {
	t := string(e.Type)
	switch {
	case strings.Contains(t, "task.dispatch"), strings.Contains(t, "task.start"):
		return "last activity was a task dispatch; the task was still running when the log ended"
	case strings.Contains(t, "task.complete"):
		return "the last task completed; resume by dispatching the next task in the plan"
	case strings.Contains(t, "task.fail"):
		return "the last task failed; review failure evidence and decide repair vs abort"
	case strings.Contains(t, "verify.tier"):
		return "mid-descent — the verification ladder was escalating when the log ended"
	case strings.Contains(t, "hitl"):
		return "a human-in-the-loop gate was waiting for operator input"
	case strings.Contains(t, "session.end"), strings.Contains(t, "session.complete"):
		return "session finished cleanly; nothing to resume"
	default:
		return "last event type " + t + " — inspect the event payload for next-step context"
	}
}

func printReconstruction(sessionID string, events []bus.Event, asJSON bool) {
	st := reconstructSession(sessionID, events)
	if asJSON {
		buf, _ := json.MarshalIndent(st, "", "  ")
		fmt.Println(string(buf))
		return
	}
	if st.EventCount == 0 {
		fmt.Printf("session %q: %s\n", sessionID, st.ResumeHint)
		return
	}
	fmt.Printf("Session:         %s\n", st.SessionID)
	fmt.Printf("First event:     %s\n", st.FirstSeenAt.Format(time.RFC3339))
	fmt.Printf("Last event:      %s\n", st.LastSeenAt.Format(time.RFC3339))
	fmt.Printf("Total events:    %d\n", st.EventCount)
	fmt.Printf("Last event type: %s (id=%s, seq=%d)\n", st.LastEventType, st.LastEventID, st.LastSequence)
	if len(st.MissionIDs) > 0 {
		fmt.Printf("Missions:        %s\n", strings.Join(st.MissionIDs, ", "))
	}
	if len(st.TaskIDs) > 0 {
		fmt.Printf("Tasks:           %s\n", strings.Join(st.TaskIDs, ", "))
	}
	fmt.Println()
	fmt.Println("Event type distribution:")
	for _, tc := range st.EventsByType {
		fmt.Printf("  %6d  %s\n", tc.Count, tc.Type)
	}
	fmt.Println()
	fmt.Printf("Resume hint: %s\n", st.ResumeHint)
	fmt.Println()
	fmt.Println("Note: `r1 resume` is a reporting tool in this cycle. Wiring the SOW")
	fmt.Println("runner to actually re-enter mid-session from a replay cursor is the Task 18")
	fmt.Println("follow-up. For now, use the reported LastEventID / LastSequence to resume")
	fmt.Println("work manually or feed them into `r1 sow --resume-from=<seq>` once the")
	fmt.Println("runner integration lands.")
}

func listSessions(events []bus.Event) {
	set := map[string]struct{}{}
	for _, e := range events {
		if e.Scope.LoopID != "" {
			set[e.Scope.LoopID] = struct{}{}
		}
		if e.Scope.MissionID != "" {
			set[e.Scope.MissionID] = struct{}{}
		}
		if e.Scope.BranchID != "" {
			set[e.Scope.BranchID] = struct{}{}
		}
	}
	if len(set) == 0 {
		fmt.Println("(no sessions found in event log)")
		return
	}
	ids := make([]string, 0, len(set))
	for s := range set {
		ids = append(ids, s)
	}
	sort.Strings(ids)
	fmt.Printf("Sessions in event log (%d):\n", len(ids))
	for _, id := range ids {
		fmt.Printf("  %s\n", id)
	}
}
