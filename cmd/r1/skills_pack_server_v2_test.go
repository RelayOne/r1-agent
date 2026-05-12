package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newV2TestServer(t *testing.T, sourceRoot string) (*httptest.Server, *v2Handler) {
	t.Helper()
	base := newSkillPackRegistryServer(sourceRoot, "")
	rootKeyPath := filepath.Join(t.TempDir(), "root.key")
	h, err := newV2Handler(base, rootKeyPath, 60)
	if err != nil {
		t.Fatalf("newV2Handler: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/packs", base.handleList)
	mux.HandleFunc("/v1/packs/", base.handlePack)
	h.register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, h
}

func TestV2_PacksListSynthesizesV1Only(t *testing.T) {
	sourceRoot := t.TempDir()
	writePackFixture(t, filepath.Join(sourceRoot, ".r1", "skills", "packs", "legacy"), "legacy", nil)

	srv, _ := newV2TestServer(t, sourceRoot)
	resp, err := http.Get(srv.URL + "/v2/packs")
	if err != nil {
		t.Fatalf("GET /v2/packs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	if resp.Header.Get("X-R1-Registry-Sig") == "" {
		t.Fatalf("X-R1-Registry-Sig header missing")
	}
	var listed struct {
		PackCount int             `json:"pack_count"`
		Packs     []PackSummaryV2 `json:"packs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if listed.PackCount != 1 {
		t.Fatalf("PackCount = %d, want 1", listed.PackCount)
	}
	got := listed.Packs[0]
	if got.ManifestSchemaVersion != "2.0.0" {
		t.Fatalf("ManifestSchemaVersion = %q, want 2.0.0 (v1 should be synthesized to v2)", got.ManifestSchemaVersion)
	}
	if len(got.Compat) != 1 || got.Compat[0] != "r1" {
		t.Fatalf("Compat = %v, want [r1]", got.Compat)
	}
}

func TestV2_PacksFilterByCompat(t *testing.T) {
	sourceRoot := t.TempDir()
	writeV2PackFixture(t, filepath.Join(sourceRoot, ".r1", "skills", "packs", "csw"),
		"csw", []string{"r1", "cloudswarm"})
	writeV2PackFixture(t, filepath.Join(sourceRoot, ".r1", "skills", "packs", "heroa"),
		"heroa", []string{"r1", "heroa"})

	srv, _ := newV2TestServer(t, sourceRoot)
	resp, err := http.Get(srv.URL + "/v2/packs?compat=cloudswarm")
	if err != nil {
		t.Fatalf("GET filter: %v", err)
	}
	defer resp.Body.Close()
	var listed struct {
		PackCount int             `json:"pack_count"`
		Packs     []PackSummaryV2 `json:"packs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if listed.PackCount != 1 || listed.Packs[0].Name != "csw" {
		t.Fatalf("filter mismatch: %+v", listed)
	}
}

func TestV2_TrustRootSigned(t *testing.T) {
	sourceRoot := t.TempDir()
	srv, h := newV2TestServer(t, sourceRoot)
	resp, err := http.Get(srv.URL + "/v2/trust-root")
	if err != nil {
		t.Fatalf("GET trust-root: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	var doc struct {
		Version   string `json:"version"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if doc.Signature == "" {
		t.Fatalf("doc signature missing")
	}
	// The response itself must also be signed.
	if resp.Header.Get("X-R1-Registry-Sig") == "" {
		t.Fatalf("X-R1-Registry-Sig missing")
	}
	// Verify the response sig pub key matches the handler's pub key.
	pubB64 := resp.Header.Get("X-R1-Registry-Pub")
	gotPub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		t.Fatalf("decode pub: %v", err)
	}
	if string(gotPub) != string(h.rootPub) {
		t.Fatalf("response pub key mismatch")
	}
}

func TestV2_PackSigEndpoint(t *testing.T) {
	sourceRoot := t.TempDir()
	packDir := filepath.Join(sourceRoot, ".r1", "skills", "packs", "signed")
	writeV2PackFixture(t, packDir, "signed", []string{"r1"})
	keyPath := writePackSigningKey(t)
	if _, err := signSkillPack(sourceRoot, "signed", keyPath, ""); err != nil {
		t.Fatalf("signSkillPack: %v", err)
	}
	srv, _ := newV2TestServer(t, sourceRoot)
	resp, err := http.Get(srv.URL + "/v2/packs/signed/sig")
	if err != nil {
		t.Fatalf("GET sig: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	var sig map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&sig); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if sig["algorithm"] != "ed25519" {
		t.Fatalf("algorithm = %q", sig["algorithm"])
	}
}

func TestV2_RateLimitReturns429(t *testing.T) {
	sourceRoot := t.TempDir()
	base := newSkillPackRegistryServer(sourceRoot, "")
	rootKeyPath := filepath.Join(t.TempDir(), "root.key")
	h, err := newV2Handler(base, rootKeyPath, 2)
	if err != nil {
		t.Fatalf("newV2Handler: %v", err)
	}
	mux := http.NewServeMux()
	h.register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	for i := 0; i < 2; i++ {
		resp, err := http.Get(srv.URL + "/v2/packs")
		if err != nil {
			t.Fatalf("req %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("req %d status = %d", i, resp.StatusCode)
		}
	}
	resp, err := http.Get(srv.URL + "/v2/packs")
	if err != nil {
		t.Fatalf("third req: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatalf("Retry-After header missing")
	}
}

func TestV2_PackDetailIncludesV2Fields(t *testing.T) {
	sourceRoot := t.TempDir()
	packDir := filepath.Join(sourceRoot, ".r1", "skills", "packs", "detail")
	writeV2PackFixture(t, packDir, "detail", []string{"r1", "cloudswarm"})

	srv, _ := newV2TestServer(t, sourceRoot)
	resp, err := http.Get(srv.URL + "/v2/packs/detail")
	if err != nil {
		t.Fatalf("GET detail: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	var detail PackDetailV2
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if detail.ManifestSchemaVersion != "2.0.0" {
		t.Fatalf("schema = %q", detail.ManifestSchemaVersion)
	}
	if !containsStringV2(detail.Compat, "cloudswarm") {
		t.Fatalf("Compat = %v", detail.Compat)
	}
}

// Backwards-compatibility regression: the v1 surface continues to
// answer with the same shape it always has.
func TestV2_V1HandlerUnchanged(t *testing.T) {
	sourceRoot := t.TempDir()
	writePackFixture(t, filepath.Join(sourceRoot, ".r1", "skills", "packs", "legacy"), "legacy", nil)

	srv, _ := newV2TestServer(t, sourceRoot)
	resp, err := http.Get(srv.URL + "/v1/packs")
	if err != nil {
		t.Fatalf("GET v1: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	// v1 must not carry the v2 signature header
	if resp.Header.Get("X-R1-Registry-Sig") != "" {
		t.Fatalf("v1 response should not carry X-R1-Registry-Sig")
	}
	// v1 must NOT introduce v2 fields into its summaries
	if strings.Contains(string(body), "manifest_schema_version") {
		t.Fatalf("v1 response leaks v2 fields: %s", body)
	}
}

func TestV2_SearchByQueryAndCompat(t *testing.T) {
	sourceRoot := t.TempDir()
	writeV2PackFixture(t, filepath.Join(sourceRoot, ".r1", "skills", "packs", "alpha"),
		"alpha", []string{"r1", "cloudswarm"})
	writeV2PackFixture(t, filepath.Join(sourceRoot, ".r1", "skills", "packs", "beta"),
		"beta", []string{"r1", "heroa"})

	srv, _ := newV2TestServer(t, sourceRoot)
	resp, err := http.Get(srv.URL + "/v2/packs/search?q=alpha&compat=cloudswarm")
	if err != nil {
		t.Fatalf("GET search: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		MatchCount int                 `json:"match_count"`
		Matches    []PackSearchEntryV2 `json:"matches"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if out.MatchCount != 1 || out.Matches[0].Name != "alpha" {
		t.Fatalf("search mismatch: %+v", out)
	}
}

