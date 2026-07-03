// Package sandbox provides OS-level containment for the native bash tool
// (SOTA gap #14). The CLI runners already get a sandbox via the per-worktree
// settings.json (config.SandboxSettings, fail-closed); the native agentloop
// executed `bash -c` directly on the host with only process-group isolation.
// This package closes that asymmetry with three backends:
//
//   - bwrap (primary): mount-namespace containment. Read-only view of the
//     host filesystem, writable binds for the worktree and toolchain caches,
//     tmpfs/dev-null masks over credential paths, optional network unshare.
//   - landlock (fallback): kernel LSM via raw syscalls on the vendored
//     x/sys constants. Strict allow-list (no deny-inside-allow masking),
//     all-or-nothing TCP restriction on ABI >= 4. Needs a re-exec helper
//     because landlock_restrict_self binds the calling process.
//   - docker (explicit opt-in only, never auto-selected): coarse container
//     fallback modeled on internal/engine/container.go.
//
// Domain-level egress allowlists (RunSpec.SandboxDomains) are NOT
// enforceable by any backend here — kernel primitives give allow/deny of
// the network as a whole, not per-domain filtering. V1 therefore exposes
// egress as a boolean and documents the narrowing; operators cut egress
// with R1_NATIVE_SANDBOX_NET=deny.
//
// Fail-closed contract (CLAUDE.md decision #6 pattern): when a sandbox is
// requested but no backend can enforce the policy, the caller must refuse
// to run the command. The only opt-out is the explicit kill switch
// R1_NATIVE_SANDBOX=off.
package sandbox

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1/internal/r1env"
)

// Sandbox mode names accepted by Select and by the R1_NATIVE_SANDBOX env var.
const (
	// ModeAuto tries bwrap first, then landlock. Docker is never
	// auto-selected (image/toolchain choice is the operator's problem).
	ModeAuto     = "auto"
	ModeBwrap    = "bwrap"
	ModeLandlock = "landlock"
	ModeDocker   = "docker"
	// ModeOff is the explicit kill switch: no wrapping, historical
	// unsandboxed behavior. Select returns (nil, nil).
	ModeOff = "off"
)

// Policy describes what a sandboxed command may touch. It mirrors the
// semantic surface of config.SandboxSettings (the CLI-runner sandbox) so a
// single policy source can drive both paths. JSON tags exist because the
// Landlock backend serializes the policy across the re-exec boundary.
type Policy struct {
	// Mode selects the backend: auto|bwrap|landlock|docker|off.
	// Empty is treated as auto.
	Mode string `json:"mode,omitempty"`
	// AllowRead lists paths readable inside the sandbox beyond the
	// backend's baseline. bwrap baseline is the whole host fs read-only
	// (minus DenyRead masks), so AllowRead only matters there for paths
	// shadowed by the /tmp tmpfs (e.g. a runtime dir under /tmp).
	// Landlock is a strict allow-list, so AllowRead is load-bearing.
	AllowRead []string `json:"allow_read,omitempty"`
	// AllowWrite lists directories (or files) writable inside the
	// sandbox — typically just the worktree.
	AllowWrite []string `json:"allow_write,omitempty"`
	// DenyRead lists credential paths masked from the sandboxed command.
	// Enforced by bwrap (tmpfs over dirs, /dev/null over files) and by
	// docker (host fs simply not mounted). Landlock cannot mask a path
	// inside an allowed tree (allow-list-only), so DenyRead entries under
	// AllowWrite — e.g. worktree/.env — stay readable under the Landlock
	// fallback; entries outside the allow-list (like ~/.ssh) are
	// unreadable there by construction.
	DenyRead []string `json:"deny_read,omitempty"`
	// WriteCaches lists toolchain cache paths that must stay writable or
	// the execute phase breaks outright (go build needs GOCACHE, npm its
	// cache, module downloads ~/go/pkg). Kept separate from AllowWrite so
	// callers/tests can distinguish the worktree grant from the
	// pragmatic cache grant. A sandboxed command can poison these caches
	// for later host builds; operators who care set this to nil and eat
	// cold caches.
	WriteCaches []string `json:"write_caches,omitempty"`
	// AllowEgress controls network access as a whole. Default true:
	// execute-phase bash legitimately runs `go mod download` /
	// `npm install`, and no backend here can do the domain filtering the
	// CLI sandbox gets — deny-by-default would break the primary
	// workflow. Fail-closed applies to enforceability of the requested
	// policy, not maximal strictness. Harden with R1_NATIVE_SANDBOX_NET=deny.
	AllowEgress bool `json:"allow_egress"`
	// DockerImage is required by (and only used by) the docker backend.
	DockerImage string `json:"docker_image,omitempty"`
}

