// shadow_hook.go — wires per-turn shadow-git checkpoints into the native
// runner's tool handler.
package engine

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/RelayOne/r1/internal/worktree"
)

// EnvDisableShadowCheckpoint is the kill switch: when set to "1" the
// native runner takes no shadow checkpoints even if
// RunSpec.ShadowCheckpoints is set.
const EnvDisableShadowCheckpoint = "R1_DISABLE_SHADOW_CHECKPOINT"

// shadowCheckpointer captures one shadow-git checkpoint per turn for one
// dispatch. It is deliberately NOT per-tool-use: parallel tool_use blocks
// in a turn run in goroutines, so a per-tool checkpoint could snapshot a
// file a sibling tool is still writing (a torn tree), and it would land in
// the transcript BEFORE the assistant tool_use message it captures (that
// message is only recorded at the top of the NEXT turn), rewinding the
// conversation one tool_use too far.
//
// Instead the tool handler only marks the turn dirty; flush() takes the
// single checkpoint at the top of the next turn — after executeTools'
// wg.Wait() guarantees every sibling tool returned (settled tree) and
// after the turn's tool_use/tool_result messages are recorded (so the
// checkpoint marker sits AFTER them: the correct rewind target). The mutex
// guards markDirty against the parallel tool goroutines; flush() runs
// single-threaded between turns.
type shadowCheckpointer struct {
	mu       sync.Mutex
	handle   worktree.Handle
	seq      int
	dirty    bool   // a mutating tool succeeded since the last flush
	lastTool string // most recent mutating tool marked this turn
	warnOnce sync.Once
}

// newShadowCheckpointer probes the worktree once and returns nil
// (fail-open, one warning) when it isn't a git repository. Seq starts
// at 1: seq 0 is reserved for the workflow's pre-attempt baseline
// checkpoint so per-turn checkpoints never clobber its ref.
func newShadowCheckpointer(ctx context.Context, worktreeDir, runtimeDir string) *shadowCheckpointer {
	probe := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	probe.Dir = worktreeDir
	if err := probe.Run(); err != nil {
		slog.Warn("shadow checkpoints disabled: worktree is not a git repository", "dir", worktreeDir, "err", err)
		return nil
	}
	return &shadowCheckpointer{
		handle: worktree.Handle{
			Name:       filepath.Base(worktreeDir),
			Path:       worktreeDir,
			RuntimeDir: runtimeDir,
			GitBinary:  "git",
		},
	}
}

// eligible reports whether a tool call can mutate the working tree.
// mcp_* tools are included conservatively: their side effects are
// server-defined and a spurious checkpoint is cheap (cached stat info)
// while a missed one loses a rewind point.
func (s *shadowCheckpointer) eligible(name string, writable map[string]bool) bool {
	if s == nil {
		return false
	}
	return writable[name] || strings.HasPrefix(name, "mcp_")
}

// markDirty records that an eligible mutating tool succeeded this turn.
// Called from the tool handler, possibly concurrently for parallel
// tool_use blocks, so it holds the mutex. It takes no git action — the
// checkpoint is deferred to flush() between turns.
func (s *shadowCheckpointer) markDirty(tool string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirty = true
	s.lastTool = tool
}

// flush captures the settled working tree IFF a mutating tool ran since
// the last flush, returning (record, true) when a checkpoint was taken.
// It must be called between turns (no tool goroutine in flight): every
// tool of the completed turn has returned, so no sibling can be mid-write.
func (s *shadowCheckpointer) flush(ctx context.Context) (CheckpointRecord, bool, error) {
	if s == nil {
		return CheckpointRecord{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return CheckpointRecord{}, false, nil
	}
	s.seq++
	sha, err := worktree.ShadowCheckpoint(ctx, s.handle, s.seq)
	if err != nil {
		return CheckpointRecord{}, false, err
	}
	rec := CheckpointRecord{
		Seq:  s.seq,
		SHA:  sha,
		Ref:  worktree.CheckpointRefName(s.handle.Name, s.seq),
		Tool: s.lastTool,
	}
	s.dirty = false
	s.lastTool = ""
	return rec, true, nil
}

// warnFailure logs the first checkpoint failure of the run. Later
// failures (usually the same root cause repeated per turn) stay quiet so
// a broken repo doesn't flood the log.
func (s *shadowCheckpointer) warnFailure(err error) {
	s.warnOnce.Do(func() {
		slog.Warn("shadow checkpoint failed; continuing without checkpoints for this failure", "err", err)
	})
}
