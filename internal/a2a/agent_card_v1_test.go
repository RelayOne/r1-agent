// Package a2a — agent_card_v1_test.go
//
// Integration tests for the A2A v1.0.0 Agent Card schema
// migration (WORK-stoke T22). Covers:
//
//   - Canonical route /.well-known/agent-card.json returns the
//     v1.0 card with protocolVersion=="1.0.0".
//   - Legacy route /.well-known/agent.json returns HTTP 308
//     Permanent Redirect with Location pointing at the canonical
//     route, plus Deprecation + Sunset headers signaling the
//     30-day removal window.
//   - The JSON payload includes the v1.0 additive fields
//     (skills, securitySchemes, preferredTransport,
//     additionalInterfaces, supportsAuthenticatedExtendedCard).
package a2a

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// noFollowClient is an http.Client that refuses to follow
// redirects, so we can assert 308 + Location directly instead of
// the client transparently fetching the target.
func noFollowClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func TestAgentCard_V1_CanonicalRoute(t *testing.T) {
	card := Build(Options{
		Name:    "test-agent-v1",
		Version: "1.0.0",
		Capabilities: []CapabilityRef{
			{Name: "review", Version: "1"},
		},
	})
	srv := NewServer(card, NewInMemoryTaskStore(), "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+CanonicalCardPath, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get canonical card: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("canonical status=%d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type=%q want application/json", ct)
	}

	var got AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ProtocolVersion != "1.0.0" {
		t.Errorf("protocolVersion=%q want 1.0.0", got.ProtocolVersion)
	}
	if got.Name != "test-agent-v1" {
		t.Errorf("name=%q want test-agent-v1", got.Name)
	}
}

func TestAgentCard_V1_LegacyRedirect(t *testing.T) {
	card := Build(Options{Name: "legacy-test", Version: "1.0.0"})
	srv := NewServer(card, NewInMemoryTaskStore(), "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := noFollowClient()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+LegacyCardPath, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("get legacy path: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPermanentRedirect {
		t.Fatalf("legacy status=%d want 308 Permanent Redirect", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != CanonicalCardPath {
		t.Errorf("Location=%q want %q", loc, CanonicalCardPath)
	}
	// RFC 8594 sunset header must be present to signal the 30-day
	// removal window. Exact format is an HTTP-date.
	if sunset := resp.Header.Get("Sunset"); sunset == "" {
		t.Error("Sunset header missing; clients need this to log the deprecation")
	}
	if dep := resp.Header.Get("Deprecation"); dep == "" {
		t.Error("Deprecation header missing")
	}
	// Link: rel=successor-version is the standard pointer at the
	// new URL in a machine-readable form (RFC 5988).
	if link := resp.Header.Get("Link"); !strings.Contains(link, `rel="successor-version"`) {
		t.Errorf("Link header missing rel=successor-version, got %q", link)
	}
}

func TestAgentCard_V1_LegacyRedirectFollowedYieldsCanonicalCard(t *testing.T) {
	// End-to-end sanity: a default http.Client that follows
	// redirects should land on the canonical card transparently.
	// This protects the pre-T22 tests that hit the legacy path.
	card := Build(Options{Name: "follow-test", Version: "1.0.0"})
	srv := NewServer(card, NewInMemoryTaskStore(), "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+LegacyCardPath, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("follow redirect: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("followed status=%d want 200", resp.StatusCode)
	}
	// Final URL should be the canonical path.
	if !strings.HasSuffix(resp.Request.URL.Path, CanonicalCardPath) {
		t.Errorf("final path=%q want suffix %q", resp.Request.URL.Path, CanonicalCardPath)
	}
	var got AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ProtocolVersion != "1.0.0" {
		t.Errorf("protocolVersion=%q want 1.0.0", got.ProtocolVersion)
	}
}

func TestAgentCard_V1_LegacyRedirectRejectsPOST(t *testing.T) {
	// POST to the legacy path must NOT redirect — 308 preserves
	// method, so a naive client would POST against the canonical
	// path (which is GET-only). Easier + safer to 405 POSTs here.
	card := Build(Options{Name: "x", Version: "1.0.0"})
	srv := NewServer(card, NewInMemoryTaskStore(), "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := noFollowClient()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+LegacyCardPath, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post legacy: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST legacy status=%d want 405", resp.StatusCode)
	}
}

func TestAgentCard_V1_SchemaFields(t *testing.T) {
	// Build a card that exercises every A2A v1.0.0 additive
	// field so the JSON contract is pinned in CI.
	card := Build(Options{
		Name:    "schema-test",
		Version: "1.0.0",
		Skills: []SkillDescriptor{
			{
				ID:          "code-review",
				Name:        "Code Review",
				Description: "Cross-model review of proposed diffs.",
				Tags:        []string{"review", "quality"},
				Examples:    []string{"Review this PR for correctness."},
			},
		},
		SupportsAuthenticatedExtendedCard: true,
		SecuritySchemes: map[string]SecurityScheme{
			"bearer": {
				Type:         "http",
				Scheme:       "bearer",
				BearerFormat: "JWT",
			},
			"apikey": {
				Type: "apiKey",
				In:   "header",
				Name: "X-API-Key",
			},
		},
		PreferredTransport: "JSONRPC",
		AdditionalInterfaces: []InterfaceDesc{
			{URL: "grpc://example.com:443", Transport: "gRPC"},
			{URL: "wss://example.com/ws", Transport: "WebSocket"},
		},
	})
	srv := NewServer(card, NewInMemoryTaskStore(), "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+CanonicalCardPath, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	// Decode into a generic map so we can assert the raw JSON
	// field names — the camelCase JSON tags are part of the
	// contract with external A2A peers.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	for _, key := range []string{
		"protocolVersion",
		"skills",
		"supportsAuthenticatedExtendedCard",
		"securitySchemes",
		"preferredTransport",
		"additionalInterfaces",
	} {
		if _, ok := raw[key]; !ok {
			t.Errorf("v1.0 card missing JSON field %q", key)
		}
	}
	// Spot-check the nested shapes so a rename in the Go struct
	// tags would trip the test.
	var fullCard AgentCard
	if err := json.Unmarshal(mustRemarshal(t, raw), &fullCard); err != nil {
		t.Fatalf("decode typed: %v", err)
	}
	if fullCard.ProtocolVersion != "1.0.0" {
		t.Errorf("protocolVersion=%q want 1.0.0", fullCard.ProtocolVersion)
	}
	if !fullCard.SupportsAuthenticatedExtendedCard {
		t.Error("supportsAuthenticatedExtendedCard lost in round-trip")
	}
	if fullCard.PreferredTransport != "JSONRPC" {
		t.Errorf("preferredTransport=%q want JSONRPC", fullCard.PreferredTransport)
	}
	if len(fullCard.Skills) != 1 || fullCard.Skills[0].ID != "code-review" {
		t.Errorf("skills round-trip lost data: %+v", fullCard.Skills)
	}
	if len(fullCard.AdditionalInterfaces) != 2 {
		t.Errorf("additionalInterfaces count=%d want 2", len(fullCard.AdditionalInterfaces))
	}
	if scheme, ok := fullCard.SecuritySchemes["bearer"]; !ok || scheme.Scheme != "bearer" {
		t.Errorf("securitySchemes[bearer]=%+v (ok=%v)", scheme, ok)
	}
}

// mustRemarshal round-trips a map back to JSON bytes so we can
// decode it into the typed struct for field-level asserts.
func mustRemarshal(t *testing.T, raw map[string]json.RawMessage) []byte {
	t.Helper()
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	return b
}
