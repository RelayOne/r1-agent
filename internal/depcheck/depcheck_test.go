package depcheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRegistry serves 200 for a whitelist of names, 404 for everything else.
func fakeRegistry(real map[string]bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		name = strings.ReplaceAll(name, "%2F", "/") // handle scoped names
		if real[name] {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

func writePackageJSON(t *testing.T, dir, contents string) string {
	t.Helper()
	p := filepath.Join(dir, "package.json")
	if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestValidatePackageJSONFlagsHallucinatedDep(t *testing.T) {
	srv := fakeRegistry(map[string]bool{
		"nativewind":        true,
		"react":             true,
		"@types/react":      true,
		"react-native":      true,
	})
	defer srv.Close()

	c := &Client{Registry: srv.URL, HTTP: srv.Client()}
	dir := t.TempDir()
	p := writePackageJSON(t, dir, `{
		"name": "@sentinel/ui-mobile",
		"dependencies": {
			"nativewind": "^4.0.0",
			"react-native": "^0.74.0"
		},
		"peerDependencies": {
			"@nativewind/style": "^0.0.1",
			"@nativewind/cli": "^0.0.1",
			"react": "^18.2.0"
		},
		"devDependencies": {
			"@types/react": "^18"
		}
	}`)

	findings, err := c.ValidatePackageJSON(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"@nativewind/style": true,
		"@nativewind/cli":   true,
	}
	got := map[string]bool{}
	for _, f := range findings {
		got[f.Name] = true
	}
	if len(got) != len(want) {
		t.Fatalf("got findings %v, want %v", got, want)
	}
	for name := range want {
		if !got[name] {
			t.Fatalf("expected finding for %q, got %v", name, got)
		}
	}
	for _, f := range findings {
		if f.Section != "peerDependencies" {
			t.Fatalf("expected section peerDependencies, got %q for %q", f.Section, f.Name)
		}
		if f.Reason == "" {
			t.Fatal("reason must be populated")
		}
	}
}

func TestValidatePackageJSONSkipsNonRegistryRefs(t *testing.T) {
	// This test uses an unreachable registry; every dep must be skipped
	// because they're all non-registry refs.
	c := &Client{Registry: "http://127.0.0.1:1", HTTP: &http.Client{}}
	dir := t.TempDir()
	p := writePackageJSON(t, dir, `{
		"dependencies": {
			"@sentinel/types": "workspace:*",
			"local-pkg": "file:../local-pkg",
			"tarball": "https://example.com/tar.tgz",
			"from-git": "git+ssh://git@github.com/org/repo.git",
			"npm-alias": "npm:real-name@1.2.3",
			"gh-shortcut": "github:org/repo"
		}
	}`)
	findings, err := c.ValidatePackageJSON(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %+v", findings)
	}
}

func TestValidateTreeFindsAllManifests(t *testing.T) {
	srv := fakeRegistry(map[string]bool{"react": true})
	defer srv.Close()
	c := &Client{Registry: srv.URL, HTTP: srv.Client()}
	root := t.TempDir()

	// Good root package.json
	writePackageJSON(t, root, `{"dependencies":{"react":"^18"}}`)

	// Bad nested package.json under packages/foo
	nested := filepath.Join(root, "packages", "foo")
	os.MkdirAll(nested, 0o755)
	writePackageJSON(t, nested, `{"dependencies":{"not-a-real-pkg":"^1"}}`)

	// node_modules is expected to be ignored
	nm := filepath.Join(root, "node_modules", "react")
	os.MkdirAll(nm, 0o755)
	writePackageJSON(t, nm, `{"dependencies":{"also-not-real-pkg":"^1"}}`)

	findings, err := c.ValidateTree(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Name != "not-a-real-pkg" {
		t.Fatalf("expected exactly one finding for not-a-real-pkg, got %+v", findings)
	}
}

func TestTransportErrorDoesNotFlag(t *testing.T) {
	// Unreachable registry — we should see zero findings rather than
	// spurious flags, because a dead registry is not evidence that a
	// package doesn't exist.
	c := &Client{Registry: "http://127.0.0.1:1", HTTP: &http.Client{}}
	dir := t.TempDir()
	p := writePackageJSON(t, dir, `{"dependencies":{"react":"^18","@nativewind/style":"^0.0.1"}}`)
	findings, err := c.ValidatePackageJSON(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected zero findings on transport error, got %+v", findings)
	}
}
