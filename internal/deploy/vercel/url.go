// Package vercel implements the Vercel CLI adapter for Stoke's deploy
// pipeline. This file holds the URL-extraction helper used to parse the
// deployment URL out of the `vercel deploy` stdout/stderr stream.
//
// Vercel's CLI has no `--json` flag on `deploy` (deploy-phase2.md §Vercel
// CLI Contract), so Stoke scrapes the URL from the combined output. Two
// shapes are supported:
//
//   - Preview URLs: `https://<slug>-<hash>-<team>.vercel.app`, usually
//     labeled `Deployment URL:` or echoed after `Ready!`.
//   - Production URLs: `https://<custom-domain>` printed on a
//     `Production:` line, often alongside the preview.
//
// The primary regex extraction catches these labeled lines first; when no
// labeled match is found, a line-scan fallback picks the first bare
// `https://` token on a line that does NOT start with a prompt/error/
// warning marker (`?`, `Error:`, `Warning:`).
package vercel

import (
	"net/url"
	"regexp"
	"strings"
)

// primaryLineRe matches labeled Vercel CLI lines. The label set covers
// every shape the CLI emits a URL next to across 30+/31/32 minor
// releases: `Deployment URL:`, `Ready!`, `Inspect:`, `Production:`.
// The URL capture group is greedy up to the next whitespace.
var primaryLineRe = regexp.MustCompile(
	`(?m)^(?:Deployment URL:|Ready!|Inspect:|Production:)\s+(https://\S+)`,
)

// vercelAppRe matches a bare Vercel preview hostname anywhere in output.
// Used as the second extraction tier before the generic fallback scan,
// since `*.vercel.app` URLs are the dominant shape and this lets us
// find one even when the label format changes.
var vercelAppRe = regexp.MustCompile(
	`https://[a-z0-9][a-z0-9-]*\.vercel\.app\b`,
)

// ExtractURL parses the combined stdout+stderr of a `vercel deploy`
// invocation and returns the deployment URL. Returns (url, true) on
// match, ("", false) otherwise.
//
// Strategy:
//  1. Primary: scan for labeled lines (`Deployment URL:`, `Ready!`,
//     `Inspect:`, `Production:`) and return the URL that follows.
//  2. Secondary: scan for a bare `https://*.vercel.app` token anywhere
//     in the output.
//  3. Fallback: line-scan for the first `https://` token on a line that
//     does NOT begin with `?` (interactive prompt), `Error:`, or
//     `Warning:`.
//
// The returned URL is always stripped of trailing punctuation that the
// CLI occasionally appends (`.`, `,`, `)`).
func ExtractURL(output string) (string, bool) {
	if output == "" {
		return "", false
	}

	// Primary: labeled lines.
	if m := primaryLineRe.FindStringSubmatch(output); len(m) == 2 {
		if u := cleanURL(m[1]); isValidURL(u) {
			return u, true
		}
	}

	// Secondary: bare *.vercel.app token.
	if m := vercelAppRe.FindString(output); m != "" {
		if u := cleanURL(m); isValidURL(u) {
			return u, true
		}
	}

	// Fallback: line-scan.
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if skipFallbackLine(line) {
			continue
		}
		idx := strings.Index(line, "https://")
		if idx < 0 {
			continue
		}
		tok := line[idx:]
		// Cut at first whitespace.
		if sp := strings.IndexAny(tok, " \t"); sp >= 0 {
			tok = tok[:sp]
		}
		if u := cleanURL(tok); isValidURL(u) {
			return u, true
		}
	}

	return "", false
}

// skipFallbackLine reports whether the line should be ignored by the
// fallback scan. Vercel's CLI uses `?` for interactive prompts and
// `Error:` / `Warning:` prefixes for diagnostic lines — URLs on those
// lines are never the deployment URL.
func skipFallbackLine(line string) bool {
	switch {
	case strings.HasPrefix(line, "?"):
		return true
	case strings.HasPrefix(line, "Error:"):
		return true
	case strings.HasPrefix(line, "Warning:"):
		return true
	}
	return false
}

// cleanURL strips trailing punctuation the CLI occasionally tacks on
// (sentence periods, commas, closing parens) without mangling the path.
func cleanURL(u string) string {
	u = strings.TrimSpace(u)
	for len(u) > 0 {
		last := u[len(u)-1]
		if last == '.' || last == ',' || last == ')' || last == ';' {
			u = u[:len(u)-1]
			continue
		}
		break
	}
	return u
}

// isValidURL applies a minimal sanity check: must parse, must be https,
// must have a host.
func isValidURL(u string) bool {
	if u == "" {
		return false
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	if parsed.Scheme != "https" {
		return false
	}
	if parsed.Host == "" {
		return false
	}
	return true
}

// IsPreview reports whether u is a Vercel preview URL (i.e. a
// *.vercel.app hostname). Production aliases on custom domains return
// false. An unparsable URL also returns false.
func IsPreview(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Host)
	if host == "" {
		return false
	}
	// Strip any port.
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	return host == "vercel.app" || strings.HasSuffix(host, ".vercel.app")
}
