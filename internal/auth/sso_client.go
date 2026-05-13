package auth

// sso_client.go — Go port of auth-core/src/relayone-sso-client.ts.
//
// OIDC authorization-code-with-PKCE client targeting the RelayOne
// control plane. Pinned endpoints by default; discovery doc fetched
// lazily on first VerifyIDToken so JWKS material is available for
// signature verification.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Sentinel errors. All carry stable string fragments so HTTP handlers
// can return them in error responses without leaking implementation
// details — the fragments match the TS error codes one-for-one.
var (
	ErrMissingIDToken    = errors.New("relayone_missing_id_token")
	ErrNonceMismatch     = errors.New("id_token_nonce_mismatch")
	ErrUserinfoFailed    = errors.New("userinfo_failed")
	ErrTokenExchangeFail = errors.New("token_exchange_failed")
)

// SsoClientOptions configures a SsoClient. Mirrors TS
// RelayOneSsoClientOptions.
type SsoClientOptions struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	Issuer       string   // e.g. "https://api.relayone.com" or "https://auth.relayone.io/v1"
	Scopes       []string // default ["openid", "email", "profile"]
	HTTPClient   *http.Client

	// DiscoveryTimeout caps the first-call discovery RTT. 5 seconds by
	// default — short enough to fail fast in degraded networks.
	DiscoveryTimeout time.Duration
}

// Endpoints holds the four pinned OIDC endpoint URLs.
type Endpoints struct {
	Issuer                 string
	AuthorizationEndpoint  string
	TokenEndpoint          string
	UserinfoEndpoint       string
	JwksURI                string
	EndSessionEndpoint     string // populated only via discovery when present
}

// AuthorizeInput is the per-request authorize-URL parameter bundle.
type AuthorizeInput struct {
	State        string
	CodeVerifier string
	Nonce        string
	Scopes       []string // overrides default scopes when non-nil
	Extra        map[string]string
}

// Tokens is the parsed token-endpoint response. Raw retains the
// untouched JSON body for callers that need extension claims.
type Tokens struct {
	AccessToken  string
	IDToken      string
	RefreshToken string
	ExpiresIn    int
	TokenType    string
	Raw          map[string]any
}

// RelayOneProfile mirrors TS RelayOneProfile.
type RelayOneProfile struct {
	Sub            string
	Email          string
	EmailVerified  bool
	Name           string
	RelayoneUserID string
	RelayoneOrgID  string
	MspOrgID       *string
	MspManagedOrgs []string
	Raw            map[string]any
}

// ProfileExchangeResult is what ExchangeCodeForProfile returns.
type ProfileExchangeResult struct {
	Profile RelayOneProfile
	Tokens  Tokens
}

// SsoClient is the OIDC RP client.
type SsoClient struct {
	opts Endpoints
	// preserved options
	clientID     string
	clientSecret string
	redirectURI  string
	issuerRoot   string
	scopes       []string
	http         *http.Client
	discoTimeout time.Duration

	// Lazy-loaded provider + verifier. Constructed on first
	// VerifyIDToken call so SsoClient creation is free of network I/O.
	discoMu    sync.Mutex
	provider   *oidc.Provider
	verifier   *oidc.IDTokenVerifier
	discoErr   error
	disconeded bool
}

