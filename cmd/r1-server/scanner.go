// Package main — scanner.go
//
// RS-3: instance scanner + event tailer + ledger loader.
//
// Discovery is polling-only to stay within the stdlib (no fsnotify
// dependency). A single background goroutine walks the configured
// search roots every scan_interval looking for r1.session.json
// sidecars. For each discovered session, a per-session tailer
// goroutine polls the stream file at tail_interval and appends new
// NDJSON lines into session_events. A liveness sweep on each scan
// flips dead-PID sessions to status=crashed.
//
// The scanner lifetime is bound to a context. Start spawns all
// goroutines; cancelling the context drains them before returning
// from Wait.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/RelayOne/r1/internal/session"
)

// ScannerConfig controls scanner timing + search paths. Zero values
// fall through to defaults via sane helpers.
type ScannerConfig struct {
	// ScanInterval is how often the filesystem walker re-scans the
	// search roots. Default: 60s (per RS-3 spec item 9).
	ScanInterval time.Duration
	// TailInterval is how often each per-session tailer polls its
	// stream file for new content. Default: 500ms — low enough that
	// the UI feels live, high enough that a dozen idle sessions
	// don't pin a core.
	TailInterval time.Duration
	// Roots are the directories to walk looking for .stoke/r1.session.json.
	// When empty, scannerDefaultRoots() is used. Populated by tests
	// with t.TempDir() to stay out of the real $HOME.
	Roots []string
	// Logger receives scanner lifecycle + discovery events. nil is
	// replaced with slog.Default().
	Logger *slog.Logger
}

// sessionSkipDirs is the set of directory names we never descend
// into during a scan — keeps us out of giant vendor trees.
var sessionSkipDirs = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"vendor":       {},
	"target":       {},
	".cache":       {},
	"dist":         {},
	"build":        {},
}

// scannerDefaultRoots returns the search roots when the caller does
// not override ScannerConfig.Roots. Missing dirs are fine — WalkDir
// will just skip them.
func scannerDefaultRoots() []string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return nil
	}
	return []string{
		home,
		filepath.Join(home, "code"),
		filepath.Join(home, "projects"),
		filepath.Join(home, "dev"),
		filepath.Join(home, "repos"),
		filepath.Join(home, "src"),
		filepath.Join(home, "work"),
	}
}

// Scanner is the RS-3 background worker. Not safe for concurrent
// Start calls; one instance per r1-server process is the only
// supported topology.
type Scanner struct {
	db     *DB
	cfg    ScannerConfig
	logger *slog.Logger

	mu      sync.Mutex
	tailers map[string]*tailState // keyed by instance_id
	wg      sync.WaitGroup
}

// NewScanner returns a Scanner wired to db + cfg. Defaults are
// applied here so the caller can stay oblivious to them.
func NewScanner(db *DB, cfg ScannerConfig) *Scanner {
	if cfg.ScanInterval <= 0 {
		cfg.ScanInterval = 60 * time.Second
	}
	if cfg.TailInterval <= 0 {
		cfg.TailInterval = 500 * time.Millisecond
	}
	if len(cfg.Roots) == 0 {
		cfg.Roots = scannerDefaultRoots()
	}
	lg := cfg.Logger
	if lg == nil {
		lg = slog.Default()
	}
	return &Scanner{db: db, cfg: cfg, logger: lg, tailers: map[string]*tailState{}}
}

// Start launches the scan + tailer goroutines. Returns immediately.
// Call Wait after cancelling ctx to drain workers.
func (s *Scanner) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.scanLoop(ctx)
	}()
}

// Wait blocks until all scanner goroutines have returned. Safe to
// call after Start even if no context cancellation has happened yet
// — Wait will block indefinitely in that case.
func (s *Scanner) Wait() {
	s.wg.Wait()
}

// scanLoop runs one scan immediately, then every ScanInterval.
func (s *Scanner) scanLoop(ctx context.Context) {
	s.scanOnce(ctx)
	t := time.NewTicker(s.cfg.ScanInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.scanOnce(ctx)
		}
	}
}

// scanOnce executes one full scan: filesystem walk + liveness sweep
// + tailer reconciliation. Errors are logged but never fatal — a
// missing $HOME/code dir is normal on many machines.
func (s *Scanner) scanOnce(ctx context.Context) {
	start := time.Now()
	discovered := 0
	for _, root := range s.cfg.Roots {
		discovered += s.walkRoot(ctx, root)
		if ctx.Err() != nil {
			return
		}
	}
	// Liveness sweep.
	crashed := s.liveness(ctx)

	// Reconcile tailers against current sessions table.
	s.reconcileTailers(ctx)

	s.logger.Debug("scan pass",
		"roots", len(s.cfg.Roots),
		"discovered", discovered,
		"marked_crashed", crashed,
		"dur_ms", time.Since(start).Milliseconds(),
	)
}

