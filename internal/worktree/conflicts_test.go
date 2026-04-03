package worktree

import (
	"testing"
)

func TestParseConflictFiles(t *testing.T) {
	output := `some-tree-hash
CONFLICT (content): Merge conflict in src/main.go
CONFLICT (content): Merge conflict in internal/handler.go
`
	files := parseConflictFiles(output)
	if len(files) != 2 {
		t.Fatalf("expected 2 conflict files, got %d: %v", len(files), files)
	}
	if files[0] != "src/main.go" {
		t.Errorf("expected src/main.go, got %s", files[0])
	}
	if files[1] != "internal/handler.go" {
		t.Errorf("expected internal/handler.go, got %s", files[1])
	}
}

func TestParseConflictFilesEmpty(t *testing.T) {
	files := parseConflictFiles("abc123\n")
	if len(files) != 0 {
		t.Errorf("expected no conflicts, got %v", files)
	}
}

func TestParseConflictFilesDeduplicate(t *testing.T) {
	output := `CONFLICT (content): Merge conflict in file.go
CONFLICT (content): Merge conflict in file.go`
	files := parseConflictFiles(output)
	if len(files) != 1 {
		t.Errorf("expected 1 file (deduped), got %d", len(files))
	}
}

func TestConflictScannerNoHandles(t *testing.T) {
	mgr := &Manager{RepoRoot: t.TempDir(), GitBinary: "git"}
	cs := NewConflictScanner(mgr)

	conflicts := cs.Scan(nil, nil)
	if len(conflicts) != 0 {
		t.Error("expected no conflicts with no handles")
	}
	if cs.ScanCount() != 1 {
		t.Errorf("expected scan count 1, got %d", cs.ScanCount())
	}
}

func TestConflictsForFilter(t *testing.T) {
	mgr := &Manager{RepoRoot: t.TempDir(), GitBinary: "git"}
	cs := NewConflictScanner(mgr)

	// Manually set conflicts
	cs.mu.Lock()
	cs.conflicts = []ConflictPair{
		{WorktreeA: "wt-1", WorktreeB: "wt-2", Files: []string{"a.go"}},
		{WorktreeA: "wt-2", WorktreeB: "wt-3", Files: []string{"b.go"}},
		{WorktreeA: "wt-1", WorktreeB: "wt-3", Files: []string{"c.go"}},
	}
	cs.mu.Unlock()

	wt1 := cs.ConflictsFor("wt-1")
	if len(wt1) != 2 {
		t.Errorf("expected 2 conflicts for wt-1, got %d", len(wt1))
	}

	wt2 := cs.ConflictsFor("wt-2")
	if len(wt2) != 2 {
		t.Errorf("expected 2 conflicts for wt-2, got %d", len(wt2))
	}

	wt4 := cs.ConflictsFor("wt-4")
	if len(wt4) != 0 {
		t.Errorf("expected 0 conflicts for wt-4, got %d", len(wt4))
	}
}

func TestHasConflicts(t *testing.T) {
	mgr := &Manager{RepoRoot: t.TempDir(), GitBinary: "git"}
	cs := NewConflictScanner(mgr)

	if cs.HasConflicts() {
		t.Error("expected no conflicts initially")
	}

	cs.mu.Lock()
	cs.conflicts = []ConflictPair{{WorktreeA: "a", WorktreeB: "b", Files: []string{"f.go"}}}
	cs.mu.Unlock()

	if !cs.HasConflicts() {
		t.Error("expected conflicts after setting them")
	}
}
