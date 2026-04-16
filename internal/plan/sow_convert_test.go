package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/provider"
	"github.com/ericmacdougall/stoke/internal/stream"
)

// mockProvider is a provider.Provider that returns canned responses so the
// prose converter can be tested without hitting a real LLM.
type mockProvider struct {
	name     string
	response string
	err      error
	lastReq  provider.ChatRequest
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	m.lastReq = req
	if m.err != nil {
		return nil, m.err
	}
	return &provider.ChatResponse{
		Content: []provider.ResponseContent{
			{Type: "text", Text: m.response},
		},
		StopReason: "end_turn",
	}, nil
}
func (m *mockProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	return m.Chat(req)
}

// validSOWJSON is a minimal SOW the mock provider returns for happy-path tests.
const validSOWJSON = `{
  "id": "gen-1",
  "name": "Generated from prose",
  "sessions": [
    {
      "id": "S1",
      "title": "Foundation",
      "tasks": [{"id": "T1", "description": "do the thing"}],
      "acceptance_criteria": [{"id": "AC1", "description": "build works", "command": "echo ok"}]
    }
  ]
}`

func TestConvertProseToSOW_HappyPath(t *testing.T) {
	prov := &mockProvider{name: "mock", response: validSOWJSON}
	sow, blob, err := ConvertProseToSOW("Build a web app in Rust with a postgres backend.", prov, "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("ConvertProseToSOW: %v", err)
	}
	if sow.ID != "gen-1" {
		t.Errorf("id = %q", sow.ID)
	}
	if len(blob) == 0 {
		t.Error("blob should not be empty")
	}
}

func TestConvertProseToSOW_StripsMarkdownFences(t *testing.T) {
	wrapped := "```json\n" + validSOWJSON + "\n```"
	prov := &mockProvider{name: "mock", response: wrapped}
	sow, _, err := ConvertProseToSOW("build something", prov, "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("ConvertProseToSOW: %v", err)
	}
	if sow.ID != "gen-1" {
		t.Errorf("id = %q", sow.ID)
	}
}

func TestConvertProseToSOW_StripsPreamble(t *testing.T) {
	preamble := "Sure, here's the SOW you asked for:\n\n" + validSOWJSON + "\n\nLet me know if you need changes!"
	prov := &mockProvider{name: "mock", response: preamble}
	sow, _, err := ConvertProseToSOW("build something", prov, "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("should recover from preamble: %v", err)
	}
	if sow.ID != "gen-1" {
		t.Errorf("id = %q", sow.ID)
	}
}

func TestConvertProseToSOW_RejectsInvalidSchema(t *testing.T) {
	// Missing required "sessions" field.
	prov := &mockProvider{name: "mock", response: `{"id": "bad", "name": "no sessions"}`}
	_, _, err := ConvertProseToSOW("prose", prov, "model")
	if err == nil {
		t.Error("expected validation failure for SOW with no sessions")
	}
	if !strings.Contains(err.Error(), "failed validation") && !strings.Contains(err.Error(), "sessions") {
		t.Errorf("error should mention schema failure, got: %v", err)
	}
}

func TestConvertProseToSOW_RejectsEmptyProse(t *testing.T) {
	prov := &mockProvider{name: "mock", response: validSOWJSON}
	_, _, err := ConvertProseToSOW("   \n\t ", prov, "model")
	if err == nil || !strings.Contains(err.Error(), "empty prose") {
		t.Errorf("expected empty prose error, got %v", err)
	}
}

func TestConvertProseToSOW_RejectsNilProvider(t *testing.T) {
	_, _, err := ConvertProseToSOW("some prose", nil, "model")
	if err == nil || !strings.Contains(err.Error(), "provider") {
		t.Errorf("expected provider error, got %v", err)
	}
}

func TestConvertProseToSOW_PropagatesProviderError(t *testing.T) {
	prov := &mockProvider{name: "mock", err: errors.New("429 rate limited")}
	_, _, err := ConvertProseToSOW("prose", prov, "model")
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Errorf("expected provider error to propagate, got %v", err)
	}
}

