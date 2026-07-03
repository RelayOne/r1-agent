package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// bwrapWrapper contains commands with bubblewrap mount namespaces:
// read-only host fs, writable binds for the worktree + toolchain caches,
// tmpfs//dev-null masks over credential paths, fresh tmpfs /tmp, and
// optional --unshare-net egress cutoff.
//
// Known narrowing (documented, matches the recon assessment): the baseline
// is `--ro-bind / /`, i.e. the whole host filesystem is READABLE minus the
// DenyRead masks. That matches the CLI sandbox's practical behavior but is
// weaker than a strict allow-list; the Landlock backend is stricter.
type bwrapWrapper struct{}

func (b *bwrapWrapper) Name() string { return ModeBwrap }

// bwrapCanaryTimeout bounds the Available() probe. The probe catches
// hosts where the binary exists but unprivileged user namespaces are
// blocked (e.g. Ubuntu's kernel.apparmor_restrict_unprivileged_userns=1
// without the bwrap AppArmor profile, or docker/LXC guests) — there
// LookPath succeeds but every real invocation would fail at runtime.
const bwrapCanaryTimeout = 5 * time.Second

// Available checks for the bwrap binary and runs a canary with the same
// namespace-critical flags real commands use. Probed per Select call (once
// per dispatch wiring), not cached process-wide, so tests and long-lived
// daemons observe PATH/host changes.
func (b *bwrapWrapper) Available(Policy) error {
	path, err := exec.LookPath("bwrap")
	if err != nil {
		return fmt.Errorf("bwrap binary not found: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), bwrapCanaryTimeout)
	defer cancel()
	canary := exec.CommandContext(ctx, path,
		"--die-with-parent", "--unshare-pid",
		"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc",
		"--", "true")
	if out, err := canary.CombinedOutput(); err != nil {
		return fmt.Errorf("bwrap present but cannot create a sandbox (userns blocked?): %v: %s",
			err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (b *bwrapWrapper) Command(ctx context.Context, shellCmd, workDir string, p Policy) (*exec.Cmd, error) {
	args := bwrapArgs(shellCmd, workDir, p)
	cmd := exec.CommandContext(ctx, "bwrap", args...) // #nosec G204 -- binary name is hardcoded; argv is harness-constructed.
	cmd.Dir = workDir
	return cmd, nil
}

// mountOp is one bind/mask operation. Ops are sorted by path depth
// (shallower first) before emission because bwrap applies mounts in argv
// order and later mounts stack on top: a DenyRead mask nested inside an
// AllowWrite bind (worktree/.env) must be emitted AFTER the bind or the
// bind would shadow it, and an AllowWrite bind nested inside a masked
// tree must land after the mask to stay usable.
type mountOp struct {
	depth int
	// class breaks depth ties deterministically: ro-binds, then rw-binds,
	// then deny masks — so at equal depth a mask always wins.
	class int
	args  []string
}

// bwrapArgs builds the full bwrap argv for one bash command. Pure except
// for os.Stat calls that decide dir-vs-file handling and skip nonexistent
// entries (bwrap fails hard on missing bind sources; a nonexistent
// DenyRead path cannot be read anyway).
func bwrapArgs(shellCmd, workDir string, p Policy) []string {
	args := []string{
		// Second kill layer under the caller's process-group kill: the
		// whole namespace dies with the wrapper.
		"--die-with-parent",
		// Own session: blocks TIOCSTI tty injection back into the
		// harness terminal. Bash tool output is CombinedOutput (no
		// tty), so this changes nothing visible.
		"--new-session",
		"--unshare-pid",
	}
	if !p.AllowEgress {
		args = append(args, "--unshare-net")
	}
	args = append(args,
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		// Fresh writable /tmp: hides host /tmp contents from the
		// sandboxed command AND gives build tools the writable TMPDIR
		// they assume. Binds below land on top for paths under /tmp
		// (e.g. worktrees created there).
		"--tmpfs", "/tmp",
	)

	var ops []mountOp
	add := func(path string, class int, opArgs ...string) {
		// Depth = separator count of the cleaned absolute path; enough
		// for a strict-ancestor ordering (nested paths always count
		// higher than their ancestors).
		ops = append(ops, mountOp{
			depth: strings.Count(filepath.Clean(path), string(filepath.Separator)),
			class: class,
			args:  opArgs,
		})
	}
	for _, ro := range p.AllowRead {
		ro = filepath.Clean(ro)
		if _, err := os.Stat(ro); err != nil {
			continue
		}
		add(ro, 0, "--ro-bind", ro, ro)
	}
	for _, rw := range append(append([]string(nil), p.AllowWrite...), p.WriteCaches...) {
		rw = filepath.Clean(rw)
		if _, err := os.Stat(rw); err != nil {
			continue
		}
		add(rw, 1, "--bind", rw, rw)
	}
	for _, deny := range p.DenyRead {
		deny = filepath.Clean(deny)
		st, err := os.Stat(deny)
		if err != nil {
			continue
		}
		if st.IsDir() {
			// tmpfs mask: reads as an empty dir; writes go to the
			// ephemeral tmpfs, never the host.
			add(deny, 2, "--tmpfs", deny)
		} else {
			add(deny, 2, "--ro-bind", "/dev/null", deny)
		}
	}
	sort.SliceStable(ops, func(i, j int) bool {
		if ops[i].depth != ops[j].depth {
			return ops[i].depth < ops[j].depth
		}
		return ops[i].class < ops[j].class
	})
	for _, op := range ops {
		args = append(args, op.args...)
	}

	args = append(args, "--chdir", workDir, "--", "bash", "-c", shellCmd)
	return args
}
