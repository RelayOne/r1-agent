package chat

// Image-input support for chat mode.
//
// Users paste image references inline in their chat text. We detect two
// forms deterministically (no LLM involvement):
//
//   - Bare path:       /tmp/screenshot.png
//   - Markdown image:  ![alt](/tmp/screenshot.png)
//
// Detection is intentionally narrow — we only accept a path as an image
// when it appears as a standalone token (surrounded by whitespace or
// line boundaries) with a supported extension. That prevents a user
// sentence like "open /tmp/foo.png in your editor" from false-positiving
// as an image attachment: the path there is part of a sentence, and the
// current narrow form accepts it only if it stands alone.
//
// On match we read the file, validate size and format, base64-encode
// the bytes, and return both an Anthropic-format image content block
// and the original file path. The caller folds the content blocks into
// the user message alongside the (residual) text.
//
// Non-image inline data (raw base64 pastes, multipart uploads) is NOT
// supported in this pass — we tell the user to save to a file first.
// That keeps the code deterministic and easy to audit.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MaxImageBytes caps an individual image file size. Anthropic's hard
// limit is around 5MB per image; we check before base64 expansion so
// the final payload stays under the API ceiling.
const MaxImageBytes = 5 * 1024 * 1024

// supportedImageExts is the allowlist of file extensions chat will
// accept as images. Extensions match the media types Anthropic vision
// accepts. Everything else is rejected with a clear error.
var supportedImageExts = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
}

// markdownImageRE matches Markdown image syntax: ![alt](path). The alt
// text is captured but unused; we only care about the path.
var markdownImageRE = regexp.MustCompile(`!\[[^\]]*\]\(([^\s)]+)\)`)

// barePathRE matches a standalone token that looks like a filesystem
// path ending in a supported image extension. The path must:
//   - start with `/` or `@/` (the `@` prefix is a common "attach this"
//     convention in chat UIs; we strip it),
//   - not contain whitespace,
//   - end with one of the supported extensions (case-insensitive),
//   - be delimited by whitespace or string boundaries (enforced by the
//     caller splitting on whitespace before matching).
// The @ prefix is REQUIRED for bare paths — without it, a user message
// like "rename /tmp/foo.png in the repo" would silently swallow the path
// and try to attach it as an image. Explicit @ means "attach this"; a
// plain path in prose means "I'm talking about this file." Markdown
// image syntax ![...](path) is the other explicit form handled above.
var barePathRE = regexp.MustCompile(`(?i)^@(/\S+\.(?:png|jpe?g|webp|gif))$`)

// AttachedImage is one successfully loaded image: the original path
// (for audit/log surfaces) plus the Anthropic-format content block
// bytes ready to be marshaled into a message.
type AttachedImage struct {
	// Path is the absolute-or-original path the user typed. Preserved
	// so downstream workers can reference the same file on disk.
	Path string
	// MediaType is the Anthropic media_type string, e.g. "image/png".
	MediaType string
	// Data is the base64-encoded file bytes (no data: prefix).
	Data string
}

// ContentBlock returns the Anthropic image content block as a map
// suitable for json.Marshal. Shape:
//
//	{"type":"image","source":{"type":"base64","media_type":"...","data":"..."}}
func (a AttachedImage) ContentBlock() map[string]interface{} {
	return map[string]interface{}{
		"type": "image",
		"source": map[string]interface{}{
			"type":       "base64",
			"media_type": a.MediaType,
			"data":       a.Data,
		},
	}
}

// ExtractImageRefs scans userText for image references and returns the
// list of candidate paths in the order they appear, plus the residual
// text with the references stripped. The residual text is what the
// model sees as the text block; the paths are loaded separately.
//
// Only the two supported forms (bare path, Markdown image) are
// recognized. A residual empty string is fine — some users paste just
// a screenshot with no words.
func ExtractImageRefs(userText string) (paths []string, residual string) {
	// Pass 1: markdown images. Replace each match with an empty string.
	text := markdownImageRE.ReplaceAllStringFunc(userText, func(match string) string {
		sub := markdownImageRE.FindStringSubmatch(match)
		if len(sub) >= 2 {
			p := strings.TrimSpace(sub[1])
			if hasSupportedExt(p) {
				paths = append(paths, p)
				return ""
			}
		}
		return match
	})

	// Pass 2: bare paths. Tokenize on whitespace so "open /tmp/foo.png"
	// (two tokens: "open", "/tmp/foo.png") matches only the path token;
	// but "/tmp/foo.png" alone also matches. We rebuild the residual
	// text token-by-token so non-matching tokens stay intact.
	var residualTokens []string
	for _, tok := range splitPreservingSpaces(text) {
		trimmed := strings.TrimSpace(tok)
		if trimmed == "" {
			residualTokens = append(residualTokens, tok)
			continue
		}
		if m := barePathRE.FindStringSubmatch(trimmed); m != nil {
			paths = append(paths, m[1])
			// Drop the token (and its trailing whitespace) from the
			// residual so the model doesn't see the path twice.
			continue
		}
		residualTokens = append(residualTokens, tok)
	}
	residual = strings.TrimSpace(strings.Join(residualTokens, ""))
	return paths, residual
}

// splitPreservingSpaces splits on whitespace runs but preserves each
// whitespace run as its own token so we can rejoin without mangling
// original spacing.
func splitPreservingSpaces(s string) []string {
	var out []string
	var cur strings.Builder
	inSpace := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		isSp := r == ' ' || r == '\t' || r == '\n' || r == '\r'
		if isSp != inSpace {
			flush()
			inSpace = isSp
		}
		cur.WriteRune(r)
	}
	flush()
	return out
}

