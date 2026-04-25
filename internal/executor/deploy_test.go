package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RelayOne/r1-agent/internal/deploy"
)

func TestDeployExecutor_TaskType(t *testing.T) {
	e := NewDeployExecutor(deploy.DeployConfig{})
	if e.TaskType() != TaskDeploy {
		t.Fatalf("TaskType = %v, want TaskDeploy", e.TaskType())
	}
}

func TestDeployExecutor_ExecuteDryRun(t *testing.T) {
	e := NewDeployExecutor(deploy.DeployConfig{
		Provider: deploy.ProviderFly,
		AppName:  "stoke-exec",
		DryRun:   true,
	})
	d, err := e.Execute(context.Background(), Plan{}, EffortMinimal)
	if err != nil {
		t.Fatalf("Execute returned err: %v", err)
	}
	dd, ok := d.(DeployDeliverable)
	if !ok {
		t.Fatalf("Execute returned %T, want DeployDeliverable", d)
	}
	if !dd.Result.DryRun {
		t.Errorf("expected DryRun=true")
	}
	if !strings.Contains(dd.Summary(), "dry-run") {
		t.Errorf("summary should mention dry-run; got %q", dd.Summary())
	}
	if dd.Size() == 0 {
		t.Errorf("dry run deliverable should have non-zero size (preview)")
	}
}

func TestDeployExecutor_ExecuteProviderUnsupported(t *testing.T) {
	// Post-DP2-11 Execute resolves the adapter through the registry at
	// construction time; an unregistered provider (ProviderUnknown's
	// String() is "unknown") yields a descriptive registry error
	// surfaced unchanged through Execute. The pre-DP2-11 shape asserted
	// errors.Is(err, deploy.ErrProviderUnsupported) with ProviderVercel,
	// but Vercel is now a real registered adapter (DP2-3), so that
	// assertion is no longer meaningful — the "unsupported provider"
	// concept is now "unknown to the registry".
	e := NewDeployExecutor(deploy.DeployConfig{
		Provider: deploy.ProviderUnknown,
		AppName:  "x",
	})
	_, err := e.Execute(context.Background(), Plan{}, EffortMinimal)
	if err == nil {
		t.Fatal("expected non-nil error from Execute with ProviderUnknown")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("expected error to mention 'unknown provider'; got %q", err.Error())
	}
}

func TestDeployExecutor_BuildCriteria_Shape(t *testing.T) {
	e := NewDeployExecutor(deploy.DeployConfig{
		Provider: deploy.ProviderFly,
		AppName:  "x",
		DryRun:   true,
	})
	d, err := e.Execute(context.Background(), Plan{}, EffortMinimal)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	acs := e.BuildCriteria(Task{}, d)
	if len(acs) != 2 {
		t.Fatalf("BuildCriteria returned %d AC, want 2", len(acs))
	}
	wantIDs := map[string]bool{"DEPLOY-COMMIT-MATCH": false, "DEPLOY-HEALTH-200": false}
	for _, ac := range acs {
		if _, ok := wantIDs[ac.ID]; !ok {
			t.Errorf("unexpected AC id %q", ac.ID)
			continue
		}
		wantIDs[ac.ID] = true
		if ac.VerifyFunc == nil {
			t.Errorf("AC %s VerifyFunc is nil", ac.ID)
		}
	}
	for id, seen := range wantIDs {
		if !seen {
			t.Errorf("missing AC %s", id)
		}
	}
}

func TestDeployExecutor_BuildCriteria_WrongDeliverableType(t *testing.T) {
	e := NewDeployExecutor(deploy.DeployConfig{Provider: deploy.ProviderFly, AppName: "x"})
	acs := e.BuildCriteria(Task{}, CodeDeliverable{})
	if acs != nil {
		t.Errorf("BuildCriteria should return nil on wrong deliverable type; got %+v", acs)
	}
}

