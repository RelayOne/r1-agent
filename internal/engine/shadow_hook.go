// shadow_hook.go — wires per-tool-use shadow-git checkpoints into the
// native runner's tool handler.
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

// shadowCheckpointer serializes per-tool-use shadow-git checkpoints for
// one dispatch. The agentloop executes parallel tool_use blocks in
// goroutines; concurrent `git add -A` against the shared private index
// would corrupt it, so take() holds a mutex for the git window while
// leaving the tools themselves parallel.
type shadowCheckpointer struct {
	mu       sync.Mutex
	handle   worktree.Handle
	seq      int
	warnOnce sync.Once
}

// newShadowCheckpointer probes the worktree once and returns nil
// (fail-open, one warning) when it isn't a git repository. Seq starts
// at 1: seq 0 is reserved for the workflow's pre-attempt baseline
// checkpoint so per-tool checkpoints never clobber its ref.
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

// take captures the working tree after a successful mutating tool call.
func (s *shadowCheckpointer) take(ctx context.Context, tool string) (CheckpointRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	sha, err := worktree.ShadowCheckpoint(ctx, s.handle, s.seq)
	if err != nil {
		return CheckpointRecord{}, err
	}
	return CheckpointRecord{
		Seq:  s.seq,
		SHA:  sha,
		Ref:  worktree.CheckpointRefName(s.handle.Name, s.seq),
		Tool: tool,
	}, nil
}

// warnFailure logs the first checkpoint failure of the run. Later
// failures (usually the same root cause repeated per tool call) stay
// quiet so a broken repo doesn't flood the log.
func (s *shadowCheckpointer) warnFailure(err error) {
	s.warnOnce.Do(func() {
		slog.Warn("shadow checkpoint failed; continuing without checkpoints for this failure", "err", err)
	})
}
