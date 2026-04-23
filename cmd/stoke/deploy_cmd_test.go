package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/deploy"
)

func TestRunDeployCmd_DryRun(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDeployCmd([]string{"--dry-run", "--provider", "fly", "--app", "stoke-dry"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{`app = "stoke-dry"`, "primary_region", "[http_service]"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
	if !strings.Contains(stderr.String(), "dry-run") {
		t.Errorf("stderr should confirm dry-run mode; got %q", stderr.String())
	}
}

func TestRunDeployCmd_UnknownProvider(t *testing.T) {
	// Pick a provider name guaranteed not to appear in the registry
	// (the DP2-10 flag surface registers fly / vercel / cloudflare).
	// "bogus" is the name called out in the spec's acceptance criteria
	// so this test doubles as an acceptance-criteria check.
	var stdout, stderr bytes.Buffer
	code := runDeployCmd([]string{"--provider", "bogus", "--app", "x"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "bogus") {
		t.Errorf("stderr should name rejected provider; got %q", stderr.String())
	}
	// The error message should list the known providers so operators
	// see exactly what they can type next.
	for _, want := range []string{"fly", "vercel", "cloudflare"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr should list known provider %q; got %q", want, stderr.String())
		}
	}
}

func TestRunDeployCmd_MissingApp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDeployCmd([]string{"--provider", "fly"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--app is required") {
		t.Errorf("stderr should say --app is required; got %q", stderr.String())
	}
}

func TestRunDeployCmd_VerifyOnlyOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Sentinel API live"))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := runDeployCmd([]string{"--verify-only", "--health-url", srv.URL, "--expected-body", "Sentinel"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "200 OK") {
		t.Errorf("stdout should report 200 OK; got %q", stdout.String())
	}
}

func TestRunDeployCmd_VerifyOnlyFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := runDeployCmd([]string{"--verify-only", "--health-url", srv.URL}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit=%d, want 1", code)
	}
	if !strings.Contains(stdout.String(), "502") {
		t.Errorf("stdout should report HTTP 502; got %q", stdout.String())
	}
}

func TestRunDeployCmd_VerifyOnlyMissingURL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDeployCmd([]string{"--verify-only"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--health-url") {
		t.Errorf("stderr should mention --health-url requirement; got %q", stderr.String())
	}
}

func TestRunDeployCmd_FlyctlNotFound(t *testing.T) {
	// Scrub PATH and do not supply --flyctl; the flyctl-not-found
	// error should map to exit 2 (env issue, not a real deploy fail).
	t.Setenv("PATH", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := runDeployCmd([]string{"--provider", "fly", "--app", "x"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d, want 2 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "flyctl not found") {
		t.Errorf("stderr should explain flyctl not found; got %q", stderr.String())
	}
}

func TestRunDeployCmd_FakeFlyctlSuccessWithHealth(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "flyctl")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("stamp fake flyctl: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := runDeployCmd([]string{
		"--provider", "fly",
		"--app", "x",
		"--dir", dir,
		"--flyctl", fake,
		"--health-url", srv.URL,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stdout=%q stderr=%q)", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "deployed x") {
		t.Errorf("stdout should report deployed x; got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "200 OK") {
		t.Errorf("stdout should report health 200 OK; got %q", stdout.String())
	}
}

func TestRunDeployCmd_FakeFlyctlFailure(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "flyctl")
	script := "#!/bin/sh\necho 'auth: token invalid' 1>&2\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("stamp fake flyctl: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := runDeployCmd([]string{
		"--provider", "fly",
		"--app", "x",
		"--dir", dir,
		"--flyctl", fake,
	}, &stdout, &stderr)
	if code != 3 {
		t.Fatalf("exit=%d, want 3 (deploy-failed taxonomy)", code)
	}
	if !strings.Contains(stderr.String(), "auth: token invalid") {
		t.Errorf("stderr should include flyctl stderr; got %q", stderr.String())
	}
}

