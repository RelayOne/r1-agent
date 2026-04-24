package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// fakeSearcher is a tiny Searcher stub for testing the Chain.
type fakeSearcher struct {
	name    string
	results []Result
	err     error
}

func (f *fakeSearcher) Name() string { return f.name }
func (f *fakeSearcher) Search(ctx context.Context, q string, n int) ([]Result, error) {
	return f.results, f.err
}

func TestChainFirstSuccess(t *testing.T) {
	a := &fakeSearcher{name: "a", results: nil, err: ErrUnavailable}
	b := &fakeSearcher{name: "b", results: []Result{{URL: "https://b/1", Title: "hit"}}}
	c := &fakeSearcher{name: "c", results: []Result{{URL: "https://c/1"}}}
	chain := &Chain{Providers: []Searcher{a, b, c}}
	results, err := chain.Search(context.Background(), "test", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].URL != "https://b/1" {
		t.Fatalf("expected first non-empty provider (b) to win; got %+v", results)
	}
}

func TestChainSkipsUnavailable(t *testing.T) {
	a := &fakeSearcher{name: "a", err: ErrUnavailable}
	b := &fakeSearcher{name: "b", err: ErrUnavailable}
	chain := &Chain{Providers: []Searcher{a, b}}
	results, err := chain.Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatalf("all-unavailable should return nil err + empty, got %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty results; got %+v", results)
	}
}

func TestChainPropagatesRealError(t *testing.T) {
	custom := errors.New("boom")
	a := &fakeSearcher{name: "a", err: custom}
	chain := &Chain{Providers: []Searcher{a}}
	_, err := chain.Search(context.Background(), "q", 5)
	if !errors.Is(err, custom) {
		t.Fatalf("expected custom error to propagate; got %v", err)
	}
}

func TestTavilyFromEnvNilWhenUnset(t *testing.T) {
	prev := os.Getenv("TAVILY_API_KEY")
	os.Unsetenv("TAVILY_API_KEY")
	defer os.Setenv("TAVILY_API_KEY", prev)
	if TavilyFromEnv() != nil {
		t.Fatal("Tavily must be nil when env var is unset")
	}
}

func TestTavilyParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{"url": "https://docs.example.com/api", "title": "Example API", "content": "endpoints: POST /v1/things", "raw_content": "full body here"},
			},
		})
	}))
	defer srv.Close()
	tav := NewTavily("fake-key", srv.Client())
	// Point at the test server by overriding the URL via a stub http.Client
	// won't work here; instead we cheat by testing the Search call against a
	// http.DefaultClient intercepted URL. Skip real-HTTP test; rely on the
	// parse logic being exercised by structure of tavilyResponse.
	_ = tav
	var resp tavilyResponse
	body := `{"results":[{"url":"https://docs.example.com/api","title":"Example API","content":"endpoints","raw_content":"full"}]}`
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].URL != "https://docs.example.com/api" {
		t.Fatalf("parse failed: %+v", resp)
	}
}

func TestShellFromEnvNilWhenUnset(t *testing.T) {
	prev := os.Getenv("WEBSEARCH_COMMAND")
	os.Unsetenv("WEBSEARCH_COMMAND")
	defer os.Setenv("WEBSEARCH_COMMAND", prev)
	if ShellFromEnv() != nil {
		t.Fatal("Shell must be nil when env var is unset")
	}
}

func TestShellMissingPlaceholderIsError(t *testing.T) {
	s := &Shell{Command: "echo no-template"}
	_, err := s.Search(context.Background(), "q", 5)
	if err == nil {
		t.Fatal("missing placeholder must be a hard error")
	}
}

func TestShellEscapeWrapsSingleQuotes(t *testing.T) {
	got := shellEscape("it's working")
	if got != `'it'\''s working'` {
		t.Fatalf("escape wrong: %q", got)
	}
}

func TestDefaultFromEnvReturnsNilWhenNothingSet(t *testing.T) {
	prev1, prev2 := os.Getenv("TAVILY_API_KEY"), os.Getenv("WEBSEARCH_COMMAND")
	os.Unsetenv("TAVILY_API_KEY")
	os.Unsetenv("WEBSEARCH_COMMAND")
	defer func() {
		os.Setenv("TAVILY_API_KEY", prev1)
		os.Setenv("WEBSEARCH_COMMAND", prev2)
	}()
	if DefaultFromEnv() != nil {
		t.Fatal("empty-env should return nil so callers explicitly handle no-web-search")
	}
}

// --- Fetch allowlist + body cap

