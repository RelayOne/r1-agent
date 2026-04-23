package deploy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderString(t *testing.T) {
	cases := []struct {
		p    Provider
		want string
	}{
		{ProviderUnknown, "unknown"},
		{ProviderFly, "fly"},
		{ProviderVercel, "vercel"},
		{ProviderCloudflare, "cloudflare"},
		{ProviderDocker, "docker"},
		{ProviderKamal, "kamal"},
		{Provider(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.p.String(); got != tc.want {
			t.Errorf("Provider(%d).String() = %q, want %q", tc.p, got, tc.want)
		}
	}
}

func TestFlyctlTomlPreview(t *testing.T) {
	cfg := DeployConfig{
		Provider: ProviderFly,
		AppName:  "stoke-test",
		Region:   "sjc",
	}
	got := flyctlTomlPreview(cfg)
	for _, want := range []string{
		`app = "stoke-test"`,
		`primary_region = "sjc"`,
		`dockerfile = "Dockerfile"`,
		"[http_service]",
		"[[http_service.checks]]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("preview missing %q; got:\n%s", want, got)
		}
	}
}

func TestFlyctlTomlPreview_DockerImageOverridesDockerfile(t *testing.T) {
	cfg := DeployConfig{
		Provider:    ProviderFly,
		AppName:     "x",
		DockerImage: "registry.fly.io/x:deploy-1",
	}
	got := flyctlTomlPreview(cfg)
	if !strings.Contains(got, `image = "registry.fly.io/x:deploy-1"`) {
		t.Errorf("preview missing image line; got:\n%s", got)
	}
	if strings.Contains(got, "dockerfile") {
		t.Errorf("preview should omit dockerfile when image is set; got:\n%s", got)
	}
}

func TestFlyctlTomlPreview_DefaultsAppAndRegion(t *testing.T) {
	got := flyctlTomlPreview(DeployConfig{})
	if !strings.Contains(got, "<app-name-required>") {
		t.Errorf("preview should flag missing app name; got:\n%s", got)
	}
	if !strings.Contains(got, `primary_region = "iad"`) {
		t.Errorf("preview should default region to iad; got:\n%s", got)
	}
}

func TestDeploy_DryRun(t *testing.T) {
	res, err := Deploy(context.Background(), DeployConfig{
		Provider: ProviderFly,
		AppName:  "dry-app",
		Region:   "iad",
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("DryRun Deploy returned err: %v", err)
	}
	if !res.DryRun {
		t.Errorf("expected DryRun=true on result, got false")
	}
	if res.Stdout == "" {
		t.Errorf("expected non-empty Stdout preview on dry run")
	}
	if !strings.Contains(res.Stdout, "dry-app") {
		t.Errorf("preview missing app name; got:\n%s", res.Stdout)
	}
	if res.URL != "" {
		t.Errorf("dry run should not set URL; got %q", res.URL)
	}
}

func TestDeploy_DryRunSkipsAppNameValidation(t *testing.T) {
	// An empty AppName on a real deploy is an error, but a dry run
	// is meant to be runnable before the operator has decided the
	// final name — it just flags the missing value in the preview.
	res, err := Deploy(context.Background(), DeployConfig{
		Provider: ProviderFly,
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("DryRun Deploy returned err: %v", err)
	}
	if !strings.Contains(res.Stdout, "<app-name-required>") {
		t.Errorf("dry run should flag missing app name in preview")
	}
}

func TestDeploy_ProviderUnsupported(t *testing.T) {
	_, err := Deploy(context.Background(), DeployConfig{
		Provider: ProviderVercel,
		AppName:  "x",
	})
	if !errors.Is(err, ErrProviderUnsupported) {
		t.Fatalf("expected ErrProviderUnsupported, got %v", err)
	}
}

func TestDeploy_DefaultsToFly(t *testing.T) {
	// Provider==ProviderUnknown should default to Fly rather than
	// error out — keeps the CLI's common case ("stoke deploy --app
	// foo --dry-run") a one-flag command.
	res, err := Deploy(context.Background(), DeployConfig{
		AppName: "x",
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("Deploy returned err: %v", err)
	}
	if res.Provider != ProviderFly {
		t.Errorf("unknown provider should default to fly, got %v", res.Provider)
	}
}

func TestDeploy_AppNameMissing(t *testing.T) {
	_, err := Deploy(context.Background(), DeployConfig{
		Provider: ProviderFly,
	})
	if !errors.Is(err, ErrAppNameMissing) {
		t.Fatalf("expected ErrAppNameMissing, got %v", err)
	}
}

func TestDeploy_FlyctlNotFound(t *testing.T) {
	// Scrub PATH so exec.LookPath cannot find flyctl. Tests must
	// not invoke a real flyctl even if the workstation has one.
	t.Setenv("PATH", t.TempDir())
	_, err := Deploy(context.Background(), DeployConfig{
		Provider: ProviderFly,
		AppName:  "no-fly",
	})
	if !errors.Is(err, ErrFlyctlNotFound) {
		t.Fatalf("expected ErrFlyctlNotFound, got %v", err)
	}
}

func TestDeploy_FlyctlFakeFailure(t *testing.T) {
	// Stamp a fake flyctl that always exits 1 into a temp dir and
	// point FlyctlPath at it. The error should wrap the stderr tail
	// so operators can read what flyctl complained about.
	dir := t.TempDir()
	stub := filepath.Join(dir, "flyctl")
	script := "#!/bin/sh\necho 'fake flyctl stderr: auth failed' 1>&2\nexit 1\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("stamp stub: %v", err)
	}

	res, err := Deploy(context.Background(), DeployConfig{
		Provider:   ProviderFly,
		AppName:    "stub-app",
		Dir:        dir,
		FlyctlPath: stub,
	})
	if err == nil {
		t.Fatal("expected error from non-zero flyctl exit")
	}
	if !strings.Contains(err.Error(), "fake flyctl stderr: auth failed") {
		t.Errorf("error should include stderr tail; got %q", err.Error())
	}
	if res.AppName != "stub-app" {
		t.Errorf("result AppName = %q, want stub-app", res.AppName)
	}
	if res.URL != "https://stub-app.fly.dev" {
		t.Errorf("result URL = %q, want auto-derived fly.dev", res.URL)
	}
}

func TestDeploy_FlyctlStubSuccess(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "flyctl")
	script := "#!/bin/sh\necho 'deploy ok'\nexit 0\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("stamp stub: %v", err)
	}
	res, err := Deploy(context.Background(), DeployConfig{
		Provider:   ProviderFly,
		AppName:    "ok-app",
		Region:     "iad",
		Dir:        dir,
		FlyctlPath: stub,
	})
	if err != nil {
		t.Fatalf("Deploy returned err: %v", err)
	}
	if !strings.Contains(res.Stdout, "deploy ok") {
		t.Errorf("stdout missing fake output; got %q", res.Stdout)
	}
	if res.URL != "https://ok-app.fly.dev" {
		t.Errorf("URL = %q, want auto-derived", res.URL)
	}
}

func TestDeploy_FlyctlStubHealthURLOverride(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "flyctl")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("stamp stub: %v", err)
	}
	res, err := Deploy(context.Background(), DeployConfig{
		Provider:       ProviderFly,
		AppName:        "x",
		Dir:            dir,
		FlyctlPath:     stub,
		HealthCheckURL: "https://custom.example.com/",
	})
	if err != nil {
		t.Fatalf("Deploy returned err: %v", err)
	}
	if res.URL != "https://custom.example.com/" {
		t.Errorf("URL = %q, HealthCheckURL should override auto-derive", res.URL)
	}
}

