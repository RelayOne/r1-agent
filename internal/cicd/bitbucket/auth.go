// auth.go — OIDC exchange against the R1 audit-endpoint (C5, T10).
//
// Inside a Bitbucket Pipelines step that sets `oidc: true`, Atlassian
// populates the env var `BITBUCKET_STEP_OIDC_TOKEN` with a JWT signed by
// `https://api.bitbucket.org/2.0/workspaces/{workspace}/pipelines-config/identity/oidc`.
// The step trades that JWT to R1's `/auth/sso/oidc-exchange` endpoint for a
// short-lived R1 API token, avoiding the need for any long-lived
// `R1_API_TOKEN` workspace secret.
//
// Dependency: A4 RelayOne SSO must be in flight for the exchange endpoint to
// exist. Until then `ExchangeOIDC` returns `ErrSSONotReady`; the generated
// pipeline YAML detects this error and falls back to the `R1_API_TOKEN`
// workspace variable with a stderr warning (see runner.go).

package bitbucket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ErrSSONotReady is returned when the R1 SSO exchange endpoint is not yet
// available. Callers should fall back to a long-lived `R1_API_TOKEN`.
var ErrSSONotReady = errors.New("bitbucket: ExchangeOIDC: R1 SSO endpoint not yet available")

// OIDCIssuerTemplate is the JWT issuer URL Atlassian advertises for the OIDC
// identity provider; %s is the workspace slug.
const OIDCIssuerTemplate = "https://api.bitbucket.org/2.0/workspaces/%s/pipelines-config/identity/oidc"

// DefaultExchangeEndpoint is the R1 audit-endpoint that trades a Bitbucket
// OIDC JWT for a short-lived R1 token. Override via the second argument of
// ExchangeOIDC for self-hosted R1 deployments.
const DefaultExchangeEndpoint = "/auth/sso/oidc-exchange"

// EnvOIDCToken is the env var Atlassian populates when a pipeline step sets
// `oidc: true`. See https://support.atlassian.com/bitbucket-cloud/docs/integrate-pipelines-with-resource-servers-using-oidc/
const EnvOIDCToken = "BITBUCKET_STEP_OIDC_TOKEN"

// OIDCToken returns the value of the BITBUCKET_STEP_OIDC_TOKEN env var. Empty
// when called outside a pipeline step (or when the step lacks `oidc: true`).
func OIDCToken() string { return os.Getenv(EnvOIDCToken) }

// IssuerFor builds the JWT issuer URL for a workspace.
func IssuerFor(workspace string) string {
	return fmt.Sprintf(OIDCIssuerTemplate, workspace)
}

// exchangeRequest is the JSON body posted to /auth/sso/oidc-exchange.
type exchangeRequest struct {
	OIDCToken string `json:"oidc_token"`
	Issuer    string `json:"issuer"`
	Audience  string `json:"audience"`
}

// exchangeResponse is the JSON body returned by the exchange endpoint.
type exchangeResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	Error     string `json:"error,omitempty"`
}

// ExchangeOIDC trades a Bitbucket OIDC JWT for a short-lived R1 token.
//
//   - audience: the R1 audience string the operator configured in their SSO
//     integration.
//   - oidcToken: the JWT from `BITBUCKET_STEP_OIDC_TOKEN`.
//   - r1Endpoint: the R1 audit-endpoint base URL (without the path); if empty
//     and the R1 SSO endpoint cannot be reached, returns ErrSSONotReady so
//     the caller can fall back to `R1_API_TOKEN`.
//   - workspace: the Bitbucket workspace slug — used to construct the issuer.
//
// Returns the R1 token + an expiry timestamp. Caller should rotate before
// expiresAt.
func ExchangeOIDC(ctx context.Context, audience, oidcToken, r1Endpoint, workspace string) (string, time.Time, error) {
	if oidcToken == "" {
		return "", time.Time{}, errors.New("bitbucket: ExchangeOIDC: oidc token required")
	}
	if audience == "" {
		return "", time.Time{}, errors.New("bitbucket: ExchangeOIDC: audience required")
	}
	if r1Endpoint == "" {
		// A4 SSO has not landed yet; the runner falls back to R1_API_TOKEN.
		return "", time.Time{}, ErrSSONotReady
	}

	url := strings.TrimRight(r1Endpoint, "/") + DefaultExchangeEndpoint
	body := exchangeRequest{
		OIDCToken: oidcToken,
		Issuer:    IssuerFor(workspace),
		Audience:  audience,
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("bitbucket: ExchangeOIDC: encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("bitbucket: ExchangeOIDC: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("bitbucket: ExchangeOIDC: transport: %w", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		// Endpoint not yet wired up — A4 has not landed.
		return "", time.Time{}, ErrSSONotReady
	}
	if resp.StatusCode >= 400 {
		return "", time.Time{}, fmt.Errorf("bitbucket: ExchangeOIDC: %d %s: %s", resp.StatusCode, resp.Status, string(respBytes))
	}
	var out exchangeResponse
	if err := json.Unmarshal(respBytes, &out); err != nil {
		return "", time.Time{}, fmt.Errorf("bitbucket: ExchangeOIDC: decode: %w", err)
	}
	if out.Token == "" {
		return "", time.Time{}, fmt.Errorf("bitbucket: ExchangeOIDC: empty token in response (error=%q)", out.Error)
	}
	exp, _ := time.Parse(time.RFC3339, out.ExpiresAt)
	return out.Token, exp, nil
}
