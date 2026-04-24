package provider

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// withEnv sets STOKE_PROVIDERS for the lifetime of the test and
// restores the previous value (or unset state) on cleanup.
func withEnv(t *testing.T, value string, unset bool) {
	t.Helper()
	prev, had := lookupEnv()
	t.Cleanup(func() {
		if had {
			setEnv(prev)
		} else {
			clearEnv()
		}
	})
	if unset {
		clearEnv()
	} else {
		setEnv(value)
	}
}

func TestNewPoolFromEnv_Empty(t *testing.T) {
	withEnv(t, "", true)
	pool, err := NewPoolFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pool != nil {
		t.Fatalf("expected nil pool for unset env, got %+v", pool)
	}
}

func TestNewPoolFromEnv_Whitespace(t *testing.T) {
	withEnv(t, "   \n\t  ", false)
	pool, err := NewPoolFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pool != nil {
		t.Fatalf("expected nil pool for blank env, got %+v", pool)
	}
}

func TestNewPoolFromEnv_ValidTwoEntries(t *testing.T) {
	srv := httptest.NewServer(nil)
	defer srv.Close()

	json := `[
		{"name":"anthropic","url":"https://api.anthropic.com","key":"sk-ant-xxx","models":["claude-sonnet-4-6","claude-opus-4-6"],"role":"reasoning"},
		{"name":"ollama","url":"` + srv.URL + `","key":"","models":["llama3:70b"],"role":"worker"}
	]`
	withEnv(t, json, false)
	pool, err := NewPoolFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pool == nil {
		t.Fatalf("expected populated pool, got nil")
	}
	entries := pool.Entries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Name != "anthropic" || entries[1].Name != "ollama" {
		t.Fatalf("entries out of order: %+v", entries)
	}
}