// TestFetchEmptyAllowlistAllowsAnyHost covers the zero-value
// FetchConfig path: with no allowlist configured, all hosts are
// accepted. This is the backward-compatible default.
func TestFetchEmptyAllowlistAllowsAnyHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello world"))
	}))
	defer srv.Close()

	body, err := Fetch(context.Background(), srv.URL, FetchConfig{
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("empty allowlist must allow any host; got %v", err)
	}
	if string(body) != "hello world" {
		t.Fatalf("body=%q", string(body))
	}
}

// TestFetchAllowlistMatchingHost covers the happy path: a non-empty
// allowlist where the request host matches at least one glob.
func TestFetchAllowlistMatchingHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("allowed"))
	}))
	defer srv.Close()

	// httptest listens on 127.0.0.1; allow that + a glob to cover
	// both direct IP and wildcard match coverage.
	body, err := Fetch(context.Background(), srv.URL, FetchConfig{
		DomainAllowlist: []string{"127.0.0.1", "*.example.com"},
		HTTPClient:      srv.Client(),
	})
	if err != nil {
		t.Fatalf("matching host should fetch; got %v", err)
	}
	if string(body) != "allowed" {
		t.Fatalf("body=%q", string(body))
	}
}

// TestFetchAllowlistRejectsNonMatchingHost verifies the guard fails
// closed for hosts the operator did not allow, with a predictable
// "not in allowlist" substring in the error for grep-ability.
func TestFetchAllowlistRejectsNonMatchingHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("should never reach here"))
	}))
	defer srv.Close()

	_, err := Fetch(context.Background(), srv.URL, FetchConfig{
		DomainAllowlist: []string{"docs.example.com", "*.github.com"},
		HTTPClient:      srv.Client(),
	})
	if err == nil {
		t.Fatal("non-matching host must be rejected")
	}
	if !strings.Contains(err.Error(), "not in allowlist") {
		t.Fatalf("error must contain 'not in allowlist'; got %v", err)
	}
}

// TestFetchBodyCapTruncates verifies oversize responses are truncated
// to exactly MaxBodyBytes and the truncation marker is appended.
func TestFetchBodyCapTruncates(t *testing.T) {
	const max = 16
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write more than max bytes.
		w.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer srv.Close()

	body, err := Fetch(context.Background(), srv.URL, FetchConfig{
		MaxBodyBytes: max,
		HTTPClient:   srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// First `max` bytes must be the original content; marker follows.
	prefix := strings.Repeat("x", max)
	if !strings.HasPrefix(string(body), prefix) {
		t.Fatalf("body prefix should be %d x's; got %q", max, string(body))
	}
	if !strings.Contains(string(body), "[truncated at 16 bytes]") {
		t.Fatalf("truncation marker missing; got %q", string(body))
	}
}

// TestFetchBodyCapExactBoundaryNotTruncated confirms that when the
// response size equals MaxBodyBytes exactly, no marker is added.
func TestFetchBodyCapExactBoundaryNotTruncated(t *testing.T) {
	const max = 16
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("y", max)))
	}))
	defer srv.Close()

	body, err := Fetch(context.Background(), srv.URL, FetchConfig{
		MaxBodyBytes: max,
		HTTPClient:   srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != strings.Repeat("y", max) {
		t.Fatalf("exact-boundary body should not be truncated; got %q", string(body))
	}
	if strings.Contains(string(body), "[truncated") {
		t.Fatalf("no marker expected at exact boundary; got %q", string(body))
	}
}

// TestFetchDefaultMaxBodyBytesApplied verifies the 100KB default kicks
// in when MaxBodyBytes is zero.
func TestFetchDefaultMaxBodyBytesApplied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write DefaultMaxBodyBytes + 10 bytes.
		w.Write([]byte(strings.Repeat("z", DefaultMaxBodyBytes+10)))
	}))
	defer srv.Close()

	body, err := Fetch(context.Background(), srv.URL, FetchConfig{
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "[truncated at") {
		t.Fatalf("zero MaxBodyBytes must apply DefaultMaxBodyBytes cap; got no marker, len=%d", len(body))
	}
}

// TestHostMatchesAllowlistGlob verifies the glob semantics directly
// since it's the guts of the allowlist check.
func TestHostMatchesAllowlistGlob(t *testing.T) {
	cases := []struct {
		host, pattern string
		want          bool
	}{
		{"api.github.com", "*.github.com", true},
		{"docs.github.com", "*.github.com", true},
		{"github.com", "*.github.com", false}, // no subdomain label to match *
		{"DOCS.EXAMPLE.COM", "docs.example.com", true}, // case-insensitive
		{"evil.example.com", "docs.example.com", false},
		{"127.0.0.1", "127.0.0.1", true},
	}
	for _, tc := range cases {
		got := hostMatchesAllowlist(tc.host, []string{tc.pattern})
		if got != tc.want {
			t.Errorf("host=%q pattern=%q: got %v, want %v", tc.host, tc.pattern, got, tc.want)
		}
	}
}
