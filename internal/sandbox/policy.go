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
// R1_NATIVE_SANDBOX means "do not engage" (engaged=false), because the
// current containment only wraps the bash tool — notebook_cell_run and
// cron_create still exec on the host, and a sandboxed bash cannot run
// git in a linked worktree — so a default-on sandbox would be false
// assurance. Engaging requires an explicit non-"off" value:
//
//	R1_NATIVE_SANDBOX=on|auto|bwrap|landlock|docker  -> engaged, that mode
//	                                          ("on" is an alias for auto)
//	R1_NATIVE_SANDBOX=off  or unset            -> not engaged
//
// Flipping this to default-on is gated on completing containment (route
// or deny notebook_cell_run/cron_create when engaged; allowlist the
// worktree's real .git). See docs/native-sandbox.md.
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

// DefaultDenyRead returns the credential paths masked from sandboxed
// commands. This is a blocklist over an otherwise-readable host fs (bwrap
// backend), so it can never be complete — secrets outside this list (e.g.
// /etc/secrets) remain readable there; the Landlock backend's allow-list
// is stricter. Returns nil when home is unknown so an empty $HOME can
// never expand an entry to a filesystem root.
func DefaultDenyRead(home string) []string {
	if home == "" {
		return nil
	}
	rel := []string{
		".ssh", ".aws", ".gnupg", ".netrc", ".docker", ".kube",
		".claude", ".codex",
		filepath.Join(".config", "gh"),
		filepath.Join(".config", "gcloud"),
	}
	out := make([]string, 0, len(rel))
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
