// Package sessionhub: SessionHub — the multi-session daemon's per-session
// registry (specs/r1d-server.md Phase D, items 21–27).
//
// # Why a hub at all
//
// `r1 serve` is a per-user daemon that hosts MANY concurrent sessions over
// loopback HTTP/WS. Each session has a workdir, a journal, an agent loop,
// and an event stream — but the process is shared, so the only thing that
// keeps sessions from cross-contaminating is the discipline encoded in this
// hub: every public API takes a session id, every dispatched goroutine
// runs `assertCwd` (sentinel.go) before touching the filesystem, and
// workdir validation rejects any path that could let one session step on
// another's state (item 21 — see `validateWorkdir` below).
//
// # Workdir validation rules (spec §11.21)
//
// Create() refuses a workdir that is:
//
//   - not absolute (relative paths are ambiguous after a chdir somewhere
//     else in the process — fail-closed)
//   - non-existent (we don't create it; the caller proves the workspace
//     exists before asking us to host it)
//   - not a directory (an existing file with the same name is hostile)
//   - not writable (a read-only workdir means tool calls will silently
//     fail later; reject up front so the operator gets one error, not
//     N tool errors)
//   - under `~/.r1/` (the daemon's own state dir — a session whose
//     workdir overlaps the discovery file or another session's journal
//     would let a malicious or buggy tool corrupt the daemon's own
//     bookkeeping; absolutely fail-closed)
//
// All five checks return `ErrInvalidWorkdir` wrapping a precise reason so
// the WS handler can echo the reason back to the client without leaking
// filesystem layout to a non-loopback peer (the ws layer does the
// stringification).
package sessionhub

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrInvalidWorkdir wraps every workdir-validation failure. Callers can
// `errors.Is(err, ErrInvalidWorkdir)` to distinguish a workdir-rejection
// from real I/O errors. The wrapped message names the offending rule
// (e.g. "not absolute", "under ~/.r1/").
var ErrInvalidWorkdir = errors.New("sessionhub: invalid workdir")

// ErrSessionNotFound is returned by Get/Delete when the session id is
// unknown. Sentinel error so callers can map to a 404.
var ErrSessionNotFound = errors.New("sessionhub: session not found")

// ErrSessionExists is returned by Create when a session with the same
// ID is already registered. The hub mints unique IDs internally; the
// only way to hit this is a caller-supplied collision (test scaffolds).
var ErrSessionExists = errors.New("sessionhub: session already exists")

// SessionHub is the per-daemon registry of live sessions. Concurrency-
// safe via sync.Map (the hub's hot path is Get during WS dispatch,
// where lock-free reads matter most).
//
// The hub does NOT own the agent loop — `Session.Run` does. The hub
// only tracks lifecycle metadata (id ↔ *Session) so the WS handler can
// look up the right session for a given JSON-RPC call.
type SessionHub struct {
	// sessions is keyed by Session.ID -> *Session. We use sync.Map so
	// that the high-frequency Get path (every WS frame) never contends
	// against the slower Create / Delete operations.
	sessions sync.Map

	// r1Dir is the absolute path of `~/.r1/` resolved at hub
	// construction time. Captured once so workdir-validation can check
	// "is workdir under r1Dir" without re-resolving the home dir on
	// every Create call. Tests inject a sandbox via `R1_HOME`.
	r1Dir string

	// idMu serializes ID generation so the monotonic counter stays
	// race-free under concurrent Create calls.
	idMu  sync.Mutex
	idSeq uint64

	// index is the optional `~/.r1/sessions-index.json` writer. When
	// set (via SetSessionsIndex), Create appends an entry and Delete
	// marks the entry deleted, so daemon-restart replay (TASK-27) can
	// reconstruct the session list. nil = no index updates (used by
	// unit tests that don't care about persistence).
	index *SessionsIndex

	// journalDir is the absolute directory under which Create allocates
	// per-session journal paths. Empty = no journal-path allocation;
	// the IndexEntry then records an empty JournalPath, which the
	// index manager will reject. Set via SetJournalDir.
	journalDir string
}

// NewHub constructs a SessionHub. Resolves `~/.r1/` once and stashes
// it for the workdir-overlap check. Returns an error if the home dir
// cannot be resolved (extremely rare; should only happen in
// chroot/jail environments missing $HOME and a passwd entry).
func NewHub() (*SessionHub, error) {
	dir, err := resolveR1Dir()
	if err != nil {
		return nil, fmt.Errorf("sessionhub: resolve r1 dir: %w", err)
	}
	return &SessionHub{r1Dir: dir}, nil
}