func TestHealthCheck_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello from Sentinel API"))
	}))
	defer srv.Close()

	ok, detail := HealthCheck(context.Background(), srv.URL, "Sentinel")
	if !ok {
		t.Fatalf("HealthCheck ok=false, detail=%q", detail)
	}
	if !strings.Contains(detail, "200 OK") {
		t.Errorf("detail missing 200 OK marker: %q", detail)
	}
}

func TestHealthCheck_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	ok, detail := HealthCheck(context.Background(), srv.URL, "")
	if ok {
		t.Fatal("HealthCheck returned ok=true on 404")
	}
	if !strings.Contains(detail, "404") {
		t.Errorf("detail should include 404; got %q", detail)
	}
}

func TestHealthCheck_BodyMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("this is a different homepage"))
	}))
	defer srv.Close()

	ok, detail := HealthCheck(context.Background(), srv.URL, "Sentinel")
	if ok {
		t.Fatal("HealthCheck returned ok=true despite body mismatch")
	}
	if !strings.Contains(detail, "Sentinel") {
		t.Errorf("detail should name expected substring; got %q", detail)
	}
}

func TestHealthCheck_EmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ok, detail := HealthCheck(context.Background(), srv.URL, "")
	if ok {
		t.Fatal("HealthCheck returned ok=true on empty body")
	}
	if !strings.Contains(detail, "empty body") {
		t.Errorf("detail should mention empty body; got %q", detail)
	}
}

func TestHealthCheck_EmptyURL(t *testing.T) {
	ok, detail := HealthCheck(context.Background(), "", "")
	if ok {
		t.Fatal("HealthCheck returned ok=true on empty URL")
	}
	if !strings.Contains(detail, "empty") {
		t.Errorf("detail should mention empty URL; got %q", detail)
	}
}
