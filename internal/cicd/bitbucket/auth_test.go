// auth_test.go — tests for the OIDC exchange helper (C5, T10).
package bitbucket

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestExchangeOIDCSuccess covers the happy path.
func TestExchangeOIDCSuccess(t *testing.T) {
	var got exchangeRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != DefaultExchangeEndpoint {
			t.Errorf("path = %q, want %q", r.URL.Path, DefaultExchangeEndpoint)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"token":"r1-tok","expires_at":"2030-01-01T00:00:00Z"}`)
	}))
	defer srv.Close()

	tok, _, err := ExchangeOIDC(context.Background(), "r1-aud", "jwt-here", srv.URL, "myws")
	if err != nil {
		t.Fatalf("ExchangeOIDC: %v", err)
	}
	if tok != "r1-tok" {
		t.Errorf("token = %q", tok)
	}
	if got.Audience != "r1-aud" {
		t.Errorf("got.Audience = %q", got.Audience)
	}
	if got.OIDCToken != "jwt-here" {
		t.Errorf("got.OIDCToken = %q", got.OIDCToken)
	}
	if !strings.Contains(got.Issuer, "myws") {
		t.Errorf("issuer should embed workspace: %q", got.Issuer)
	}
}

// TestExchangeOIDC404FallsBack returns ErrSSONotReady when the endpoint is
// missing (A4 not yet landed).
func TestExchangeOIDC404FallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, _, err := ExchangeOIDC(context.Background(), "aud", "jwt", srv.URL, "ws")
	if !errors.Is(err, ErrSSONotReady) {
		t.Errorf("err = %v, want ErrSSONotReady", err)
	}
}

// TestExchangeOIDCEmptyEndpoint also surfaces ErrSSONotReady — the runner
// uses this to know it should fall back to R1_API_TOKEN.
func TestExchangeOIDCEmptyEndpoint(t *testing.T) {
	_, _, err := ExchangeOIDC(context.Background(), "aud", "jwt", "", "ws")
	if !errors.Is(err, ErrSSONotReady) {
		t.Errorf("err = %v, want ErrSSONotReady", err)
	}
}

// TestExchangeOIDCRequiresInputs.
func TestExchangeOIDCRequiresInputs(t *testing.T) {
	if _, _, err := ExchangeOIDC(context.Background(), "aud", "", "http://x", "ws"); err == nil {
		t.Error("missing token should error")
	}
	if _, _, err := ExchangeOIDC(context.Background(), "", "jwt", "http://x", "ws"); err == nil {
		t.Error("missing audience should error")
	}
}

// TestIssuerForEmbedsWorkspace.
func TestIssuerForEmbedsWorkspace(t *testing.T) {
	got := IssuerFor("my-ws")
	if !strings.Contains(got, "my-ws") {
		t.Errorf("IssuerFor(my-ws) = %q", got)
	}
	if !strings.HasPrefix(got, "https://api.bitbucket.org/2.0/workspaces/") {
		t.Errorf("IssuerFor prefix wrong: %q", got)
	}
}
