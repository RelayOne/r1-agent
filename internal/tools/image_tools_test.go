// image_tools_test.go — tests for the image_read tool (T-R1P-004).
package tools

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageReadPNG(t *testing.T) {
	dir := t.TempDir()
	// Write a minimal 2x2 PNG.
	imgPath := filepath.Join(dir, "test.png")
	f, err := os.Create(imgPath)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if encErr := png.Encode(f, img); encErr != nil {
		f.Close()
		t.Fatal(encErr)
	}
	f.Close()

	r := NewRegistry(dir)
	result, err := r.Handle(context.Background(), "image_read", toJSON(map[string]string{"path": "test.png"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "mime_type: image/png") {
		t.Error("result should contain mime_type: image/png")
	}
	if !strings.Contains(result, "base64_data_uri: data:image/png;base64,") {
		t.Error("result should contain base64 data URI")
	}
	if !strings.Contains(result, "dimensions: 2 x 2") {
		t.Errorf("result should contain dimensions 2x2, got: %s", result)
	}
}

func TestImageReadUnsupportedFormat(t *testing.T) {
	dir := t.TempDir()
	txtPath := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(txtPath, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry(dir)
	result, err := r.Handle(context.Background(), "image_read", toJSON(map[string]string{"path": "file.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "unsupported format") {
		t.Errorf("expected unsupported format message, got: %s", result)
	}
}

func TestImageReadMissing(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	_, err := r.Handle(context.Background(), "image_read", toJSON(map[string]string{"path": "nofile.png"}))
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestImageReadHandleRouting(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	// Verify the tool is reachable by name — use a known-bad path to confirm routing (not unknown-tool error).
	_, err := r.Handle(context.Background(), "image_read", toJSON(map[string]string{"path": "x.png"}))
	if err != nil && strings.Contains(err.Error(), "unknown tool") {
		t.Error("image_read not wired into Handle()")
	}
}