// walkRoot scans one search root for r1.session.json files, parsing
// and upserting each into the DB. Returns the count of files found
// (useful for debug logging). Silently skips dirs that don't exist.
func (s *Scanner) walkRoot(ctx context.Context, root string) int {
	if root == "" {
		return 0
	}
	found := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if err != nil {
			// Permission-denied etc. — skip this branch.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if _, skip := sessionSkipDirs[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "r1.session.json" {
			return nil
		}
		if err := s.ingestSignatureFile(path); err != nil {
			s.logger.Warn("ingest signature", "path", path, "err", err)
			return nil
		}
		found++
		return nil
	})
	return found
}

// ingestSignatureFile reads one r1.session.json and upserts the row.
// Also loads its ledger into the DB on first sight.
func (s *Scanner) ingestSignatureFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var sig session.SignatureFile
	if err := json.Unmarshal(raw, &sig); err != nil {
		return err
	}
	if sig.InstanceID == "" {
		return errors.New("empty instance_id")
	}
	if err := s.db.UpsertSession(sig); err != nil {
		return err
	}
	// Walk the ledger dir on every ingest — cheap (usually a few
	// files) and covers the "session added nodes after registration"
	// case without a separate fsnotify-style watcher.
	if sig.LedgerDir != "" {
		s.loadLedger(sig.InstanceID, sig.LedgerDir)
	}
	return nil
}

// loadLedger ingests every node + edge JSON file under ledgerDir into
// the ledger_nodes / ledger_edges tables. Missing dirs and malformed
// JSON are logged, never fatal.
func (s *Scanner) loadLedger(instanceID, ledgerDir string) {
	s.loadLedgerSide(instanceID, filepath.Join(ledgerDir, "nodes"), true)
	s.loadLedgerSide(instanceID, filepath.Join(ledgerDir, "edges"), false)
}

func (s *Scanner) loadLedgerSide(instanceID, dir string, isNode bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(full)
		if err != nil {
			s.logger.Warn("read ledger file", "path", full, "err", err)
			continue
		}
		if isNode {
			var n ledgerNodePayload
			if err := json.Unmarshal(raw, &n); err != nil {
				s.logger.Warn("parse ledger node", "path", full, "err", err)
				continue
			}
			id := n.ID
			if id == "" {
				id = strings.TrimSuffix(e.Name(), ".json")
			}
			if err := s.db.UpsertLedgerNode(
				instanceID, id, n.Type, n.MissionID,
				n.CreatedAt, n.CreatedBy, n.ParentHash, raw,
			); err != nil {
				s.logger.Warn("upsert ledger node", "id", id, "err", err)
			}
			continue
		}
		var edge ledgerEdgePayload
		if err := json.Unmarshal(raw, &edge); err != nil {
			s.logger.Warn("parse ledger edge", "path", full, "err", err)
			continue
		}
		id := edge.ID
		if id == "" {
			// filename-derived ID keeps the <from>-<to>-<type>.json
			// convention from the spec intact when no explicit id is
			// in the payload.
			id = strings.TrimSuffix(e.Name(), ".json")
		}
		if err := s.db.UpsertLedgerEdge(
			instanceID, id, edge.From, edge.To, edge.Type, raw,
		); err != nil {
			s.logger.Warn("upsert ledger edge", "id", id, "err", err)
		}
	}
}

// ledgerNodePayload + ledgerEdgePayload are minimal projections of
// the on-disk ledger JSON — we only extract the indexed columns;
// the full raw bytes are stored too so the UI can read any field.
type ledgerNodePayload struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	MissionID  string `json:"mission_id"`
	CreatedAt  string `json:"created_at"`
	CreatedBy  string `json:"created_by"`
	ParentHash string `json:"parent_hash"`
}

type ledgerEdgePayload struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"`
}

// liveness checks each "running" session's PID. When the process is
// gone, the row is flipped to status=crashed. Returns the count of
// crashes detected (for scan-pass logging).
func (s *Scanner) liveness(ctx context.Context) int {
	rows, err := s.db.ListSessions("running")
	if err != nil {
		s.logger.Warn("liveness list", "err", err)
		return 0
	}
	n := 0
	for _, r := range rows {
		if ctx.Err() != nil {
			return n
		}
		if r.PID <= 0 {
			continue
		}
		if processAlive(r.PID) {
			continue
		}
		if err := s.db.MarkSessionCrashed(r.InstanceID, time.Now().UTC()); err != nil {
			s.logger.Warn("mark crashed", "instance_id", r.InstanceID, "err", err)
			continue
		}
		s.logger.Info("session marked crashed", "instance_id", r.InstanceID, "pid", r.PID)
		n++
	}
	return n
}

