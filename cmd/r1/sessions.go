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

	"github.com/RelayOne/r1/internal/checkpoint"
)

// sessionsCmd is the `r1 sessions` subcommand. Provides
// headless + interactive exploration of checkpoints, session
// markers, and SOW state for a repo.
//
// Subcommands:
//
//	r1 sessions list           — headless: print checkpoint timeline
//	r1 sessions status         — headless: print session markers
//	r1 sessions inspect CP-042 — headless: print one checkpoint's full state
//	r1 sessions diff CP-005 CP-010 — headless: show what changed between two checkpoints
//	r1 sessions explore        — interactive: TUI checkpoint browser (TODO)
func sessionsCmd(args []string) {
	if len(args) == 0 {
		args = []string{"help"}
	}
	// Separate subcommand, positional args (CP-IDs), and flags
	// (--repo, --json) from the flat arg list. Allows any ordering:
	//   r1 sessions list --repo /tmp/x
	//   r1 sessions --repo /tmp/x inspect CP-005
	//   r1 sessions diff CP-001 CP-005 --repo /tmp/x
	var sub string
	var positionals []string
	var flagArgs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "-"):
			flagArgs = append(flagArgs, a)
			// If the flag takes a value (not a bool), grab next arg too.
			if (a == "--repo" || a == "-repo") && i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
		case sub == "":
			sub = a
		default:
			positionals = append(positionals, a)
		}
	}
	if sub == "" {
		sub = "help"
	}
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	repo := fs.String("repo", ".", "Repository root")
	jsonOut := fs.Bool("json", false, "Output as JSON instead of human-readable table")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(flagArgs); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sessions:", err)
		os.Exit(1)
	}

	switch sub {
	case "list", "ls":
		sessionsListCmd(absRepo, *jsonOut)
	case "status", "st":
		sessionsStatusCmd(absRepo, *jsonOut)
	case "inspect", "show":
		if len(positionals) < 1 {
			fmt.Fprintln(os.Stderr, "usage: r1 sessions inspect <CP-ID>")
			os.Exit(2)
		}
		sessionsInspectCmd(absRepo, positionals[0], *jsonOut)
	case "diff":
		if len(positionals) < 2 {
			fmt.Fprintln(os.Stderr, "usage: r1 sessions diff <CP-FROM> <CP-TO>")
			os.Exit(2)
		}
		sessionsDiffCmd(absRepo, positionals[0], positionals[1])
	case "help", "-h", "--help":
		fmt.Println("r1 sessions <subcommand> [--repo <path>] [--json]")
		fmt.Println()
		fmt.Println("Subcommands:")
		fmt.Println("  list (ls)              Show checkpoint timeline")
		fmt.Println("  status (st)            Show session completion markers")
		fmt.Println("  inspect (show) <CP-ID> Show one checkpoint's full state")
		fmt.Println("  diff <CP-FROM> <CP-TO> Show what changed between two checkpoints")
		fmt.Println("  help                   This message")
		fmt.Println()
		fmt.Println("Flags:")
		fmt.Println("  --repo <path>   Repository root (default: .)")
		fmt.Println("  --json          Output as JSON")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  r1 sessions list")
		fmt.Println("  r1 sessions status --repo /tmp/sentinel-clients")
		fmt.Println("  r1 sessions inspect CP-008")
		fmt.Println("  r1 sessions diff CP-003 CP-010")
		fmt.Println("  r1 sow --resume-from CP-008 ...  # resume from a checkpoint")
	default:
		fmt.Fprintf(os.Stderr, "unknown sessions subcommand: %q\n", sub)
		os.Exit(2)
	}
}

// --- list ---

func sessionsListCmd(repo string, jsonOut bool) {
	entries, err := checkpoint.ListCheckpoints(repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sessions list:", err)
		os.Exit(1)
	}
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(entries)
		return
	}
	if len(entries) == 0 {
		fmt.Println("No checkpoints found. Run `r1 sow` to generate them.")
		return
	}
	fmt.Print(checkpoint.FormatCheckpointList(entries))
	fmt.Printf("\n%d checkpoint(s). Resume from any with: r1 sow --resume-from <ID>\n", len(entries))
}

// --- status ---

type sessionMarker struct {
	SessionID   string    `json:"session_id"`
	Title       string    `json:"title"`
	CompletedAt time.Time `json:"completed_at"`
	Note        string    `json:"note,omitempty"`
}

