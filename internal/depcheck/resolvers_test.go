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

// fakeMultiRegistry serves 200 for names the caller whitelists, 404 otherwise.
// Path normalization lets one server back NPM / PyPI / crates.io / Go proxy.
func fakeMultiRegistry(exists map[string]bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Normalize: trim registry-specific prefixes and suffixes.
		p := strings.Trim(r.URL.Path, "/")
		p = strings.TrimSuffix(p, "/@latest") // go proxy
		p = strings.TrimSuffix(p, "/json")    // pypi
		p = strings.ReplaceAll(p, "%2F", "/") // scoped npm
		if exists[p] {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPyPIFlagsHallucinatedPackage(t *testing.T) {
	srv := fakeMultiRegistry(map[string]bool{"numpy": true, "pandas": true})
	defer srv.Close()
	c := &Client{PyPI: srv.URL, HTTP: srv.Client()}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "requirements.txt"),
		"numpy>=1.24\npandas\nthis-is-definitely-not-a-real-pypi-package==0.0.1\n# comment ignored\n-e .\n")
	findings, err := c.Validate(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(findings), findings)
	}
	if findings[0].Name != "this-is-definitely-not-a-real-pypi-package" {
		t.Fatalf("wrong name: %s", findings[0].Name)
	}
}

func TestCratesFlagsHallucinatedPackage(t *testing.T) {
	srv := fakeMultiRegistry(map[string]bool{"serde": true, "tokio": true})
	defer srv.Close()
	c := &Client{Crates: srv.URL, HTTP: srv.Client()}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Cargo.toml"), `
[package]
name = "example"
version = "0.1.0"

[dependencies]
serde = "1"
tokio = { version = "1", features = ["full"] }
not-a-real-crate-12345 = "0.0.1"

[dev-dependencies]
also-not-real-67890 = "1"
`)
	findings, err := c.Validate(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, f := range findings {
		names[f.Name] = f.Section
	}
	if names["not-a-real-crate-12345"] != "dependencies" {
		t.Fatalf("missing crate finding in dependencies; got %+v", names)
	}
	if names["also-not-real-67890"] != "dev-dependencies" {
		t.Fatalf("missing dev-dep finding; got %+v", names)
	}
}

func TestGoModFlagsHallucinatedModule(t *testing.T) {
	srv := fakeMultiRegistry(map[string]bool{
		"github.com/stretchr/testify": true,
		"github.com/google/uuid":      true,
	})
	defer srv.Close()
	c := &Client{GoMod: srv.URL, HTTP: srv.Client()}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), `module example.com/app

go 1.22

require (
	github.com/stretchr/testify v1.8.0
	github.com/google/uuid v1.3.0
	github.com/example-org/made-up-module v0.0.1
)
`)
	findings, err := c.Validate(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Name != "github.com/example-org/made-up-module" {
		t.Fatalf("expected exactly the hallucinated module; got %+v", findings)
	}
}
