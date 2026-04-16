// Package skill — apifirst.go
//
// STOKE-022 primitive #6: API-first / browser-fallback tool
// positioning. When an agent has both structured API tools
// (web_search / web_extract / specific REST tools) AND a
// generic browser automation tool in its toolset, the
// browser tool should be positioned as a FALLBACK — only
// invoked when the structured API can't answer the query.
//
// This package defines a PositioningHint applied to tool
// descriptions at prompt-build time so the agent sees:
//
//     PRIMARY tools (try these first):
//       - web_search ...
//       - web_extract ...
//     FALLBACK tools (use only when PRIMARY insufficient):
//       - browser_navigate ...
//       - browser_click ...
//
// The ACI-optimized DOM representation the SOW mentions
// lives inside the browser tool's own invocation path;
// positioning is the agent-steering half.
//
// Scope of this file:
//
//   - ToolTier enum + DefaultTier heuristic
//   - PositionedTool wrapper + Sort
//   - RenderPromptSection renders a tier-grouped block
package skill

import (
	"sort"
	"strings"
)

// ToolTier classifies a tool's position in the API-first
// ladder.
type ToolTier string

const (
	// TierPrimary tools are tried first. Structured APIs,
	// specific domain tools (calendar_list_events, etc.),
	// registry queries.
	TierPrimary ToolTier = "primary"

	// TierFallback tools are reserved for when primaries
	// can't complete the job. Browser automation, generic
	// HTTP clients against unknown endpoints, shell-escape
	// tools.
	TierFallback ToolTier = "fallback"

	// TierRestricted tools are neither primary nor general
	// fallback — they require explicit policy opt-in and
	// shouldn't appear in the default tool catalog. Example:
	// filesystem delete, privileged shell.
	TierRestricted ToolTier = "restricted"
)

// PositionedTool pairs a tool name + description with its
// tier. The prompt builder sorts primaries before fallbacks
// so the agent's attention lands on the correct
// first-choice option.
type PositionedTool struct {
	Name        string
	Description string
	Tier        ToolTier
	// Reason is the one-line explanation for the tier
	// assignment. Shown in the rendered prompt so the
	// agent sees WHY a tool is fallback rather than
	// primary.
	Reason string
}

// DefaultTier applies the SOW's heuristic to a tool name:
// browser-shaped names default to Fallback; dangerous names
// default to Restricted; everything else is Primary.
//
// Operators override by setting the Tier field explicitly
// before Sort/Render.
func DefaultTier(name string) ToolTier {
	lower := strings.ToLower(name)
	// Restricted hints checked FIRST. A tool name carrying
	// both a browser-shaped and a restricted-shaped hint
	// (e.g. `playwright_privileged_exec`) must classify as
	// Restricted so the explicit-policy gate applies rather
	// than the softer fallback treatment. Earlier version
	// had these in the opposite order and returned
	// TierFallback for such names.
	for _, restrictedHint := range []string{"rm_rf", "sudo", "format_disk", "drop_table", "privileged"} {
		if strings.Contains(lower, restrictedHint) {
			return TierRestricted
		}
	}
	for _, browserHint := range []string{"browser", "playwright", "puppeteer", "selenium", "webdriver"} {
		if strings.Contains(lower, browserHint) {
			return TierFallback
		}
	}
	return TierPrimary
}

// Sort orders tools by tier (primary → fallback →
// restricted), then alphabetically by name within a tier.
// Deterministic so repeated prompt builds produce identical
// text (cache-friendly).
func Sort(tools []PositionedTool) {
	sort.SliceStable(tools, func(i, j int) bool {
		ti, tj := tierRank(tools[i].Tier), tierRank(tools[j].Tier)
		if ti != tj {
			return ti < tj
		}
		return tools[i].Name < tools[j].Name
	})
}

func tierRank(t ToolTier) int {
	switch t {
	case TierPrimary:
		return 0
	case TierFallback:
		return 1
	case TierRestricted:
		return 2
	default:
		return 3
	}
}

// RenderPromptSection produces a prompt-ready text block
// listing tools grouped by tier with headers. The intended
// caller path is: loop over the agent's available tool
// list, wrap each in a PositionedTool (applying DefaultTier
// unless the capability manifest already declares one),
// Sort(), then Render.
//
// Tiers with zero members are omitted. Restricted tier gets
// an extra "require explicit policy grant" note so a worker
// that sees it doesn't assume it's free to call.
func RenderPromptSection(tools []PositionedTool) string {
	Sort(tools)
	var primary, fallback, restricted []PositionedTool
	for _, t := range tools {
		switch t.Tier {
		case TierPrimary:
			primary = append(primary, t)
		case TierFallback:
			fallback = append(fallback, t)
		case TierRestricted:
			restricted = append(restricted, t)
		}
	}
	var b strings.Builder
	if len(primary) > 0 {
		b.WriteString("PRIMARY TOOLS (try these first — structured APIs for the declared task):\n")
		for _, t := range primary {
			renderEntry(&b, t)
		}
		b.WriteString("\n")
	}
	if len(fallback) > 0 {
		b.WriteString("FALLBACK TOOLS (use only when PRIMARY tools cannot answer the query — e.g. the content requires JavaScript execution, the endpoint isn't exposed as an API, or the primary tool returned insufficient data):\n")
		for _, t := range fallback {
			renderEntry(&b, t)
		}
		b.WriteString("\n")
	}
	if len(restricted) > 0 {
		b.WriteString("RESTRICTED TOOLS (require explicit policy grant before invocation — agents without the matching scope MUST NOT call these):\n")
		for _, t := range restricted {
			renderEntry(&b, t)
		}
	}
	return b.String()
}

func renderEntry(b *strings.Builder, t PositionedTool) {
	b.WriteString("  - ")
	b.WriteString(t.Name)
	if strings.TrimSpace(t.Description) != "" {
		b.WriteString(": ")
		b.WriteString(strings.TrimSpace(t.Description))
	}
	if strings.TrimSpace(t.Reason) != "" {
		b.WriteString("  (")
		b.WriteString(strings.TrimSpace(t.Reason))
		b.WriteString(")")
	}
	b.WriteString("\n")
}
