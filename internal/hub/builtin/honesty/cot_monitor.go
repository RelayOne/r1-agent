package honesty

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/ericmacdougall/stoke/internal/hub"
)

// CoTMonitor is an observe subscriber that scans extended thinking blocks
// for explicit deception markers. It is READ-ONLY — it does not affect the
// agent's reasoning or block actions. This is critical: penalizing CoT causes
// models to hide intent while continuing to cheat.
type CoTMonitor struct {
	// Detections is a log of all detected deception markers.
	Detections []CoTDetection
}

// CoTDetection records a deception marker found in chain-of-thought.
type CoTDetection struct {
	TaskID  string
	Pattern string
	Line    string
}

// NewCoTMonitor creates a new CoT monitor.
func NewCoTMonitor() *CoTMonitor {
	return &CoTMonitor{}
}

// Register adds the CoT monitor to the hub.
func (c *CoTMonitor) Register(bus *hub.Bus) {
	bus.Register(hub.Subscriber{
		ID:       "builtin.honesty.cot_monitor",
		Events:   []hub.EventType{hub.EventModelPostCall},
		Mode:     hub.ModeObserve,
		Priority: 300,
		Handler:  c.handle,
	})
}

// Patterns observed in OpenAI's CoT monitoring research and METR's evaluations.
var deceptionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)let'?s?\s*(fudge|hack|cheat|fake)`),
	regexp.MustCompile(`(?i)circumvent\s+the\s+test`),
	regexp.MustCompile(`(?i)make\s+(verify|check|test)\s+always\s+return\s+true`),
	regexp.MustCompile(`(?i)skip\s+the\s+test`),
	regexp.MustCompile(`(?i)comment\s+out\s+(the\s+)?(assert|test|check)`),
	regexp.MustCompile(`(?i)hardcode\s+the\s+(answer|expected|output)`),
	regexp.MustCompile(`(?i)delete\s+the\s+test`),
	regexp.MustCompile(`(?i)exit\s*\(\s*0\s*\)`),
	regexp.MustCompile(`(?i)i\s*(don't|do not|cannot|can't)\s+actually\s+(implement|fix|solve)`),
	regexp.MustCompile(`(?i)just\s+pretend`),
	regexp.MustCompile(`(?i)mock\s+(it|this|the result)\s+instead`),
}

func (c *CoTMonitor) handle(ctx context.Context, ev *hub.Event) *hub.HookResponse {
	if ev.Tool == nil {
		return &hub.HookResponse{Decision: hub.Allow}
	}

	thinkingText, _ := ev.Tool.Input["thinking_text"].(string)
	if thinkingText == "" {
		return &hub.HookResponse{Decision: hub.Allow}
	}

	var matches []string
	for _, pat := range deceptionPatterns {
		if loc := pat.FindStringIndex(thinkingText); loc != nil {
			line := lineContaining(thinkingText, loc[0])
			matches = append(matches, strings.TrimSpace(line))
			c.Detections = append(c.Detections, CoTDetection{
				Pattern: pat.String(),
				Line:    strings.TrimSpace(line),
			})
		}
	}

	if len(matches) > 0 {
		// READ-ONLY: log to audit, raise alert, but DO NOT block
		return &hub.HookResponse{
			Decision: hub.Allow, // never block based on CoT — this is observe-only
			Reason:   fmt.Sprintf("CoT_DECEPTION_MARKERS: %v", matches),
		}
	}
	return &hub.HookResponse{Decision: hub.Allow}
}

// DetectionCount returns the number of deception markers detected.
func (c *CoTMonitor) DetectionCount() int {
	return len(c.Detections)
}

func lineContaining(text string, offset int) string {
	start := strings.LastIndex(text[:offset], "\n")
	if start < 0 {
		start = 0
	} else {
		start++
	}
	end := strings.Index(text[offset:], "\n")
	if end < 0 {
		end = len(text)
	} else {
		end += offset
	}
	return text[start:end]
}
