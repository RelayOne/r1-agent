package builtin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/internal/hub"
)

// WorkspaceReconciler is an event-driven self-supervision hook that keeps a
// Node workspace's node_modules in sync with its package.json files. It
// observes every write_file / edit_file tool call, tracks whether any
// package.json was touched since the last reconciliation, and on task
// completion runs `pnpm install --silent` (with `npm install` as fallback)
// so the next task starts with a consistent dependency graph.
//
// Why this exists:
//
// The Sentinel SOW run repeatedly produced the same failure: task N added
// a dependency to a package.json, ended, and task N+1 started with stale
// node_modules and hit "Cannot find module X" during its own work. The
// repair loop would then try to fix the import error without understanding
// that a simple `pnpm install` was all that was missing. Stoke-level
// reconciliation removes that class of failure without requiring the
// model to remember ecosystem hygiene on every turn.
//
// Scope: only fires when the workspace root has a package.json. Go, Rust,
// Python workspaces are untouched. The reconciler is best-effort — any
// install failure is logged but does not fail the task (the AC will
// surface the real problem downstream if install couldn't fix it).
type WorkspaceReconciler struct {
	// WorkDir is the workspace root. Typically cfg.RepoRoot in the SOW
	// native runner.
	WorkDir string

	mu               sync.Mutex
	packageJSONDirty bool
	lastInstall      time.Time
}

// NewWorkspaceReconciler creates a reconciler rooted at workDir.
func NewWorkspaceReconciler(workDir string) *WorkspaceReconciler {
	return &WorkspaceReconciler{WorkDir: workDir}
}

// Register wires the reconciler to tool.post_use and task.completed events.
func (w *WorkspaceReconciler) Register(bus *hub.Bus) {
	bus.Register(hub.Subscriber{
		ID:       "builtin.workspace_reconciler",
		Events:   []hub.EventType{hub.EventToolPostUse, hub.EventTaskCompleted},
		Mode:     hub.ModeObserve,
		Priority: 500,
		Handler:  w.handle,
	})
}

func (w *WorkspaceReconciler) handle(ctx context.Context, ev *hub.Event) *hub.HookResponse {
	switch ev.Type {
	case hub.EventToolPostUse:
		w.observeTool(ev)
	case hub.EventTaskCompleted:
		w.reconcile(ctx)
	}
	return &hub.HookResponse{Decision: hub.Allow}
}

// observeTool inspects a post-tool-use event and marks the workspace as
// dirty if the tool wrote or edited a package.json file.
func (w *WorkspaceReconciler) observeTool(ev *hub.Event) {
	if ev.Tool == nil {
		return
	}
	name := ev.Tool.Name
	if name != "write_file" && name != "edit_file" && name != "Write" && name != "Edit" {
		return
	}
	path, _ := ev.Tool.Input["path"].(string)
	if path == "" {
		return
	}
	// package.json or any package.json nested in the workspace.
	// Don't trigger on node_modules/**/package.json — those are
	// dependency manifests, not workspace edits.
	base := filepath.Base(path)
	if base != "package.json" && base != "pnpm-workspace.yaml" {
		return
	}
	if strings.Contains(path, "node_modules") {
		return
	}
	w.mu.Lock()
	w.packageJSONDirty = true
	w.mu.Unlock()
}

// reconcile runs pnpm install (or npm install fallback) when the workspace
// is dirty. No-ops when clean, when the workspace isn't a Node project, or
// when neither pnpm nor npm is on PATH.
func (w *WorkspaceReconciler) reconcile(ctx context.Context) {
	w.mu.Lock()
	dirty := w.packageJSONDirty
	w.packageJSONDirty = false
	w.mu.Unlock()
	if !dirty || w.WorkDir == "" {
		return
	}
	// Only reconcile Node workspaces.
	if _, err := os.Stat(filepath.Join(w.WorkDir, "package.json")); err != nil {
		return
	}

	// Prefer pnpm when the workspace looks pnpm-shaped.
	pnpmWorkspace := false
	if _, err := os.Stat(filepath.Join(w.WorkDir, "pnpm-workspace.yaml")); err == nil {
		pnpmWorkspace = true
	}
	if _, err := os.Stat(filepath.Join(w.WorkDir, "pnpm-lock.yaml")); err == nil {
		pnpmWorkspace = true
	}

	tryRun := func(bin string, args ...string) bool {
		if _, err := exec.LookPath(bin); err != nil {
			return false
		}
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = w.WorkDir
		// Stdout/stderr discarded on purpose — this is best-effort.
		// The AC stage will surface any real problem the model
		// needs to fix.
		return cmd.Run() == nil
	}

	if pnpmWorkspace {
		if tryRun("pnpm", "install", "--silent") {
			w.mu.Lock()
			w.lastInstall = time.Now()
			w.mu.Unlock()
			return
		}
	}
	if tryRun("pnpm", "install", "--silent") {
		w.mu.Lock()
		w.lastInstall = time.Now()
		w.mu.Unlock()
		return
	}
	if tryRun("npm", "install", "--silent") {
		w.mu.Lock()
		w.lastInstall = time.Now()
		w.mu.Unlock()
		return
	}
}

// LastInstall returns the timestamp of the most recent successful
// reconciliation. Zero value means no reconciliation has run yet.
func (w *WorkspaceReconciler) LastInstall() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastInstall
}
