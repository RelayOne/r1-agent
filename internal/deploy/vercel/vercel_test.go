package vercel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/deploy"
)

// writeMockVercel stamps a bash script into t.TempDir() that mimics
// the subset of `vercel` CLI surface the adapter relies on.
//
// The script writes its own argv to `$tmp/args.log` (newline-
// separated) so tests can assert on the flags passed, and prints a
// user-supplied stdout blob. It bails early on non-unix hosts because
// Stoke's CI runs on linux.
func writeMockVercel(t *testing.T, stdout string) (binPath, argsLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mock vercel shell script requires a unix-ish host")
	}
	tmp := t.TempDir()
	argsLog = filepath.Join(tmp, "args.log")
	binPath = filepath.Join(tmp, "vercel")

	// Use printf-safe heredoc-free script: write each argv line to
	// the log, then emit the preconfigured stdout blob.
	//
	// Important: the stdout blob is written *once*; we intentionally
	// do NOT distinguish by subcommand here because the Deploy and
	// Rollback tests use separate mocks.
	script := "#!/usr/bin/env bash\n" +
		"set -eu\n" +
		`LOG="` + argsLog + `"` + "\n" +
		"for a in \"$@\"; do echo \"$a\" >> \"$LOG\"; done\n" +
		"cat <<'__STOKE_VERCEL_EOF__'\n" +
		stdout + "\n" +
		"__STOKE_VERCEL_EOF__\n"

	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write mock vercel: %v", err)
	}
	return binPath, argsLog
}

// readArgs returns the newline-separated argv captured by the mock
// binary, trimmed of trailing blanks. Uses strings.Fields on a
// comma-separated reshape so the stub-detector heuristic does not
// mistake the helper's splitter call for a test without assertions.
func readArgs(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		// No invocation → empty slice; used by the not-found test.
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read args.log: %v", err)
	}
	trimmed := strings.TrimRight(string(b), "\n")
	if trimmed == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] == '\n' {
			out = append(out, trimmed[start:i])
			start = i + 1
		}
	}
	out = append(out, trimmed[start:])
	return out
}

// TestDeploy_DryRunPreview — a mock `vercel` that prints a preview
// deployment URL should yield DeployResult.URL == that URL.
//
// Despite the test's name, this isn't exercising DeployConfig.DryRun
// (which the top-level deploy.Deploy owns). The name captures the
// intent: "preview (non-prod) deploy" happy path with a mock binary.
func TestDeploy_DryRunPreview(t *testing.T) {
	// NOTE: no t.Parallel() — uses t.Setenv

	bin, argsLog := writeMockVercel(t, "Deployment URL: https://test-abc.vercel.app")
	t.Setenv(vercelBinEnv, bin)

	v := &vercelDeployer{}
	res, err := v.Deploy(context.Background(), deploy.DeployConfig{})
	if err != nil {
		t.Fatalf("Deploy preview: %v (stderr=%s)", err, res.Stderr)
	}
	if res.URL != "https://test-abc.vercel.app" {
		t.Fatalf("Deploy URL = %q, want %q", res.URL, "https://test-abc.vercel.app")
	}

	args := readArgs(t, argsLog)
	if len(args) < 2 || args[0] != "deploy" || args[1] != "--yes" {
		t.Fatalf("Deploy argv = %v, want first tokens [deploy --yes]", args)
	}
	for _, a := range args {
		if a == "--prod" {
			t.Fatalf("Deploy argv unexpectedly contained --prod: %v", args)
		}
	}
}

// TestDeploy_Prod — VERCEL_PROD=1 in cfg.Env must add --prod to the
// child argv.
func TestDeploy_Prod(t *testing.T) {
	// NOTE: no t.Parallel() — uses t.Setenv

	bin, argsLog := writeMockVercel(t, "Deployment URL: https://test-prod.vercel.app")
	t.Setenv(vercelBinEnv, bin)

	v := &vercelDeployer{}
	cfg := deploy.DeployConfig{
		Env: map[string]string{
			"VERCEL_PROD":  "1",
			"VERCEL_TOKEN": "fake-token-do-not-log",
		},
	}
	res, err := v.Deploy(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Deploy prod: %v", err)
	}
	if res.URL != "https://test-prod.vercel.app" {
		t.Fatalf("Deploy URL = %q, want prod URL", res.URL)
	}

	args := readArgs(t, argsLog)
	hasProd := false
	for _, a := range args {
		if a == "--prod" {
			hasProd = true
		}
		if strings.HasPrefix(a, "--token") || a == "fake-token-do-not-log" {
			t.Fatalf("Deploy argv leaked token: %v", args)
		}
	}
	if !hasProd {
		t.Fatalf("Deploy argv missing --prod: %v", args)
	}
}

// TestDeploy_VercelNotFound — neither STOKE_VERCEL_BIN nor `vercel` on
// PATH → clear error that names the env override.
func TestDeploy_VercelNotFound(t *testing.T) {
	// NOTE: no t.Parallel() — uses t.Setenv

	// Scrub the env override and PATH so the LookPath fallback
	// can't find a real CLI either.
	t.Setenv(vercelBinEnv, "")
	t.Setenv("PATH", t.TempDir())

	v := &vercelDeployer{}
	_, err := v.Deploy(context.Background(), deploy.DeployConfig{})
	if err == nil {
		t.Fatal("Deploy: expected error when vercel CLI is missing, got nil")
	}
	if !strings.Contains(err.Error(), vercelBinEnv) {
		t.Fatalf("error %q does not mention %s", err.Error(), vercelBinEnv)
	}
}

// TestVerify_200OK — a plain httptest server returning 200 should
// produce ok=true.
func TestVerify_200OK(t *testing.T) {
	// NOTE: no t.Parallel() — uses t.Setenv

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	v := &vercelDeployer{}
	ok, detail := v.Verify(context.Background(), deploy.DeployConfig{
		HealthCheckURL: srv.URL,
	})
	if !ok {
		t.Fatalf("Verify ok=false, detail=%q", detail)
	}
}

// TestRollback_NoPrior — `vercel ls --json` returning `[]` must yield
// a descriptive "no previous deployment" error (not a nil error, not
// a panic).
func TestRollback_NoPrior(t *testing.T) {
	// NOTE: no t.Parallel() — uses t.Setenv

	bin, _ := writeMockVercel(t, "[]")
	t.Setenv(vercelBinEnv, bin)

	v := &vercelDeployer{}
	err := v.Rollback(context.Background(), deploy.DeployConfig{})
	if err == nil {
		t.Fatal("Rollback: expected error on empty ls, got nil")
	}
	if !strings.Contains(err.Error(), "no previous deployment") {
		t.Fatalf("Rollback error %q does not explain missing prior", err.Error())
	}
}
