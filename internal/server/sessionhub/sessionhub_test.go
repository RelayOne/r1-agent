package sessionhub

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

// withSandbox redirects R1_HOME to a tmp dir so workdir-validation's
// "under ~/.r1/" rule has a known, isolated target. Returns the sandbox
// path and a cleanup func; callers should `defer cleanup()`.
func withSandbox(t *testing.T) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	prev, had := os.LookupEnv("R1_HOME")
	t.Setenv("R1_HOME", dir)
	cleanup := func() {
		if had {
			_ = os.Setenv("R1_HOME", prev)
		} else {
			_ = os.Unsetenv("R1_HOME")
		}
	}
	return dir, cleanup
}

// TestSessionHubCreate exercises the happy path: a valid absolute,
// existing, writable directory NOT under ~/.r1/ should yield a session
// reachable via Get.
func TestSessionHubCreate(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub, err := NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	wd := t.TempDir()
	s, err := hub.Create(CreateOptions{Workdir: wd, Model: "test-model"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if s.ID == "" {
		t.Fatalf("expected minted ID; got empty")
	}
	got, err := hub.Get(s.ID)
	if err != nil {
		t.Fatalf("Get(%s): %v", s.ID, err)
	}
	if got != s {
		t.Fatalf("Get returned different *Session")
	}
	if got.SessionRoot != filepath.Clean(wd) {
		t.Fatalf("SessionRoot: got %q, want %q", got.SessionRoot, wd)
	}
}

// TestSessionHubGet covers the not-found path.
func TestSessionHubGet(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub, err := NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	if _, err := hub.Get("nope"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get(unknown): got %v, want ErrSessionNotFound", err)
	}
}

// TestSessionHubDelete asserts Delete removes the session and a
// follow-up Get returns ErrSessionNotFound.
func TestSessionHubDelete(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub, err := NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	wd := t.TempDir()
	s, err := hub.Create(CreateOptions{Workdir: wd})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := hub.Delete(s.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := hub.Get(s.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get after Delete: got %v, want ErrSessionNotFound", err)
	}
	if err := hub.Delete(s.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("double-Delete: got %v, want ErrSessionNotFound", err)
	}
}

// TestSessionHubList asserts List returns every registered session
// (order-independent — sync.Map iteration is unsorted).
func TestSessionHubList(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub, err := NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	var ids []string
	for i := 0; i < 4; i++ {
		wd := t.TempDir()
		s, err := hub.Create(CreateOptions{Workdir: wd})
		if err != nil {
			t.Fatalf("Create #%d: %v", i, err)
		}
		ids = append(ids, s.ID)
	}
	got := hub.List()
	if len(got) != len(ids) {
		t.Fatalf("List len: got %d, want %d", len(got), len(ids))
	}
	var gotIDs []string
	for _, s := range got {
		gotIDs = append(gotIDs, s.ID)
	}
	sort.Strings(gotIDs)
	sort.Strings(ids)
	for i := range ids {
		if gotIDs[i] != ids[i] {
			t.Fatalf("List ids[%d]: got %q, want %q", i, gotIDs[i], ids[i])
		}
	}
}

// TestWorkdirValidation enumerates each of the five rules from
// spec §11.21 and asserts each one fires with ErrInvalidWorkdir.
func TestWorkdirValidation(t *testing.T) {
	sandbox, cleanup := withSandbox(t)
	defer cleanup()
	hub, err := NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}

	// Rule 1: not absolute.
	if _, err := hub.Create(CreateOptions{Workdir: "relative/path"}); !errors.Is(err, ErrInvalidWorkdir) {
		t.Fatalf("relative: got %v, want ErrInvalidWorkdir", err)
	}
	// Rule 1 (corner): empty.
	if _, err := hub.Create(CreateOptions{Workdir: ""}); !errors.Is(err, ErrInvalidWorkdir) {
		t.Fatalf("empty: got %v, want ErrInvalidWorkdir", err)
	}
	// Rule 2: non-existent.
	if _, err := hub.Create(CreateOptions{Workdir: "/tmp/r1-test-does-not-exist-zzzz"}); !errors.Is(err, ErrInvalidWorkdir) {
		t.Fatalf("nonexistent: got %v, want ErrInvalidWorkdir", err)
	}
	// Rule 3: not a directory.
	tmpFile := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(tmpFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := hub.Create(CreateOptions{Workdir: tmpFile}); !errors.Is(err, ErrInvalidWorkdir) {
		t.Fatalf("not-dir: got %v, want ErrInvalidWorkdir", err)
	}
	// Rule 4: not writable. Build a 0o500 dir.
	roDir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(roDir, 0o500); err != nil {
		t.Fatalf("mkdir ro: %v", err)
	}
	defer func() { _ = os.Chmod(roDir, 0o700) }()
	// On some CI sandboxes (e.g. running as root), 0o500 is not
	// enforced. Skip the check there rather than report a false
	// positive — root bypasses mode bits and the validator's
	// probe-by-CreateTemp will succeed.
	if os.Geteuid() != 0 {
		if _, err := hub.Create(CreateOptions{Workdir: roDir}); !errors.Is(err, ErrInvalidWorkdir) {
			t.Fatalf("not-writable: got %v, want ErrInvalidWorkdir", err)
		}
	}
	// Rule 5: under ~/.r1/. The sandbox IS ~/.r1/, so any subdir of it
	// must be rejected.
	insideR1 := filepath.Join(sandbox, "session-1")
	if err := os.MkdirAll(insideR1, 0o700); err != nil {
		t.Fatalf("mkdir under r1: %v", err)
	}
	if _, err := hub.Create(CreateOptions{Workdir: insideR1}); !errors.Is(err, ErrInvalidWorkdir) {
		t.Fatalf("under-r1: got %v, want ErrInvalidWorkdir", err)
	}
	// Rule 5 corner: workdir == r1Dir.
	if _, err := hub.Create(CreateOptions{Workdir: sandbox}); !errors.Is(err, ErrInvalidWorkdir) {
		t.Fatalf("equal-r1: got %v, want ErrInvalidWorkdir", err)
	}
}

// TestSessionHubConcurrentCreate exercises -race coverage: many
// goroutines hitting Create with distinct workdirs should each get
// distinct IDs and a successful registration.
func TestSessionHubConcurrentCreate(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub, err := NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	const N = 32
	var barrier sync.WaitGroup
	ids := make([]string, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wd := t.TempDir()
		i := i
		wd2 := wd
		barrier.Add(1)
		go func() {
			defer barrier.Done()
			s, err := hub.Create(CreateOptions{Workdir: wd2})
			if err != nil {
				errs[i] = err
				return
			}
			ids[i] = s.ID
		}()
	}
	barrier.Wait()
	// assert.distinct-ids: every concurrent Create succeeded with a
	// unique id and every session is reachable via Get.
	seen := make(map[string]bool, N)
	for i := 0; i < N; i++ {
		if errs[i] != nil {
			t.Fatalf("create #%d: %v", i, errs[i])
		}
		if ids[i] == "" {
			t.Fatalf("empty id at #%d", i)
		}
		if seen[ids[i]] {
			t.Fatalf("duplicate id #%d: %s", i, ids[i])
		}
		seen[ids[i]] = true
		if _, err := hub.Get(ids[i]); err != nil {
			t.Fatalf("Get(%s): %v", ids[i], err)
		}
	}
	if got := len(hub.List()); got != N {
		t.Fatalf("List len: got %d, want %d", got, N)
	}
}

// TestCreateExplicitID asserts caller-supplied IDs are honored and a
// duplicate triggers ErrSessionExists.
func TestCreateExplicitID(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub, err := NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	wd1 := t.TempDir()
	wd2 := t.TempDir()
	s, err := hub.Create(CreateOptions{Workdir: wd1, ID: "fixed-id"})
	if err != nil {
		t.Fatalf("Create #1: %v", err)
	}
	if s.ID != "fixed-id" {
		t.Fatalf("ID: got %q, want %q", s.ID, "fixed-id")
	}
	if _, err := hub.Create(CreateOptions{Workdir: wd2, ID: "fixed-id"}); !errors.Is(err, ErrSessionExists) {
		t.Fatalf("dup-id: got %v, want ErrSessionExists", err)
	}
}
