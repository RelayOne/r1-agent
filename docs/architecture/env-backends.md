# Environment Backends

Package: `internal/env/`

## Interface

```go
type Environment interface {
    Provision(ctx, spec) (*Handle, error)
    Exec(ctx, handle, cmd, opts) (*ExecResult, error)
    CopyIn(ctx, handle, hostPath, envPath) error
    CopyOut(ctx, handle, envPath, hostPath) error
    Service(ctx, handle, serviceName) (*ServiceAddr, error)
    Teardown(ctx, handle) error
    Cost(ctx, handle) (*CostEstimate, error)
}
```

Optional: `Snapshotter` interface adds `Snapshot()` and `Restore()`.

## Backends

### InProc (`internal/env/inproc/`)

Direct `os/exec` on the host machine. No isolation.

- **Pros**: Zero overhead, fastest, no dependencies
- **Cons**: No isolation, cannot limit filesystem/network
- **Use when**: Local development, trusted code, CI environments

### Docker (`internal/env/docker/`)

510 LOC with 729 LOC tests. Full container lifecycle management.

- Creates isolated Docker networks per environment
- Spawns service containers (Postgres, Redis, etc.) before main container
- Main container: `docker run -d --name <id> --network <net> -w /workspace <image> sleep infinity`
- Execution via `docker exec`
- Point-in-time snapshots via `docker commit`
- Cleanup: containers + network removal (idempotent)

- **Pros**: Full isolation, service dependencies, snapshots
- **Cons**: Docker required, overhead per container
- **Use when**: Default for developer machines, tasks needing service dependencies

### SSH (`internal/env/ssh/`)

Remote execution via SSH connection.

- **Pros**: Use existing infrastructure, no container overhead
- **Cons**: Manual setup, limited isolation
- **Use when**: Existing dev servers, legacy infrastructure

### Fly (`internal/env/fly/`)

Fly-compatible REST API client. Works against Fly.io and Flare.

- **Pros**: Cloud-native scaling, pay-per-use
- **Cons**: Network latency, API key required
- **Use when**: Burst compute, CI/CD at scale

### Ember (`internal/env/ember/`)

458 LOC backend + 197 LOC AI client. Ember's `/v1/workers` API.

- Routes AI requests through Ember's managed model pool
- Spawns Flare VMs for burst compute
- Reports progress to Ember dashboard

- **Pros**: Managed infrastructure, integrated billing, team visibility
- **Cons**: Requires Ember account
- **Use when**: Teams using Ember for cloud dev machines

## Choosing a Backend

```yaml
# stoke.yaml
env:
  backend: docker    # inproc | docker | ssh | fly | ember
  image: "node:22"   # base image for docker/fly/ember
  services:
    - name: postgres
      image: "postgres:16"
```

The backend is selected per-task or globally. The `Environment` interface
ensures all backends expose the same API to the rest of the system.
