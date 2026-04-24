package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/env"
)

// mockDockerRun records calls and returns scripted responses.
type mockDocker struct {
	mu       sync.Mutex
	calls    [][]string
	response map[string]mockResp
}

type mockResp struct {
	out string
	err error
}

func newMockDocker() *mockDocker {
	return &mockDocker{
		response: make(map[string]mockResp),
	}
}

func (m *mockDocker) run(_ context.Context, args []string) (string, error) {
	m.mu.Lock()
	m.calls = append(m.calls, args)
	m.mu.Unlock()

	key := args[0]
	if len(args) > 1 {
		key = strings.Join(args[:2], " ")
	}

	m.mu.Lock()
	resp, ok := m.response[key]
	m.mu.Unlock()
	if ok {
		return resp.out, resp.err
	}

	// Default: return a fake container/network ID.
	switch args[0] {
	case "network":
		return "net-abc123\n", nil
	case "run":
		return "container-abc123\n", nil
	case "exec":
		return "exec output\n", nil
	case "rm":
		return "", nil
	case "cp":
		return "", nil
	case "inspect":
		return "8080", nil
	case "commit":
		return "sha256:abc123", nil
	}
	return "", nil
}

func (m *mockDocker) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockDocker) callContains(substr string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, call := range m.calls {
		for _, arg := range call {
			if strings.Contains(arg, substr) {
				return true
			}
		}
	}
	return false
}

func setup(t *testing.T) (*Backend, *mockDocker) {
	t.Helper()
	mock := newMockDocker()
	origRun := dockerRunFunc
	origCheck := checkDockerFunc
	origExec := execInContainerFunc
	dockerRunFunc = mock.run
	checkDockerFunc = func(_ context.Context) error { return nil }
	execInContainerFunc = func(ctx context.Context, containerID, dir string, cmdArgs []string, opts env.ExecOpts) (*env.ExecResult, error) {
		args := []string{"exec", "-w", dir}
		for k, v := range opts.Env {
			args = append(args, "-e", k+"="+v)
		}
		args = append(args, containerID)
		args = append(args, cmdArgs...)

		mock.mu.Lock()
		mock.calls = append(mock.calls, args)
		mock.mu.Unlock()

		// Check for scripted error responses.
		key := "exec " + containerID
		mock.mu.Lock()
		resp, ok := mock.response[key]
		mock.mu.Unlock()
		if ok && resp.err != nil {
			return nil, resp.err
		}

		return &env.ExecResult{
			Stdout:   "exec output\n",
			ExitCode: 0,
		}, nil
	}
	t.Cleanup(func() {
		dockerRunFunc = origRun
		checkDockerFunc = origCheck
		execInContainerFunc = origExec
	})
	return New(), mock
}

func TestProvisionBasic(t *testing.T) {
	b, mock := setup(t)
	ctx := context.Background()

	h, err := b.Provision(ctx, env.Spec{
		BaseImage: "golang:1.22",
	})
	if err != nil {
		t.Fatalf("Provision failed: %v", err)
	}
	if h.Backend != env.BackendDocker {
		t.Errorf("backend=%q, want docker", h.Backend)
	}
	if h.WorkDir != "/workspace" {
		t.Errorf("workdir=%q, want /workspace", h.WorkDir)
	}
	if h.Meta["container_id"] != "container-abc123" {
		t.Errorf("container_id=%q, want container-abc123", h.Meta["container_id"])
	}

	// Should have called network create + run
	if !mock.callContains("create") {
		t.Error("should call network create")
	}
	if !mock.callContains("sleep") {
		t.Error("should run container with sleep infinity")
	}
}

func TestProvisionCustomWorkDir(t *testing.T) {
	b, _ := setup(t)
	ctx := context.Background()

	h, err := b.Provision(ctx, env.Spec{WorkDir: "/app"})
	if err != nil {
		t.Fatalf("Provision failed: %v", err)
	}
	if h.WorkDir != "/app" {
		t.Errorf("workdir=%q, want /app", h.WorkDir)
	}
}

func TestProvisionDockerNotAvailable(t *testing.T) {
	b := New()
	origCheck := checkDockerFunc
	checkDockerFunc = func(_ context.Context) error {
		return fmt.Errorf("docker not available")
	}
	t.Cleanup(func() { checkDockerFunc = origCheck })

	_, err := b.Provision(context.Background(), env.Spec{})
	if err == nil {
		t.Fatal("expected error when docker not available")
	}
	if !strings.Contains(err.Error(), "docker") {
		t.Errorf("error should mention docker: %v", err)
	}
}