func TestDeployExecutor_HealthCriterionPasses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello from Sentinel"))
	}))
	defer srv.Close()

	e := NewDeployExecutor(deploy.DeployConfig{
		Provider:       deploy.ProviderFly,
		AppName:        "x",
		HealthCheckURL: srv.URL,
		ExpectedBody:   "Sentinel",
	})
	// Manually synthesize a non-dry-run deliverable so the health
	// criterion actually fires (dry-run soft-passes by design).
	dd := DeployDeliverable{Result: deploy.DeployResult{
		Provider: deploy.ProviderFly,
		AppName:  "x",
		URL:      srv.URL,
	}}
	acs := e.BuildCriteria(Task{}, dd)
	var health *func(context.Context) (bool, string)
	for i, ac := range acs {
		if ac.ID == "DEPLOY-HEALTH-200" {
			health = &acs[i].VerifyFunc
			break
		}
	}
	if health == nil {
		t.Fatal("DEPLOY-HEALTH-200 AC not found")
	}
	ok, detail := (*health)(context.Background())
	if !ok {
		t.Fatalf("health criterion failed: %s", detail)
	}
}

func TestDeployExecutor_HealthCriterionFailsOnBodyMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("wrong body"))
	}))
	defer srv.Close()

	e := NewDeployExecutor(deploy.DeployConfig{
		Provider:       deploy.ProviderFly,
		AppName:        "x",
		HealthCheckURL: srv.URL,
		ExpectedBody:   "Sentinel",
	})
	dd := DeployDeliverable{Result: deploy.DeployResult{AppName: "x", URL: srv.URL}}
	acs := e.BuildCriteria(Task{}, dd)
	for _, ac := range acs {
		if ac.ID != "DEPLOY-HEALTH-200" {
			continue
		}
		ok, detail := ac.VerifyFunc(context.Background())
		if ok {
			t.Fatal("health criterion should fail on body mismatch")
		}
		if !strings.Contains(detail, "Sentinel") {
			t.Errorf("detail should reference expected body; got %q", detail)
		}
		return
	}
	t.Fatal("DEPLOY-HEALTH-200 not built")
}

func TestDeployExecutor_CommitMatchDryRunSoftPass(t *testing.T) {
	e := NewDeployExecutor(deploy.DeployConfig{
		Provider: deploy.ProviderFly,
		AppName:  "x",
		DryRun:   true,
	})
	d, err := e.Execute(context.Background(), Plan{}, EffortMinimal)
	if err != nil {
		t.Fatal(err)
	}
	acs := e.BuildCriteria(Task{}, d)
	for _, ac := range acs {
		if ac.ID != "DEPLOY-COMMIT-MATCH" {
			continue
		}
		ok, detail := ac.VerifyFunc(context.Background())
		if !ok {
			t.Errorf("commit-match should soft-pass on dry run; detail=%q", detail)
		}
		if !strings.Contains(detail, "dry run") {
			t.Errorf("detail should mention dry run; got %q", detail)
		}
		return
	}
	t.Fatal("DEPLOY-COMMIT-MATCH not built")
}

func TestDeployExecutor_BuildEnvFixFuncTransient(t *testing.T) {
	e := NewDeployExecutor(deploy.DeployConfig{})
	fix := e.BuildEnvFixFunc()
	if fix == nil {
		t.Fatal("BuildEnvFixFunc returned nil")
	}
	transient := []string{"timeout hit", "HTTP 502 bad gateway", "503 unavailable", "504 Gateway Timeout", "temporary failure in name resolution", "read: i/o timeout"}
	for _, msg := range transient {
		if !fix(context.Background(), "", msg) {
			t.Errorf("env-fix should treat %q as transient", msg)
		}
	}
	permanent := []string{"404 not found", "401 unauthorized", "config invalid", "fly.toml parse error"}
	for _, msg := range permanent {
		if fix(context.Background(), "", msg) {
			t.Errorf("env-fix should NOT treat %q as transient", msg)
		}
	}
}

func TestDeployExecutor_BuildRepairFuncRunsDeploy(t *testing.T) {
	// Repair re-runs Deploy with DryRun forced off; we assert it
	// returns the registry "unknown provider" error rather than
	// silently succeeding, proving it actually invoked the deployer
	// (via the registry-selected path) rather than short-circuiting.
	// Pre-DP2-11 this test keyed on errors.Is(err,
	// deploy.ErrProviderUnsupported) via ProviderVercel; the new
	// dispatch resolves Vercel to a real adapter, so we use
	// ProviderUnknown to reach the same "adapter unavailable" branch.
	e := NewDeployExecutor(deploy.DeployConfig{
		Provider: deploy.ProviderUnknown,
		AppName:  "x",
	})
	repair := e.BuildRepairFunc(Plan{})
	if repair == nil {
		t.Fatal("BuildRepairFunc returned nil")
	}
	err := repair(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected non-nil error from repair with ProviderUnknown")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("expected error to mention 'unknown provider'; got %q", err.Error())
	}
}