// processAlive probes PID with signal 0 (POSIX) to learn if it's
// still around. Returns false on any error — a dead process and a
// permission error both count as "not ours to tail."
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		p, err := os.FindProcess(pid)
		if err != nil {
			return false
		}
		// Windows FindProcess never errors; best-effort via Signal.
		return p.Signal(syscall.Signal(0)) == nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return true
}

// reconcileTailers starts tailers for sessions we don't already have
// one for, and stops tailers for sessions that have reached a
// terminal state (completed/failed/crashed).
func (s *Scanner) reconcileTailers(ctx context.Context) {
	rows, err := s.db.ListSessions("")
	if err != nil {
		s.logger.Warn("reconcile list", "err", err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	active := map[string]struct{}{}
	for _, r := range rows {
		active[r.InstanceID] = struct{}{}
		// Terminal statuses: no need to tail, stop if running.
		if r.Status != "running" && r.Status != "paused" {
			if ts, ok := s.tailers[r.InstanceID]; ok {
				ts.stop()
				delete(s.tailers, r.InstanceID)
			}
			continue
		}
		if _, ok := s.tailers[r.InstanceID]; ok {
			continue
		}
		if r.StreamFile == "" {
			continue
		}
		ts := newTailState(r.InstanceID, r.StreamFile, s.db, s.cfg.TailInterval, s.logger)
		s.tailers[r.InstanceID] = ts
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ts.run(ctx)
		}()
	}

	// Stop tailers for any session that has vanished from the table.
	for id, ts := range s.tailers {
		if _, live := active[id]; !live {
			ts.stop()
			delete(s.tailers, id)
		}
	}
}

// tailState tracks the per-session stream tailer: current offset,
// stop signal, file path, and DB handle for appending events.
type tailState struct {
	instanceID string
	path       string
	db         *DB
	interval   time.Duration
	logger     *slog.Logger

	stopCh   chan struct{}
	stopOnce sync.Once

	offset int64
}

func newTailState(id, path string, db *DB, interval time.Duration, logger *slog.Logger) *tailState {
	return &tailState{
		instanceID: id, path: path, db: db,
		interval: interval, logger: logger,
		stopCh: make(chan struct{}),
	}
}

func (t *tailState) stop() {
	t.stopOnce.Do(func() { close(t.stopCh) })
}

// run polls the stream file for new content and appends each
// complete NDJSON line into session_events. Partial trailing lines
// are held until the next poll.
func (t *tailState) run(ctx context.Context) {
	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.stopCh:
			return
		case <-ticker.C:
			t.pollOnce()
		}
	}
}

func (t *tailState) pollOnce() {
	info, err := os.Stat(t.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			t.logger.Debug("tail stat", "path", t.path, "err", err)
		}
		return
	}
	if info.Size() <= t.offset {
		// If the file shrank (rotation), reset to start and reread.
		if info.Size() < t.offset {
			t.offset = 0
		}
		return
	}
	f, err := os.Open(t.path)
	if err != nil {
		t.logger.Debug("tail open", "path", t.path, "err", err)
		return
	}
	defer f.Close()
	if _, err := f.Seek(t.offset, 0); err != nil {
		t.logger.Debug("tail seek", "path", t.path, "err", err)
		return
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var read int64
	for sc.Scan() {
		line := sc.Bytes()
		read += int64(len(line)) + 1 // +1 for the newline
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			continue
		}
		eventType, ts := extractEventMeta(line)
		if err := t.db.InsertEvent(t.instanceID, eventType, []byte(trimmed), ts); err != nil {
			t.logger.Warn("insert event", "instance_id", t.instanceID, "err", err)
		}
	}
	if err := sc.Err(); err != nil {
		t.logger.Debug("tail scan", "path", t.path, "err", err)
	}
	t.offset += read
}

// extractEventMeta pulls the "type" and "ts" fields out of an event
// line for indexing. Missing fields default to "" and time.Now().
func extractEventMeta(line []byte) (string, time.Time) {
	var meta struct {
		Type string `json:"type"`
		Ts   string `json:"ts"`
	}
	if err := json.Unmarshal(line, &meta); err != nil {
		return "", time.Now().UTC()
	}
	ts, err := time.Parse(time.RFC3339Nano, meta.Ts)
	if err != nil {
		ts = time.Now().UTC()
	}
	return meta.Type, ts
}
