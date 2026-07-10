package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAuthorizeURLBuildsCorrectQuery(t *testing.T) {
	c := NewRelayOneSsoClient("https://sso.example.com", "client-1", "secret", "https://api.r1.run/v1/auth/sso/callback")
	urlStr, err := c.AuthorizeURL("abc123")
	if err != nil {
		t.Fatalf("AuthorizeURL: %v", err)
	}
	u, _ := url.Parse(urlStr)
	q := u.Query()
	if q.Get("response_type") != "code" {
		t.Errorf("response_type=%q", q.Get("response_type"))
	}
	if q.Get("client_id") != "client-1" {
		t.Errorf("client_id=%q", q.Get("client_id"))
	}
	if q.Get("state") != "abc123" {
		t.Errorf("state=%q", q.Get("state"))
	}
	if !strings.Contains(q.Get("scope"), "msp") {
		t.Errorf("scope missing msp: %q", q.Get("scope"))
	}
}

func TestAuthorizeURLRejectsEmptyState(t *testing.T) {
	c := NewRelayOneSsoClient("https://sso.example.com", "id", "secret", "https://r1.run/cb")
	if _, err := c.AuthorizeURL(""); err == nil {
		t.Fatalf("expected empty state to fail")
	}
}

func TestExchangeCodeAndFetchUserInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			if r.Method != http.MethodPost {
				http.Error(w, "wrong method", http.StatusMethodNotAllowed)
				return
			}
			// The RelayOne control plane rejects cookie-less POSTs without
			// the anti-CSRF header; the token request must send it.
			if r.Header.Get("X-CSRF-Protection") != "1" {
				http.Error(w, "csrf_header_missing", http.StatusForbidden)
				return
			}
			_ = r.ParseForm()
			if r.PostForm.Get("code") != "the-code" {
				http.Error(w, "wrong code", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(TokenResponse{
				AccessToken: "access-1", TokenType: "Bearer", ExpiresIn: 3600,
			})
		case "/oauth/userinfo":
			if r.Header.Get("Authorization") != "Bearer access-1" {
				http.Error(w, "missing/wrong bearer", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(UserInfo{
				Sub: "user-42", Email: "u@example.com", MSP: "msp-9", Org: "org-x", Roles: []string{"operator"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewRelayOneSsoClient(srv.URL, "client-1", "secret", "https://r1.run/cb")
	tr, err := c.ExchangeCode(context.Background(), "the-code")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if tr.AccessToken != "access-1" {
		t.Fatalf("AccessToken=%q", tr.AccessToken)
	}
	ui, err := c.FetchUserInfo(context.Background(), tr.AccessToken)
	if err != nil {
		t.Fatalf("FetchUserInfo: %v", err)
	}
	if ui.Sub != "user-42" || ui.MSP != "msp-9" {
		t.Fatalf("userinfo mismatch: %+v", ui)
	}
}

func TestLoginEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(TokenResponse{AccessToken: "tok"})
		case "/oauth/userinfo":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(UserInfo{
				Sub: "u-1", Email: "u@e.com", MSP: "msp-1", Roles: []string{"member"},
			})
		}
	}))
	defer srv.Close()

	sso := NewRelayOneSsoClient(srv.URL, "id", "secret", "/cb")
	jwt := newTestService()
	tok, ui, err := sso.Login(context.Background(), "code", jwt)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if ui.Sub != "u-1" {
		t.Fatalf("ui.Sub=%q", ui.Sub)
	}
	c, err := jwt.Verify(tok)
	if err != nil {
		t.Fatalf("Verify issued JWT: %v", err)
	}
	if c.Sub != "u-1" || c.MSP != "msp-1" {
		t.Fatalf("Claims mismatch: %+v", c)
	}
	if len(c.Roles) != 1 || c.Roles[0] != "member" {
		t.Fatalf("Roles=%v", c.Roles)
	}
}

func TestExchangeCodeNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid_grant", http.StatusBadRequest)
	}))
	defer srv.Close()
	c := NewRelayOneSsoClient(srv.URL, "id", "secret", "/cb")
	if _, err := c.ExchangeCode(context.Background(), "bad"); err == nil {
		t.Fatalf("expected non-2xx error, got nil")
	}
}

func TestGenerateStateProducesUniqueStrings(t *testing.T) {
	a, _ := GenerateState()
	b, _ := GenerateState()
	if a == "" || a == b {
		t.Fatalf("expected unique non-empty states, got a=%q b=%q", a, b)
	}
}