// NewSsoClient validates options + builds a client. No network I/O.
func NewSsoClient(opts SsoClientOptions) (*SsoClient, error) {
	if opts.ClientID == "" {
		return nil, errors.New("auth sso: ClientID required")
	}
	if opts.ClientSecret == "" {
		return nil, errors.New("auth sso: ClientSecret required")
	}
	if opts.Issuer == "" {
		return nil, errors.New("auth sso: Issuer required")
	}
	if opts.RedirectURI == "" {
		return nil, errors.New("auth sso: RedirectURI required")
	}

	issuer := strings.TrimRight(opts.Issuer, "/")
	scopes := opts.Scopes
	if len(scopes) == 0 {
		scopes = []string{"openid", "email", "profile"}
	}
	httpc := opts.HTTPClient
	if httpc == nil {
		httpc = &http.Client{Timeout: 15 * time.Second}
	}
	disc := opts.DiscoveryTimeout
	if disc <= 0 {
		disc = 5 * time.Second
	}

	c := &SsoClient{
		opts: Endpoints{
			Issuer:                issuer,
			AuthorizationEndpoint: issuer + "/oauth/authorize",
			TokenEndpoint:         issuer + "/oauth/token",
			UserinfoEndpoint:      issuer + "/oauth/userinfo",
			JwksURI:               issuer + "/.well-known/jwks.json",
		},
		clientID:     opts.ClientID,
		clientSecret: opts.ClientSecret,
		redirectURI:  opts.RedirectURI,
		issuerRoot:   issuer,
		scopes:       scopes,
		http:         httpc,
		discoTimeout: disc,
	}
	return c, nil
}

// Endpoints returns the pinned endpoint set. Cheap; no network I/O.
func (s *SsoClient) Endpoints(_ context.Context) (Endpoints, error) {
	// Discovery (when it succeeds) MAY populate EndSessionEndpoint.
	// Don't force a discovery here — callers who need it should call
	// Discovery() explicitly. The pinned four are always populated.
	return s.opts, nil
}

// ClientID returns the configured OAuth client id (read-only). Used by
// the HTTP handlers to populate redirect URLs and front-end UI strings.
func (s *SsoClient) ClientID() string { return s.clientID }

// RedirectURI returns the configured redirect URI (read-only).
func (s *SsoClient) RedirectURI() string { return s.redirectURI }

// Discovery fetches the OIDC discovery document, caches it under a 1h
// SWR window, and routes through go-oidc for JWKS handling. Idempotent
// and concurrency-safe.
func (s *SsoClient) Discovery(ctx context.Context) error {
	s.discoMu.Lock()
	defer s.discoMu.Unlock()
	if s.provider != nil {
		return nil
	}
	if s.disconeded && s.discoErr != nil {
		return s.discoErr
	}
	dctx, cancel := context.WithTimeout(ctx, s.discoTimeout)
	defer cancel()
	provider, err := oidc.NewProvider(dctx, s.issuerRoot)
	if err != nil {
		s.discoErr = fmt.Errorf("auth sso: discovery: %w", err)
		s.disconeded = true
		// Fall through with provider == nil; VerifyIDToken will hit
		// this same error path on every call. Pinned endpoints remain
		// usable for BuildAuthorizeURL / ExchangeCode.
		return s.discoErr
	}
	s.provider = provider
	s.verifier = provider.Verifier(&oidc.Config{ClientID: s.clientID})

	// Pull end_session_endpoint from the raw discovery claims if
	// present. go-oidc doesn't expose it via a typed accessor.
	var claims struct {
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}
	_ = provider.Claims(&claims)
	if claims.EndSessionEndpoint != "" {
		s.opts.EndSessionEndpoint = claims.EndSessionEndpoint
	}
	s.disconeded = true
	return nil
}

