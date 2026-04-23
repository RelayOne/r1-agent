package research

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestStubFetcherFound(t *testing.T) {
	s := &StubFetcher{Pages: map[string]string{
		"https://example.com/": "hello world",
	}}
	body, err := s.Fetch(context.Background(), "https://example.com/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "hello world" {
		t.Errorf("want body %q, got %q", "hello world", body)
	}
	if len(body) == 0 {
		t.Error("body must not be empty")
	}
}

func TestStubFetcher_Miss_DefaultError(t *testing.T) {
	s := &StubFetcher{Pages: map[string]string{}}
	body, err := s.Fetch(context.Background(), "https://absent.example/")
	if err == nil {
		t.Fatal("want error for missing URL, got nil")
	}
	if body != "" {
		t.Errorf("want empty body on miss, got %q", body)
	}
	if !strings.Contains(err.Error(), "no page") {
		t.Errorf("want default miss error mentioning 'no page', got %v", err)
	}
}

func TestStubFetcher_Miss_CustomError(t *testing.T) {
	sentinel := errors.New("network down")
	s := &StubFetcher{Err: sentinel}
	_, err := s.Fetch(context.Background(), "https://absent.example/")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("want sentinel error, got %v", err)
	}
}

func TestStubFetcher_NilReceiver(t *testing.T) {
	var s *StubFetcher
	_, err := s.Fetch(context.Background(), "https://x/")
	if err == nil {
		t.Fatal("want error from nil receiver, got nil")
	}
}

func TestStubFetcher_ContextCanceled(t *testing.T) {
	s := &StubFetcher{Pages: map[string]string{"https://a/": "body"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Fetch(ctx, "https://a/")
	if err == nil {
		t.Fatal("want context-canceled error, got nil")
	}
}

func TestHTTPFetcher_Allowlist(t *testing.T) {
	f := &HTTPFetcher{Allowlist: []string{"example.com"}}
	_, err := f.Fetch(context.Background(), "https://evil.test/foo")
	if err == nil {
		t.Fatal("want allowlist rejection, got nil")
	}
	if !strings.Contains(err.Error(), "allowlist") {
		t.Errorf("want allowlist error, got %v", err)
	}
}

func TestHTTPFetcher_RejectsPrivate(t *testing.T) {
	f := &HTTPFetcher{}
	cases := []string{
		"http://127.0.0.1/",
		"http://10.0.0.1/",
		"http://localhost/",
		"http://192.168.1.1/",
	}
	for _, host := range cases {
		_, err := f.Fetch(context.Background(), host)
		if err == nil {
			t.Errorf("want rejection for %s, got nil", host)
		}
	}
}

func TestHTTPFetcher_RejectsNonHTTP(t *testing.T) {
	f := &HTTPFetcher{}
	_, err := f.Fetch(context.Background(), "file:///etc/passwd")
	if err == nil {
		t.Fatal("want rejection of file:// scheme, got nil")
	}
	if !strings.Contains(err.Error(), "scheme") {
		t.Errorf("want scheme error, got %v", err)
	}
}

func TestHTTPFetcher_RequireTLS(t *testing.T) {
	f := &HTTPFetcher{RequireTLS: true}
	_, err := f.Fetch(context.Background(), "http://example.com/")
	if err == nil {
		t.Fatal("want rejection of http:// when RequireTLS=true, got nil")
	}
}

func TestHostOnAllowlist(t *testing.T) {
	allow := []string{"example.com", "docs.golang.org"}
	if !hostOnAllowlist("example.com", allow) {
		t.Error("exact match should pass")
	}
	if !hostOnAllowlist("blog.example.com", allow) {
		t.Error("subdomain should pass")
	}
	if hostOnAllowlist("evilexample.com", allow) {
		t.Error("host without dot boundary must not pass (evilexample.com vs example.com)")
	}
	if hostOnAllowlist("other.tld", allow) {
		t.Error("unrelated host must not pass")
	}
}