// resolveR1Dir mirrors daemondisco.r1Dir — duplicated here rather than
// imported to avoid a coupling that would force daemondisco to depend
// on sessionhub for tests. R1_HOME wins for sandboxes.
func resolveR1Dir() (string, error) {
	if home := os.Getenv("R1_HOME"); home != "" {
		return filepath.Clean(home), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".r1"), nil
}

// CreateOptions are the construction parameters for a new session.
// We accept a struct (rather than positional args) so adding fields
// later doesn't break callers. All fields are required unless
// documented otherwise.
type CreateOptions struct {
	// Workdir is the absolute path the session will operate against.
	// Validated by validateWorkdir; see package doc for rules.
	Workdir string

	// Model is the provider model id (e.g. "claude-sonnet-4-5-20250929").
	// Empty is permitted for tests that drive a mock provider; production
	// callers should always pass a non-empty value.
	Model string

	// ID is a caller-supplied session id. When empty, the hub mints one
	// (preferred). Tests pass an explicit id for deterministic logging.
	ID string
}

// SetSessionsIndex installs the `~/.r1/sessions-index.json` writer.
// Subsequent Create / Delete calls update the index atomically; a
// nil argument disables index updates.
func (h *SessionHub) SetSessionsIndex(idx *SessionsIndex) {
	h.index = idx
}

// SetJournalDir sets the directory under which per-session journal
// files live. Create derives `<journalDir>/<id>.jsonl` and stamps it
// onto the IndexEntry. Empty = no journal-path allocation.
func (h *SessionHub) SetJournalDir(dir string) {
	h.journalDir = dir
}

// Create registers a new session after validating its workdir. The
// returned *Session is registered in the hub's sync.Map and reachable
// via Get(id). The Session is NOT started — the caller invokes
// `s.Run(ctx)` when ready.
//
// Workdir validation runs first (spec §11.21). On any failure, no
// session is registered and ErrInvalidWorkdir is returned wrapping the
// specific reason.
//
// When SetSessionsIndex was called, an entry is appended to
// `sessions-index.json` AFTER the in-memory registration succeeds. An
// index-write error rolls back the in-memory registration so the
// daemon's on-disk and in-memory views stay consistent.
func (h *SessionHub) Create(opts CreateOptions) (*Session, error) {
	if err := h.validateWorkdir(opts.Workdir); err != nil {
		return nil, err
	}
	id := opts.ID
	if id == "" {
		id = h.mintID()
	}
	if _, loaded := h.sessions.Load(id); loaded {
		return nil, fmt.Errorf("%w: id=%s", ErrSessionExists, id)
	}
	abs, _ := filepath.Abs(opts.Workdir)
	s := newSession(id, filepath.Clean(abs), opts.Model)
	// LoadOrStore guards against a goroutine race: two Creates with the
	// same caller-supplied id should land deterministically on
	// ErrSessionExists, not silently overwrite.
	if _, loaded := h.sessions.LoadOrStore(id, s); loaded {
		return nil, fmt.Errorf("%w: id=%s", ErrSessionExists, id)
	}
	// Best-effort index append. On failure roll back the in-memory
	// entry so the caller sees one error and Create is observably
	// transactional.
	if h.index != nil {
		journalPath := h.journalPathFor(id)
		entry := IndexEntry{
			ID:          id,
			Workdir:     s.SessionRoot,
			Model:       opts.Model,
			JournalPath: journalPath,
		}
		if err := h.index.Append(entry); err != nil {
			h.sessions.Delete(id)
			return nil, fmt.Errorf("sessionhub: index append: %w", err)
		}
	}
	return s, nil
}

// journalPathFor returns the per-session journal path. Empty when no
// journal directory was configured; the index manager rejects an
// empty JournalPath, so callers without a journal dir must also leave
// the SessionsIndex unset.
func (h *SessionHub) journalPathFor(id string) string {
	if h.journalDir == "" {
		return ""
	}
	return filepath.Join(h.journalDir, id+".jsonl")
}

// Get returns the session with the given id, or ErrSessionNotFound.
func (h *SessionHub) Get(id string) (*Session, error) {
	v, ok := h.sessions.Load(id)
	if !ok {
		return nil, fmt.Errorf("%w: id=%s", ErrSessionNotFound, id)
	}
	s, ok := v.(*Session)
	if !ok {
		// Should be impossible — the map only ever stores *Session.
		// Treat as not-found so callers don't crash.
		return nil, fmt.Errorf("%w: id=%s (type assertion)", ErrSessionNotFound, id)
	}
	return s, nil
}

