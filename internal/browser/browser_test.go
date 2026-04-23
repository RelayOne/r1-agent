package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetch_OKWithBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<!doctype html><html><head><title>Hello Stoke</title></head><body><h1>body</h1><p>ok</p></body></html>`))
	}))
	defer srv.Close()
	c := NewClient()
	r, err := c.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if r.Status != 200 {
		t.Errorf("status=%d", r.Status)
	}
	if r.Title != "Hello Stoke" {
		t.Errorf("title=%q", r.Title)
	}
	if !strings.Contains(r.Text, "body") || !strings.Contains(r.Text, "ok") {
		t.Errorf("text missing markers: %q", r.Text)
	}
	if strings.Contains(r.Text, "<") {
		t.Errorf("html tags leaked: %q", r.Text)
	}
}

func TestFetch_NonExistentHost(t *testing.T) {
	c := NewClient()
	_, err := c.Fetch(context.Background(), "http://127.0.0.1:1/nothing")
	if err == nil {
		t.Fatal("expected error on unreachable host")
	}
}

func TestFetch_EmptyURL(t *testing.T) {
	c := NewClient()
	_, err := c.Fetch(context.Background(), "")
	if err == nil {
		t.Fatal("expected error on empty url")
	}
}

func TestFetch_Returns404Cleanly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte("not found"))
	}))
	defer srv.Close()
	c := NewClient()
	r, err := c.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("non-2xx should not be an error: %v", err)
	}
	if r.Status != 404 {
		t.Errorf("status=%d", r.Status)
	}
}

func TestVerifyContains(t *testing.T) {
	r := FetchResult{Text: "The answer is forty-two."}
	if ok, _ := VerifyContains(r, "Forty-Two"); !ok {
		t.Error("case-insensitive match should succeed")
	}
	if ok, _ := VerifyContains(r, "FORTY-THREE"); ok {
		t.Error("mismatch should fail")
	}
	if ok, _ := VerifyContains(r, ""); !ok {
		t.Error("empty expected should pass through")
	}
}

func TestVerifyRegex(t *testing.T) {
	r := FetchResult{Text: "price: $42.00 today"}
	if ok, _ := VerifyRegex(r, `\$\d+\.\d{2}`); !ok {
		t.Error("regex should match")
	}
	if ok, _ := VerifyRegex(r, `nonsense`); ok {
		t.Error("unmatched regex should fail")
	}
	if ok, reason := VerifyRegex(r, `[`); ok || !strings.Contains(reason, "regex compile") {
		t.Errorf("invalid regex should surface compile error; got ok=%v reason=%q", ok, reason)
	}
	if ok, _ := VerifyRegex(r, ""); !ok {
		t.Error("empty pattern should pass through")
	}
}

func TestExtractText_StripsScriptsAndStyles(t *testing.T) {
	html := `<html><head><style>body{color:red}</style><script>alert('x')</script></head><body><p>Hello</p></body></html>`
	got := ExtractText(html)
	if strings.Contains(got, "alert") {
		t.Errorf("script leaked: %q", got)
	}
	if strings.Contains(got, "color:red") {
		t.Errorf("style leaked: %q", got)
	}
	if !strings.Contains(got, "Hello") {
		t.Errorf("body text missing: %q", got)
	}
}

func TestExtractTitle_OGFallback(t *testing.T) {
	html := `<html><head><meta property="og:title" content="Open Graph Title"></head></html>`
	if ExtractTitle(html) != "Open Graph Title" {
		t.Errorf("og fallback failed: %q", ExtractTitle(html))
	}
}

func TestExtractTitle_NoTitle(t *testing.T) {
	if ExtractTitle("<html><body>no title</body></html>") != "" {
		t.Error("empty title expected")
	}
}

func TestMaxBody_Truncates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		big := strings.Repeat("AB", 1000)
		w.Write([]byte(big))
	}))
	defer srv.Close()
	c := NewClient()
	c.MaxBody = 500 // tiny cap
	r, err := c.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(r.Text, "truncated by browser.Client") {
		t.Errorf("missing truncation marker: %q", r.Text)
	}
}
