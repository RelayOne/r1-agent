package sessionctl

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Takeover is a single operator-driven handoff over a running agent session.
// At most one is active per TakeoverManager at a time. The agent PGID is
// SIGSTOPped on Request and SIGCONTed on Release; the captured PreCommit
// serves as the base for the diff summary emitted on Release.
type Takeover struct {
	ID          string
	SessionID   string
	StartedAt   time.Time
	MaxDuration time.Duration
	Signaler    Signaler
	PGID        int

	// PreCommit is captured git SHA at start for diff-on-release.
	PreCommit string

	// PTYPath is the PTY scratch file path for this takeover. Release
	// os.Removes it on a best-effort basis to keep /tmp clean.
	PTYPath string

	// ReleasedAt, Reason, DiffSummary populate after Release.
	ReleasedAt  time.Time
	Reason      string // "user" | "timeout" | "crash"
	DiffSummary string // e.g. "3 files changed, 12 insertions"

	mu     sync.Mutex
	done   chan struct{}
	closed bool
}

// TakeoverManager owns the single-slot state machine. The zero value is not
// usable; always construct via NewTakeoverManager.
type TakeoverManager struct {
	mu        sync.Mutex
	active    *Takeover // at most one per session at a time
	signaler  Signaler
	pgid      int
	sessionID string
	emit      func(kind string, payload any) string

	// Repo is the git working tree root; used for pre-takeover SHA capture
	// and post-takeover `git diff --stat` computation. If empty, or if the
	// commands fail, the DiffSummary is left empty and a single warning is
	// logged (Repo-level problems are operator-visible anyway).
	Repo string
}

// NewTakeoverManager builds a manager for the given session. pgid is the
// agent process group to pause/resume. emit may be nil; the manager tolerates
// it (mirrors sessionctl handler behavior).
func NewTakeoverManager(sessionID string, pgid int, sig Signaler, emit func(string, any) string, repo string) *TakeoverManager {
	return &TakeoverManager{
		sessionID: sessionID,
		pgid:      pgid,
		signaler:  sig,
		emit:      emit,
		Repo:      repo,
	}
}

// Active returns the currently active takeover, or nil. Safe for concurrent
// use; the returned pointer must not be mutated by callers.
func (tm *TakeoverManager) Active() *Takeover {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.active
}

// Request starts a takeover. Returns (takeover_id, pty_path, error). If a
// takeover is already active, returns error "takeover already active".
func (tm *TakeoverManager) Request(reason string, maxDur time.Duration) (string, string, error) {
	tm.mu.Lock()
	if tm.active != nil {
		tm.mu.Unlock()
		return "", "", fmt.Errorf("takeover already active")
	}
	if tm.signaler == nil {
		tm.mu.Unlock()
		return "", "", fmt.Errorf("signaler unavailable")
	}

	preCommit := capturePreCommit(tm.Repo)

	if err := tm.signaler.Pause(tm.pgid); err != nil {
		tm.mu.Unlock()
		return "", "", fmt.Errorf("pause: %w", err)
	}

	id := newTakeoverID()
	ptyPath := filepath.Join(os.TempDir(), "stoke-tko-"+id+".pts")
	// Touch the path so Release can os.Remove it unconditionally. The real
	// PTY allocation replaces this Create in the follow-on wiring task.
	if f, err := os.Create(ptyPath); err != nil {
		// Non-fatal: we have already paused the agent, and the file is a
		// cleanup handle, not a correctness primitive. Log once and carry on.
		log.Printf("sessionctl/takeover: create %s: %v", ptyPath, err)
	} else {
		_ = f.Close()
	}

	now := time.Now().UTC()
	t := &Takeover{
		ID:          id,
		SessionID:   tm.sessionID,
		StartedAt:   now,
		MaxDuration: maxDur,
		Signaler:    tm.signaler,
		PGID:        tm.pgid,
		PreCommit:   preCommit,
		PTYPath:     ptyPath,
		Reason:      reason,
		done:        make(chan struct{}),
	}
	tm.active = t
	tm.mu.Unlock()

	if maxDur > 0 {
		go tm.autoReleaseAfter(t, maxDur)
	}

	if tm.emit != nil {
		tm.emit("operator.takeover_start", map[string]any{
			"session_id":     tm.sessionID,
			"takeover_id":    id,
			"reason":         reason,
			"pty_path":       ptyPath,
			"max_duration_s": int(maxDur / time.Second),
			"actor":          "cli:socket",
		})
	}

	return id, ptyPath, nil
}

