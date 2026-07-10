// sso.go — RelayOneSsoClient. OIDC client against RelayOne's
// /oauth/authorize, /oauth/token, /oauth/userinfo. Lifts MSP claims
// (msp, org, roles) into the issued r1 JWT.
//
// Mirrors @relayone/auth-core RelayOneSsoClient public surface.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RelayOneSsoClient implements the OIDC code-flow handshake against
// RelayOne's identity provider.
type RelayOneSsoClient struct {
	BaseURL      string // e.g. https://sso.relayone.ai
	ClientID     string
	ClientSecret string
	RedirectURI  string

	HTTP    *http.Client
	Now     func() time.Time
	Scopes  []string

	// AuthCodeEndpointPath defaults to /oauth/authorize.
	AuthCodeEndpointPath string
	// TokenEndpointPath defaults to /oauth/token.
	TokenEndpointPath string
	// UserInfoEndpointPath defaults to /oauth/userinfo.
	UserInfoEndpointPath string
}

// NewRelayOneSsoClient builds a client with safe defaults.
func NewRelayOneSsoClient(baseURL, clientID, clientSecret, redirectURI string) *RelayOneSsoClient {
	return &RelayOneSsoClient{
		BaseURL:              strings.TrimRight(baseURL, "/"),
		ClientID:             clientID,
		ClientSecret:         clientSecret,
		RedirectURI:          redirectURI,
		HTTP:                 &http.Client{Timeout: 15 * time.Second},
		Now:                  time.Now,
		Scopes:               []string{"openid", "email", "profile", "msp"},
		AuthCodeEndpointPath: "/oauth/authorize",
		TokenEndpointPath:    "/oauth/token",
		UserInfoEndpointPath: "/oauth/userinfo",
	}
}

// AuthorizeURL returns the URL to redirect the user to for SSO. The
// caller should set state to a CSRF-safe per-session value.
func (c *RelayOneSsoClient) AuthorizeURL(state string) (string, error) {
	if state == "" {
		return "", errors.New("state must not be empty")
	}
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {c.ClientID},
		"redirect_uri":  {c.RedirectURI},
		"scope":         {strings.Join(c.Scopes, " ")},
		"state":         {state},
	}
	return c.BaseURL + c.AuthCodeEndpointPath + "?" + q.Encode(), nil
}

// GenerateState returns a CSRF-safe random state string for the auth
// flow. Caller stores this in a short-lived cookie or session, then
// validates that the callback's state matches.
func GenerateState() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// TokenResponse mirrors RFC 6749 + OIDC.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// ExchangeCode swaps an authorization code for an access token.
func (c *RelayOneSsoClient) ExchangeCode(ctx context.Context, code string) (*TokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.RedirectURI},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+c.TokenEndpointPath, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	// RelayOne's control plane guards every cookie-less state-changing POST
	// with a custom-header anti-CSRF check (X-CSRF-Protection: 1). This
	// token request authenticates via client_secret in the body
	// (client_secret_post), so it carries neither a Bearer token nor an
	// API-key header that would otherwise exempt it — send the header so the
	// exchange is not rejected with csrf_header_missing.
	req.Header.Set("X-CSRF-Protection", "1")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("token endpoint %d: %s", resp.StatusCode, body)
	}
	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, errors.New("token response missing access_token")
	}
	return &tr, nil
}

// UserInfo holds the userinfo claims plus the MSP-specific fields.
type UserInfo struct {
	Sub           string   `json:"sub"`
	Email         string   `json:"email,omitempty"`
	EmailVerified bool     `json:"email_verified,omitempty"`
	Name          string   `json:"name,omitempty"`
	MSP           string   `json:"msp,omitempty"`           // RelayOne MSP-specific
	Org           string   `json:"org,omitempty"`           // org slug within the MSP
	Roles         []string `json:"roles,omitempty"`         // operator / member / readonly / etc.
}

// FetchUserInfo calls /oauth/userinfo with the bearer access token.
func (c *RelayOneSsoClient) FetchUserInfo(ctx context.Context, accessToken string) (*UserInfo, error) {
	if accessToken == "" {
		return nil, errors.New("access token must not be empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+c.UserInfoEndpointPath, nil)
	if err != nil {
		return nil, fmt.Errorf("build userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("userinfo endpoint %d: %s", resp.StatusCode, body)
	}
	var ui UserInfo
	if err := json.Unmarshal(body, &ui); err != nil {
		return nil, fmt.Errorf("parse userinfo: %w", err)
	}
	if ui.Sub == "" {
		return nil, errors.New("userinfo missing sub")
	}
	return &ui, nil
}

// Login is the convenience method consumers call from the OIDC callback.
// It exchanges the code, fetches userinfo, lifts MSP claims, and returns
// a Claims struct ready to be Sign()-ed by JwtService.
func (c *RelayOneSsoClient) Login(ctx context.Context, code string, jwt *JwtService) (string, *UserInfo, error) {
	tr, err := c.ExchangeCode(ctx, code)
	if err != nil {
		return "", nil, err
	}
	ui, err := c.FetchUserInfo(ctx, tr.AccessToken)
	if err != nil {
		return "", nil, err
	}
	claims := Claims{
		Sub:   ui.Sub,
		Email: ui.Email,
		MSP:   ui.MSP,
		Org:   ui.Org,
		Roles: ui.Roles,
	}
	tok, err := jwt.Sign(claims)
	if err != nil {
		return "", nil, fmt.Errorf("sign r1 jwt: %w", err)
	}
	return tok, ui, nil
}
