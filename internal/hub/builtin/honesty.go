// Package builtin provides the built-in hub subscribers that Stoke ships with.
// These are registered on the event bus at startup and provide deterministic
// enforcement of code quality and security rules.
package builtin

import (
	"context"
	"regexp"
	"strings"

	"github.com/RelayOne/r1/internal/hub"
)

// HonestyGate is a gate subscriber that detects faking-completeness patterns
// in proposed file writes: placeholders, type suppressions, and test removal.
type HonestyGate struct {
	DiffSizeHardLimit int // lines, default 1000
}

var (
	// Patterns that indicate placeholder code
	placeholderPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^\s*(//|#)\s*todo`),
		regexp.MustCompile(`(?i)^\s*(//|#)\s*fixme`),
		regexp.MustCompile(`(?i)^\s*(//|#)\s*xxx`),
		regexp.MustCompile(`(?i)panic\(\s*"not implemented"\s*\)`),
		regexp.MustCompile(`(?i)throw new Error\(\s*"not implemented"\s*\)`),
		regexp.MustCompile(`(?i)raise NotImplementedError`),
	}

	// Suppression markers
	suppressionPatterns = []*regexp.Regexp{
		regexp.MustCompile(`@ts-ignore`),
		regexp.MustCompile(`\bas any\b`),
		regexp.MustCompile(`eslint-disable`),
		regexp.MustCompile(`//\s*nolint`),
	}
)

// Register adds the honesty gate to the bus.
func (h *HonestyGate) Register(bus *hub.Bus) {
	bus.Register(hub.Subscriber{
		ID:       "builtin.honesty",
		Events:   []hub.EventType{hub.EventToolFileWrite},
		Mode:     hub.ModeGateStrict,
		Priority: 100,
		Handler:  h.handle,
	})
}

func (h *HonestyGate) handle(ctx context.Context, ev *hub.Event) *hub.HookResponse {
	if ev.File == nil {
		return &hub.HookResponse{Decision: hub.Allow}
	}

	content := ""
	if ev.Tool != nil {
		content = ev.Tool.Output
	}
	if content == "" && ev.File != nil {
		// For file write events, the content might be in Custom payload
		if c, ok := ev.Custom["content"]; ok {
			if s, ok := c.(string); ok {
				content = s
			}
		}
	}
	if content == "" {
		return &hub.HookResponse{Decision: hub.Allow}
	}

	// Check 1: diff size
	hardLimit := h.DiffSizeHardLimit
	if hardLimit == 0 {
		hardLimit = 1000
	}
	addedLines := strings.Count(content, "\n")
	if addedLines > hardLimit {
		return &hub.HookResponse{
			Decision: hub.Deny,
			Reason:   "diff exceeds hard line limit; split into smaller changes",
		}
	}

	// Check 2: placeholders and suppressions
	for _, line := range strings.Split(content, "\n") {
		for _, pat := range placeholderPatterns {
			if pat.MatchString(line) {
				return &hub.HookResponse{
					Decision: hub.Deny,
					Reason:   "placeholder code detected: " + strings.TrimSpace(line),
				}
			}
		}
		for _, pat := range suppressionPatterns {
			if pat.MatchString(line) {
				return &hub.HookResponse{
					Decision: hub.Deny,
					Reason:   "type/lint suppression detected: " + strings.TrimSpace(line),
				}
			}
		}
	}

	// Check 3: test removal (test file shrunk by >50%)
	if ev.File != nil && isTestFile(ev.File.Path) {
		if oldContent, ok := ev.Custom["old_content"]; ok {
			if old, ok := oldContent.(string); ok && len(old) > 0 && len(content) < len(old)/2 {
				return &hub.HookResponse{
					Decision: hub.Deny,
					Reason:   "test file shrunk by more than 50% — possible test removal",
				}
			}
		}
	}

	return &hub.HookResponse{Decision: hub.Allow}
}

func isTestFile(path string) bool {
	return strings.HasSuffix(path, "_test.go") ||
		strings.Contains(path, ".test.") ||
		strings.Contains(path, ".spec.") ||
		strings.HasPrefix(path, "test_")
}