// Release ends the active takeover with the given ID. Idempotent in the
// sense that a second call with the same ID after a successful release will
// return an "unknown takeover_id" error (there is no longer anything active).
func (tm *TakeoverManager) Release(takeoverID string, reason string) (string, error) {
	tm.mu.Lock()
	if tm.active == nil || tm.active.ID != takeoverID {
		tm.mu.Unlock()
		return "", fmt.Errorf("unknown takeover_id: %s", takeoverID)
	}
	t := tm.active

	// Cancel the auto-release timer goroutine if still running.
	t.mu.Lock()
	if !t.closed {
		t.closed = true
		close(t.done)
	}
	t.mu.Unlock()

	// Resume before computing the diff so the agent is making forward
	// progress while we stat.
	resumeErr := tm.signaler.Resume(tm.pgid)
	diff := computeDiffStat(tm.Repo, t.PreCommit)
	releasedAt := time.Now().UTC()

	t.ReleasedAt = releasedAt
	t.Reason = reason
	t.DiffSummary = diff

	// Remove the PTY scratch file we touched in Request.
	if t.PTYPath != "" {
		_ = os.Remove(t.PTYPath)
	}

	tm.active = nil
	emit := tm.emit
	sessID := tm.sessionID
	tm.mu.Unlock()

	if emit != nil {
		emit("operator.takeover_end", map[string]any{
			"session_id":   sessID,
			"takeover_id":  takeoverID,
			"released_at":  releasedAt,
			"diff_summary": diff,
			"reason":       reason,
			"actor":        "cli:socket",
		})
	}

	if resumeErr != nil {
		return diff, fmt.Errorf("resume: %w", resumeErr)
	}
	return diff, nil
}

// autoReleaseAfter fires a timeout-triggered Release. If Release has already
// been called, the done channel is closed and the timer is a no-op.
func (tm *TakeoverManager) autoReleaseAfter(t *Takeover, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-t.done:
		return
	case <-timer.C:
		// Best-effort; if Release already ran concurrently, the ID check in
		// Release will return an error which we ignore.
		_, _ = tm.Release(t.ID, "timeout")
	}
}

// ---- helpers ---------------------------------------------------------------

// newTakeoverID returns a compact "tko_" + 16 hex chars identifier. Not a
// ULID, but adequate for logs and audit (unique within a session).
func newTakeoverID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Extremely unlikely; fall back to nanosecond timestamp.
		return fmt.Sprintf("tko_%x", time.Now().UnixNano())
	}
	return "tko_" + hex.EncodeToString(b[:])
}

// capturePreCommit runs `git -C repo rev-parse HEAD`. On any error (not a
// git repo, git missing, empty repo), returns "" and logs once. The caller
// degrades gracefully: an empty PreCommit means DiffSummary is also "".
func capturePreCommit(repo string) string {
	if repo == "" {
		return ""
	}
	cmd := exec.Command("git", "-C", repo, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		log.Printf("sessionctl/takeover: rev-parse HEAD in %s failed: %v", repo, err)
		return ""
	}
	return strings.TrimSpace(string(out))
}

// computeDiffStat runs `git -C repo diff --stat <sha>..HEAD`, returning the
// trimmed last line (e.g. "3 files changed, 12 insertions(+)"). Empty on
// error or when no base SHA is available.
func computeDiffStat(repo, preCommit string) string {
	if repo == "" || preCommit == "" {
		return ""
	}
	cmd := exec.Command("git", "-C", repo, "diff", "--stat", preCommit+"..HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return ""
	}
	// Last non-empty line of `git diff --stat` is the summary; earlier lines
	// are per-file entries. For a compact audit string we return just the
	// summary line.
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}
	return ""
}
