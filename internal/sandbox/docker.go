package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// dockerWrapper is the coarse container fallback, modeled on the hardened
// argv in internal/engine/container.go (pool runs). Explicit opt-in only
// (Mode=docker + R1_SANDBOX_IMAGE); never auto-selected, because the image
// must carry the project's toolchain and that choice belongs to the
// operator. DenyRead is inherently satisfied: the host fs simply is not
// mounted beyond the AllowRead/AllowWrite binds.
type dockerWrapper struct{}

func (d *dockerWrapper) Name() string { return ModeDocker }

func (d *dockerWrapper) Available(p Policy) error {
	if p.DockerImage == "" {
		return errors.New("docker sandbox requires an image (set R1_SANDBOX_IMAGE)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker binary not found: %w", err)
	}
	return nil
}

func (d *dockerWrapper) Command(ctx context.Context, shellCmd, workDir string, p Policy) (*exec.Cmd, error) {
	args := dockerArgs(shellCmd, workDir, p)
	cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- binary name is hardcoded; argv is harness-constructed.
	cmd.Dir = workDir
	return cmd, nil
}

func dockerArgs(shellCmd, workDir string, p Policy) []string {
	args := []string{
		"run", "--rm",
		"--security-opt=no-new-privileges",
		"--cap-drop=ALL",
	}
	// Run as the invoking host user so files the command creates in the
	// bind-mounted worktree are owned by that user, not root. A root-owned
	// worktree breaks the host-side shadow-checkpoint / commit / cleanup
	// that runs after the sandboxed command returns. On Windows Getuid
	// returns -1; skip the flag there (docker-for-windows maps ownership
	// differently and a negative uid is invalid).
	if uid := os.Getuid(); uid >= 0 {
		args = append(args, "--user", strconv.Itoa(uid)+":"+strconv.Itoa(os.Getgid()))
	}
	if p.AllowEgress {
		args = append(args, "--network=bridge")
	} else {
		args = append(args, "--network=none")
	}
	for _, ro := range p.AllowRead {
		ro = filepath.Clean(ro)
		if _, err := os.Stat(ro); err != nil {
			continue
		}
		args = append(args, "-v", ro+":"+ro+":ro")
	}
	for _, rw := range p.AllowWrite {
		rw = filepath.Clean(rw)
		if _, err := os.Stat(rw); err != nil {
			continue
		}
		args = append(args, "-v", rw+":"+rw)
	}
	args = append(args, "-w", workDir, p.DockerImage, "bash", "-c", shellCmd)
	return args
}
