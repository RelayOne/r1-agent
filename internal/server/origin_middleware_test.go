package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequireLoopbackOrigin_AllowsSafeMethods(t *testing.T) {
	mw := requireLoopbackOrigin()
	srv := httptest.NewServer(mw(okHandler))
	defer srv.Close()

	for _, method := range []string{"GET", "HEAD", "OPTIONS"} {
		t.Run(method, func(t *testing.T) {
			req, _ := http.NewRequest(method, srv.URL+"/", nil)
			req.Header.Set("Origin", "https://evil.com")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200 (safe method, Origin not gated)", resp.StatusCode)
			}
		})
	}
}

func TestRequireLoopbackOrigin_RejectsStateChangingFromCrossOrigin(t *testing.T) {
	mw := requireLoopbackOrigin()
	srv := httptest.NewServer(mw(okHandler))
	defer srv.Close()

	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			req, _ := http.NewRequest(method, srv.URL+"/", strings.NewReader(""))
			req.Header.Set("Origin", "https://evil.com")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("status = %d, want 403", resp.StatusCode)
			}
		})
	}
}

func TestRequireLoopbackOrigin_AllowsLoopbackOrigins(t *testing.T) {
	mw := requireLoopbackOrigin()
	srv := httptest.NewServer(mw(okHandler))
	defer srv.Close()

	good := []string{
		"http://localhost",
		"http://localhost:50321",
		"https://localhost:50321",
		"http://127.0.0.1",
		"http://127.0.0.1:50321",
		"https://127.0.0.1:50321",
		"http://[::1]:50321",
	}
	for _, origin := range good {
		t.Run(origin, func(t *testing.T) {
			req, _ := http.NewRequest("POST", srv.URL+"/", strings.NewReader(""))
			req.Header.Set("Origin", origin)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("origin=%q status=%d, want 200", origin, resp.StatusCode)
			}
		})
	}
}

func TestRequireLoopbackOrigin_AllowsNullAndMissing(t *testing.T) {
	mw := requireLoopbackOrigin()
	srv := httptest.NewServer(mw(okHandler))
	defer srv.Close()

	t.Run("null", func(t *testing.T) {
		req, _ := http.NewRequest("POST", srv.URL+"/", strings.NewReader(""))
		req.Header.Set("Origin", "null")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("null Origin status=%d, want 200 (sandboxed iframe / file://)", resp.StatusCode)
		}
	})

	t.Run("missing", func(t *testing.T) {
		req, _ := http.NewRequest("POST", srv.URL+"/", strings.NewReader(""))
		// No Origin header at all — non-browser CLI client.
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("missing Origin status=%d, want 200 (CLI HTTP client)", resp.StatusCode)
		}
	})
}

func TestRequireLoopbackOrigin_GatesWSUpgrade(t *testing.T) {
	mw := requireLoopbackOrigin()
	srv := httptest.NewServer(mw(okHandler))
	defer srv.Close()

	// GET with Upgrade: websocket from cross-origin must be 403.
	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.Header.Set("Connection", "upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Origin", "https://evil.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("WS upgrade from evil.com status=%d, want 403", resp.StatusCode)
	}

	// GET with Upgrade: websocket from loopback must pass.
	req2, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req2.Header.Set("Connection", "upgrade")
	req2.Header.Set("Upgrade", "websocket")
	req2.Header.Set("Origin", "http://localhost:50321")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("WS upgrade from localhost status=%d, want 200", resp2.StatusCode)
	}
}

func TestRequireLoopbackOrigin_ConnectionTokenCaseInsensitive(t *testing.T) {
	// RFC 6455 §4.2.1 allows comma-separated, case-insensitive
	// token lists — Connection: keep-alive, Upgrade is valid.
	mw := requireLoopbackOrigin()
	srv := httptest.NewServer(mw(okHandler))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.Header.Set("Connection", "keep-alive, Upgrade")
	req.Header.Set("Upgrade", "WebSocket")
	req.Header.Set("Origin", "https://evil.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("multi-token Connection upgrade not gated; status=%d, want 403", resp.StatusCode)
	}
}
