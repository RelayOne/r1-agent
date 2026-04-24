package smoketest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/RelayOne/r1/internal/plan"
)

func sessWithFiles(files ...string) plan.Session {
	return plan.Session{
		Tasks: []plan.Task{{ID: "t1", Files: files}},
	}
}

func tempRoot(t *testing.T, layout map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range layout {
		full := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDetectMobileRNIsStaticOnly(t *testing.T) {
	root := tempRoot(t, map[string]string{"package.json": "{}"})
	sess := sessWithFiles("apps/caregiver/app/login.tsx", "apps/installer/app/index.tsx")
	rt := DetectCapability(sess, root)
	if rt.Capability != CapabilityMobileRNExpo {
		t.Fatalf("want mobile-rn-expo, got %s", rt.Capability)
	}
	if rt.Runnable {
		t.Fatal("mobile RN must be static-only on Linux runtime")
	}
	if rt.Reason == "" {
		t.Fatal("static-only verdicts must include a reason")
	}
}

func TestDetectWebNextJSIsRunnable(t *testing.T) {
	root := tempRoot(t, map[string]string{
		"package.json":            "{}",
		"apps/web/next.config.js": "module.exports = {}",
	})
	sess := sessWithFiles("apps/web/app/page.tsx")
	rt := DetectCapability(sess, root)
	if rt.Capability != CapabilityWebNextJS {
		t.Fatalf("want web-nextjs, got %s", rt.Capability)
	}
	if !rt.Runnable {
		t.Fatal("Next.js web app should be runnable via pnpm build")
	}
	if len(rt.RunCommands) == 0 {
		t.Fatal("web-nextjs must populate RunCommands")
	}
}

func TestDetectNativeGo(t *testing.T) {
	root := tempRoot(t, map[string]string{"go.mod": "module x"})
	sess := sessWithFiles("cmd/app/main.go")
	rt := DetectCapability(sess, root)
	if rt.Capability != CapabilityNativeGo {
		t.Fatalf("want native-go, got %s", rt.Capability)
	}
	if !rt.Runnable {
		t.Fatal("Go project should be runnable")
	}
}

func TestDetectExternalAPIFromTitle(t *testing.T) {
	root := tempRoot(t, map[string]string{"go.mod": "module x"})
	// go.mod exists but the session title says Guesty integration.
	// External-API precedence should NOT fire over the concrete Go
	// stack detection — we want the session smoked with go test.
	// So external-api should only win when there's no stack match.
	sess := plan.Session{
		Title:       "Guesty connection wizard",
		Description: "Wire up the Guesty PMS integration",
		Tasks:       []plan.Task{{ID: "t1", Files: []string{}}}, // no files
	}
	// Force no stack match by using an empty repo root without go.mod.
	emptyRoot := t.TempDir()
	rt := DetectCapability(sess, emptyRoot)
	_ = root
	if rt.Capability != CapabilityExternalAPI {
		t.Fatalf("want external-api fallback when no stack match, got %s", rt.Capability)
	}
	if rt.Runnable {
		t.Fatal("external-api must be static-only")
	}
}

func TestDetectUnknownFallback(t *testing.T) {
	root := t.TempDir()
	sess := sessWithFiles()
	rt := DetectCapability(sess, root)
	if rt.Capability != CapabilityUnknown {
		t.Fatalf("want unknown fallback, got %s", rt.Capability)
	}
	if rt.Runnable {
		t.Fatal("unknown capability must not be runnable")
	}
}
