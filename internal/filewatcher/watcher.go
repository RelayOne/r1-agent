// Package filewatcher provides file system monitoring with cache invalidation.
// Inspired by Aider's file tracking and claw-code's workspace awareness:
//
// AI coding tools need to know when files change externally (editor saves,
// git operations, build artifacts). This watcher:
// - Monitors directories for file changes via polling (portable)
// - Tracks file states (mtime, size, hash) for change detection
// - Fires callbacks on create/modify/delete events
// - Integrates with cache invalidation (toolcache, tfidf index, etc.)
// - Debounces rapid changes (editor save → format → save)
package filewatcher

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// EventType classifies a file change.
type EventType string

const (
	EventCreate EventType = "create"
	EventModify EventType = "modify"
	EventDelete EventType = "delete"
)

// Event describes a file system change.
type Event struct {
	Type    EventType `json:"type"`
	Path    string    `json:"path"`
	RelPath string    `json:"rel_path"`
	Time    time.Time `json:"time"`
	Size    int64     `json:"size,omitempty"`
}

// Handler receives file change events.
type Handler func(Event)

// fileState tracks the last-known state of a file.
type fileState struct {
	ModTime time.Time
	Size    int64
	Hash    string // SHA256, computed lazily
}

// Config controls watcher behavior.
type Config struct {
	Root         string        // directory to watch
	Interval     time.Duration // poll interval (default 500ms)
	Debounce     time.Duration // debounce window (default 100ms)
	IgnoreHidden bool          // skip dotfiles/dotdirs
	IgnoreDirs   []string      // directory names to skip
	Extensions   []string      // if set, only watch these extensions (e.g. ".go", ".ts")
	UseHash      bool          // use SHA256 for change detection (slower but precise)
}

// Watcher monitors a directory tree for changes.
type Watcher struct {
	config   Config
	handlers []Handler
	state    map[string]fileState
	mu       sync.RWMutex
	stopCh   chan struct{}
	running  bool

	// debounce tracking
	pending   map[string]*Event
	pendingMu sync.Mutex
}

// New creates a watcher for the given root directory.
func New(cfg Config) *Watcher {
	if cfg.Interval == 0 {
		cfg.Interval = 500 * time.Millisecond
	}
	if cfg.Debounce == 0 {
		cfg.Debounce = 100 * time.Millisecond
	}
	if cfg.IgnoreDirs == nil {
		cfg.IgnoreDirs = []string{".git", "node_modules", "vendor", "__pycache__", ".idea", ".vscode"}
	}
	return &Watcher{
		config:  cfg,
		state:   make(map[string]fileState),
		stopCh:  make(chan struct{}),
		pending: make(map[string]*Event),
	}
}

// OnChange registers a handler for file change events.
func (w *Watcher) OnChange(h Handler) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.handlers = append(w.handlers, h)
}

// Start begins watching. Call Stop() to end.
func (w *Watcher) Start() error {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return fmt.Errorf("watcher already running")
	}
	w.running = true
	w.mu.Unlock()

	// Initial scan to build baseline state
	if err := w.scan(false); err != nil {
		return fmt.Errorf("initial scan: %w", err)
	}

	go w.pollLoop()
	return nil
}

// Stop ends the watcher.
func (w *Watcher) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running {
		close(w.stopCh)
		w.running = false
	}
}

// Scan performs a one-shot scan and returns changes since last scan.
func (w *Watcher) Scan() ([]Event, error) {
	return w.scanCollect()
}

// Snapshot returns the current known file states.
func (w *Watcher) Snapshot() map[string]time.Time {
	w.mu.RLock()
	defer w.mu.RUnlock()
	result := make(map[string]time.Time, len(w.state))
	for path, st := range w.state {
		result[path] = st.ModTime
	}
	return result
}

// FileCount returns the number of tracked files.
func (w *Watcher) FileCount() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.state)
}

// HasChanged checks if a specific file has changed since last scan.
func (w *Watcher) HasChanged(path string) bool {
	w.mu.RLock()
	st, exists := w.state[path]
	w.mu.RUnlock()

	if !exists {
		// Check if file now exists (new file)
		_, err := os.Stat(path)
		return err == nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return true // deleted
	}

	if info.ModTime() != st.ModTime || info.Size() != st.Size {
		return true
	}

	if w.config.UseHash {
		hash, _ := fileHash(path)
		return hash != st.Hash
	}

	return false
}

// InvalidationSet returns paths that changed since last scan,
// useful for cache invalidation.
func (w *Watcher) InvalidationSet() ([]string, error) {
	events, err := w.scanCollect()
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(events))
	for i, e := range events {
		paths[i] = e.Path
	}
	return paths, nil
}

