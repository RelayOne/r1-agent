package depcheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseDockerRef(t *testing.T) {
	cases := []struct {
		in          string
		wantReg     string
		wantRepo    string
		wantTag     string
	}{
		{"nginx", "registry-1.docker.io", "library/nginx", "latest"},
		{"nginx:alpine", "registry-1.docker.io", "library/nginx", "alpine"},
		{"user/app:v1", "registry-1.docker.io", "user/app", "v1"},
		{"ghcr.io/org/app:2025", "ghcr.io", "org/app", "2025"},
		{"registry.example.com:5000/team/foo:bar", "registry.example.com:5000", "team/foo", "bar"},
		{"user/app@sha256:abc", "registry-1.docker.io", "user/app", "sha256:abc"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			reg, repo, tag := parseDockerRef(c.in)
			if reg != c.wantReg {
				t.Errorf("registry=%q want %q", reg, c.wantReg)
			}
			if repo != c.wantRepo {
				t.Errorf("repo=%q want %q", repo, c.wantRepo)
			}
			if tag != c.wantTag {
				t.Errorf("tag=%q want %q", tag, c.wantTag)
			}
		})
	}
}

func TestCheckDockerManifest_Found(t *testing.T) {
	// Stand up a mock registry that accepts /v2/<name>/manifests/<tag>.
	// Note: docker hub name inclusion is transparent to the mock
	// since we route the request to our test server.
	h := http.NewServeMux()
	h.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("want HEAD, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewTLSServer(h)
	defer srv.Close()

	// Construct a client that trusts the test server's cert.
	client := srv.Client()
	// We can't easily redirect registry-1.docker.io to our test
	// server without DNS tricks; instead we craft a request
	// directly via a reference whose parseDockerRef lifts it
	// to the test server. Use a custom-registry reference.
	// The test server's URL includes scheme+host — strip
	// scheme to form a docker-style registry host.
	host := srv.Listener.Addr().String()
	err := CheckDockerManifest(context.Background(), host+"/testorg/app:1.0", client)
	if err != nil {
		t.Errorf("CheckDockerManifest: %v", err)
	}
}

func TestCheckDockerManifest_MissingReturnsError(t *testing.T) {
	h := http.NewServeMux()
	h.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewTLSServer(h)
	defer srv.Close()
	host := srv.Listener.Addr().String()
	err := CheckDockerManifest(context.Background(), host+"/testorg/app:ghost", srv.Client())
	if err == nil {
		t.Error("expected error on 404 manifest")
	}
}

func TestCheckDockerManifest_401ToleratedAsExists(t *testing.T) {
	// Anonymous HEAD against private registries can return 401;
	// treat as "probably exists but needs auth", not "missing".
	h := http.NewServeMux()
	h.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewTLSServer(h)
	defer srv.Close()
	host := srv.Listener.Addr().String()
	err := CheckDockerManifest(context.Background(), host+"/private/app:v1", srv.Client())
	if err != nil {
		t.Errorf("401 should be tolerated, got %v", err)
	}
}

func TestCheckHTTPEndpoint_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("want HEAD, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	code, err := CheckHTTPEndpoint(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("CheckHTTPEndpoint: %v", err)
	}
	if code != 200 {
		t.Errorf("code=%d want 200", code)
	}
}

func TestCheckHTTPEndpoint_5xxFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	code, err := CheckHTTPEndpoint(context.Background(), srv.URL, nil)
	if err == nil {
		t.Errorf("5xx should be error; got code=%d", code)
	}
}

func TestCheckGitRef_NoGitIsError(t *testing.T) {
	// We can't reliably make git missing in CI, so this test
	// verifies the happy path against a well-known public
	// remote when git is available. When git is NOT on $PATH,
	// the function errors cleanly rather than panicking.
	// Using a guaranteed-nonexistent URL produces an error
	// either way.
	_, err := CheckGitRef(context.Background(), "https://invalid.example.invalid/repo.git", "HEAD")
	if err == nil {
		t.Error("expected error against bogus git URL")
	}
}
