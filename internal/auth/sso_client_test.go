package auth

// sso_client_test.go — unit tests for the SsoClient port. Mirrors
// auth-core/test/relayone-sso-client.test.ts case-for-case.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestEndpointsPinned(t *testing.T) {
	c, err := NewSsoClient(SsoClientOptions{
		ClientID:     "cid",
		ClientSecret: "cs",
		Issuer:       "https://api.relayone.com/",
		RedirectURI:  "https://app/cb",
	})
	if err != nil {
		t.Fatalf("NewSsoClient: %v", err)
	}
	e, err := c.Endpoints(nil)
	if err != nil {
		t.Fatalf("Endpoints: %v", err)
	}
	want := map[string]string{
		"AuthorizationEndpoint": "https://api.relayone.com/oauth/authorize",
		"TokenEndpoint":         "https://api.relayone.com/oauth/token",
		"UserinfoEndpoint":      "https://api.relayone.com/oauth/userinfo",
		"JwksURI":               "https://api.relayone.com/.well-known/jwks.json",
		"Issuer":                "https://api.relayone.com",
	}
	got := map[string]string{
		"AuthorizationEndpoint": e.AuthorizationEndpoint,
		"TokenEndpoint":         e.TokenEndpoint,
		"UserinfoEndpoint":      e.UserinfoEndpoint,
		"JwksURI":               e.JwksURI,
		"Issuer":                e.Issuer,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
}

func TestNormalizeProfile(t *testing.T) {
	c, err := NewSsoClient(SsoClientOptions{
		ClientID: "cid", ClientSecret: "cs", RedirectURI: "https://app/cb",
		Issuer: "https://api.relayone.com",
	})
	if err != nil {
		t.Fatalf("NewSsoClient: %v", err)
	}
	profile := c.NormalizeProfile(map[string]any{
		"sub":               "ro_user_42",
		"email":             "admin@msp.example",
		"email_verified":    true,
		"name":              "MSP Admin",
		"relayone_user_id":  "ro_user_42",
		"relayone_org_id":   "ro_org_msp",
		"msp_org_id":        "ro_org_msp",
		"msp_managed_orgs":  []any{"ro_org_a", "ro_org_b"},
	})
	if profile.MspOrgID == nil || *profile.MspOrgID != "ro_org_msp" {
		t.Errorf("MspOrgID = %v, want ro_org_msp", profile.MspOrgID)
	}
	if len(profile.MspManagedOrgs) != 2 || profile.MspManagedOrgs[0] != "ro_org_a" {
		t.Errorf("MspManagedOrgs = %v", profile.MspManagedOrgs)
	}
	if profile.RelayoneUserID != "ro_user_42" {
		t.Errorf("RelayoneUserID = %q", profile.RelayoneUserID)
	}
	if profile.Email != "admin@msp.example" {
		t.Errorf("Email = %q", profile.Email)
	}
	if !profile.EmailVerified {
		t.Error("EmailVerified should be true")
	}
}

func TestSsoClientFromEnv_DegradedWhenUnconfigured(t *testing.T) {
	c, err := SsoClientFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatalf("error from degraded env: %v", err)
	}
	if c != nil {
		t.Errorf("want nil client, got %v", c)
	}
}

func TestSsoClientFromEnv_AllFourPresent(t *testing.T) {
	env := map[string]string{
		"RELAYONE_SSO_CLIENT_ID":     "cid",
		"RELAYONE_SSO_CLIENT_SECRET": "cs",
		"RELAYONE_SSO_ISSUER":        "https://api.relayone.com",
		"RELAYONE_SSO_REDIRECT_URI":  "https://app/cb",
	}
	c, err := SsoClientFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if c == nil {
		t.Fatal("want client, got nil")
	}
}