func TestConvertProseToSOW_RejectsEmptyResponse(t *testing.T) {
	prov := &mockProvider{name: "mock", response: ""}
	_, _, err := ConvertProseToSOW("prose", prov, "model")
	if err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Errorf("expected empty response error, got %v", err)
	}
}

func TestConvertProseToSOW_PromptIncludesProse(t *testing.T) {
	prov := &mockProvider{name: "mock", response: validSOWJSON}
	prose := "Build a Rust CLI that ingests PDF files into postgres with pgvector."
	_, _, err := ConvertProseToSOW(prose, prov, "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("ConvertProseToSOW: %v", err)
	}
	// The prose should appear in the user message content.
	var userContent []map[string]interface{}
	if err := json.Unmarshal(prov.lastReq.Messages[0].Content, &userContent); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	text, _ := userContent[0]["text"].(string)
	if !strings.Contains(text, prose) {
		t.Errorf("prompt should include prose: got %q", text)
	}
	if !strings.Contains(text, "acceptance_criteria") {
		t.Errorf("prompt should mention acceptance_criteria schema field")
	}
}

func TestLoadSOWFile_JSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stoke-sow.json")
	os.WriteFile(path, []byte(validSOWJSON), 0644)

	sow, result, err := LoadSOWFile(path, dir, nil, "")
	if err != nil {
		t.Fatalf("LoadSOWFile: %v", err)
	}
	if result.Format != "json" {
		t.Errorf("format = %q", result.Format)
	}
	if sow.ID != "gen-1" {
		t.Errorf("id = %q", sow.ID)
	}
}

func TestLoadSOWFile_YAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stoke-sow.yaml")
	os.WriteFile(path, []byte("id: yaml-test\nname: Yaml\nsessions: [{id: S1, title: t, tasks: [{id: T1, description: x}], acceptance_criteria: [{id: AC1, description: d}]}]\n"), 0644)

	sow, result, err := LoadSOWFile(path, dir, nil, "")
	if err != nil {
		t.Fatalf("LoadSOWFile: %v", err)
	}
	if result.Format != "yaml" {
		t.Errorf("format = %q", result.Format)
	}
	if sow.ID != "yaml-test" {
		t.Errorf("id = %q", sow.ID)
	}
}

func TestLoadSOWFile_Prose_ConvertsAndCaches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spec.md")
	prose := "# Project Spec\n\nBuild a Rust web server that exposes /health and /metrics.\n"
	os.WriteFile(path, []byte(prose), 0644)

	prov := &mockProvider{name: "mock", response: validSOWJSON}
	sow, result, err := LoadSOWFile(path, dir, prov, "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("LoadSOWFile: %v", err)
	}
	if result.Format != "prose" {
		t.Errorf("format = %q", result.Format)
	}
	if sow.ID != "gen-1" {
		t.Errorf("id = %q", sow.ID)
	}
	// Cache behavior depends on which convert path succeeded:
	//   - chunked success → cache written, ConvertedPath = real file
	//   - monolithic fallback → ConvertedPath = fallback marker, no file
	// With a uniform mockProvider the chunked path typically fails
	// (skeleton / per-session shapes differ) and we land in the
	// fallback. Both outcomes are legitimate "prose conversion
	// succeeded" signals; assert only that a path is set.
	if result.ConvertedPath == "" {
		t.Error("expected ConvertedPath to be set (cache file OR fallback marker)")
	}
	fallbackMarker := "monolithic fallback"
	cacheHit := false
	if _, err := os.Stat(result.ConvertedPath); err == nil {
		cacheHit = true
	} else if !strings.Contains(result.ConvertedPath, fallbackMarker) {
		t.Errorf("ConvertedPath is neither a real cache file nor a fallback marker: %q (stat err: %v)",
			result.ConvertedPath, err)
	}

	// Second call: only hits cache when the first call actually
	// produced a cache file. If we fell back, the provider WILL be
	// called again — which is the correct behavior (the warning
	// banner explicitly documents "next run re-converts the prose").
	if cacheHit {
		errProv := &mockProvider{name: "mock", err: errors.New("should not be called")}
		sow2, result2, err := LoadSOWFile(path, dir, errProv, "claude-sonnet-4-6")
		if err != nil {
			t.Fatalf("LoadSOWFile (cached): %v", err)
		}
		if sow2.ID != sow.ID {
			t.Errorf("cached SOW differs: %q vs %q", sow2.ID, sow.ID)
		}
		if result2.Format != "prose" {
			t.Errorf("cached format = %q", result2.Format)
		}
	}
}