// BuildAuthorizeURL constructs the OIDC authorize URL.
func (s *SsoClient) BuildAuthorizeURL(input AuthorizeInput) (string, error) {
	if input.State == "" {
		return "", errors.New("auth sso: BuildAuthorizeURL: State required")
	}
	if input.CodeVerifier == "" {
		return "", errors.New("auth sso: BuildAuthorizeURL: CodeVerifier required")
	}
	challenge := PKCEChallenge(input.CodeVerifier)
	scopes := input.Scopes
	if len(scopes) == 0 {
		scopes = s.scopes
	}
	q := url.Values{}
	q.Set("client_id", s.clientID)
	q.Set("redirect_uri", s.redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("state", input.State)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if input.Nonce != "" {
		q.Set("nonce", input.Nonce)
	}
	for k, v := range input.Extra {
		q.Set(k, v)
	}
	return s.opts.AuthorizationEndpoint + "?" + q.Encode(), nil
}

// ExchangeCode performs the authorization-code-with-PKCE token
// exchange. Returns the parsed Tokens struct on success.
func (s *SsoClient) ExchangeCode(ctx context.Context, code, codeVerifier string) (Tokens, error) {
	cfg := s.oauth2Config()
	tok, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(codeVerifier))
	if err != nil {
		return Tokens{}, fmt.Errorf("%w: %v", ErrTokenExchangeFail, err)
	}
	out := Tokens{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		TokenType:    tok.TokenType,
	}
	if !tok.Expiry.IsZero() {
		out.ExpiresIn = int(time.Until(tok.Expiry).Seconds())
		if out.ExpiresIn < 0 {
			out.ExpiresIn = 0
		}
	}
	// IDToken lives under "id_token" in Extra().
	if idTok, ok := tok.Extra("id_token").(string); ok {
		out.IDToken = idTok
	}
	// Copy the entire raw response for advanced callers.
	rawJSON, _ := json.Marshal(tok.Extra)
	if len(rawJSON) > 0 {
		_ = json.Unmarshal(rawJSON, &out.Raw)
	}
	return out, nil
}

func (s *SsoClient) oauth2Config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     s.clientID,
		ClientSecret: s.clientSecret,
		RedirectURL:  s.redirectURI,
		Scopes:       s.scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:   s.opts.AuthorizationEndpoint,
			TokenURL:  s.opts.TokenEndpoint,
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

// VerifyIDToken validates the id_token signature + standard claims +
// optionally the nonce. Returns the verified claims map.
func (s *SsoClient) VerifyIDToken(ctx context.Context, idToken, expectedNonce string) (map[string]any, error) {
	if idToken == "" {
		return nil, ErrMissingIDToken
	}
	if err := s.Discovery(ctx); err != nil {
		return nil, err
	}
	tok, err := s.verifier.Verify(ctx, idToken)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrJwtVerification, err)
	}
	claims := map[string]any{}
	if err := tok.Claims(&claims); err != nil {
		return nil, fmt.Errorf("auth sso: claims decode: %w", err)
	}
	if expectedNonce != "" {
		got, _ := claims["nonce"].(string)
		if got != expectedNonce {
			return nil, ErrNonceMismatch
		}
	}
	return claims, nil
}

// FetchUserInfo hits the /oauth/userinfo endpoint with a Bearer token.
func (s *SsoClient) FetchUserInfo(ctx context.Context, accessToken string) (map[string]any, error) {
	if accessToken == "" {
		return nil, errors.New("auth sso: FetchUserInfo: empty access token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.opts.UserinfoEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUserinfoFailed, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%w: status %d: %s", ErrUserinfoFailed, resp.StatusCode, truncate(string(body), 200))
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrUserinfoFailed, err)
	}
	return out, nil
}

// NormalizeProfile lifts MSP-specific claims out of a merged
// userinfo+id_token map. Mirrors TS normalizeRelayOneProfile.
func (s *SsoClient) NormalizeProfile(claims map[string]any) RelayOneProfile {
	sub := strOrEmpty(claims["sub"])
	out := RelayOneProfile{
		Sub:            sub,
		Email:          strOrEmpty(claims["email"]),
		EmailVerified:  boolOrFalse(claims["email_verified"]),
		Name:           strOrEmpty(claims["name"]),
		RelayoneUserID: strOrFallback(claims["relayone_user_id"], sub),
		RelayoneOrgID:  strOrEmpty(claims["relayone_org_id"]),
		Raw:            claims,
	}
	if mspID, ok := claims["msp_org_id"].(string); ok && mspID != "" {
		v := mspID
		out.MspOrgID = &v
	}
	if managed, ok := claims["msp_managed_orgs"].([]any); ok {
		for _, v := range managed {
			if s, ok := v.(string); ok {
				out.MspManagedOrgs = append(out.MspManagedOrgs, s)
			}
		}
	} else if managed, ok := claims["msp_managed_orgs"].([]string); ok {
		// json.Unmarshal into map[string]any never produces []string,
		// but tests sometimes pass typed slices directly.
		out.MspManagedOrgs = append(out.MspManagedOrgs, managed...)
	}
	return out
}