func TestSsoClientFromEnv_FallbackToSsoURL(t *testing.T) {
	env := map[string]string{
		"RELAYONE_SSO_CLIENT_ID":     "cid",
		"RELAYONE_SSO_CLIENT_SECRET": "cs",
		"RELAYONE_SSO_URL":           "https://api.relayone.com",
		"RELAYONE_SSO_REDIRECT_URI":  "https://app/cb",
	}
	c, err := SsoClientFromEnv(func(k string) string { return env[k] })
	if err != nil || c == nil {
		t.Fatalf("want client, got %v err=%v", c, err)
	}
}

func TestBuildAuthorizeURL(t *testing.T) {
	c, err := NewSsoClient(SsoClientOptions{
		ClientID: "cid", ClientSecret: "cs", RedirectURI: "https://app/cb",
		Issuer: "https://api.relayone.com",
	})
	if err != nil {
		t.Fatalf("NewSsoClient: %v", err)
	}
	verifier, _, err := GeneratePKCEPair()
	if err != nil {
		t.Fatalf("PKCE: %v", err)
	}
	u, err := c.BuildAuthorizeURL(AuthorizeInput{
		State:        "s-1",
		CodeVerifier: verifier,
		Nonce:        "n-1",
	})
	if err != nil {
		t.Fatalf("BuildAuthorizeURL: %v", err)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	q := parsed.Query()
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
	}
	if q.Get("response_type") != "code" {
		t.Errorf("response_type = %q", q.Get("response_type"))
	}
	if q.Get("state") != "s-1" {
		t.Errorf("state = %q", q.Get("state"))
	}
	if q.Get("client_id") != "cid" {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("nonce") != "n-1" {
		t.Errorf("nonce = %q", q.Get("nonce"))
	}
	if q.Get("code_challenge") == "" {
		t.Errorf("missing code_challenge")
	}
}

func TestExchangeCodeMissingIDToken(t *testing.T) {
	// Mock IdP that returns a token response WITHOUT id_token.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth/token") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"at","expires_in":900,"token_type":"Bearer"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c, err := NewSsoClient(SsoClientOptions{
		ClientID: "cid", ClientSecret: "cs", RedirectURI: "https://app/cb",
		Issuer: srv.URL,
	})
	if err != nil {
		t.Fatalf("NewSsoClient: %v", err)
	}
	_, err = c.ExchangeCodeForProfile(t.Context(), "code", "verifier", "")
	if !errors.Is(err, ErrMissingIDToken) {
		t.Errorf("want ErrMissingIDToken, got %v", err)
	}
}

func TestPKCEChallenge(t *testing.T) {
	// Known answer: PKCE challenge for verifier "abc" is
	// base64url(sha256("abc")) unpadded.
	// sha256("abc") = ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad
	// base64url unpadded = "ungWv48Bz-pBQUDeXa4iI7ADYaOWF3qctBD_YfIAFa0"
	got := PKCEChallenge("abc")
	want := "ungWv48Bz-pBQUDeXa4iI7ADYaOWF3qctBD_YfIAFa0"
	if got != want {
		t.Errorf("PKCEChallenge(abc) = %q, want %q", got, want)
	}
}

func TestTenantFromProfile_Precedence(t *testing.T) {
	cases := []struct {
		name string
		p    RelayOneProfile
		want string
	}{
		{
			name: "relayone_org_id wins",
			p:    RelayOneProfile{RelayoneOrgID: "ro_org"},
			want: "ro_org",
		},
		{
			name: "msp_org_id fallback",
			p:    RelayOneProfile{MspOrgID: strPtr("msp_org")},
			want: "msp_org",
		},
		{
			name: "default fallback",
			p:    RelayOneProfile{},
			want: "default",
		},
		{
			name: "relayone over msp",
			p:    RelayOneProfile{RelayoneOrgID: "ro", MspOrgID: strPtr("msp")},
			want: "ro",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TenantFromProfile(tc.p)
			if got != tc.want {
				t.Errorf("TenantFromProfile = %q, want %q", got, tc.want)
			}
		})
	}
}

func strPtr(s string) *string { return &s }