func sessionsStatusCmd(repo string, jsonOut bool) {
	markerDir := filepath.Join(repo, ".stoke", "sow-state-markers")
	entries, err := os.ReadDir(markerDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No session markers found. No sessions have completed yet.")
			return
		}
		fmt.Fprintln(os.Stderr, "sessions status:", err)
		os.Exit(1)
	}
	markers := make([]sessionMarker, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(markerDir, e.Name()))
		if err != nil {
			continue
		}
		var m sessionMarker
		_ = json.Unmarshal(b, &m)
		if m.SessionID == "" {
			m.SessionID = strings.TrimSuffix(e.Name(), ".json")
		}
		markers = append(markers, m)
	}
	sort.Slice(markers, func(i, j int) bool {
		return markers[i].CompletedAt.Before(markers[j].CompletedAt)
	})
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(markers)
		return
	}
	if len(markers) == 0 {
		fmt.Println("No session markers found. No sessions have completed yet.")
		return
	}
	fmt.Printf("%-12s %-40s %-20s %s\n", "Session", "Title", "Completed", "Note")
	fmt.Printf("%-12s %-40s %-20s %s\n", "--------", "----------------------------------------", "--------------------", "----")
	for _, m := range markers {
		title := m.Title
		if len(title) > 40 {
			title = title[:37] + "..."
		}
		ts := m.CompletedAt.Format("2006-01-02 15:04")
		if m.CompletedAt.IsZero() {
			ts = "-"
		}
		note := m.Note
		if len(note) > 30 {
			note = note[:27] + "..."
		}
		fmt.Printf("%-12s %-40s %-20s %s\n", m.SessionID, title, ts, note)
	}
	fmt.Printf("\n%d session(s) completed.\n", len(markers))
}

// --- inspect ---

func sessionsInspectCmd(repo, cpID string, jsonOut bool) {
	entry, err := checkpoint.FindCheckpoint(repo, cpID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sessions inspect:", err)
		os.Exit(1)
	}
	if entry == nil {
		fmt.Fprintf(os.Stderr, "checkpoint %s not found\n", cpID)
		os.Exit(1)
	}
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(entry)
		return
	}
	fmt.Printf("Checkpoint: %s\n", entry.ID)
	fmt.Printf("  Label:     %s\n", entry.Label)
	fmt.Printf("  Timestamp: %s\n", entry.Timestamp.Format(time.RFC3339))
	fmt.Printf("  Git SHA:   %s\n", entry.GitSHA)
	fmt.Printf("  Cost:      $%.2f\n", entry.CostUSD)
	fmt.Printf("  Tasks:     %d\n", entry.TasksCompleted)
	fmt.Printf("  Session:   %s\n", entry.SessionID)
	if len(entry.CompletedSessions) > 0 {
		fmt.Printf("  Completed: %s\n", strings.Join(entry.CompletedSessions, ", "))
	}
	if len(entry.Metadata) > 0 {
		b, _ := json.MarshalIndent(entry.Metadata, "             ", "  ")
		fmt.Printf("  Metadata:  %s\n", string(b))
	}
	fmt.Printf("\nResume from this checkpoint:\n  r1 sow --resume-from %s --repo %s ...\n", cpID, repo)
}

// --- diff ---

func sessionsDiffCmd(repo, fromID, toID string) {
	from, err := checkpoint.FindCheckpoint(repo, fromID)
	if err != nil || from == nil {
		fmt.Fprintf(os.Stderr, "checkpoint %s not found\n", fromID)
		os.Exit(1)
	}
	to, err := checkpoint.FindCheckpoint(repo, toID)
	if err != nil || to == nil {
		fmt.Fprintf(os.Stderr, "checkpoint %s not found\n", toID)
		os.Exit(1)
	}
	fmt.Printf("Diff: %s (%s) → %s (%s)\n\n", from.ID, from.Label, to.ID, to.Label)
	fmt.Printf("  Cost:    $%.2f → $%.2f  (+$%.2f)\n", from.CostUSD, to.CostUSD, to.CostUSD-from.CostUSD)
	fmt.Printf("  Tasks:   %d → %d  (+%d)\n", from.TasksCompleted, to.TasksCompleted, to.TasksCompleted-from.TasksCompleted)
	// Sessions completed between the two checkpoints.
	fromSet := map[string]bool{}
	for _, s := range from.CompletedSessions {
		fromSet[s] = true
	}
	var added []string
	for _, s := range to.CompletedSessions {
		if !fromSet[s] {
			added = append(added, s)
		}
	}
	toSet := map[string]bool{}
	for _, s := range to.CompletedSessions {
		toSet[s] = true
	}
	var removed []string
	for _, s := range from.CompletedSessions {
		if !toSet[s] {
			removed = append(removed, s)
		}
	}
	if len(added) > 0 {
		fmt.Printf("  Sessions completed: +%s\n", strings.Join(added, ", +"))
	}
	if len(removed) > 0 {
		fmt.Printf("  Sessions removed:   -%s\n", strings.Join(removed, ", -"))
	}
	if len(added) == 0 && len(removed) == 0 {
		fmt.Println("  Sessions: no change")
	}
	elapsed := to.Timestamp.Sub(from.Timestamp)
	fmt.Printf("  Elapsed: %s\n", elapsed.Round(time.Second))
}
