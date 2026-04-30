package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillPackRegistryListPrefersCanonicalAndServesDetails(t *testing.T) {
	sourceRoot := t.TempDir()

	canonicalPack := filepath.Join(sourceRoot, ".r1", "skills", "packs", "ledger-pack")
	legacyPack := filepath.Join(sourceRoot, ".stoke", "skills", "packs", "ledger-pack")
	writePackFixture(t, canonicalPack, "ledger-pack", []string{"base-pack"})
	writePackFixture(t, legacyPack, "ledger-pack", nil)
	if err := os.WriteFile(filepath.Join(canonicalPack, "pack.yaml"), []byte(strings.Join([]string{
		"name: ledger-pack",
		"version: 2.4.0",
		"description: Canonical published copy",
		"skill_count: 1",
		"dependencies:",
		"  - base-pack",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("WriteFile(pack.yaml): %v", err)
	}

	srv := httptest.NewServer(newSkillPackRegistryServer(sourceRoot, "").Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/packs")
	if err != nil {
		t.Fatalf("GET /v1/packs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var listed struct {
		PackCount int                   `json:"pack_count"`
		Packs     []registryPackSummary `json:"packs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatalf("Decode(list): %v", err)
	}
	if listed.PackCount != 1 {
		t.Fatalf("PackCount = %d, want 1", listed.PackCount)
	}
	if len(listed.Packs) != 1 {
		t.Fatalf("len(Packs) = %d, want 1", len(listed.Packs))
	}
	got := listed.Packs[0]
	if got.Version != "2.4.0" {
		t.Fatalf("Version = %q, want 2.4.0", got.Version)
	}
	if got.Description != "Canonical published copy" {
		t.Fatalf("Description = %q, want canonical copy", got.Description)
	}
	if got.DownloadURLPath != "/v1/packs/ledger-pack/archive.tar.gz" {
		t.Fatalf("DownloadURLPath = %q", got.DownloadURLPath)
	}

	detailResp, err := http.Get(srv.URL + "/v1/packs/ledger-pack")
	if err != nil {
		t.Fatalf("GET /v1/packs/ledger-pack: %v", err)
	}
	defer detailResp.Body.Close()
	var detail registryPackDetail
	if err := json.NewDecoder(detailResp.Body).Decode(&detail); err != nil {
		t.Fatalf("Decode(detail): %v", err)
	}
	if detail.SourcePath != canonicalPack {
		t.Fatalf("SourcePath = %q, want %q", detail.SourcePath, canonicalPack)
	}
	if len(detail.ManifestNames) != 1 || detail.ManifestNames[0] != "ledger-pack.skill" {
		t.Fatalf("ManifestNames = %v, want [ledger-pack.skill]", detail.ManifestNames)
	}
}

func TestSkillPackRegistryArchiveReturnsTarGz(t *testing.T) {
	sourceRoot := t.TempDir()
	packDir := filepath.Join(sourceRoot, ".r1", "skills", "packs", "archive-pack")
	writePackFixture(t, packDir, "archive-pack", nil)

	srv := httptest.NewServer(newSkillPackRegistryServer(sourceRoot, "").Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/packs/archive-pack/archive.tar.gz")
	if err != nil {
		t.Fatalf("GET archive: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/gzip" {
		t.Fatalf("Content-Type = %q, want application/gzip", got)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader(): %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tr.Next(): %v", err)
		}
		seen[hdr.Name] = true
	}
	for _, want := range []string{
		"archive-pack/pack.yaml",
		"archive-pack/archive-pack.skill/manifest.json",
	} {
		if !seen[want] {
			t.Fatalf("archive missing %q; seen=%v", want, seen)
		}
	}
}

func TestSkillPackRegistryRequiresBearerWhenConfigured(t *testing.T) {
	sourceRoot := t.TempDir()
	writePackFixture(t, filepath.Join(sourceRoot, ".r1", "skills", "packs", "secure-pack"), "secure-pack", nil)

	srv := httptest.NewServer(newSkillPackRegistryServer(sourceRoot, "secret-token").Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/packs", nil)
	if err != nil {
		t.Fatalf("NewRequest(): %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do(no-auth): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); got == "" {
		t.Fatal("WWW-Authenticate header empty")
	}

	req, err = http.NewRequest(http.MethodGet, srv.URL+"/v1/packs", nil)
	if err != nil {
		t.Fatalf("NewRequest(auth): %v", err)
	}
	req.Header.Set("Authorization", "Bearer secret-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do(auth): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}
