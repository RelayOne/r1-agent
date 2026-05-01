package engine

import (
	"context"
	"os"
	"os/exec"

	"github.com/RelayOne/r1/internal/procutil"
)

// containerNetworkMode returns the docker --network flag value for
// pool runs. Defaults to "none" so the container's process cannot
// exfiltrate the mounted Claude/Codex credentials over the network.
//
// R1-V1 audit Domain 8 P0 #1: prior to this change, container pool
// runs inherited the docker default network and the OAuth token in
// the credential volume could be uploaded to any HTTPS endpoint
// reachable from the container. With --network=none the container
// has no network namespace at all; egress proxying through the host
// harness is the supported way to reach upstream API providers.
//
// Operators that need outbound reachability (e.g. running a CLI
// model that talks to an external API directly inside the pool
// container) can opt out via R1_CONTAINER_NETWORK / STOKE_CONTAINER_NETWORK,
// e.g. "host" or a docker network name. The opt-out is logged via
// the spec metadata path used by the engine; no silent fallback.
func containerNetworkMode() string {
	if v := os.Getenv("R1_CONTAINER_NETWORK"); v != "" {
		return v
	}
	if v := os.Getenv("STOKE_CONTAINER_NETWORK"); v != "" {
		return v
	}
	return "none"
}

// wrapInDocker wraps a prepared command in a docker run invocation for
// container pool execution. The container gets:
// - The credential volume mounted at the container config dir
// - The worktree bind-mounted at the same host path
// - The runtime dir bind-mounted at the same host path
// - Environment variables passed through via -e flags
// - --network=none by default (operator-overridable; see containerNetworkMode)
func wrapInDocker(ctx context.Context, prepared PreparedCommand, spec RunSpec) *exec.Cmd {
	configDir := spec.ContainerConfigDir
	if configDir == "" {
		configDir = "/config"
	}

	args := []string{
		"run", "--rm",
		"--security-opt=no-new-privileges",
		"--cap-drop=ALL",
		"--network=" + containerNetworkMode(),
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

	cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- CLI runner launches vetted provider binary with Stoke-generated args.
	procutil.ConfigureProcessGroup(cmd)
	return cmd
}
