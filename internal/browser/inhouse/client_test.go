// client_test.go: in-house client unit + conformance tests.

package inhouse

import (
	"context"
	"errors"
	"testing"

	"github.com/RelayOne/r1/internal/browser"
)

// TestClient_Conformance runs the shared Provider contract suite
// against the mock CDP server. Same body as Browserless — that
// identical-conformance property is what spec C6 §T13 demands.
func TestClient_Conformance(t *testing.T) {
	t.Parallel()
	browser.ProviderConformance(t, func() (browser.Provider, error) {
		mock := startMockCDP(t)
		return NewClient(Config{
			Endpoint:      mock.wsURL,
			AllowInsecure: true,
			TokenMinter:   &StubMinter{Token: "stub-id-token"},
		})
	})
}

// TestClient_NetworkPolicy runs the shared denial-path suite.
func TestClient_NetworkPolicy(t *testing.T) {
	t.Parallel()
	browser.NetworkPolicyDenialTest(t, func() (browser.Provider, error) {
		mock := startMockCDP(t)
		return NewClient(Config{
			Endpoint:      mock.wsURL,
			AllowInsecure: true,
			TokenMinter:   &StubMinter{Token: "stub-id-token"},
		})
	})
}

// TestClient_SendsBearerHeader asserts that the WS dial carries
// `Authorization: Bearer <stub-token>` — the load-bearing T3a
// security claim.
func TestClient_SendsBearerHeader(t *testing.T) {
	t.Parallel()
	mock := startMockCDP(t)
	mock.requireBearer = "secret-id-token"
	c, err := NewClient(Config{
		Endpoint:      mock.wsURL,
		AllowInsecure: true,
		TokenMinter:   &StubMinter{Token: "secret-id-token"},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	s, err := c.Open(context.Background(), browser.SessionOpts{TenantID: "acme"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close(context.Background())

	got := mock.receivedAuth.Load()
	if got == nil {
		t.Fatal("mock did not record Authorization header")
	}
	if *got != "Bearer secret-id-token" {
		t.Errorf("Authorization = %q, want Bearer secret-id-token", *got)
	}
}

// TestClient_AuthFailure surfaces as ErrAuthFailed when the mock
// rejects the bearer token.
func TestClient_AuthFailure(t *testing.T) {
	t.Parallel()
	mock := startMockCDP(t)
	mock.requireBearer = "the-correct-one"
	c, err := NewClient(Config{
		Endpoint:      mock.wsURL,
		AllowInsecure: true,
		TokenMinter:   &StubMinter{Token: "the-wrong-one"},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.Open(context.Background(), browser.SessionOpts{TenantID: "acme"})
	if !errors.Is(err, browser.ErrAuthFailed) {
		t.Errorf("Open with bad bearer = %v, want ErrAuthFailed", err)
	}
}

// TestConfigValidate pins the scheme rules.
func TestConfigValidate(t *testing.T) {
	t.Parallel()
	if err := (Config{Endpoint: ""}).Validate(); err == nil {
		t.Errorf("empty endpoint should fail")
	}
	if err := (Config{Endpoint: "wss://browser.r1.run"}).Validate(); err != nil {
		t.Errorf("wss should pass: %v", err)
	}
	if err := (Config{Endpoint: "ws://local"}).Validate(); err == nil {
		t.Errorf("ws:// without AllowInsecure should fail")
	}
	if err := (Config{Endpoint: "ws://local", AllowInsecure: true}).Validate(); err != nil {
		t.Errorf("ws:// with AllowInsecure should pass: %v", err)
	}
}

// TestStubMinter pins the test-only minter.
func TestStubMinter(t *testing.T) {
	t.Parallel()
	tok, err := (&StubMinter{Token: "t1"}).MintIDToken(context.Background(), "aud")
	if err != nil || tok != "t1" {
		t.Errorf("StubMinter = %q %v", tok, err)
	}
	wantErr := errors.New("boom")
	_, err = (&StubMinter{Err: wantErr}).MintIDToken(context.Background(), "aud")
	if !errors.Is(err, wantErr) {
		t.Errorf("StubMinter err = %v, want %v", err, wantErr)
	}
}

// TestClient_TokenCache asserts the 25-min refresh policy uses the
// cached token across multiple Open calls.
func TestClient_TokenCache(t *testing.T) {
	t.Parallel()
	mock := startMockCDP(t)
	mint := &countingMinter{token: "cached-token"}
	c, err := NewClient(Config{
		Endpoint:      mock.wsURL,
		AllowInsecure: true,
		TokenMinter:   mint,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	// Two consecutive Opens should reuse one token mint.
	for i := 0; i < 3; i++ {
		s, err := c.Open(context.Background(), browser.SessionOpts{TenantID: "acme"})
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		s.Close(context.Background())
	}
	if mint.calls != 1 {
		t.Errorf("token minter calls = %d, want 1 (cache)", mint.calls)
	}
}

type countingMinter struct {
	token string
	calls int
}

func (m *countingMinter) MintIDToken(ctx context.Context, aud string) (string, error) {
	m.calls++
	return m.token, nil
}