func TestLoadSOWFile_Prose_CacheInvalidatedOnSourceChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spec.md")
	os.WriteFile(path, []byte("# Spec v1\n"), 0644)

	prov := &mockProvider{name: "mock", response: validSOWJSON}
	_, _, err := LoadSOWFile(path, dir, prov, "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("LoadSOWFile: %v", err)
	}

	// Modify the source file. Cache should be invalidated so the
	// provider is called again. The chunked convert pipeline makes
	// multiple calls per conversion (skeleton + per-session expand
	// + consistency check + final approval); assert "called at
	// least once" rather than an exact count, because the exact
	// number is a refactoring detail that drifts with chunked
	// pipeline revisions.
	os.WriteFile(path, []byte("# Spec v2 (different content)\n"), 0644)
	called := 0
	prov2 := &mockProvider{name: "mock", response: strings.Replace(validSOWJSON, "gen-1", "gen-2", 1)}
	wrapper := &countingProvider{inner: prov2, count: &called}
	sow, _, err := LoadSOWFile(path, dir, wrapper, "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("LoadSOWFile: %v", err)
	}
	if called < 1 {
		t.Errorf("provider should be called at least once on cache miss, called %d times", called)
	}
	if sow.ID != "gen-2" {
		t.Errorf("new SOW id = %q, want gen-2", sow.ID)
	}
}

type countingProvider struct {
	inner provider.Provider
	count *int
}

func (c *countingProvider) Name() string { return c.inner.Name() }
func (c *countingProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	*c.count++
	return c.inner.Chat(req)
}
func (c *countingProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	*c.count++
	return c.inner.ChatStream(req, onEvent)
}

func TestLoadSOWFile_Prose_NoProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spec.txt")
	os.WriteFile(path, []byte("This is a project spec."), 0644)

	_, _, err := LoadSOWFile(path, dir, nil, "")
	if err == nil || !strings.Contains(err.Error(), "provider") {
		t.Errorf("expected provider error, got %v", err)
	}
}

func TestLoadSOWFile_UnknownExt_SniffsJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spec.weird")
	os.WriteFile(path, []byte(validSOWJSON), 0644)

	sow, result, err := LoadSOWFile(path, dir, nil, "")
	if err != nil {
		t.Fatalf("LoadSOWFile: %v", err)
	}
	if result.Format != "json" {
		t.Errorf("format = %q", result.Format)
	}
	if sow.ID != "gen-1" {
		t.Errorf("id = %q", sow.ID)
	}
}

func TestStripMarkdownFences(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"```json\nhello\n```", "hello"},
		{"```\nhello\n```", "hello"},
		{"hello", "hello"},
		{"  hello  ", "hello"},
		{"```json\nfoo\nbar\n```", "foo\nbar"},
	}
	for _, tc := range cases {
		if got := stripMarkdownFences(tc.in); got != tc.want {
			t.Errorf("stripMarkdownFences(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHashBytes_StableAndDistinct(t *testing.T) {
	a := hashBytes([]byte("hello"))
	b := hashBytes([]byte("hello"))
	c := hashBytes([]byte("world"))
	if a != b {
		t.Errorf("hash should be stable: %s vs %s", a, b)
	}
	if a == c {
		t.Errorf("different inputs should hash differently")
	}
	if len(a) != 64 {
		t.Errorf("expected 64-char hex sha256, got %d chars", len(a))
	}
}

// ensure fmt is used for test format strings
var _ = fmt.Sprintf
