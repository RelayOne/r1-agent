// Package session — signature.go
//
// Session signature file for the r1-server visual execution trace
// server (spec r1-server RS-1). Every running Stoke instance writes
// a JSON sidecar at <repo>/.stoke/r1.session.json advertising
// itself: pid, instance_id, started_at, repo_root, mode, sow_name,
// model, status, stream_file, ledger_dir, checkpoint_file, bus_wal,
// updated_at. A background heartbeat goroutine refreshes
// updated_at every 30s while the process lives.
//
// r1-server (a separate binary, one per machine) discovers
// instances by scanning for r1.session.json files. Stoke also
// attempts a best-effort POST /api/register to r1-server on
// startup; if r1-server is not running, the POST fails silently
// within 1s and Stoke continues normally. r1-server is strictly
// optional — Stoke works without it.
//
// The signature write is atomic via tmp+rename. The heartbeat
// goroutine stops on Close(). Close() also sets status to
// "completed" or "failed" based on the exit path.
package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SignatureVersion is the current schema version for r1.session.json.
// Consumers (r1-server) read this to handle forward compatibility.
const SignatureVersion = "1.0"

// HeartbeatInterval is how often the background goroutine refreshes
// updated_at. r1-server considers a session stale if updated_at is
// more than 3× this value behind wall clock and the PID is dead.
const HeartbeatInterval = 30 * time.Second

// RegisterTimeout is the hard cap on the POST /api/register call to
// r1-server. Missed because r1-server is down is an expected, silent
// outcome — this is a best-effort heads-up, not a required IPC.
const RegisterTimeout = 1 * time.Second

// SignatureConfig carries the static fields a caller supplies at
// WriteSignature time. The Signature value populates remaining
// fields (pid, instance_id, started_at, updated_at, status).
type SignatureConfig struct {
	// Mode identifies the entry point: "sow", "chat", "ship",
	// "run", etc. Used by the UI to group sessions.
	Mode string

	// SowName, if the mode involves a SOW, is the SOW file name
	// or its human-readable title. Empty for non-SOW modes.
	SowName string

	// Model is the primary model name (e.g., "claude-4.5-sonnet").
	Model string

	// StreamFile, if non-empty, is the absolute path to the
	// stream.jsonl being written by this instance. r1-server
	// tails this file via fsnotify to surface live events.
	StreamFile string

	// LedgerDir, if non-empty, is the absolute path to the
	// .stoke/ledger directory. r1-server reads nodes/edges
	// under here to render the DAG.
	LedgerDir string

	// CheckpointFile, if non-empty, is the absolute path to
	// .stoke/checkpoints/timeline.jsonl.
	CheckpointFile string

	// BusWAL, if non-empty, is the absolute path to the bus
	// WAL at .stoke/bus/events.log.
	BusWAL string

	// RegisterURL, if non-empty, overrides the default
	// http://localhost:3948/api/register for tests. Leave empty
	// in production.
	RegisterURL string
}