func (w *Watcher) pollLoop() {
	ticker := time.NewTicker(w.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			_ = w.scan(true)
			w.flushDebounced()
		}
	}
}

func (w *Watcher) scan(notify bool) error {
	current := make(map[string]fileState)

	err := filepath.Walk(w.config.Root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable
		}

		if info.IsDir() {
			name := info.Name()
			if w.config.IgnoreHidden && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			for _, skip := range w.config.IgnoreDirs {
				if name == skip {
					return filepath.SkipDir
				}
			}
			return nil
		}

		if !w.matchExtension(path) {
			return nil
		}

		if w.config.IgnoreHidden && strings.HasPrefix(info.Name(), ".") {
			return nil
		}

		st := fileState{
			ModTime: info.ModTime(),
			Size:    info.Size(),
		}
		if w.config.UseHash {
			st.Hash, _ = fileHash(path)
		}
		current[path] = st
		return nil
	})
	if err != nil {
		return err
	}

	if notify {
		w.detectChanges(current)
	}

	w.mu.Lock()
	w.state = current
	w.mu.Unlock()

	return nil
}

func (w *Watcher) scanCollect() ([]Event, error) {
	current := make(map[string]fileState)

	err := filepath.Walk(w.config.Root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if w.config.IgnoreHidden && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			for _, skip := range w.config.IgnoreDirs {
				if name == skip {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !w.matchExtension(path) {
			return nil
		}
		if w.config.IgnoreHidden && strings.HasPrefix(info.Name(), ".") {
			return nil
		}
		st := fileState{ModTime: info.ModTime(), Size: info.Size()}
		if w.config.UseHash {
			st.Hash, _ = fileHash(path)
		}
		current[path] = st
		return nil
	})
	if err != nil {
		return nil, err
	}

	var events []Event
	w.mu.RLock()
	old := w.state
	w.mu.RUnlock()

	now := time.Now()
	for path, st := range current {
		if _, ok := old[path]; !ok {
			events = append(events, Event{Type: EventCreate, Path: path, RelPath: w.relPath(path), Time: now, Size: st.Size})
		} else if w.changed(old[path], st) {
			events = append(events, Event{Type: EventModify, Path: path, RelPath: w.relPath(path), Time: now, Size: st.Size})
		}
	}
	for path := range old {
		if _, ok := current[path]; !ok {
			events = append(events, Event{Type: EventDelete, Path: path, RelPath: w.relPath(path), Time: now})
		}
	}

	w.mu.Lock()
	w.state = current
	w.mu.Unlock()

	return events, nil
}

func (w *Watcher) detectChanges(current map[string]fileState) {
	w.mu.RLock()
	old := w.state
	w.mu.RUnlock()

	now := time.Now()

	for path, st := range current {
		if _, ok := old[path]; !ok {
			w.queueEvent(Event{Type: EventCreate, Path: path, RelPath: w.relPath(path), Time: now, Size: st.Size})
		} else if w.changed(old[path], st) {
			w.queueEvent(Event{Type: EventModify, Path: path, RelPath: w.relPath(path), Time: now, Size: st.Size})
		}
	}

	for path := range old {
		if _, ok := current[path]; !ok {
			w.queueEvent(Event{Type: EventDelete, Path: path, RelPath: w.relPath(path), Time: now})
		}
	}
}

func (w *Watcher) changed(oldSt, newSt fileState) bool {
	if oldSt.ModTime != newSt.ModTime || oldSt.Size != newSt.Size {
		if w.config.UseHash {
			return oldSt.Hash != newSt.Hash
		}
		return true
	}
	return false
}

func (w *Watcher) queueEvent(e Event) {
	w.pendingMu.Lock()
	defer w.pendingMu.Unlock()
	w.pending[e.Path] = &e
}

func (w *Watcher) flushDebounced() {
	w.pendingMu.Lock()
	if len(w.pending) == 0 {
		w.pendingMu.Unlock()
		return
	}
	events := make([]Event, 0, len(w.pending))
	for _, e := range w.pending {
		events = append(events, *e)
	}
	w.pending = make(map[string]*Event)
	w.pendingMu.Unlock()

	w.mu.RLock()
	handlers := make([]Handler, len(w.handlers))
	copy(handlers, w.handlers)
	w.mu.RUnlock()

	for _, e := range events {
		for _, h := range handlers {
			h(e)
		}
	}
}

func (w *Watcher) matchExtension(path string) bool {
	if len(w.config.Extensions) == 0 {
		return true
	}
	ext := filepath.Ext(path)
	for _, e := range w.config.Extensions {
		if ext == e {
			return true
		}
	}
	return false
}

func (w *Watcher) relPath(path string) string {
	rel, err := filepath.Rel(w.config.Root, path)
	if err != nil {
		return path
	}
	return rel
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