func TestParseDeployProvider(t *testing.T) {
	ok, err := parseDeployProvider("fly")
	if err != nil || ok.String() != "fly" {
		t.Errorf("parseDeployProvider(fly) = (%v, %v)", ok, err)
	}
	// DP2-10: vercel + cloudflare are now wired through the registry,
	// so parseDeployProvider returns their enum successfully. The enum
	// is still only honored by the legacy top-level deploy.Deploy path
	// (fly only); the multi-provider surface dispatches via
	// deploy.Get(string) instead.
	if p, err := parseDeployProvider("vercel"); err != nil || p != deploy.ProviderVercel {
		t.Errorf("parseDeployProvider(vercel) = (%v, %v); want (ProviderVercel, nil)", p, err)
	}
	if p, err := parseDeployProvider("cloudflare"); err != nil || p != deploy.ProviderCloudflare {
		t.Errorf("parseDeployProvider(cloudflare) = (%v, %v); want (ProviderCloudflare, nil)", p, err)
	}
	if _, err := parseDeployProvider("lolnope"); err == nil {
		t.Errorf("lolnope should error")
	}
}

// --- DP2-10 multi-provider CLI surface -----------------------------------
//
// The tests below exercise the flag wiring added in DP2-10: --provider
// validation against the registry, --auto + stack detection, --prod /
// --env mutual exclusion, and the registered-names error shape.

// TestRunDeployCmd_ProviderValidation asserts every registered
// provider (fly / vercel / cloudflare) is accepted by the --provider
// flag, using --dry-run to stay entirely offline.
func TestRunDeployCmd_ProviderValidation(t *testing.T) {
	cases := []struct {
		provider string
		want     int
	}{
		{"fly", 0},
		{"vercel", 0},
		{"cloudflare", 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.provider, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := []string{"--provider", tc.provider, "--dry-run"}
			if tc.provider == "fly" {
				args = append(args, "--app", "stoke-dry")
			}
			code := runDeployCmd(args, &stdout, &stderr)
			if code != tc.want {
				t.Fatalf("exit=%d, want %d (stderr=%q)", code, tc.want, stderr.String())
			}
		})
	}
}

// TestRunDeployCmd_AutoHappyPath plants a vercel.json in a fresh temp
// dir and asserts --auto resolves to vercel and emits a dry-run
// preview naming the provider.
func TestRunDeployCmd_AutoHappyPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vercel.json"), []byte(`{"version":2}`), 0o644); err != nil {
		t.Fatalf("plant vercel.json: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := runDeployCmd([]string{"--auto", "--dir", dir, "--dry-run"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "provider=vercel") {
		t.Errorf("stdout should name resolved vercel provider; got %q", stdout.String())
	}
}

