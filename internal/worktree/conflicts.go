// conflicts.go adds cross-worktree conflict detection during execution.
// Inspired by Clash and the SOTA pattern: detect file-level conflicts between
// active worktrees DURING execution (not just at merge time) using git merge-tree.
// Early detection allows task reassignment before merge-time surprises.
package worktree

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ConflictPair identifies two worktrees with conflicting file changes.
type ConflictPair struct {
	WorktreeA    string   `json:"worktree_a"`
	WorktreeB    string   `json:"worktree_b"`
	Files        []string `json:"files"`        // conflicting files
	DetectedAt   time.Time `json:"detected_at"`
}

// ConflictScanner periodically checks active worktrees for cross-conflicts.
type ConflictScanner struct {
	mu          sync.Mutex
	manager     *Manager
	conflicts   []ConflictPair
	lastScan    time.Time
	scanCount   int
}

// NewConflictScanner creates a scanner for the given worktree manager.
func NewConflictScanner(manager *Manager) *ConflictScanner {
	return &ConflictScanner{manager: manager}
}

// Scan checks all pairs of active worktrees for file-level conflicts.
// Returns newly detected conflicts. Uses git merge-tree for zero-side-effect
// conflict detection (design decision #8).
func (cs *ConflictScanner) Scan(ctx context.Context, handles []Handle) []ConflictPair {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	cs.lastScan = time.Now()
	cs.scanCount++

	var newConflicts []ConflictPair

	// Check each pair
	for i := 0; i < len(handles); i++ {
		for j := i + 1; j < len(handles); j++ {
			a := handles[i]
			b := handles[j]

			files, err := detectConflicts(ctx, cs.manager, a, b)
			if err != nil || len(files) == 0 {
				continue
			}

			pair := ConflictPair{
				WorktreeA:  a.Name,
				WorktreeB:  b.Name,
				Files:      files,
				DetectedAt: time.Now(),
			}
			newConflicts = append(newConflicts, pair)
		}
	}

	cs.conflicts = newConflicts
	return newConflicts
}

// Conflicts returns the last detected conflicts.
func (cs *ConflictScanner) Conflicts() []ConflictPair {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cp := make([]ConflictPair, len(cs.conflicts))
	copy(cp, cs.conflicts)
	return cp
}

// HasConflicts returns true if any conflicts exist.
func (cs *ConflictScanner) HasConflicts() bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return len(cs.conflicts) > 0
}

// ConflictsFor returns conflicts involving a specific worktree.
func (cs *ConflictScanner) ConflictsFor(worktreeName string) []ConflictPair {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	var result []ConflictPair
	for _, c := range cs.conflicts {
		if c.WorktreeA == worktreeName || c.WorktreeB == worktreeName {
			result = append(result, c)
		}
	}
	return result
}

// ScanCount returns the number of scans performed.
func (cs *ConflictScanner) ScanCount() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.scanCount
}

// detectConflicts uses git merge-tree to check if two worktrees have conflicting changes.
// Returns the list of conflicting file paths, or nil if no conflicts.
func detectConflicts(ctx context.Context, mgr *Manager, a, b Handle) ([]string, error) {
	if a.BaseCommit == "" || b.BaseCommit == "" {
		return nil, fmt.Errorf("missing base commits")
	}

	// Get the current HEAD of each worktree's branch
	headA, err := worktreeHead(ctx, mgr, a)
	if err != nil {
		return nil, err
	}
	headB, err := worktreeHead(ctx, mgr, b)
	if err != nil {
		return nil, err
	}

	if headA == "" || headB == "" {
		return nil, nil // no commits yet
	}

	// Use merge-tree to detect conflicts without modifying anything
	// git merge-tree --write-tree <base> <headA> <headB>
	// Exit code 1 = conflicts exist, files listed in output
	cmd := exec.CommandContext(ctx, mgr.GitBinary, "merge-tree", "--write-tree", a.BaseCommit, headA, headB)
	cmd.Dir = mgr.RepoRoot
	out, err := cmd.CombinedOutput()

	if err == nil {
		// Clean merge — no conflicts
		return nil, nil
	}

	// Parse conflicting files from merge-tree output
	return parseConflictFiles(string(out)), nil
}

// worktreeHead returns the current HEAD commit of a worktree's branch.
func worktreeHead(ctx context.Context, mgr *Manager, h Handle) (string, error) {
	cmd := exec.CommandContext(ctx, mgr.GitBinary, "rev-parse", h.Branch)
	cmd.Dir = mgr.RepoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", nil // branch may not have commits yet
	}
	return strings.TrimSpace(string(out)), nil
}

// parseConflictFiles extracts file paths from merge-tree conflict output.
func parseConflictFiles(output string) []string {
	seen := make(map[string]bool)
	var files []string

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		// merge-tree outputs conflict markers and file paths
		// Look for lines that look like file paths
		if line == "" || strings.HasPrefix(line, "CONFLICT") {
			// Extract filename from "CONFLICT (content): Merge conflict in <file>"
			if idx := strings.Index(line, "Merge conflict in "); idx >= 0 {
				file := strings.TrimSpace(line[idx+len("Merge conflict in "):])
				if file != "" && !seen[file] {
					seen[file] = true
					files = append(files, file)
				}
			}
			continue
		}
	}
	return files
}

// ModifiedFilesList returns files modified in a worktree (wrapper for conflict scanning).
func ModifiedFilesList(ctx context.Context, handle Handle) []string {
	files, err := ModifiedFiles(ctx, handle)
	if err != nil {
		return nil
	}
	return files
}

// OverlappingFiles returns files modified by both worktrees (potential conflicts
// even without merge-tree, just by file-scope overlap).
func OverlappingFiles(ctx context.Context, a, b Handle) []string {
	filesA := ModifiedFilesList(ctx, a)
	filesB := ModifiedFilesList(ctx, b)

	setA := make(map[string]bool, len(filesA))
	for _, f := range filesA {
		setA[f] = true
	}

	var overlap []string
	for _, f := range filesB {
		if setA[f] {
			overlap = append(overlap, f)
		}
	}
	return overlap
}
