package research

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestVerifyClaim_Supported(t *testing.T) {
	claim := Claim{
		ID:        "C-1",
		Text:      "The quick brown fox jumps over the lazy dog",
		SourceURL: "https://example.com/pangram",
	}
	stub := &StubFetcher{Pages: map[string]string{
		"https://example.com/pangram": "The quick brown fox jumps over the lazy dog. " +
			"This sentence contains every letter of the English alphabet.",
	}}
	ok, reason := VerifyClaim(context.Background(), claim, stub)
	if !ok {
		t.Fatalf("want supported, got unsupported (reason: %s)", reason)
	}
	if !strings.Contains(reason, "supported") {
		t.Errorf("reason should describe support: %s", reason)
	}
}

func TestVerifyClaim_Unsupported(t *testing.T) {
	claim := Claim{
		ID:        "C-2",
		Text:      "Neural networks require billions of training examples",
		SourceURL: "https://example.com/unrelated",
	}
	stub := &StubFetcher{Pages: map[string]string{
		"https://example.com/unrelated": "Cooking lasagna requires ricotta, noodles, and tomato sauce. " +
			"Layer them carefully and bake at 375 for forty minutes.",
	}}
	ok, reason := VerifyClaim(context.Background(), claim, stub)
	if ok {
		t.Fatalf("want unsupported, got supported (reason: %s)", reason)
	}
	if !strings.Contains(reason, "unsupported") {
		t.Errorf("reason should say unsupported: %s", reason)
	}
}

func TestVerifyClaim_FetchError(t *testing.T) {
	claim := Claim{
		ID:        "C-3",
		Text:      "Anything meaningful here",
		SourceURL: "https://example.com/missing",
	}
	sentinel := errors.New("HTTP 404 not found")
	stub := &StubFetcher{Err: sentinel}
	ok, reason := VerifyClaim(context.Background(), claim, stub)
	if ok {
		t.Fatalf("want unsupported on fetch error, got supported")
	}
	if !strings.Contains(reason, "fetch error") {
		t.Errorf("reason should mention fetch error, got %q", reason)
	}
	if !strings.Contains(reason, "404") {
		t.Errorf("reason should include upstream error detail, got %q", reason)
	}
}

func TestVerifyClaim_EmptyClaim(t *testing.T) {
	ok, reason := VerifyClaim(context.Background(), Claim{SourceURL: "https://x"}, &StubFetcher{})
	if ok {
		t.Fatal("want unsupported for empty claim")
	}
	if !strings.Contains(reason, "empty") {
		t.Errorf("reason should say empty, got %q", reason)
	}
}

func TestVerifyClaim_NoURL(t *testing.T) {
	ok, _ := VerifyClaim(context.Background(), Claim{Text: "x"}, &StubFetcher{})
	if ok {
		t.Fatal("want unsupported when claim has no URL")
	}
}

func TestVerifyClaim_NilFetcher(t *testing.T) {
	claim := Claim{Text: "x", SourceURL: "https://y/"}
	ok, reason := VerifyClaim(context.Background(), claim, nil)
	if ok {
		t.Fatal("want unsupported with nil fetcher")
	}
	if !strings.Contains(reason, "fetcher") {
		t.Errorf("reason should mention fetcher, got %q", reason)
	}
}

func TestStripHTML(t *testing.T) {
	html := `<html><head><title>T</title><style>x{}</style></head><body><p>Hello <b>world</b></p><script>alert(1)</script></body></html>`
	got := stripHTML(html)
	if !strings.Contains(got, "Hello world") {
		t.Errorf("want Hello world, got %q", got)
	}
	if strings.Contains(got, "alert") {
		t.Errorf("script content must be stripped, got %q", got)
	}
	if strings.Contains(got, "x{}") {
		t.Errorf("style content must be stripped, got %q", got)
	}
}

func TestTokenize_DropsStopwordsAndShort(t *testing.T) {
	got := tokenize("The quick brown fox is a fast animal")
	want := map[string]bool{
		"quick": true, "brown": true, "fox": true, "fast": true, "animal": true,
	}
	if len(got) != len(want) {
		t.Errorf("want %d tokens, got %d (%v)", len(want), len(got), got)
	}
	for _, tok := range got {
		if !want[tok] {
			t.Errorf("unexpected token %q", tok)
		}
	}
}

func TestPhrases(t *testing.T) {
	got := phrases("alpha beta gamma delta", 3)
	if len(got) != 2 {
		t.Fatalf("want 2 3-grams, got %d: %v", len(got), got)
	}
	if got[0] != "alpha beta gamma" || got[1] != "beta gamma delta" {
		t.Errorf("mismatched phrases: %v", got)
	}
}
