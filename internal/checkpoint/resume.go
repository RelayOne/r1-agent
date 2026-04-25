// Package checkpoint — resume.go
//
// Implements the "resume from checkpoint" workflow:
//
//   1. Operator fixes code + rebuilds binary
//   2. `stoke sow --resume-from CP-042 ...`
//   3. RestoreFromCheckpoint reads the WAL, finds CP-042,
//      rewrites session markers on disk to match the
//      checkpoint's CompletedSessions snapshot, and returns
//      the ResumeState the session scheduler needs to
//      re-enter at the right position.
//
// The session scheduler checks ResumeState.Skip(sessionID)
// before dispatching each session. Sessions completed
// before the checkpoint are skipped; everything else runs
// fresh with the new binary.
package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1-agent/internal/r1dir"
)

// ResumeState is returned by RestoreFromCheckpoint and
// consumed by the session scheduler. It carries the
// checkpoint's snapshot so the scheduler knows which
// sessions to skip and which to (re-)run.
type ResumeState struct {
	// CheckpointID is the CP-NNN that was resumed from.
	CheckpointID string

	// Label is the human-readable label (e.g.,
	// "session-start:S2", "ac-attempt:S1:3").
	Label string

	// CompletedSessions is the set of session IDs that had
	// completion markers at checkpoint time. The scheduler
	// skips these.
	CompletedSessions map[string]bool

	// ResumeSessionID is the session that was active at the
	// checkpoint (e.g., "S2" from "session-start:S2"). The
	// scheduler re-runs this session from scratch with the
	// new binary. Empty for run-level checkpoints like
	// "sow-converted".
	ResumeSessionID string

	// CostUSD is the cumulative cost at the checkpoint.
	// The scheduler deducts this from the run's cost
	// tracking so budget enforcement is accurate.
	CostUSD float64
}

// Skip reports whether the scheduler should skip a session
// because it was completed before the checkpoint.
func (rs *ResumeState) Skip(sessionID string) bool {
	if rs == nil {
		return false
	}
	return rs.CompletedSessions[sessionID]
}

// RestoreFromCheckpoint reads the WAL, finds the target
// checkpoint, rewrites session markers on disk, and returns
// the ResumeState.
//
// Marker rewrite: any marker that exists on disk but is NOT
// in the checkpoint's CompletedSessions is DELETED so the
// session scheduler re-runs it. Any marker in the snapshot
// that's missing on disk is a no-op (the scheduler will
// skip it via ResumeState.Skip anyway, and the marker was
// presumably written by the checkpoint's originating run
// then cleaned up — re-creating it from thin air would be
// a fabrication).
//
// Returns an error if the checkpoint ID doesn't exist in
// the WAL or if marker cleanup fails.
func RestoreFromCheckpoint(repoRoot, checkpointID string) (*ResumeState, error) {
	entry, err := FindCheckpoint(repoRoot, checkpointID)
	if err != nil {
		return nil, fmt.Errorf("checkpoint: find %s: %w", checkpointID, err)
	}
	if entry == nil {
		available, _ := ListCheckpoints(repoRoot)
		hint := ""
		if len(available) > 0 {
			last := available[len(available)-1]
			hint = fmt.Sprintf(" (latest available: %s %q)", last.ID, last.Label)
		}
		return nil, fmt.Errorf("checkpoint %s not found in WAL%s", checkpointID, hint)
	}

	// Build the set of sessions that should be marked complete.
	completed := map[string]bool{}
	for _, sid := range entry.CompletedSessions {
		completed[sid] = true
	}

	// Clean up markers that shouldn't exist per the checkpoint.
	markerDir := r1dir.JoinFor(repoRoot, "sow-state-markers")
	if entries, err := os.ReadDir(markerDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			sid := strings.TrimSuffix(e.Name(), ".json")
			if !completed[sid] {
				path := filepath.Join(markerDir, e.Name())
				if err := os.Remove(path); err != nil {
					return nil, fmt.Errorf("checkpoint: remove stale marker %s: %w", sid, err)
				}
				fmt.Printf("  📌 resume: removed marker %s (post-checkpoint work)\n", sid)
			}
		}
	}

	// Extract the resume session from the label.
	resumeSession := entry.SessionID
	if resumeSession == "" {
		// Try parsing from label: "session-start:S2" → "S2"
		if strings.HasPrefix(entry.Label, "session-start:") {
			resumeSession = strings.TrimPrefix(entry.Label, "session-start:")
		} else if strings.HasPrefix(entry.Label, "ac-attempt:") {
			parts := strings.SplitN(strings.TrimPrefix(entry.Label, "ac-attempt:"), ":", 2)
			if len(parts) > 0 {
				resumeSession = parts[0]
			}
		}
	}

	return &ResumeState{
		CheckpointID:      entry.ID,
		Label:             entry.Label,
		CompletedSessions: completed,
		ResumeSessionID:   resumeSession,
		CostUSD:           entry.CostUSD,
	}, nil
}

// FormatCheckpointList produces a human-readable table of
// checkpoints for `stoke sow --list-checkpoints`.
func FormatCheckpointList(entries []TimelineEntry) string {
	if len(entries) == 0 {
		return "No checkpoints found.\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-8s %-30s %-10s %-6s %s\n", "ID", "Label", "Cost", "Tasks", "Sessions Done")
	fmt.Fprintf(&b, "%-8s %-30s %-10s %-6s %s\n", "--------", "------------------------------", "----------", "------", "-------------")
	for _, e := range entries {
		sessions := "-"
		if len(e.CompletedSessions) > 0 {
			sessions = strings.Join(e.CompletedSessions, ",")
			if len(sessions) > 30 {
				sessions = sessions[:27] + "..."
			}
		}
		label := e.Label
		if len(label) > 30 {
			label = label[:27] + "..."
		}
		fmt.Fprintf(&b, "%-8s %-30s $%-9.2f %-6d %s\n",
			e.ID, label, e.CostUSD, e.TasksCompleted, sessions)
	}
	return b.String()
}

// PruneTimelineAfter removes all WAL entries after the given
// checkpoint ID so the next run starts from a clean timeline
// position. This is called during --resume-from to prevent
// the new run's checkpoints from mixing with stale entries
// from the prior run's post-checkpoint work.
func PruneTimelineAfter(repoRoot, checkpointID string) error {
	entries, err := ListCheckpoints(repoRoot)
	if err != nil {
		return err
	}
	// Find the target index.
	idx := -1
	for i, e := range entries {
		if e.ID == checkpointID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("checkpoint %s not found for pruning", checkpointID)
	}
	// Keep entries[0..idx] inclusive. Rewrite the WAL.
	keep := entries[:idx+1]
	p := r1dir.JoinFor(repoRoot, "checkpoints", "timeline.jsonl")
	tmp := p + ".prune-tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("checkpoint: prune create tmp: %w", err)
	}
	for _, e := range keep {
		b, _ := json.Marshal(e)
		f.Write(b)
		f.Write([]byte{'\n'})
	}
	f.Close()
	return os.Rename(tmp, p)
}
