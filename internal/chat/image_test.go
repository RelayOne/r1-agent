package chat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePNG writes a 1x1 red PNG so tests exercise the full
// load/encode pipeline against a real image file.
func writePNG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
}

func TestExtractImageRefs_MarkdownAndAt(t *testing.T) {
	// Markdown ![](path) and @/path are the two EXPLICIT forms.
	// Bare paths in prose are NOT auto-attached — codex flagged
	// that "rename /tmp/foo.png in the repo" silently uploading is
	// a regression. Authoritative behavior: explicit signal required.
	paths, residual := ExtractImageRefs("fix this: ![](/tmp/a.png) and also @/tmp/b.jpg please")
	if len(paths) != 2 {
		t.Fatalf("paths = %v, want 2", paths)
	}
	if paths[0] != "/tmp/a.png" || paths[1] != "/tmp/b.jpg" {
		t.Errorf("paths = %v", paths)
	}
	if !strings.Contains(residual, "fix this") || !strings.Contains(residual, "please") {
		t.Errorf("residual lost text: %q", residual)
	}
	if strings.Contains(residual, "/tmp/a.png") || strings.Contains(residual, "/tmp/b.jpg") {
		t.Errorf("residual still contains image paths: %q", residual)
	}
}

func TestExtractImageRefs_BarePathsNeverLifted(t *testing.T) {
	// Regression guard: bare "/tmp/foo.png" in prose or alone must
	// NOT be extracted. User must use @ prefix or ![](...) syntax.
	for _, msg := range []string{
		"rename /tmp/foo.png in the repo",
		"/tmp/foo.png",
		"/tmp/b.jpg please",
	} {
		paths, residual := ExtractImageRefs(msg)
		if len(paths) != 0 {
			t.Errorf("bare path in %q must NOT be extracted, got %v", msg, paths)
		}
		if !strings.Contains(residual, ".") {
			t.Errorf("mentioned path must remain in residual for %q: %q", msg, residual)
		}
	}
}

func TestExtractImageRefs_AtPrefix(t *testing.T) {
	paths, residual := ExtractImageRefs("@/tmp/screenshot.png look here")
	if len(paths) != 1 || paths[0] != "/tmp/screenshot.png" {
		t.Errorf("paths = %v", paths)
	}
	if !strings.Contains(residual, "look here") {
		t.Errorf("residual = %q", residual)
	}
}

func TestExtractImageRefs_NonImageExtensionIgnored(t *testing.T) {
	paths, residual := ExtractImageRefs("see @/tmp/notes.txt and @/tmp/a.png")
	if len(paths) != 1 || paths[0] != "/tmp/a.png" {
		t.Errorf("paths = %v", paths)
	}
	if !strings.Contains(residual, "/tmp/notes.txt") {
		t.Errorf("non-image path should remain in residual: %q", residual)
	}
}

func TestLoadImage_ValidPNG(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "shot.png")
	writePNG(t, p)

	img, err := LoadImage(p)
	if err != nil {
		t.Fatalf("LoadImage: %v", err)
	}
	if img.MediaType != "image/png" {
		t.Errorf("media type = %q", img.MediaType)
	}
	if _, err := base64.StdEncoding.DecodeString(img.Data); err != nil {
		t.Errorf("data is not base64: %v", err)
	}
	block := img.ContentBlock()
	if block["type"] != "image" {
		t.Errorf("type = %v", block["type"])
	}
	src, _ := block["source"].(map[string]interface{})
	if src["type"] != "base64" || src["media_type"] != "image/png" {
		t.Errorf("source = %v", src)
	}
}