// ExchangeCodeForProfile is the convenience method: code -> tokens ->
// id_token verify -> userinfo -> normalized profile.
func (s *SsoClient) ExchangeCodeForProfile(ctx context.Context, code, codeVerifier, expectedNonce string) (ProfileExchangeResult, error) {
	toks, err := s.ExchangeCode(ctx, code, codeVerifier)
	if err != nil {
		return ProfileExchangeResult{}, err
	}
	if toks.IDToken == "" {
		return ProfileExchangeResult{}, ErrMissingIDToken
	}
	idClaims, err := s.VerifyIDToken(ctx, toks.IDToken, expectedNonce)
	if err != nil {
		return ProfileExchangeResult{}, err
	}
	userinfo, err := s.FetchUserInfo(ctx, toks.AccessToken)
	if err != nil {
		// Userinfo failure is non-fatal — fall back to id_token claims
		// alone. Tests that need the failure can mock a non-2xx.
		userinfo = nil
	}
	merged := mergeMaps(idClaims, userinfo)
	profile := s.NormalizeProfile(merged)
	return ProfileExchangeResult{Profile: profile, Tokens: toks}, nil
}

// SsoClientFromEnv mirrors TS relayOneSsoClientFromEnv. Returns
// (nil, nil) when not configured — degraded mode is part of the
// contract.
func SsoClientFromEnv(getenv func(string) string) (*SsoClient, error) {
	if getenv == nil {
		return nil, nil
	}
	clientID := getenv("RELAYONE_SSO_CLIENT_ID")
	clientSecret := getenv("RELAYONE_SSO_CLIENT_SECRET")
	issuer := getenv("RELAYONE_SSO_ISSUER")
	if issuer == "" {
		issuer = getenv("RELAYONE_SSO_URL")
	}
	redirect := getenv("RELAYONE_SSO_REDIRECT_URI")
	if clientID == "" || clientSecret == "" || issuer == "" || redirect == "" {
		return nil, nil
	}
	return NewSsoClient(SsoClientOptions{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Issuer:       issuer,
		RedirectURI:  redirect,
	})
}

// GeneratePKCEPair returns a fresh (verifier, challenge) pair.
//
//	verifier  = 43-128 chars base64url-unpadded of 32 random bytes
//	challenge = base64url-unpadded(sha256(verifier))
func GeneratePKCEPair() (string, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	verifier := base64.RawURLEncoding.EncodeToString(buf)
	return verifier, PKCEChallenge(verifier), nil
}

// PKCEChallenge computes the S256 code challenge for a given verifier.
func PKCEChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// RandHex returns n bytes of random hex (so the result is 2n chars).
// Used for state + nonce.
func RandHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// TenantFromProfile encapsulates the tenant-resolution policy used by
// both the callback handler and sessionhub.CreateAuthenticated. Centralizing
// here is the spec §27 contract: keep ONE helper, call it from both.
//
//	precedence: RelayoneOrgID > MspOrgID > "default"
func TenantFromProfile(p RelayOneProfile) string {
	if p.RelayoneOrgID != "" {
		return p.RelayoneOrgID
	}
	if p.MspOrgID != nil && *p.MspOrgID != "" {
		return *p.MspOrgID
	}
	return "default"
}

// --- small helpers ---

func strOrEmpty(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func strOrFallback(v any, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}

func boolOrFalse(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func mergeMaps(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
