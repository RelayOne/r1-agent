package filewatcher

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewWatcher(t *testing.T) {
	w := New(Config{Root: "/tmp"})
	if w.config.Interval != 500*time.Millisecond {
		t.Error("default interval should be 500ms")
	}
	if w.config.Debounce != 100*time.Millisecond {
		t.Error("default debounce should be 100ms")
	}
	if len(w.config.IgnoreDirs) == 0 {
		t.Error("should have default ignore dirs")
	}
}

func TestScanDetectsFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a"), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b"), 0644)

	w := New(Config{Root: dir})
	if err := w.scan(false); err != nil {
		t.Fatal(err)
	}

	if w.FileCount() != 2 {
		t.Errorf("expected 2 files, got %d", w.FileCount())
	}
}

func TestScanDetectsChanges(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.go")
	os.WriteFile(file, []byte("v1"), 0644)

	w := New(Config{Root: dir})
	w.scan(false)

	// Modify the file
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(file, []byte("v2-modified"), 0644)

	events, err := w.Scan()
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, e := range events {
		if e.Type == EventModify && filepath.Base(e.Path) == "a.go" {
			found = true
		}
	}
	if !found {
		t.Error("should detect modified file")
	}
}

func TestScanDetectsCreate(t *testing.T) {
	dir := t.TempDir()

	w := New(Config{Root: dir})
	w.scan(false)

	os.WriteFile(filepath.Join(dir, "new.go"), []byte("package new"), 0644)

	events, err := w.Scan()
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, e := range events {
		if e.Type == EventCreate {
			found = true
		}
	}
	if !found {
		t.Error("should detect new file")
	}
}

func TestScanDetectsDelete(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "del.go")
	os.WriteFile(file, []byte("bye"), 0644)

	w := New(Config{Root: dir})
	w.scan(false)

	os.Remove(file)

	events, err := w.Scan()
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, e := range events {
		if e.Type == EventDelete {
			found = true
		}
	}
	if !found {
		t.Error("should detect deleted file")
	}
}

func TestExtensionFilter(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("go"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("txt"), 0644)
	os.WriteFile(filepath.Join(dir, "c.js"), []byte("js"), 0644)

	w := New(Config{Root: dir, Extensions: []string{".go"}})
	w.scan(false)

	if w.FileCount() != 1 {
		t.Errorf("expected 1 .go file, got %d", w.FileCount())
	}
}

func TestIgnoreHidden(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "visible.go"), []byte("v"), 0644)
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte("h"), 0644)

	w := New(Config{Root: dir, IgnoreHidden: true})
	w.scan(false)

	if w.FileCount() != 1 {
		t.Errorf("expected 1 visible file, got %d", w.FileCount())
	}
}

func TestIgnoreDirs(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "node_modules")
	os.MkdirAll(subdir, 0755)
	os.WriteFile(filepath.Join(subdir, "pkg.js"), []byte("js"), 0644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("go"), 0644)

	w := New(Config{Root: dir})
	w.scan(false)

	if w.FileCount() != 1 {
		t.Errorf("expected 1 file (node_modules ignored), got %d", w.FileCount())
	}
}

func TestSnapshot(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("a"), 0644)

	w := New(Config{Root: dir})
	w.scan(false)

	snap := w.Snapshot()
	if len(snap) != 1 {
		t.Errorf("expected 1 entry in snapshot, got %d", len(snap))
	}
}

func TestHashMode(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.go")
	os.WriteFile(file, []byte("content"), 0644)

	w := New(Config{Root: dir, UseHash: true})
	w.scan(false)

	// Same content, touch mtime
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(file, []byte("content"), 0644)

	events, _ := w.Scan()
	for _, e := range events {
		if e.Type == EventModify {
			t.Error("hash mode should not detect unchanged content")
		}
	}
}

func TestHasChanged(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.go")
	os.WriteFile(file, []byte("v1"), 0644)

	w := New(Config{Root: dir})
	w.scan(false)

	if w.HasChanged(file) {
		t.Error("should not be changed right after scan")
	}

	time.Sleep(10 * time.Millisecond)
	os.WriteFile(file, []byte("v2"), 0644)

	if !w.HasChanged(file) {
		t.Error("should detect change")
	}
}

func TestInvalidationSet(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("v1"), 0644)

	w := New(Config{Root: dir})
	w.scan(false)

	os.WriteFile(filepath.Join(dir, "b.go"), []byte("new"), 0644)

	paths, err := w.InvalidationSet()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Errorf("expected 1 invalidation, got %d", len(paths))
	}
}

func TestOnChangeHandler(t *testing.T) {
	dir := t.TempDir()

	w := New(Config{Root: dir, Interval: 50 * time.Millisecond, Debounce: 10 * time.Millisecond})

	var received []Event
	w.OnChange(func(e Event) {
		received = append(received, e)
	})

	w.scan(false) // baseline

	os.WriteFile(filepath.Join(dir, "new.go"), []byte("new"), 0644)

	// Simulate one poll cycle
	w.scan(true)
	w.flushDebounced()

	if len(received) == 0 {
		t.Error("handler should have received events")
	}
}

func TestStartStop(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("a"), 0644)

	w := New(Config{Root: dir, Interval: 50 * time.Millisecond})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}

	// Double start should error
	if err := w.Start(); err == nil {
		t.Error("double start should error")
	}

	w.Stop()
}

func TestRelPath(t *testing.T) {
	w := New(Config{Root: "/home/user/project"})
	rel := w.relPath("/home/user/project/src/main.go")
	if rel != "src/main.go" {
		t.Errorf("expected src/main.go, got %s", rel)
	}
}