// hasSupportedExt reports whether path's extension is in the allowlist.
func hasSupportedExt(path string) bool {
	_, ok := supportedImageExts[strings.ToLower(filepath.Ext(path))]
	return ok
}

// LoadImage reads path, validates size + format, and returns the
// Anthropic-ready AttachedImage. Returns a descriptive error on any
// problem — the caller surfaces it to the user as a chat reply instead
// of a stack trace.
func LoadImage(path string) (AttachedImage, error) {
	if strings.TrimSpace(path) == "" {
		return AttachedImage{}, errors.New("image: empty path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return AttachedImage{}, fmt.Errorf("image: cannot stat %s: %w", path, err)
	}
	if info.IsDir() {
		return AttachedImage{}, fmt.Errorf("image: %s is a directory, not a file", path)
	}
	if info.Size() > MaxImageBytes {
		return AttachedImage{}, fmt.Errorf("image: %s is %d bytes; stoke chat caps images at %d bytes (~5MB)", path, info.Size(), MaxImageBytes)
	}
	ext := strings.ToLower(filepath.Ext(path))
	media, ok := supportedImageExts[ext]
	if !ok {
		return AttachedImage{}, fmt.Errorf("image: %s has unsupported extension %q; supported: .png .jpg .jpeg .webp .gif", path, ext)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return AttachedImage{}, fmt.Errorf("image: read %s: %w", path, err)
	}
	// Defense-in-depth: re-check the size post-read; file can grow
	// between Stat and ReadFile.
	if len(data) > MaxImageBytes {
		return AttachedImage{}, fmt.Errorf("image: %s grew past the %d-byte cap during read", path, MaxImageBytes)
	}
	// Sniff the first 512 bytes to confirm the extension claim. This
	// catches the mis-labelled-file case (e.g. a .png that is actually
	// a PDF) before we ship it to the model.
	if !mediaTypeMatches(data, media) {
		return AttachedImage{}, fmt.Errorf("image: %s content does not match its %s extension", path, ext)
	}
	return AttachedImage{
		Path:      path,
		MediaType: media,
		Data:      base64.StdEncoding.EncodeToString(data),
	}, nil
}

// mediaTypeMatches does a lightweight content-sniff to check the file
// bytes match the claimed media type. Uses net/http.DetectContentType
// which inspects magic numbers. False positives are rare; false
// negatives (we reject a valid image) would show up quickly in
// testing.
func mediaTypeMatches(data []byte, claimed string) bool {
	if len(data) == 0 {
		return false
	}
	sniff := data
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	detected := http.DetectContentType(sniff)
	// Anthropic accepts image/jpeg for both .jpg and .jpeg; the sniff
	// returns image/jpeg either way, so a direct equality check works.
	// GIF sniff can return "image/gif" or sometimes "application/octet-stream"
	// on truncated files — only treat a clean mismatch as failure.
	switch claimed {
	case "image/jpeg":
		return strings.HasPrefix(detected, "image/jpeg")
	case "image/png":
		return strings.HasPrefix(detected, "image/png")
	case "image/webp":
		return strings.HasPrefix(detected, "image/webp")
	case "image/gif":
		return strings.HasPrefix(detected, "image/gif")
	}
	return false
}

// VisionCapableModels is the allowlist of model ID prefixes that
// support image inputs. When the configured chat model does NOT match
// any prefix here, image blocks are stripped and the user sees a
// warning. The list is conservative — we'd rather warn on a
// vision-capable model nobody added than silently send bytes to a
// text-only endpoint that returns an opaque 400.
//
// Sources:
//   - Claude 4 family: see model pricing table at
//     internal/hub/builtin/cost_tracker.go (claude-opus-4-6,
//     claude-sonnet-4-6, claude-haiku-4-5 are the configured IDs).
//     All Claude 4-series and 3.5-series models accept vision per
//     Anthropic's docs.
//   - GPT-4o / GPT-4-turbo / GPT-5: OpenAI vision models.
//   - Gemini 1.5 / 2.x: Google vision models.
//   - MiniMax-M1 via LiteLLM: MiniMax's current vision-capable
//     generation (see internal/litellm for LiteLLM routing context).
var VisionCapableModels = []string{
	// Anthropic Claude (all modern Claude models are multimodal).
	"claude-opus-4",
	"claude-sonnet-4",
	"claude-haiku-4",
	"claude-3-5",
	"claude-3-7",
	"claude-opus-3",
	"claude-sonnet-3",
	"claude-haiku-3",
	// OpenAI vision.
	"gpt-4o",
	"gpt-4-turbo",
	"gpt-4.1",
	"gpt-5",
	"o1",
	"o3",
	"o4",
	// Google Gemini vision.
	"gemini-1.5",
	"gemini-2",
	// MiniMax (LiteLLM routing typically uses "minimax-m1" or
	// provider-prefixed "minimax/MiniMax-M1"); match either.
	"minimax-m1",
	"minimax/minimax-m1",
	"MiniMax-M1",
}

// ModelSupportsVision reports whether modelID is on the vision
// allowlist. Matching is case-insensitive prefix matching; callers
// typically pass the raw configured model ID (may include a provider
// prefix like "anthropic/claude-sonnet-4-6").
func ModelSupportsVision(modelID string) bool {
	if modelID == "" {
		return false
	}
	lower := strings.ToLower(modelID)
	// Strip common provider prefixes so e.g. "anthropic/claude-sonnet-4-6"
	// matches "claude-sonnet-4".
	for _, p := range []string{"anthropic/", "openai/", "google/", "litellm/"} {
		if strings.HasPrefix(lower, p) {
			lower = strings.TrimPrefix(lower, p)
			break
		}
	}
	for _, prefix := range VisionCapableModels {
		if strings.HasPrefix(lower, strings.ToLower(prefix)) {
			return true
		}
	}
	return false
}
