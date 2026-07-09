package acp

import (
	"context"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// gitRun runs a git command (optionally in dir) with a fixed identity, failing
// the test on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	base := []string{"-c", "user.email=t@r1.local", "-c", "user.name=t", "-c", "commit.gpgsign=false"}
	if dir != "" {
		base = append(base, "-C", dir)
	}
	cmd := exec.Command("git", append(base, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// setupRemote creates a bare coordination repo seeded with a free LOCK.md and
// returns its path.
func setupRemote(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	gitRun(t, "", "init", "--quiet", "--bare", "-b", "main", bare)

	seed := filepath.Join(root, "seed")
	gitRun(t, "", "clone", "--quiet", bare, seed)
	if err := writeFile(seed, LockFile, State{Version: 0, Status: StatusFree}.Render()); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seed, "add", LockFile)
	gitRun(t, seed, "commit", "--quiet", "-m", "seed: free lock v0")
	gitRun(t, seed, "push", "--quiet", "origin", "HEAD:main")
	return bare
}

// cloneAgent clones the remote into a fresh dir and returns a Coordinator.
func cloneAgent(t *testing.T, bare, id string) *Coordinator {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone-"+id)
	gitRun(t, "", "clone", "--quiet", bare, dir)
	c, err := NewCoordinator(Options{Dir: dir, ID: id, Now: func() time.Time { return time.Unix(0, 0).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestParseRenderRoundTrip(t *testing.T) {
	st := State{Version: 7, Status: StatusHeld, Holder: "a", Op: OpDispatch, Ref: "task-1", UpdatedAt: time.Unix(0, 0).UTC()}
	got := ParseState(st.Render())
	if got.Version != 7 || got.Status != StatusHeld || got.Holder != "a" || got.Op != OpDispatch || got.Ref != "task-1" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	// Blank body => free, v0.
	if b := ParseState(""); b.Status != StatusFree || b.Version != 0 {
		t.Fatalf("blank must be free v0, got %+v", b)
	}
}

func TestMonotoneVersion(t *testing.T) {
	ctx := context.Background()
	bare := setupRemote(t)
	a := cloneAgent(t, bare, "A")

	acq, err := a.Acquire(ctx)
	if err != nil || !acq.Acquired || acq.Version != 1 {
		t.Fatalf("acquire => %+v err=%v (want v1 acquired)", acq, err)
	}
	dv, err := a.Dispatch(ctx, "task-1")
	if err != nil || dv != 2 {
		t.Fatalf("dispatch => v%d err=%v (want v2)", dv, err)
	}
	rv, err := a.Receipt(ctx, "task-1")
	if err != nil || rv != 3 {
		t.Fatalf("receipt => v%d err=%v (want v3)", rv, err)
	}
	relv, err := a.Release(ctx)
	if err != nil || relv != 4 {
		t.Fatalf("release => v%d err=%v (want v4)", relv, err)
	}
	st, err := a.State(ctx)
	if err != nil || st.Version != 4 || st.Status != StatusFree {
		t.Fatalf("final state => %+v err=%v (want v4 free)", st, err)
	}
}

func TestStalePushRejectedAndRetry(t *testing.T) {
	ctx := context.Background()
	bare := setupRemote(t)
	a := cloneAgent(t, bare, "A")
	b := cloneAgent(t, bare, "B")

	// A acquires -> remote at v1 (held by A).
	if acq, err := a.Acquire(ctx); err != nil || !acq.Acquired {
		t.Fatalf("A acquire failed: %+v %v", acq, err)
	}

	// B, still at the seed view (v0), builds a commit on the stale base and
	// pushes directly: the compare-and-swap must reject it (non-fast-forward).
	stale := State{Version: 1, Status: StatusHeld, Holder: "B", Op: OpAcquire, UpdatedAt: time.Unix(0, 0).UTC()}
	err := b.commitPush(ctx, stale, "acp: stale acquire")
	if err != ErrPushRejected {
		t.Fatalf("stale push must be rejected, got %v", err)
	}

	// B recovers via the normal path: Acquire syncs, sees A holds, declines.
	acq, err := b.Acquire(ctx)
	if err != nil {
		t.Fatalf("B acquire errored: %v", err)
	}
	if acq.Acquired {
		t.Fatal("B must not acquire a lock A holds")
	}
	if acq.Holder != "A" {
		t.Fatalf("B should see holder A, got %q", acq.Holder)
	}

	// After A releases, B can acquire (version keeps climbing monotonically).
	if _, err := a.Release(ctx); err != nil {
		t.Fatalf("A release: %v", err)
	}
	acq, err = b.Acquire(ctx)
	if err != nil || !acq.Acquired {
		t.Fatalf("B acquire after release: %+v %v", acq, err)
	}
	if acq.Version <= 1 {
		t.Fatalf("version must climb monotonically, got %d", acq.Version)
	}
}

func TestReleaseRequiresHolder(t *testing.T) {
	ctx := context.Background()
	bare := setupRemote(t)
	a := cloneAgent(t, bare, "A")
	b := cloneAgent(t, bare, "B")

	if _, err := a.Acquire(ctx); err != nil {
		t.Fatalf("A acquire: %v", err)
	}
	// B is not the holder; release must refuse.
	if _, err := b.Release(ctx); err != ErrNotHolder {
		t.Fatalf("non-holder release must be ErrNotHolder, got %v", err)
	}
	// B cannot dispatch/receipt either.
	if _, err := b.Dispatch(ctx, "x"); err != ErrNotHolder {
		t.Fatalf("non-holder dispatch must be ErrNotHolder, got %v", err)
	}
}

func TestTwoAgentsContendOneAcquires(t *testing.T) {
	ctx := context.Background()
	bare := setupRemote(t)

	const n = 4
	agents := make([]*Coordinator, n)
	for i := 0; i < n; i++ {
		agents[i] = cloneAgent(t, bare, string(rune('A'+i)))
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	acquired := 0
	winner := ""
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(c *Coordinator) {
			defer wg.Done()
			<-start // maximize contention
			res, err := c.Acquire(ctx)
			if err != nil && err != ErrContended {
				t.Errorf("acquire error: %v", err)
				return
			}
			if res.Acquired {
				mu.Lock()
				acquired++
				winner = c.id
				mu.Unlock()
			}
		}(agents[i])
	}
	close(start)
	wg.Wait()

	if acquired != 1 {
		t.Fatalf("exactly one agent must acquire, got %d", acquired)
	}
	// The winner is the holder of record in the shared state.
	st, err := agents[0].State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != StatusHeld || st.Holder != winner {
		t.Fatalf("final state must be held by the winner %q, got %+v", winner, st)
	}
}