func TestNewPoolFromEnv_InvalidJSON(t *testing.T) {
	withEnv(t, "not-json", false)
	_, err := NewPoolFromEnv()
	if err == nil {
		t.Fatalf("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "STOKE_PROVIDERS parse") {
		t.Fatalf("error should mention STOKE_PROVIDERS parse: %v", err)
	}
}

func TestValidatePool_EmptyName(t *testing.T) {
	entries := []PoolEntry{
		{Name: "", URL: "https://api.anthropic.com", Models: []string{"claude-sonnet-4-6"}, Role: RoleWorker},
	}
	if err := validatePool(entries); err == nil {
		t.Fatalf("expected validation error for empty name")
	}
}

func TestValidatePool_EmptyURL(t *testing.T) {
	entries := []PoolEntry{
		{Name: "a", URL: "", Models: []string{"m"}, Role: RoleWorker},
	}
	err := validatePool(entries)
	if err == nil || !strings.Contains(err.Error(), "empty url") {
		t.Fatalf("expected empty url error, got %v", err)
	}
}

func TestValidatePool_InvalidRole(t *testing.T) {
	entries := []PoolEntry{
		{Name: "a", URL: "http://x", Models: []string{"m"}, Role: "bogus"},
	}
	err := validatePool(entries)
	if err == nil || !strings.Contains(err.Error(), "invalid role") {
		t.Fatalf("expected invalid role error, got %v", err)
	}
}

func TestValidatePool_EmptyModels(t *testing.T) {
	entries := []PoolEntry{
		{Name: "a", URL: "http://x", Models: nil, Role: RoleWorker},
	}
	err := validatePool(entries)
	if err == nil || !strings.Contains(err.Error(), "empty models") {
		t.Fatalf("expected empty models error, got %v", err)
	}
}

func TestValidatePool_DuplicateNames(t *testing.T) {
	entries := []PoolEntry{
		{Name: "a", URL: "http://x", Models: []string{"m"}, Role: RoleWorker},
		{Name: "a", URL: "http://y", Models: []string{"m2"}, Role: RoleReviewer},
	}
	err := validatePool(entries)
	if err == nil || !strings.Contains(err.Error(), "duplicate name") {
		t.Fatalf("expected duplicate name error, got %v", err)
	}
}

func TestValidatePool_EmptyArray(t *testing.T) {
	err := validatePool(nil)
	if err == nil {
		t.Fatalf("expected error for empty entries")
	}
}

func TestValidatePool_BlankModelID(t *testing.T) {
	entries := []PoolEntry{
		{Name: "a", URL: "http://x", Models: []string{"good", "  "}, Role: RoleWorker},
	}
	err := validatePool(entries)
	if err == nil || !strings.Contains(err.Error(), "blank") {
		t.Fatalf("expected blank model error, got %v", err)
	}
}

func TestBuildProvider_ExactRoleAndModel(t *testing.T) {
	srv := httptest.NewServer(nil)
	defer srv.Close()
	pool := mustPool(t, `[
		{"name":"anthropic","url":"https://api.anthropic.com","key":"k1","models":["claude-sonnet-4-6"],"role":"reasoning"},
		{"name":"ollama","url":"`+srv.URL+`","key":"","models":["llama3:70b"],"role":"worker"}
	]`)

	// Worker → ollama entry. srv.URL is 127.0.0.1:PORT which doesn't
	// match any OpenAI-compat heuristic token, so this path returns
	// an Anthropic provider. We're asserting lookup succeeds, not
	// concrete type — the dedicated Ollama test below covers type
	// selection via the 11434 port trigger.
	provOllama, err := pool.BuildProvider(RoleWorker, "llama3:70b")
	if err != nil {
		t.Fatalf("worker lookup: %v", err)
	}
	if provOllama == nil {
		t.Fatalf("nil provider for worker role")
	}

	provReasoning, err := pool.BuildProvider(RoleReasoning, "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("reasoning lookup: %v", err)
	}
	if provReasoning == nil {
		t.Fatalf("nil provider for reasoning role")
	}
	if provReasoning.Name() != "anthropic" {
		// NewAnthropicProvider returns a provider whose Name() is "anthropic".
		t.Fatalf("expected anthropic provider name, got %q", provReasoning.Name())
	}
}

func TestBuildProvider_FallbackToAny(t *testing.T) {
	pool := mustPool(t, `[
		{"name":"universal","url":"https://api.anthropic.com","key":"k","models":["model-a","model-b"],"role":"any"}
	]`)

	// No worker-specific entry, but role=any covers the model → should resolve.
	prov, err := pool.BuildProvider(RoleWorker, "model-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prov == nil {
		t.Fatalf("nil provider from any fallback")
	}
}

func TestBuildProvider_NoMatch(t *testing.T) {
	pool := mustPool(t, `[
		{"name":"anthropic","url":"https://api.anthropic.com","key":"k","models":["claude-sonnet-4-6"],"role":"reasoning"}
	]`)

	// Asking for a model not in the pool → error mentioning role + model.
	_, err := pool.BuildProvider(RoleWorker, "gpt-4o")
	if err == nil {
		t.Fatalf("expected error for unmatched combo")
	}
	if !strings.Contains(err.Error(), "role=worker") || !strings.Contains(err.Error(), "model=gpt-4o") {
		t.Fatalf("error should identify role+model: %v", err)
	}
}

func TestBuildProvider_CachesSecondCall(t *testing.T) {
	pool := mustPool(t, `[
		{"name":"anthropic","url":"https://api.anthropic.com","key":"k","models":["claude-sonnet-4-6"],"role":"reasoning"}
	]`)

	p1, err := pool.BuildProvider(RoleReasoning, "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	p2, err := pool.BuildProvider(RoleReasoning, "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	// Cache returns the same pointer. Compare via interface equality
	// — both concrete types are *AnthropicProvider and Go treats
	// interface equality as (type, pointer) so this is the cleanest
	// way to assert cache identity without reflecting on private fields.
	if p1 != p2 {
		t.Fatalf("expected cached provider on second call, got different instance")
	}
}

func TestBuildProviderByRole_PicksFirstMatch(t *testing.T) {
	pool := mustPool(t, `[
		{"name":"a","url":"https://api.anthropic.com","key":"k","models":["m-1","m-2"],"role":"worker"},
		{"name":"b","url":"https://api.anthropic.com","key":"k","models":["m-3"],"role":"worker"}
	]`)

	prov, model, err := pool.BuildProviderByRole(RoleWorker)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model != "m-1" {
		t.Fatalf("expected first entry's first model, got %q", model)
	}
	if prov == nil {
		t.Fatalf("nil provider")
	}
}

func TestBuildProviderByRole_FallsBackToAny(t *testing.T) {
	pool := mustPool(t, `[
		{"name":"generic","url":"https://api.anthropic.com","key":"k","models":["generic-model"],"role":"any"}
	]`)
	prov, model, err := pool.BuildProviderByRole(RoleReviewer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model != "generic-model" {
		t.Fatalf("expected generic-model, got %q", model)
	}
	if prov == nil {
		t.Fatalf("nil provider")
	}
}

func TestBuildProviderByRole_NoMatch(t *testing.T) {
	pool := mustPool(t, `[
		{"name":"w","url":"https://api.anthropic.com","key":"k","models":["m"],"role":"worker"}
	]`)
	_, _, err := pool.BuildProviderByRole(RoleReviewer)
	if err == nil {
		t.Fatalf("expected error when no reviewer/any entry exists")
	}
}

func TestBuildProvider_OllamaURLPicksOpenAICompat(t *testing.T) {
	pool := mustPool(t, `[
		{"name":"ollama","url":"http://localhost:11434","key":"","models":["llama3:70b"],"role":"worker"}
	]`)
	prov, err := pool.BuildProvider(RoleWorker, "llama3:70b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The Ollama default port triggers the OpenAI-compat branch. The
	// provider constructor names it after the PoolEntry.Name, so we
	// assert on the concrete type rather than Name().
	if _, ok := prov.(*OpenAICompatProvider); !ok {
		t.Fatalf("expected OpenAICompatProvider for Ollama URL, got %T", prov)
	}
}

func TestBuildProvider_AnthropicURLPicksAnthropic(t *testing.T) {
	pool := mustPool(t, `[
		{"name":"anthropic","url":"https://api.anthropic.com","key":"k","models":["claude-sonnet-4-6"],"role":"reasoning"}
	]`)
	prov, err := pool.BuildProvider(RoleReasoning, "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := prov.(*AnthropicProvider); !ok {
		t.Fatalf("expected AnthropicProvider, got %T", prov)
	}
}

// --- test helpers ---

func mustPool(t *testing.T, json string) *Pool {
	t.Helper()
	pool, err := NewPoolFromJSON(json)
	if err != nil {
		t.Fatalf("mustPool: %v", err)
	}
	if pool == nil {
		t.Fatalf("mustPool: got nil")
	}
	return pool
}

// envKey is the single name we read in tests. Centralizing prevents
// typos from drifting the tests out of sync with the code path.
const envKey = "R1_PROVIDERS"

func lookupEnv() (string, bool) {
	return os.LookupEnv(envKey)
}

func setEnv(v string) { os.Setenv(envKey, v) }
func clearEnv()       { os.Unsetenv(envKey) }
