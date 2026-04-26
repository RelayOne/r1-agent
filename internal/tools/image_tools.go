// image_tools.go — image_read tool handler.
//
// T-R1P-004: Image / vision input — reads an image file from disk, encodes it
// as a base64 data-URI, and returns metadata (dimensions, MIME type, size).
// The base64 payload can be passed directly to the Anthropic Messages API
// vision content block (type: "image", source.type: "base64").
//
// Supported formats: JPEG, PNG, GIF, WebP (Anthropic-supported).
// Graceful: returns an error string (not a Go error) for unsupported formats
// so the model can report the issue and continue.
//
// Security: path is confined to the registry working directory.
// Size cap: 5MB (Anthropic max image upload).
package tools

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"  // register GIF decoder
	_ "image/jpeg" // register JPEG decoder
	_ "image/png"  // register PNG decoder
	"os"
	"path/filepath"
	"strings"
)

const (
	// maxImageBytes is the max file size we'll encode (5MB — Anthropic cap).
	maxImageBytes = 5 * 1024 * 1024
)

// mimeForExt returns the MIME type for recognised image extensions.
// Returns "" for unsupported extensions.
func mimeForExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return ""
	}
}

// handleImageRead implements the image_read tool (T-R1P-004).
func (r *Registry) handleImageRead(input json.RawMessage) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	resolved, err := r.resolvePath(args.Path)
	if err != nil {
		return "", err
	}

	ext := filepath.Ext(resolved)
	mimeType := mimeForExt(ext)
	if mimeType == "" {
		return fmt.Sprintf("image_read: unsupported format %q — supported: .jpg .jpeg .png .gif .webp", ext), nil
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("cannot stat %s: %w", args.Path, err)
	}
	if info.Size() > maxImageBytes {
		return fmt.Sprintf("image_read: file too large (%d bytes, max %d). Use a smaller image.", info.Size(), maxImageBytes), nil
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", args.Path, err)
	}

	// Decode to get dimensions (best-effort — skip for WebP which stdlib doesn't decode).
	widthStr := "unknown"
	heightStr := "unknown"
	if mimeType != "image/webp" {
		f, openErr := os.Open(resolved)
		if openErr == nil {
			if cfg, _, decodeErr := image.DecodeConfig(f); decodeErr == nil {
				widthStr = fmt.Sprintf("%d", cfg.Width)
				heightStr = fmt.Sprintf("%d", cfg.Height)
			}
			f.Close()
		}
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	dataURI := fmt.Sprintf("data:%s;base64,%s", mimeType, encoded)

	var sb strings.Builder
	fmt.Fprintf(&sb, "image_read: %s\n", resolved)
	fmt.Fprintf(&sb, "mime_type: %s\n", mimeType)
	fmt.Fprintf(&sb, "size_bytes: %d\n", len(data))
	fmt.Fprintf(&sb, "dimensions: %s x %s\n", widthStr, heightStr)
	fmt.Fprintf(&sb, "base64_data_uri: %s\n", dataURI)
	return sb.String(), nil
}