// TestDeployExecutor_SelectsFromRegistry asserts DP2-11 wiring: the
// executor resolves a Deployer through deploy.Get at construction time
// so Provider=ProviderVercel picks the "vercel" adapter registered by
// internal/deploy/vercel (blank-imported in deploy.go). The check uses
// Deployer.Name() because that round-trips the registry key and keeps
// the assertion independent of adapter struct layout.
func TestDeployExecutor_SelectsFromRegistry(t *testing.T) {
	e := NewDeployExecutor(deploy.DeployConfig{
		Provider: deploy.ProviderVercel,
		AppName:  "x",
	})
	if e.deployer == nil {
		t.Fatalf("expected non-nil deployer for ProviderVercel; deployerErr=%v", e.deployerErr)
	}
	if got := e.deployer.Name(); got != "vercel" {
		t.Fatalf("deployer.Name() = %q, want %q", got, "vercel")
	}

	// Also exercise Cloudflare to confirm the blank-import pulls in
	// both adapters — a regression that drops the cloudflare import
	// would silently fall through to deployerErr for CF users.
	eCF := NewDeployExecutor(deploy.DeployConfig{
		Provider: deploy.ProviderCloudflare,
		AppName:  "x",
	})
	if eCF.deployer == nil {
		t.Fatalf("expected non-nil deployer for ProviderCloudflare; deployerErr=%v", eCF.deployerErr)
	}
	if got := eCF.deployer.Name(); got != "cloudflare" {
		t.Fatalf("deployer.Name() = %q, want %q", got, "cloudflare")
	}

	// And Fly for completeness — the flyAdapter registration happens
	// inside the deploy package itself, not via blank import, but it
	// still must be reachable through the registry path.
	eFly := NewDeployExecutor(deploy.DeployConfig{
		Provider: deploy.ProviderFly,
		AppName:  "x",
	})
	if eFly.deployer == nil {
		t.Fatalf("expected non-nil deployer for ProviderFly; deployerErr=%v", eFly.deployerErr)
	}
	if got := eFly.deployer.Name(); got != "fly" {
		t.Fatalf("deployer.Name() = %q, want %q", got, "fly")
	}
}

// TestDeployExecutor_UnknownProviderError confirms DP2-11 requirement
// #5: a registry miss (ProviderUnknown → "unknown" key) is captured at
// construction time and surfaced through Execute so the descent engine
// sees it as a task-result error rather than a panic or a silent
// dispatch. The error text is the registry's "deploy: unknown provider
// <name>" shape that cmd/stoke/deploy_cmd.go already grep-matches on.
func TestDeployExecutor_UnknownProviderError(t *testing.T) {
	e := NewDeployExecutor(deploy.DeployConfig{
		Provider: deploy.ProviderUnknown,
		AppName:  "x",
	})
	if e.deployerErr == nil {
		t.Fatal("expected non-nil deployerErr for ProviderUnknown")
	}
	_, err := e.Execute(context.Background(), Plan{}, EffortMinimal)
	if err == nil {
		t.Fatal("expected Execute to return non-nil error for ProviderUnknown")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("expected 'unknown provider' in error; got %q", err.Error())
	}
}

func TestDeployDeliverable_Summary(t *testing.T) {
	live := DeployDeliverable{Result: deploy.DeployResult{
		Provider: deploy.ProviderFly, AppName: "api", URL: "https://api.fly.dev",
	}}
	if !strings.Contains(live.Summary(), "live") {
		t.Errorf("live summary should say live; got %q", live.Summary())
	}
	if !strings.Contains(live.Summary(), "https://api.fly.dev") {
		t.Errorf("summary should include URL; got %q", live.Summary())
	}
	dry := DeployDeliverable{Result: deploy.DeployResult{DryRun: true, AppName: "x"}}
	if !strings.Contains(dry.Summary(), "dry-run") {
		t.Errorf("dry summary should say dry-run; got %q", dry.Summary())
	}
}