// ModeFromEnv reads the sandbox mode kill-switch/override from
// R1_NATIVE_SANDBOX (legacy twin STOKE_NATIVE_SANDBOX). Empty means
// ModeAuto. The value is validated by Select, not here, so a typo like
// "bwarp" fails closed at wiring time instead of silently meaning auto.
func ModeFromEnv() string {
	v := strings.ToLower(strings.TrimSpace(r1env.Get("R1_NATIVE_SANDBOX", "STOKE_NATIVE_SANDBOX")))
	if v == "" {
		return ModeAuto
	}
	return v
}

// EngageFromEnv decides whether the native OS sandbox should wrap tool
// execution and, if so, in which mode. It is OPT-IN: an unset
// R1_NATIVE_SANDBOX means "do not engage" (engaged=false). Engaging
// requires an explicit non-"off" value:
//
//	R1_NATIVE_SANDBOX=on|auto|bwrap|landlock|docker  -> engaged, that mode
//	                                          ("on" is an alias for auto)
//	R1_NATIVE_SANDBOX=off  or unset            -> not engaged
//
// Containment is otherwise complete: host-exec tools that can't be wrapped
// (notebook_cell_run/cron_create) are denied while engaged, and the
// worktree's real .git (common dir) is allow-listed so git works. The one
// remaining reason it stays opt-in rather than default-on is the network
// posture: egress defaults to allow (module fetches) and no backend can do
// per-domain filtering. See docs/native-sandbox.md.
func EngageFromEnv() (mode string, engaged bool) {
	v := strings.ToLower(strings.TrimSpace(r1env.Get("R1_NATIVE_SANDBOX", "STOKE_NATIVE_SANDBOX")))
	switch v {
	case "", ModeOff:
		return ModeOff, false
	case "on":
		return ModeAuto, true
	default:
		return v, true
	}
}

// EgressFromEnv reads R1_NATIVE_SANDBOX_NET (legacy twin
// STOKE_NATIVE_SANDBOX_NET). Only the explicit allow spellings return
// true for a non-empty value; anything else — including typos — cuts the
// network, which is the safe direction for an unrecognized value.
func EgressFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(r1env.Get("R1_NATIVE_SANDBOX_NET", "STOKE_NATIVE_SANDBOX_NET"))) {
	case "", "allow", "true", "1", "on":
		return true
	default:
		return false
	}
}

// ImageFromEnv reads the docker-backend image from R1_SANDBOX_IMAGE
// (legacy twin STOKE_SANDBOX_IMAGE).
func ImageFromEnv() string {
	return strings.TrimSpace(r1env.Get("R1_SANDBOX_IMAGE", "STOKE_SANDBOX_IMAGE"))
}

// DefaultDenySockets returns well-known daemon control sockets to mask from
// sandboxed commands. These are AF_UNIX endpoints, and egress denial
// (--unshare-net for bwrap, Landlock's TCP-only net restriction) does NOT
// cover unix sockets — so a reachable /run/docker.sock is a full host
// escape regardless of the network posture. The bwrap backend masks each
// (tmpfs over a dir, /dev/null over a socket file); the Landlock backend
// cannot rely on this list (it is allow-list-only) and instead keeps these
// paths out of its read baseline (see landlockROPaths / the narrowed /run
// grant). Paths are absolute and $HOME-independent, so they are masked even
// when home is unknown.
func DefaultDenySockets() []string {
	return []string{
		"/run/docker.sock",
		"/var/run/docker.sock",
		"/run/containerd",     // dir: containerd.sock + .ttrpc live here
		"/var/run/containerd", // symlink twin on some distros
		"/run/podman",         // podman.sock
		"/run/crio",           // crio.sock
		"/run/dbus/system_bus_socket",
		"/run/systemd/private",
	}
}