func TestProvisionWithServices(t *testing.T) {
	b, mock := setup(t)
	ctx := context.Background()

	h, err := b.Provision(ctx, env.Spec{
		Services: []env.ServiceSpec{
			{Name: "postgres", Image: "postgres:16", Ports: []int{5432}},
			{Name: "redis", Image: "redis:7", Ports: []int{6379}},
		},
	})
	if err != nil {
		t.Fatalf("Provision failed: %v", err)
	}

	// Should have created service containers
	if !mock.callContains("postgres") {
		t.Error("should create postgres container")
	}
	if !mock.callContains("redis") {
		t.Error("should create redis container")
	}

	// Services should be queryable.
	addr, err := b.Service(ctx, h, "postgres")
	if err != nil {
		t.Fatalf("Service postgres: %v", err)
	}
	if addr.Port == 0 {
		t.Error("postgres port should be set")
	}
}

func TestProvisionWithSetupCommands(t *testing.T) {
	b, mock := setup(t)
	ctx := context.Background()

	_, err := b.Provision(ctx, env.Spec{
		SetupCommands: []string{"apt-get update", "apt-get install -y curl"},
	})
	if err != nil {
		t.Fatalf("Provision failed: %v", err)
	}

	if !mock.callContains("apt-get update") {
		t.Error("should run setup command: apt-get update")
	}
	if !mock.callContains("apt-get install -y curl") {
		t.Error("should run setup command: apt-get install")
	}
}

func TestProvisionSetupCommandFailure(t *testing.T) {
	b := New()
	mock := newMockDocker()
	origRun := dockerRunFunc
	origCheck := checkDockerFunc
	origExec := execInContainerFunc
	dockerRunFunc = mock.run
	checkDockerFunc = func(_ context.Context) error { return nil }
	// Make exec return a failed result for setup commands.
	execInContainerFunc = func(_ context.Context, containerID, dir string, cmdArgs []string, opts env.ExecOpts) (*env.ExecResult, error) {
		return &env.ExecResult{
			ExitCode: 1,
			Stderr:   "bad-command: not found",
		}, nil
	}
	t.Cleanup(func() {
		dockerRunFunc = origRun
		checkDockerFunc = origCheck
		execInContainerFunc = origExec
	})
	ctx := context.Background()

	_, err := b.Provision(ctx, env.Spec{
		SetupCommands: []string{"bad-command"},
	})
	if err == nil {
		t.Fatal("expected error for failing setup command")
	}
	if !strings.Contains(err.Error(), "setup command") {
		t.Errorf("error should mention setup command: %v", err)
	}

	// Should have cleaned up.
	if !mock.callContains("rm") {
		t.Error("should cleanup on failure")
	}
}

func TestExec(t *testing.T) {
	b, _ := setup(t)
	ctx := context.Background()

	h, err := b.Provision(ctx, env.Spec{})
	if err != nil {
		t.Fatal(err)
	}

	result, err := b.Exec(ctx, h, []string{"echo", "hello"}, env.ExecOpts{})
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if !strings.Contains(result.Stdout, "exec output") {
		t.Errorf("stdout=%q, want exec output", result.Stdout)
	}
}

func TestExecNilHandle(t *testing.T) {
	b, _ := setup(t)
	_, err := b.Exec(context.Background(), nil, []string{"echo"}, env.ExecOpts{})
	if !errors.Is(err, env.ErrNotProvisioned) {
		t.Errorf("err=%v, want ErrNotProvisioned", err)
	}
}

