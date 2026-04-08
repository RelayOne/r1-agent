package engine

import (
	"context"
	"os/exec"
	"syscall"
)

// wrapInDocker wraps a prepared command in a docker run invocation for
// container pool execution. The container gets:
// - The credential volume mounted at the container config dir
// - The worktree bind-mounted at the same host path
// - The runtime dir bind-mounted at the same host path
// - Environment variables passed through via -e flags
func wrapInDocker(ctx context.Context, prepared PreparedCommand, spec RunSpec) *exec.Cmd {
	configDir := spec.ContainerConfigDir
	if configDir == "" {
		configDir = "/config"
	}

	args := []string{
		"run", "--rm",
		"-v", spec.ContainerVol + ":" + configDir,
		"-v", spec.WorktreeDir + ":" + spec.WorktreeDir,
		"-v", spec.RuntimeDir + ":" + spec.RuntimeDir,
		"-w", prepared.Dir,
	}

	// Pass environment as -e flags
	for _, e := range prepared.Env {
		args = append(args, "-e", e)
	}

	// Override config dir to point to the container mount
	if spec.PoolConfigDir != "" {
		args = append(args, "-e", "CLAUDE_CONFIG_DIR="+configDir)
		args = append(args, "-e", "CODEX_HOME="+configDir)
	}

	args = append(args, spec.ContainerImage)
	args = append(args, prepared.Binary)
	args = append(args, prepared.Args...)

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}