func TestLoadImage_MissingFile(t *testing.T) {
	_, err := LoadImage("/tmp/definitely-not-here-9999.png")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadImage_UnsupportedExtension(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "blob.bmp")
	if err := os.WriteFile(p, []byte("BM fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadImage(p)
	if err == nil || !strings.Contains(err.Error(), "unsupported extension") {
		t.Errorf("expected unsupported-extension error, got %v", err)
	}
}

func TestLoadImage_TooLarge(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.png")
	big := make([]byte, MaxImageBytes+1)
	if err := os.WriteFile(p, big, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadImage(p)
	if err == nil || !strings.Contains(err.Error(), "caps images") {
		t.Errorf("expected size cap error, got %v", err)
	}
}

func TestLoadImage_MislabeledContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "fake.png")
	if err := os.WriteFile(p, []byte("not a png"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadImage(p)
	if err == nil || !strings.Contains(err.Error(), "content does not match") {
		t.Errorf("expected content-mismatch error, got %v", err)
	}
}

func TestModelSupportsVision(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"claude-sonnet-4-6", true},
		{"claude-opus-4-6", true},
		{"claude-haiku-4-5", true},
		{"claude-3-5-sonnet-20241022", true},
		{"gpt-4o", true},
		{"gpt-4o-mini", true},
		{"gpt-5", true},
		{"gemini-2.0-flash", true},
		{"minimax-m1", true},
		{"anthropic/claude-sonnet-4-6", true},
		{"some-random-model", false},
		{"", false},
		{"gpt-3.5-turbo", false},
	}
	for _, c := range cases {
		if got := ModelSupportsVision(c.model); got != c.want {
			t.Errorf("ModelSupportsVision(%q) = %v, want %v", c.model, got, c.want)
		}
	}
}

func TestSend_AttachesImageBlockWhenModelSupportsVision(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ui.png")
	writePNG(t, p)

	mp := newMockProvider(mockResponse{deltas: []string{"got it"}})
	s, _ := NewSession(mp, Config{Model: "claude-sonnet-4-6"})

	if _, err := s.Send(context.Background(), "what's wrong here: @"+p, nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(mp.calls) != 1 {
		t.Fatalf("calls = %d", len(mp.calls))
	}
	lastUser := mp.calls[0].Messages[0]
	var blocks []map[string]interface{}
	if err := json.Unmarshal(lastUser.Content, &blocks); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	hasImage := false
	for _, b := range blocks {
		if b["type"] == "image" {
			hasImage = true
			src, _ := b["source"].(map[string]interface{})
			if src["media_type"] != "image/png" {
				t.Errorf("media_type = %v", src["media_type"])
			}
		}
	}
	if !hasImage {
		t.Errorf("no image block in user message, blocks=%v", blocks)
	}
	imgs := s.LastTurnImages()
	if len(imgs) != 1 || imgs[0] != p {
		t.Errorf("LastTurnImages = %v, want [%s]", imgs, p)
	}
}

func TestSend_StripsImageForNonVisionModelButKeepsPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ui.png")
	writePNG(t, p)

	mp := newMockProvider(mockResponse{deltas: []string{"ok"}})
	s, _ := NewSession(mp, Config{Model: "some-obscure-model"})

	if _, err := s.Send(context.Background(), "check @"+p, nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	lastUser := mp.calls[0].Messages[0]
	var blocks []map[string]interface{}
	_ = json.Unmarshal(lastUser.Content, &blocks)
	for _, b := range blocks {
		if b["type"] == "image" {
			t.Errorf("image block leaked through to non-vision model: %v", b)
		}
	}
	hasNotice := false
	for _, b := range blocks {
		if b["type"] == "text" {
			if s, ok := b["text"].(string); ok && strings.Contains(s, "does not support vision") {
				hasNotice = true
			}
		}
	}
	if !hasNotice {
		t.Errorf("no vision-notice text, blocks=%v", blocks)
	}
	if got := s.LastTurnImages(); len(got) != 1 || got[0] != p {
		t.Errorf("LastTurnImages = %v", got)
	}
}

func TestSend_BadImagePathAbortsTurn(t *testing.T) {
	mp := newMockProvider(mockResponse{deltas: []string{"should not run"}})
	s, _ := NewSession(mp, Config{Model: "claude-sonnet-4-6"})

	_, err := s.Send(context.Background(), "look at @/tmp/does-not-exist-xyz.png", nil, nil)
	if err == nil {
		t.Fatal("expected error for missing image path")
	}
	if len(mp.calls) != 0 {
		t.Errorf("provider should not have been called: %d calls", len(mp.calls))
	}
}

func TestSend_ImagesResetPerTurn(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "shot.png")
	writePNG(t, p)

	mp := newMockProvider(
		mockResponse{deltas: []string{"ok"}},
		mockResponse{deltas: []string{"still ok"}},
	)
	s, _ := NewSession(mp, Config{Model: "claude-sonnet-4-6"})

	if _, err := s.Send(context.Background(), "see @"+p, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.LastTurnImages(); len(got) != 1 {
		t.Fatalf("turn 1 images = %v", got)
	}
	if _, err := s.Send(context.Background(), "now text only", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.LastTurnImages(); len(got) != 0 {
		t.Errorf("turn 2 should have no images, got %v", got)
	}
}

func TestRunToolCallWithImages_PropagatesToDispatcher(t *testing.T) {
	d := &stubDispatcher{}
	paths := []string{"/tmp/a.png", "/tmp/b.jpg"}
	if _, err := RunToolCallWithImages(d, "dispatch_build", json.RawMessage(`{"description":"fix ui"}`), paths); err != nil {
		t.Fatalf("RunToolCallWithImages: %v", err)
	}
	if len(d.lastImages) != 2 || d.lastImages[0] != "/tmp/a.png" {
		t.Errorf("lastImages = %v", d.lastImages)
	}
	if _, err := RunToolCallWithImages(d, "dispatch_build", json.RawMessage(`{"description":"x"}`), nil); err != nil {
		t.Fatalf("reset call: %v", err)
	}
	if d.lastImages != nil {
		t.Errorf("lastImages should reset, got %v", d.lastImages)
	}
}
