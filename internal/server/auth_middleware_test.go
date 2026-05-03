package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// okHandler is a tiny handler the middleware tests wrap. Returns
// 200 OK with body "passed" so tests can assert the request reached
// the inner handler.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("passed"))
})

func TestRequireBearer_AcceptsMatch(t *testing.T) {
	mw := requireBearer("supersecret")
	srv := httptest.NewServer(mw(okHandler))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.Header.Set("Authorization", "Bearer supersecret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestRequireBearer_RejectsMissing(t *testing.T) {
	mw := requireBearer("supersecret")
	srv := httptest.NewServer(mw(okHandler))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); got != `Bearer realm="r1"` {
		t.Errorf("WWW-Authenticate = %q, want Bearer realm=\"r1\"", got)
	}
}

func TestRequireBearer_RejectsWrongToken(t *testing.T) {
	mw := requireBearer("supersecret")
	srv := httptest.NewServer(mw(okHandler))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.Header.Set("Authorization", "Bearer wrongtoken")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestRequireBearer_RejectsWrongScheme(t *testing.T) {
	mw := requireBearer("supersecret")
	srv := httptest.NewServer(mw(okHandler))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestRequireBearer_RejectsEmptyToken(t *testing.T) {
	// Empty token construction is a programming error; the
	// middleware must refuse every request rather than fall open.
	mw := requireBearer("")
	srv := httptest.NewServer(mw(okHandler))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.Header.Set("Authorization", "Bearer ")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("empty-token middleware fell open (status=%d)", resp.StatusCode)
	}
}
