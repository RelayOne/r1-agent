package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireLoopbackHost_AcceptsLoopback(t *testing.T) {
	mw := requireLoopbackHost(50321)
	srv := httptest.NewServer(mw(okHandler))
	defer srv.Close()

	cases := []string{"127.0.0.1:50321", "localhost:50321"}
	for _, host := range cases {
		t.Run(host, func(t *testing.T) {
			req, _ := http.NewRequest("GET", srv.URL+"/", nil)
			req.Host = host
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200", resp.StatusCode)
			}
		})
	}
}

func TestRequireLoopbackHost_RejectsNonLoopback(t *testing.T) {
	mw := requireLoopbackHost(50321)
	srv := httptest.NewServer(mw(okHandler))
	defer srv.Close()

	bad := []string{
		"evil.com",
		"evil.com:50321",
		"127.0.0.1:9999",   // wrong port
		"localhost:1",      // wrong port
		"::1:50321",        // ipv6 — not in allow-list
		"attacker.localhost:50321",
		"",                 // empty Host
	}
	for _, host := range bad {
		t.Run(host, func(t *testing.T) {
			req, _ := http.NewRequest("GET", srv.URL+"/", nil)
			req.Host = host
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("host=%q status=%d, want 403", host, resp.StatusCode)
			}
		})
	}
}

func TestRequireLoopbackHost_PortMismatchRejected(t *testing.T) {
	// Build with port 50321, query with port 50322 — the mismatch
	// must be caught even though the loopback-IP literal matches.
	mw := requireLoopbackHost(50321)
	srv := httptest.NewServer(mw(okHandler))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.Host = fmt.Sprintf("127.0.0.1:%d", 50322)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("port-mismatch status = %d, want 403", resp.StatusCode)
	}
}
