package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// sowNativeConfig telemetry helpers: session-lookup + SOW snapshot.
// Kept in their own file so sow_native.go (already 5000+ lines) doesn't
// grow further. All methods are read-only against cfg except for the
// lazy-populated snapshotOnce map.

var (
	sessionSOWSnapshotMu   sync.Mutex
	sessionSOWSnapshotPath = map[string]string{} // "<repoRoot>|<sessionID>" -> snapshot path
)

// sessionIDForTask resolves a task ID to its session ID by scanning
// cfg.sowDocument (if available) for a session that declares this
// task. Falls back to empty string if the task isn't in any known
// session (e.g. synthetic repair tasks created by descent). Empty
// return is fine — addCtx elides it from the JSONL entry.
func (cfg sowNativeConfig) sessionIDForTask(taskID string) string {
	if cfg.sowDocRef != nil {
		for _, s := range cfg.sowDocRef.Sessions {
			for _, t := range s.Tasks {
				if t.ID == taskID {
					return s.ID
				}
			}
		}
	}
	// Heuristic: descent-repair tasks embed the session ID as a
	// prefix (see descent_bridge.go RepairFunc). Recover it even
	// when the task isn't in sowDocRef.Sessions.
	if idx := strings.Index(taskID, "-descent-repair-"); idx > 0 {
		return taskID[:idx]
	}
	// Fix-DAG promoted tasks follow "<sessionID>-fix-FIXn".
	if strings.Contains(taskID, "-fix-") {
		if idx := strings.Index(taskID, "-fix-"); idx > 0 {
			return taskID[:idx]
		}
	}
	// Continuation sessions: "<sessionID>-cont-tN".
	if idx := strings.Index(taskID, "-cont-"); idx > 0 {
		return taskID[:idx]
	}
	return ""
}

// maybeWriteSOWSnapshot writes the raw SOW text to
// <repoRoot>/.stoke/sessions/<sessionID>/sow-snapshot.md the first
// time a task from that session runs, and returns the snapshot path
// for embedding into the worker JSONL. Subsequent tasks in the same
// session reuse the existing snapshot (so it truly reflects the
// SOW AT SESSION START, not the evolving continuation state).
//
// Returns empty string when no SOW text is available (chat-mode
// dispatches, unit tests) — caller writes no sow_path field in that
// case. Failures to write are non-fatal: log + return empty.
func maybeWriteSOWSnapshot(cfg sowNativeConfig, sessionID string) string {
	if cfg.RepoRoot == "" || sessionID == "" || strings.TrimSpace(cfg.RawSOWText) == "" {
		return ""
	}
	key := cfg.RepoRoot + "|" + sessionID
	sessionSOWSnapshotMu.Lock()
	defer sessionSOWSnapshotMu.Unlock()
	if p, ok := sessionSOWSnapshotPath[key]; ok {
		return p
	}
	dir := filepath.Join(cfg.RepoRoot, ".stoke", "sessions", sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Printf("    ⚠ sow snapshot: mkdir %s: %v (non-fatal)\n", dir, err)
		return ""
	}
	path := filepath.Join(dir, "sow-snapshot.md")
	if err := os.WriteFile(path, []byte(cfg.RawSOWText), 0o644); err != nil {
		fmt.Printf("    ⚠ sow snapshot: write %s: %v (non-fatal)\n", path, err)
		return ""
	}
	sessionSOWSnapshotPath[key] = path
	return path
}
