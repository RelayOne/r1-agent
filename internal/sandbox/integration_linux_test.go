package sandbox

// Guarded end-to-end enforcement tests. Every test t.Skips cleanly when
// the host cannot run the backend (CI is a bare golang container where
// bwrap is absent and Landlock syscalls may be seccomp-blocked), so the
// suite stays green offline and without host privileges.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireBwrap(t *testing.T) {
	t.Helper()
	w := &bwrapWrapper{}
	if err := w.Available(Policy{}); err != nil {
		t.Skipf("bwrap unavailable on this host: %v", err)
	}
}

func runWrapped(t *testing.T, w Wrapper, shellCmd, workDir string, p Policy) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd, err := w.Command(ctx, shellCmd, workDir, p)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestBwrapEnforcement(t *testing.T) {
	requireBwrap(t)
	w := &bwrapWrapper{}
	work := t.TempDir()
	secretDir := filepath.Join(work, "secrets")
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(secretDir, "key")
	if err := os.WriteFile(secretFile, []byte("TOPSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	pol := Policy{
		Mode:        ModeBwrap,
		AllowWrite:  []string{work},
		DenyRead:    []string{secretDir},
		AllowEgress: false,
	}

	t.Run("write inside allow-write succeeds on host", func(t *testing.T) {
		out, err := runWrapped(t, w, "echo data > allowed.txt", work, pol)
		if err != nil {
			t.Fatalf("write failed: %v: %s", err, out)
		}
		if _, err := os.Stat(filepath.Join(work, "allowed.txt")); err != nil {
			t.Errorf("file not visible on host: %v", err)
		}
	})

	t.Run("deny-read dir inside allow-write is masked", func(t *testing.T) {
		out, _ := runWrapped(t, w, "ls secrets | wc -l && cat secrets/key 2>&1; true", work, pol)
		if strings.Contains(out, "TOPSECRET") {
			t.Errorf("masked secret leaked: %q", out)
		}
		if !strings.HasPrefix(strings.TrimSpace(out), "0") {
			t.Errorf("masked dir should list empty, got: %q", out)
		}
	})

	t.Run("write outside allow-write does not reach host", func(t *testing.T) {
		outside := t.TempDir() // under /tmp → shadowed by the sandbox tmpfs
		out, _ := runWrapped(t, w, "touch "+outside+"/escaped 2>&1; true", work, pol)
		if _, err := os.Stat(filepath.Join(outside, "escaped")); err == nil {
			t.Errorf("write escaped the sandbox to the host: %s", out)
		}
	})

	t.Run("host fs outside binds is read-only", func(t *testing.T) {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			t.Skip("no home dir")
		}
		probe := filepath.Join(home, ".r1-sandbox-ro-probe")
		out, _ := runWrapped(t, w, "touch "+probe+" 2>&1; true", work, pol)
		if _, err := os.Stat(probe); err == nil {
			os.Remove(probe)
			t.Fatalf("host home was writable inside sandbox: %s", out)
		}
		if !strings.Contains(out, "Read-only file system") {
			t.Logf("expected EROFS message, got: %q (still not created on host)", out)
		}
	})

	t.Run("egress denied blocks tcp connect", func(t *testing.T) {
		// 192.0.2.1 (TEST-NET-1): in the empty netns there is no route
		// at all, so the connect fails immediately; loopback is not a
		// valid probe because a fresh netns still has a lo device.
		out, _ := runWrapped(t, w, "timeout 5 bash -c 'exec 3<>/dev/tcp/192.0.2.1/80 && echo CONNECTED' 2>&1; true", work, pol)
		if strings.Contains(out, "CONNECTED") {
			t.Fatalf("tcp connect succeeded under --unshare-net: %q", out)
		}
		if !strings.Contains(strings.ToLower(out), "unreachable") {
			t.Errorf("expected network-unreachable under --unshare-net, got: %q", out)
		}
	})
}

func TestLandlockEnforcement(t *testing.T) {
	if probeLandlockABI() < 1 {
		t.Skip("landlock unavailable on this host (kernel or seccomp)")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir for out-of-allowlist probe")
	}
	// The secret must live OUTSIDE every allowed tree; /tmp is rw-allowed
	// under Landlock, so use the real home dir.
	secret, err := os.CreateTemp(home, ".r1-sbx-secret-*")
	if err != nil {
		t.Skipf("cannot create probe file in home: %v", err)
	}
	secretPath := secret.Name()
	if _, err := secret.WriteString("TOPSECRET"); err != nil {
		t.Fatal(err)
	}
	secret.Close()
	defer os.Remove(secretPath)

	work := t.TempDir()
	w := &landlockWrapper{}
	pol := Policy{
		Mode:        ModeLandlock,
		AllowWrite:  []string{work},
		AllowEgress: true, // keep ABI requirement at 1 for wider host coverage
	}
	if err := w.Available(pol); err != nil {
		t.Skipf("landlock wrapper unavailable: %v", err)
	}

	t.Run("write inside allow-write succeeds", func(t *testing.T) {
		out, err := runWrapped(t, w, "echo ok > inside.txt", work, pol)
		if err != nil {
			t.Fatalf("write failed: %v: %s", err, out)
		}
		if _, err := os.Stat(filepath.Join(work, "inside.txt")); err != nil {
			t.Errorf("file missing on host: %v", err)
		}
	})

	t.Run("read outside allow-list denied", func(t *testing.T) {
		out, _ := runWrapped(t, w, "cat "+secretPath+" 2>&1; true", work, pol)
		if strings.Contains(out, "TOPSECRET") {
			t.Errorf("out-of-allowlist read succeeded: %q", out)
		}
	})

	t.Run("write outside allow-list denied", func(t *testing.T) {
		probe := filepath.Join(home, ".r1-sbx-write-probe")
		out, _ := runWrapped(t, w, "touch "+probe+" 2>&1; true", work, pol)
		if _, err := os.Stat(probe); err == nil {
			os.Remove(probe)
			t.Errorf("out-of-allowlist write succeeded: %s", out)
		}
	})
}

// TestBwrapCommandKeepsProcessGroupCompatible pins that the wrapper does
// not preclude the caller's Setpgid+group-kill plumbing (tools.go relies
// on it for timeout cleanup).
func TestBwrapCommandKeepsProcessGroupCompatible(t *testing.T) {
	w := &bwrapWrapper{}
	cmd, err := w.Command(context.Background(), "true", t.TempDir(), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.SysProcAttr != nil {
		t.Error("wrapper must leave SysProcAttr for the caller's process-group config")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
}