func TestExecAfterTeardown(t *testing.T) {
	b, _ := setup(t)
	ctx := context.Background()

	h, err := b.Provision(ctx, env.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	b.Teardown(ctx, h)

	_, err = b.Exec(ctx, h, []string{"echo"}, env.ExecOpts{})
	if !errors.Is(err, env.ErrAlreadyTornDown) {
		t.Errorf("err=%v, want ErrAlreadyTornDown", err)
	}
}

func TestExecWithOptions(t *testing.T) {
	b, mock := setup(t)
	ctx := context.Background()

	h, err := b.Provision(ctx, env.Spec{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = b.Exec(ctx, h, []string{"ls"}, env.ExecOpts{
		Dir: "/tmp",
		Env: map[string]string{"FOO": "bar"},
	})
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}

	// Check that -w and -e flags were passed.
	if !mock.callContains("/tmp") {
		t.Error("should pass -w /tmp")
	}
	if !mock.callContains("FOO=bar") {
		t.Error("should pass -e FOO=bar")
	}
}

func TestCopyIn(t *testing.T) {
	b, mock := setup(t)
	ctx := context.Background()

	h, err := b.Provision(ctx, env.Spec{})
	if err != nil {
		t.Fatal(err)
	}

	err = b.CopyIn(ctx, h, "/local/file.txt", "/remote/file.txt")
	if err != nil {
		t.Fatalf("CopyIn failed: %v", err)
	}

	if !mock.callContains("/local/file.txt") {
		t.Error("should pass source path")
	}
}

func TestCopyInNilHandle(t *testing.T) {
	b, _ := setup(t)
	err := b.CopyIn(context.Background(), nil, "/a", "/b")
	if !errors.Is(err, env.ErrNotProvisioned) {
		t.Errorf("err=%v, want ErrNotProvisioned", err)
	}
}

func TestCopyOut(t *testing.T) {
	b, mock := setup(t)
	ctx := context.Background()

	h, err := b.Provision(ctx, env.Spec{})
	if err != nil {
		t.Fatal(err)
	}

	err = b.CopyOut(ctx, h, "/remote/out.txt", "/local/out.txt")
	if err != nil {
		t.Fatalf("CopyOut failed: %v", err)
	}

	if !mock.callContains("/local/out.txt") {
		t.Error("should pass dest path")
	}
}

func TestCopyOutNilHandle(t *testing.T) {
	b, _ := setup(t)
	err := b.CopyOut(context.Background(), nil, "/a", "/b")
	if !errors.Is(err, env.ErrNotProvisioned) {
		t.Errorf("err=%v, want ErrNotProvisioned", err)
	}
}

func TestServiceNotFound(t *testing.T) {
	b, _ := setup(t)
	ctx := context.Background()

	h, err := b.Provision(ctx, env.Spec{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = b.Service(ctx, h, "nonexistent")
	if !errors.Is(err, env.ErrServiceNotFound) {
		t.Errorf("err=%v, want ErrServiceNotFound", err)
	}
}

func TestServiceNilHandle(t *testing.T) {
	b, _ := setup(t)
	_, err := b.Service(context.Background(), nil, "pg")
	if !errors.Is(err, env.ErrNotProvisioned) {
		t.Errorf("err=%v, want ErrNotProvisioned", err)
	}
}

func TestTeardown(t *testing.T) {
	b, mock := setup(t)
	ctx := context.Background()

	h, err := b.Provision(ctx, env.Spec{
		Services: []env.ServiceSpec{
			{Name: "postgres", Image: "postgres:16"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	callsBefore := mock.callCount()
	err = b.Teardown(ctx, h)
	if err != nil {
		t.Fatalf("Teardown failed: %v", err)
	}

	// Should have made cleanup calls (rm, network rm).
	if mock.callCount() <= callsBefore {
		t.Error("Teardown should make docker cleanup calls")
	}
}

func TestTeardownIdempotent(t *testing.T) {
	b, _ := setup(t)
	ctx := context.Background()

	h, err := b.Provision(ctx, env.Spec{})
	if err != nil {
		t.Fatal(err)
	}

	// First teardown should work.
	if err := b.Teardown(ctx, h); err != nil {
		t.Fatalf("first teardown: %v", err)
	}
	// Second teardown should be no-op.
	if err := b.Teardown(ctx, h); err != nil {
		t.Fatalf("second teardown: %v", err)
	}
}

func TestTeardownNilHandle(t *testing.T) {
	b, _ := setup(t)
	if err := b.Teardown(context.Background(), nil); err != nil {
		t.Fatalf("Teardown(nil) should not error: %v", err)
	}
}

func TestCost(t *testing.T) {
	b, _ := setup(t)
	ctx := context.Background()

	h, err := b.Provision(ctx, env.Spec{})
	if err != nil {
		t.Fatal(err)
	}

	cost, err := b.Cost(ctx, h)
	if err != nil {
		t.Fatalf("Cost: %v", err)
	}
	if cost.TotalUSD != 0 {
		t.Errorf("docker cost should be 0, got %f", cost.TotalUSD)
	}
	if cost.Elapsed < 0 {
		t.Error("elapsed should be non-negative")
	}
}

func TestCostNilHandle(t *testing.T) {
	b, _ := setup(t)
	_, err := b.Cost(context.Background(), nil)
	if !errors.Is(err, env.ErrNotProvisioned) {
		t.Errorf("err=%v, want ErrNotProvisioned", err)
	}
}

func TestSnapshot(t *testing.T) {
	b, mock := setup(t)
	ctx := context.Background()

	h, err := b.Provision(ctx, env.Spec{})
	if err != nil {
		t.Fatal(err)
	}

	snapID, err := b.Snapshot(ctx, h)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapID == "" {
		t.Error("snapshot ID should not be empty")
	}

	if !mock.callContains("commit") {
		t.Error("should call docker commit")
	}
}

func TestSnapshotNilHandle(t *testing.T) {
	b, _ := setup(t)
	_, err := b.Snapshot(context.Background(), nil)
	if !errors.Is(err, env.ErrNotProvisioned) {
		t.Errorf("err=%v, want ErrNotProvisioned", err)
	}
}

func TestRestore(t *testing.T) {
	b, mock := setup(t)
	ctx := context.Background()

	h, err := b.Provision(ctx, env.Spec{})
	if err != nil {
		t.Fatal(err)
	}

	err = b.Restore(ctx, h, "stoke-snap-123")
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Should rm old container and run new one from snapshot.
	if !mock.callContains("stoke-snap-123") {
		t.Error("should run container from snapshot image")
	}
}

func TestRestoreNilHandle(t *testing.T) {
	b, _ := setup(t)
	err := b.Restore(context.Background(), nil, "snap-1")
	if !errors.Is(err, env.ErrNotProvisioned) {
		t.Errorf("err=%v, want ErrNotProvisioned", err)
	}
}

func TestProvisionResourceLimits(t *testing.T) {
	b, mock := setup(t)
	ctx := context.Background()

	_, err := b.Provision(ctx, env.Spec{
		CPUs:     4,
		MemoryMB: 2048,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !mock.callContains("--cpus") {
		t.Error("should set CPU limit")
	}
	if !mock.callContains("2048m") {
		t.Error("should set memory limit")
	}
}

func TestProvisionWithVolumes(t *testing.T) {
	b, mock := setup(t)
	ctx := context.Background()

	_, err := b.Provision(ctx, env.Spec{
		Volumes: []env.VolumeSpec{
			{Source: "/host/data", Target: "/container/data"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !mock.callContains("/host/data:/container/data") {
		t.Error("should mount volume")
	}
}

func TestProvisionWithEnvVars(t *testing.T) {
	b, mock := setup(t)
	ctx := context.Background()

	_, err := b.Provision(ctx, env.Spec{
		Env: map[string]string{"DATABASE_URL": "postgres://localhost"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !mock.callContains("DATABASE_URL=postgres://localhost") {
		t.Error("should pass environment variable")
	}
}

func TestProvisionWithExpose(t *testing.T) {
	b, mock := setup(t)
	ctx := context.Background()

	_, err := b.Provision(ctx, env.Spec{
		Expose: []env.ExposeSpec{{Port: 3000}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !mock.callContains("3000:3000") {
		t.Error("should expose port")
	}
}

func TestInterfaceCompliance(t *testing.T) {
	var _ env.Environment = (*Backend)(nil)
	var _ env.Snapshotter = (*Backend)(nil)
}

func TestConcurrentExec(t *testing.T) {
	b, _ := setup(t)
	ctx := context.Background()

	h, err := b.Provision(ctx, env.Spec{})
	if err != nil {
		t.Fatal(err)
	}

	// Run 10 concurrent execs.
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := b.Exec(ctx, h, []string{"echo", fmt.Sprintf("worker-%d", i)}, env.ExecOpts{})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent exec failed: %v", err)
	}
}

func TestProvisionNetworkFailure(t *testing.T) {
	b := New()
	origRun := dockerRunFunc
	origCheck := checkDockerFunc
	checkDockerFunc = func(_ context.Context) error { return nil }
	dockerRunFunc = func(_ context.Context, args []string) (string, error) {
		if args[0] == "network" {
			return "", fmt.Errorf("network create failed")
		}
		return "", nil
	}
	t.Cleanup(func() {
		dockerRunFunc = origRun
		checkDockerFunc = origCheck
	})

	_, err := b.Provision(context.Background(), env.Spec{})
	if err == nil {
		t.Fatal("expected error on network failure")
	}
	if !strings.Contains(err.Error(), "network") {
		t.Errorf("error should mention network: %v", err)
	}
}

func TestProvisionDefaultImage(t *testing.T) {
	b, mock := setup(t)
	ctx := context.Background()

	_, err := b.Provision(ctx, env.Spec{})
	if err != nil {
		t.Fatal(err)
	}

	if !mock.callContains("ubuntu:22.04") {
		t.Error("should use default image ubuntu:22.04")
	}
}

func TestElapsedCostTracking(t *testing.T) {
	b, _ := setup(t)
	ctx := context.Background()

	h, err := b.Provision(ctx, env.Spec{})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(10 * time.Millisecond)

	cost, err := b.Cost(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if cost.Elapsed < 10*time.Millisecond {
		t.Errorf("elapsed=%v, should be at least 10ms", cost.Elapsed)
	}
}