// SignatureFile is the on-disk JSON shape at <repo>/.stoke/r1.session.json.
// Fields are tagged so consumers can unmarshal without importing Stoke.
type SignatureFile struct {
	Version        string    `json:"version"`
	PID            int       `json:"pid"`
	InstanceID     string    `json:"instance_id"`
	StartedAt      time.Time `json:"started_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	RepoRoot       string    `json:"repo_root"`
	Mode           string    `json:"mode"`
	SowName        string    `json:"sow_name,omitempty"`
	Model          string    `json:"model,omitempty"`
	Status         string    `json:"status"`
	StreamFile     string    `json:"stream_file,omitempty"`
	LedgerDir      string    `json:"ledger_dir,omitempty"`
	CheckpointFile string    `json:"checkpoint_file,omitempty"`
	BusWAL         string    `json:"bus_wal,omitempty"`
}

// Signature is the live handle returned by WriteSignature. The caller
// must invoke Close on exit to stop the heartbeat and mark the final
// status. Safe for concurrent Close calls.
type Signature struct {
	path string

	mu   sync.Mutex
	data SignatureFile
	// done is closed by Close to signal the heartbeat to exit.
	done chan struct{}
	// closed protects against double-close panic on done.
	closed bool

	// registerURL is the POST endpoint (empty => default).
	registerURL string
}

// NewInstanceID produces a short, random instance ID of the form
// "r1-<8-hex-chars>". Collisions are astronomically unlikely for any
// realistic load; consumers treat this as opaque.
func NewInstanceID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing means the OS has no entropy source —
		// fall back to time-based bits so WriteSignature still works.
		now := time.Now().UnixNano()
		for i := 0; i < 4; i++ {
			b[i] = byte(now >> (i * 8))
		}
	}
	return "r1-" + hex.EncodeToString(b[:])
}

// signaturePath returns <repoRoot>/.stoke/r1.session.json.
func signaturePath(repoRoot string) string {
	return filepath.Join(repoRoot, ".stoke", "r1.session.json")
}

// WriteSignature writes the r1.session.json file for the running
// Stoke process and starts the heartbeat goroutine. Callers must
// defer (*Signature).Close() to stop the heartbeat and set the
// final status.
//
// If repoRoot does not exist or is not writable, WriteSignature
// returns an error and no goroutine is started. The caller should
// treat this as a non-fatal warning: Stoke runs fine without the
// signature file, it just means r1-server won't discover the
// instance until next run.
func WriteSignature(repoRoot string, cfg SignatureConfig) (*Signature, error) {
	if repoRoot == "" {
		return nil, errors.New("session: repo root is empty")
	}
	now := time.Now().UTC()
	sig := &Signature{
		path: signaturePath(repoRoot),
		data: SignatureFile{
			Version:        SignatureVersion,
			PID:            os.Getpid(),
			InstanceID:     NewInstanceID(),
			StartedAt:      now,
			UpdatedAt:      now,
			RepoRoot:       repoRoot,
			Mode:           cfg.Mode,
			SowName:        cfg.SowName,
			Model:          cfg.Model,
			Status:         "running",
			StreamFile:     cfg.StreamFile,
			LedgerDir:      cfg.LedgerDir,
			CheckpointFile: cfg.CheckpointFile,
			BusWAL:         cfg.BusWAL,
		},
		done:        make(chan struct{}),
		registerURL: cfg.RegisterURL,
	}

	if err := sig.writeAtomic(); err != nil {
		return nil, err
	}

	go sig.heartbeat()
	go sig.registerWithServer()

	return sig, nil
}

// writeAtomic renders the current signature data to disk via
// tmp+rename. Callers must hold sig.mu.
//
// Concurrency note: writeAtomic ONLY reads sig.data under the
// caller's lock; it does not acquire sig.mu itself.
func (s *Signature) writeAtomic() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("signature: mkdir %s: %w", dir, err)
	}
	buf, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("signature: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "r1.session.*.tmp")
	if err != nil {
		return fmt.Errorf("signature: tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("signature: write tmp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("signature: sync tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("signature: close tmp: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("signature: rename: %w", err)
	}
	return nil
}

// heartbeat runs in its own goroutine, updating updated_at every
// HeartbeatInterval until Close is called. Write errors are
// swallowed — the signature is a best-effort discovery aid, not a
// critical-path write.
func (s *Signature) heartbeat() {
	t := time.NewTicker(HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			s.mu.Lock()
			s.data.UpdatedAt = time.Now().UTC()
			_ = s.writeAtomic()
			s.mu.Unlock()
		}
	}
}

// registerWithServer posts the current signature to r1-server's
// /api/register endpoint. Silent on connection refused (r1-server
// not running is the common case and is explicitly OK).
//
// Timeout is RegisterTimeout (1s). r1-server is expected to be
// on localhost — a slower response indicates it's wedged and we
// don't want to block Stoke startup on it.
func (s *Signature) registerWithServer() {
	s.mu.Lock()
	body, err := json.Marshal(s.data)
	url := s.registerURL
	s.mu.Unlock()
	if err != nil {
		return
	}
	if url == "" {
		url = "http://localhost:3948/api/register"
	}

	ctx, cancel := context.WithTimeout(context.Background(), RegisterTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: RegisterTimeout}
	resp, err := client.Do(req)
	if err != nil {
		// r1-server down, DNS hiccup, etc. — silent by design.
		return
	}
	_ = resp.Body.Close()
}

// Close stops the heartbeat, writes the final signature with the
// given terminal status, and returns. status should be "completed"
// on clean exit paths or "failed" on error paths. Any other string
// is accepted verbatim; callers are encouraged to use the constants.
//
// Close is idempotent — calling it twice is safe. The second call
// is a no-op.
func (s *Signature) Close(status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.done)

	// Normalize empty status to "completed" — callers should set
	// explicitly on error paths but we guard against omission.
	status = strings.TrimSpace(status)
	if status == "" {
		status = "completed"
	}
	s.data.Status = status
	s.data.UpdatedAt = time.Now().UTC()
	return s.writeAtomic()
}

// Path returns the absolute path of the signature file. Useful for
// tests and logging.
func (s *Signature) Path() string {
	return s.path
}

// InstanceID returns the instance ID assigned at WriteSignature
// time. Stable for the life of the Signature.
func (s *Signature) InstanceID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.InstanceID
}

// Snapshot returns a copy of the current signature data. Useful for
// tests and for callers that want to emit the instance_id into
// their own events.
func (s *Signature) Snapshot() SignatureFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data
}

// LoadSignature reads an existing r1.session.json file into a
// SignatureFile. Used by r1-server's scanner and by tests.
func LoadSignature(path string) (SignatureFile, error) {
	var sf SignatureFile
	buf, err := os.ReadFile(path)
	if err != nil {
		return sf, err
	}
	if err := json.Unmarshal(buf, &sf); err != nil {
		return sf, fmt.Errorf("signature: parse %s: %w", path, err)
	}
	return sf, nil
}