// Delete removes a session from the hub. Returns ErrSessionNotFound if
// the id is unknown. The session's cancel func (if any) is invoked so
// any in-flight Run goroutine winds down before the caller proceeds.
//
// When SetSessionsIndex was called, the index entry is mark-deleted
// atomically. An index-write error is surfaced to the caller; the
// in-memory deletion still happens (we cannot un-cancel a Run, and
// keeping the in-memory entry while the user thinks Delete failed
// would be more confusing than an "in-memory deleted, on-disk
// inconsistent" error message).
func (h *SessionHub) Delete(id string) error {
	v, ok := h.sessions.LoadAndDelete(id)
	if !ok {
		return fmt.Errorf("%w: id=%s", ErrSessionNotFound, id)
	}
	if s, ok := v.(*Session); ok {
		s.cancelRun()
	}
	if h.index != nil {
		if err := h.index.MarkDeleted(id); err != nil {
			return fmt.Errorf("sessionhub: index mark-deleted: %w", err)
		}
	}
	return nil
}

// List returns a snapshot of all registered sessions. Order is
// unspecified (sync.Map iteration is unsorted); callers that need a
// stable order must sort by ID themselves.
func (h *SessionHub) List() []*Session {
	var out []*Session
	h.sessions.Range(func(_, v any) bool {
		if s, ok := v.(*Session); ok {
			out = append(out, s)
		}
		return true
	})
	return out
}

// mintID returns a per-hub monotonic id of the form `s-NNN`. We
// deliberately do NOT use crypto/rand here because the id appears in
// log lines and is intended to be operator-readable; the only
// uniqueness requirement is "no collision within one daemon's
// lifetime", which a counter satisfies cheaply.
func (h *SessionHub) mintID() string {
	h.idMu.Lock()
	defer h.idMu.Unlock()
	h.idSeq++
	return fmt.Sprintf("s-%d", h.idSeq)
}

// validateWorkdir enforces the spec §11.21 rules. Each branch returns
// an ErrInvalidWorkdir-wrapped error whose message names the failing
// rule, so the caller (and ultimately the WS error response) can be
// specific without exposing filesystem layout.
//
//nolint:gocyclo // straight-line guard sequence — splitting it would obscure the rule list.
func (h *SessionHub) validateWorkdir(workdir string) error {
	if workdir == "" {
		return fmt.Errorf("%w: empty path", ErrInvalidWorkdir)
	}
	// Rule 1: must be absolute.
	if !filepath.IsAbs(workdir) {
		return fmt.Errorf("%w: not absolute: %s", ErrInvalidWorkdir, workdir)
	}
	// Normalize for the remaining checks. We use Clean (not EvalSymlinks)
	// because EvalSymlinks can hang on a stale NFS mount and the validator
	// must remain cheap and bounded.
	clean := filepath.Clean(workdir)
	// Rule 2: must exist.
	info, err := os.Stat(clean)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: does not exist: %s", ErrInvalidWorkdir, clean)
		}
		return fmt.Errorf("%w: stat: %s: %v", ErrInvalidWorkdir, clean, err)
	}
	// Rule 3: must be a directory (not a regular file or socket).
	if !info.IsDir() {
		return fmt.Errorf("%w: not a directory: %s", ErrInvalidWorkdir, clean)
	}
	// Rule 4: must be writable. We probe by attempting to create a
	// temp file rather than trusting mode bits — ACLs, mount-options
	// (read-only mount), and effective-uid all defeat a mode check.
	if err := h.probeWritable(clean); err != nil {
		return fmt.Errorf("%w: not writable: %s: %v", ErrInvalidWorkdir, clean, err)
	}
	// Rule 5: must not be inside `~/.r1/`. The test treats r1Dir and
	// `clean` as path-prefixes; we also reject when they're EQUAL
	// (workdir == r1Dir would let tool calls overwrite daemon.json).
	if h.r1Dir != "" {
		r1 := filepath.Clean(h.r1Dir)
		// Normalize separator so the contains-check works under Windows
		// path-list semantics. Append a separator to r1 to avoid the
		// "/foo/.r1bar" false positive.
		r1WithSep := r1 + string(filepath.Separator)
		if clean == r1 || strings.HasPrefix(clean, r1WithSep) {
			return fmt.Errorf("%w: under ~/.r1/: %s", ErrInvalidWorkdir, clean)
		}
	}
	return nil
}

// probeWritable attempts to create (and immediately remove) a hidden
// temp file in `dir`. Successful creation proves write access for the
// effective uid/gid under the actual mount-options. Failure surfaces
// the exact filesystem error so the caller error is actionable.
func (h *SessionHub) probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".r1-workdir-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}