// DefaultDenyRead returns the credential paths (and daemon sockets) masked
// from sandboxed commands. This is a blocklist over an otherwise-readable
// host fs (bwrap backend), so it can never be complete — secrets outside
// this list (e.g. /etc/secrets) remain readable there; the Landlock
// backend's allow-list is stricter. The home-relative credential entries
// are omitted when home is unknown so an empty $HOME can never expand an
// entry to a filesystem root; the absolute daemon sockets are always
// included (see DefaultDenySockets).
func DefaultDenyRead(home string) []string {
	out := DefaultDenySockets()
	if home == "" {
		return out
	}
	rel := []string{
		".ssh", ".aws", ".gnupg", ".netrc", ".docker", ".kube",
		".claude", ".codex",
		filepath.Join(".config", "gh"),
		filepath.Join(".config", "gcloud"),
	}
	for _, r := range rel {
		out = append(out, filepath.Join(home, r))
	}
	return out
}

// WorkDirDenyRead returns dotenv-style secret files inside the worktree to
// mask. Only files that exist at wrap time can be masked (bwrap binds are
// taken once per command); a file the model creates afterward inside the
// writable worktree is a documented residual gap.
func WorkDirDenyRead(workDir string) []string {
	if workDir == "" {
		return nil
	}
	return []string{
		filepath.Join(workDir, ".env"),
		filepath.Join(workDir, ".env.local"),
	}
}

// WorktreeGitDirs returns the git directories a sandboxed command needs
// writable to run `git` inside a LINKED worktree. A linked worktree's `.git`
// is a FILE ("gitdir: <parent>/.git/worktrees/<name>"), not a directory, and
// the real object store / refs / config live in the parent repo's `.git`
// (the "common dir") which is OUTSIDE the worktree tree — so without these
// grants `git status`/`git commit` fail closed inside the sandbox.
//
// Returns the per-worktree gitdir AND the common dir (both need to be
// writable: git writes HEAD/index/logs under the worktree gitdir and objects
// under the common dir). Returns nil for a normal checkout (`.git` is a
// directory already under workDir, hence already covered by the AllowWrite
// worktree grant) or when workDir has no `.git` at all. Pure file parsing —
// no `git` subprocess, so it works offline and with no git installed.
func WorktreeGitDirs(workDir string) []string {
	if workDir == "" {
		return nil
	}
	dotGit := filepath.Join(workDir, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil {
		return nil
	}
	if info.IsDir() {
		// Normal checkout: the object store is under workDir/.git, already
		// inside the worktree AllowWrite grant. Nothing extra to add.
		return nil
	}
	// Linked worktree: `.git` is a file "gitdir: <path>".
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return nil
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return nil
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if gitDir == "" {
		return nil
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(workDir, gitDir)
	}
	gitDir = filepath.Clean(gitDir)
	out := []string{gitDir}

	// Resolve the common dir. Prefer the authoritative `commondir` file the
	// worktree gitdir carries (usually "../.." relative to gitDir); fall
	// back to the structural default <parent>/.git = dirname(dirname(gitDir))
	// (gitDir is <parent>/.git/worktrees/<name>).
	commonDir := ""
	if cd, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		rel := strings.TrimSpace(string(cd))
		if rel != "" {
			if filepath.IsAbs(rel) {
				commonDir = filepath.Clean(rel)
			} else {
				commonDir = filepath.Clean(filepath.Join(gitDir, rel))
			}
		}
	}
	if commonDir == "" {
		commonDir = filepath.Dir(filepath.Dir(gitDir))
	}
	if commonDir != "" && commonDir != gitDir {
		out = append(out, commonDir)
	}
	return out
}

// DefaultWriteCaches returns the toolchain cache paths kept writable so
// build/test commands work inside the sandbox (see Policy.WriteCaches).
// Returns nil when home is unknown.
func DefaultWriteCaches(home string) []string {
	if home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".cache"),
		filepath.Join(home, "go", "pkg"),
		filepath.Join(home, ".npm"),
	}
}
