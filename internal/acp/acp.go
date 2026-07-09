// Package acp is R1's git-native coordination primitive.
//
// A shared git repository is the coordination substrate: a single LOCK.md at the
// repo root carries a monotone version + current holder, and every state
// transition (acquire / release / dispatch / receipt) is a git commit. `git
// push` is the compare-and-swap — a push built on a stale view is rejected
// (non-fast-forward) by the shared remote, so the coordinator fetches the winner,
// re-reads, and retries. Because the remote serializes pushes, versions are
// globally monotone and exactly one contender acquires a free lock.
//
// It cooperates with (does not replace) higher-level scheduling/collision rules:
// it is a mechanism, not a policy.
package acp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Status values for the lock.
const (
	StatusFree = "free"
	StatusHeld = "held"
)

// Op labels the last transition recorded in LOCK.md.
const (
	OpAcquire  = "acquire"
	OpRelease  = "release"
	OpDispatch = "dispatch"
	OpReceipt  = "receipt"
)

// LockFile is the single coordination file at the repo root.
const LockFile = "LOCK.md"

// ErrPushRejected is returned internally when a push loses the compare-and-swap
// (the remote advanced). Callers retry after fetching the winner.
var ErrPushRejected = errors.New("acp: push rejected (remote advanced)")

// ErrNotHolder is returned when a holder-only op (release/dispatch/receipt) is
// attempted by an agent that does not currently hold the lock.
var ErrNotHolder = errors.New("acp: not the current lock holder")

// ErrContended is returned by Acquire when the lock could not be taken within
// the retry budget because another agent held or kept winning it.
var ErrContended = errors.New("acp: lock contended, not acquired")

// State is the parsed LOCK.md content.
type State struct {
	Version   int
	Status    string
	Holder    string
	Op        string
	Ref       string
	UpdatedAt time.Time
}

// Render serializes a State to the LOCK.md body (stable key: value lines).
func (s State) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# ACP LOCK\n")
	fmt.Fprintf(&b, "version: %d\n", s.Version)
	fmt.Fprintf(&b, "status: %s\n", s.Status)
	fmt.Fprintf(&b, "holder: %s\n", s.Holder)
	fmt.Fprintf(&b, "op: %s\n", s.Op)
	fmt.Fprintf(&b, "ref: %s\n", s.Ref)
	fmt.Fprintf(&b, "updated_at: %s\n", s.UpdatedAt.UTC().Format(time.RFC3339Nano))
	return b.String()
}

// ParseState parses a LOCK.md body. Missing fields default to zero values; a
// missing/blank file parses to a free, version-0 state.
func ParseState(body string) State {
	st := State{Status: StatusFree}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "version":
			st.Version, _ = strconv.Atoi(val)
		case "status":
			if val != "" {
				st.Status = val
			}
		case "holder":
			st.Holder = val
		case "op":
			st.Op = val
		case "ref":
			st.Ref = val
		case "updated_at":
			if t, err := time.Parse(time.RFC3339Nano, val); err == nil {
				st.UpdatedAt = t
			}
		}
	}
	return st
}

// Coordinator drives coordination through a local git clone whose "origin" is
// the shared coordination remote.
type Coordinator struct {
	dir        string // local clone working directory
	branch     string // coordination branch
	id         string // this agent's holder id
	maxRetries int
	now        func() time.Time
}

// Options configures a Coordinator.
type Options struct {
	// Dir is the local git clone working directory (its origin is the shared
	// coordination remote).
	Dir string
	// Branch is the coordination branch (default "main").
	Branch string
	// ID is this agent's holder identity (required).
	ID string
	// MaxRetries bounds the compare-and-swap retry loop (default 8).
	MaxRetries int
	// Now is injected for deterministic tests (default time.Now).
	Now func() time.Time
}