// TestRunDeployCmd_AutoAmbiguityExits asserts --auto with no signals
// exits 2 and prints a reason + the known-provider list. A clean
// t.TempDir() has no provider markers so Detect returns an empty
// Provider.
func TestRunDeployCmd_AutoAmbiguityExits(t *testing.T) {
	dir := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := runDeployCmd([]string{"--auto", "--dir", dir}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d, want 2 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--auto") {
		t.Errorf("stderr should mention --auto; got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "could not resolve") {
		t.Errorf("stderr should explain the unresolved state; got %q", stderr.String())
	}
}

// TestRunDeployCmd_AutoAmbiguousSignals plants BOTH vercel.json
// and wrangler.toml so Detect returns Ambiguous=true. --auto must
// refuse to silently pick one.
func TestRunDeployCmd_AutoAmbiguousSignals(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vercel.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wrangler.toml"), []byte("name = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runDeployCmd([]string{"--auto", "--dir", dir}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d, want 2 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "both present") {
		t.Errorf("stderr should explain ambiguity; got %q", stderr.String())
	}
}

// TestRunDeployCmd_ProviderAutoMutex asserts --provider + --auto is a
// usage error (exit 2) so operators cannot accidentally wire both and
// wonder which one won.
func TestRunDeployCmd_ProviderAutoMutex(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDeployCmd([]string{"--provider", "fly", "--auto", "--dry-run"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d, want 2 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr should call out mutual exclusion; got %q", stderr.String())
	}
}

// TestRunDeployCmd_ProdEnvMutex asserts --prod combined with a
// non-"production" --env exits 2. The mutex is relaxed only when the
// operator explicitly types --env production (the redundancy is
// harmless in that shape).
func TestRunDeployCmd_ProdEnvMutex(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDeployCmd([]string{"--provider", "vercel", "--prod", "--env", "staging", "--dry-run"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d, want 2 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--prod") || !strings.Contains(stderr.String(), "staging") {
		t.Errorf("stderr should name both flags; got %q", stderr.String())
	}
}

// TestRunDeployCmd_ProdEnvRedundantOK asserts --prod --env production
// is accepted (redundant, not contradictory).
func TestRunDeployCmd_ProdEnvRedundantOK(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDeployCmd([]string{"--provider", "vercel", "--prod", "--env", "production", "--dry-run"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%q)", code, stderr.String())
	}
	// The dry-run preview should report VERCEL_PROD=1 since --prod
	// set it regardless of the redundant --env typing.
	if !strings.Contains(stdout.String(), "VERCEL_PROD=1") {
		t.Errorf("stdout should report VERCEL_PROD=1; got %q", stdout.String())
	}
}

// TestRunDeployCmd_VercelEnvMapsCorrectly asserts --env flows into
// VERCEL_ENV on the Vercel path and into CF_ENV on the Cloudflare
// path, per the spec's Provider-Selection CLI table.
func TestRunDeployCmd_VercelEnvMapsCorrectly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDeployCmd([]string{"--provider", "vercel", "--env", "staging", "--dry-run"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "VERCEL_ENV=staging") {
		t.Errorf("stdout should map --env to VERCEL_ENV; got %q", stdout.String())
	}
}

func TestRunDeployCmd_CloudflareEnvMapsCorrectly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDeployCmd([]string{"--provider", "cloudflare", "--env", "preview", "--cf-mode", "workers", "--dry-run"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "CF_ENV=preview") {
		t.Errorf("stdout should map --env to CF_ENV; got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "CF_MODE=workers") {
		t.Errorf("stdout should carry --cf-mode as CF_MODE; got %q", stdout.String())
	}
}

// TestRunDeployCmd_Prebuilt asserts --prebuilt sets VERCEL_PREBUILT=1
// in the cfg Env map (dry-run preview exposes it).
func TestRunDeployCmd_Prebuilt(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDeployCmd([]string{"--provider", "vercel", "--prebuilt", "--dry-run"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "VERCEL_PREBUILT=1") {
		t.Errorf("stdout should set VERCEL_PREBUILT=1; got %q", stdout.String())
	}
}

// TestRunDeployCmd_WriteConfigVercel asserts --write-config drops a
// vercel.json into --dir when absent and reports the wrote path. A
// second run finds the file present and reports "already present".
func TestRunDeployCmd_WriteConfigVercel(t *testing.T) {
	dir := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := runDeployCmd([]string{"--provider", "vercel", "--write-config", "--dir", dir, "--app", "stoke-writer", "--dry-run"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("first write exit=%d, want 0 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "wrote ") || !strings.Contains(stdout.String(), "vercel.json") {
		t.Errorf("stdout should confirm vercel.json write; got %q", stdout.String())
	}
	// File should now exist.
	if _, err := os.Stat(filepath.Join(dir, "vercel.json")); err != nil {
		t.Fatalf("vercel.json not written: %v", err)
	}

	// Second run must see the file and refuse to overwrite.
	stdout.Reset()
	stderr.Reset()
	code = runDeployCmd([]string{"--provider", "vercel", "--write-config", "--dir", dir, "--app", "stoke-writer", "--dry-run"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("second write exit=%d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "already present") {
		t.Errorf("stdout should note existing file; got %q", stdout.String())
	}
}

// TestRunDeployCmd_KnownProvidersIncludePhase2 is a guard that the
// registry actually has the Vercel and Cloudflare adapters blank-
// imported from deploy_cmd.go. Without the blank imports, those
// packages' registration hooks never run and the --provider
// validation would reject both.
func TestRunDeployCmd_KnownProvidersIncludePhase2(t *testing.T) {
	names := deploy.Names()
	for _, want := range []string{"fly", "vercel", "cloudflare"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("deploy.Names() missing %q; got %v", want, names)
		}
	}
}