// NewCoordinator builds a Coordinator over an existing clone directory.
func NewCoordinator(opts Options) (*Coordinator, error) {
	if opts.Dir == "" || opts.ID == "" {
		return nil, errors.New("acp: Dir and ID are required")
	}
	c := &Coordinator{
		dir:        opts.Dir,
		branch:     opts.Branch,
		id:         opts.ID,
		maxRetries: opts.MaxRetries,
		now:        opts.Now,
	}
	if c.branch == "" {
		c.branch = "main"
	}
	if c.maxRetries <= 0 {
		c.maxRetries = 8
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c, nil
}

// AcquireResult reports the outcome of an Acquire attempt.
type AcquireResult struct {
	Acquired bool
	Version  int
	Holder   string // current holder when not acquired
}

// Acquire attempts to take the lock. It fetches the latest state; if the lock is
// free (or already held by this agent) it commits a new held state and pushes as
// the compare-and-swap. A push rejection means another agent advanced the
// remote: it re-reads and retries within the retry budget. If the lock is held
// by another agent it returns Acquired=false immediately.
func (c *Coordinator) Acquire(ctx context.Context) (AcquireResult, error) {
	for attempt := 0; attempt < c.maxRetries; attempt++ {
		if err := c.sync(ctx); err != nil {
			return AcquireResult{}, err
		}
		st, err := c.read(ctx)
		if err != nil {
			return AcquireResult{}, err
		}
		if st.Status == StatusHeld && st.Holder != c.id {
			return AcquireResult{Acquired: false, Version: st.Version, Holder: st.Holder}, nil
		}
		next := State{Version: st.Version + 1, Status: StatusHeld, Holder: c.id, Op: OpAcquire, UpdatedAt: c.now()}
		err = c.commitPush(ctx, next, fmt.Sprintf("acp: acquire v%d %s", next.Version, c.id))
		if errors.Is(err, ErrPushRejected) {
			continue // remote advanced; re-read and retry
		}
		if err != nil {
			return AcquireResult{}, err
		}
		return AcquireResult{Acquired: true, Version: next.Version}, nil
	}
	return AcquireResult{Acquired: false}, ErrContended
}

// Release frees the lock. Only the current holder may release. On a CAS
// rejection it re-checks: if it lost the lock meanwhile it returns ErrNotHolder.
func (c *Coordinator) Release(ctx context.Context) (int, error) {
	return c.holderOp(ctx, OpRelease, "", func(st State) State {
		return State{Version: st.Version + 1, Status: StatusFree, Holder: "", Op: OpRelease, UpdatedAt: c.now()}
	})
}

// Dispatch records a dispatch event as a coordination commit. Holder-only.
func (c *Coordinator) Dispatch(ctx context.Context, ref string) (int, error) {
	return c.holderOp(ctx, OpDispatch, ref, func(st State) State {
		return State{Version: st.Version + 1, Status: StatusHeld, Holder: c.id, Op: OpDispatch, Ref: ref, UpdatedAt: c.now()}
	})
}

// Receipt records a receipt event as a coordination commit. Holder-only.
func (c *Coordinator) Receipt(ctx context.Context, ref string) (int, error) {
	return c.holderOp(ctx, OpReceipt, ref, func(st State) State {
		return State{Version: st.Version + 1, Status: StatusHeld, Holder: c.id, Op: OpReceipt, Ref: ref, UpdatedAt: c.now()}
	})
}

// LoopGate adapts a Coordinator to the minimal acquire(bool)/release gate the
// agentloop consumes (agentloop.Coordinator), so the git-native lock can gate
// worker dispatch with zero coupling from the loop to this package.
type LoopGate struct{ C *Coordinator }

// Acquire reports whether the lock was taken.
func (g LoopGate) Acquire(ctx context.Context) (bool, error) {
	res, err := g.C.Acquire(ctx)
	return res.Acquired, err
}

// Release frees the lock (no-op-safe: a non-holder release surfaces as an error
// the caller may ignore at run end).
func (g LoopGate) Release(ctx context.Context) error {
	_, err := g.C.Release(ctx)
	return err
}

// State returns the latest coordination state (after a fetch).
func (c *Coordinator) State(ctx context.Context) (State, error) {
	if err := c.sync(ctx); err != nil {
		return State{}, err
	}
	return c.read(ctx)
}

// holderOp performs a holder-only transition (release/dispatch/receipt) under the
// compare-and-swap retry loop.
func (c *Coordinator) holderOp(ctx context.Context, op, ref string, mk func(State) State) (int, error) {
	for attempt := 0; attempt < c.maxRetries; attempt++ {
		if err := c.sync(ctx); err != nil {
			return 0, err
		}
		st, err := c.read(ctx)
		if err != nil {
			return 0, err
		}
		if st.Status != StatusHeld || st.Holder != c.id {
			return 0, ErrNotHolder
		}
		next := mk(st)
		msg := fmt.Sprintf("acp: %s v%d %s", op, next.Version, c.id)
		if ref != "" {
			msg += " ref=" + ref
		}
		err = c.commitPush(ctx, next, msg)
		if errors.Is(err, ErrPushRejected) {
			continue
		}
		if err != nil {
			return 0, err
		}
		return next.Version, nil
	}
	return 0, ErrContended
}

// ---- git plumbing ----

func (c *Coordinator) sync(ctx context.Context) error {
	if _, err := c.git(ctx, "fetch", "--quiet", "origin"); err != nil {
		return fmt.Errorf("acp: fetch: %w", err)
	}
	if _, err := c.git(ctx, "reset", "--quiet", "--hard", "origin/"+c.branch); err != nil {
		return fmt.Errorf("acp: reset: %w", err)
	}
	return nil
}

func (c *Coordinator) read(ctx context.Context) (State, error) {
	// Read LOCK.md from the working tree at HEAD.
	out, err := c.git(ctx, "show", "HEAD:"+LockFile)
	if err != nil {
		// Missing lock file => a fresh, free coordination repo.
		return State{Status: StatusFree}, nil
	}
	return ParseState(out), nil
}

func (c *Coordinator) commitPush(ctx context.Context, st State, msg string) error {
	if err := writeFile(c.dir, LockFile, st.Render()); err != nil {
		return err
	}
	if _, err := c.git(ctx, "add", LockFile); err != nil {
		return err
	}
	if _, err := c.git(ctx, "commit", "--quiet", "-m", msg); err != nil {
		return fmt.Errorf("acp: commit: %w", err)
	}
	_, err := c.git(ctx, "push", "--quiet", "origin", "HEAD:"+c.branch)
	if err != nil {
		if isPushRejection(err) {
			return ErrPushRejected
		}
		return fmt.Errorf("acp: push: %w", err)
	}
	return nil
}

// git runs a git command in the coordinator's clone with a fixed identity and
// signing disabled, so it works in headless CI/test environments.
func (c *Coordinator) git(ctx context.Context, args ...string) (string, error) {
	full := append([]string{
		"-c", "user.email=acp@r1.local",
		"-c", "user.name=acp",
		"-c", "commit.gpgsign=false",
		"-C", c.dir,
	}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), &gitError{args: args, stderr: stderr.String(), err: err}
	}
	return stdout.String(), nil
}

type gitError struct {
	args   []string
	stderr string
	err    error
}

func (e *gitError) Error() string {
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.args, " "), e.err, strings.TrimSpace(e.stderr))
}

func isPushRejection(err error) bool {
	var ge *gitError
	if !errors.As(err, &ge) {
		return false
	}
	s := ge.stderr
	return strings.Contains(s, "rejected") ||
		strings.Contains(s, "non-fast-forward") ||
		strings.Contains(s, "fetch first") ||
		strings.Contains(s, "Updates were rejected")
}
