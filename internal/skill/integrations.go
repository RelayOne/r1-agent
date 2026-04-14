// Package skill — integrations.go
//
// Bundled API reference docs for well-known third-party services.
// Files at internal/skill/builtin/integrations/<name>.md ship
// embedded in the stoke binary. When a caller (feasibility gate,
// task briefing injector) detects a reference to one of these
// services, the matching doc is surfaced to the worker — so workers
// get a deterministic, verified reference page instead of relying on
// model training alone.
//
// Keyword-triggered, flat-file: no index, no embedding, no LLM. If
// the service name (or one of its aliases) matches a file basename,
// we return that file's content. Cost is zero after the binary
// ships.

package skill

import (
	"embed"
	"strings"
)

//go:embed builtin/integrations/*.md
var integrationsFS embed.FS

// integrationAliases maps service canonical name → bundled doc
// basename (without .md). Listed explicitly so that a missing file
// becomes a test-detectable drift rather than a silent miss.
var integrationAliases = map[string]string{
	"algolia":         "algolia",
	"amplitude":       "amplitude",
	"anthropic":       "anthropic",
	"apns":            "apns",
	"auth0":           "auth0",
	"aws-s3":          "aws-s3",
	"aws-ses":         "aws-ses",
	"bitbucket":       "bitbucket",
	"clerk":           "clerk",
	"cloudflare-r2":   "cloudflare-r2",
	"cloudinary":      "cloudinary",
	"cohere":          "cohere",
	"datadog":         "datadog",
	"discord":         "discord",
	"expo":            "expo-push",
	"expo-push":       "expo-push",
	"fcm":             "fcm",
	"firebase-auth":   "firebase-auth",
	"gemini":          "gemini",
	"github":          "github",
	"gitlab":          "gitlab",
	"google-maps":     "google-maps",
	"hubspot":         "hubspot",
	"intercom":        "intercom",
	"lemon-squeezy":   "lemon-squeezy",
	"mailgun":         "mailgun",
	"mapbox":          "mapbox",
	"meilisearch":     "meilisearch",
	"microsoft-teams": "microsoft-teams",
	"mistral":         "mistral",
	"mixpanel":        "mixpanel",
	"okta":            "okta",
	"onesignal":       "onesignal",
	"openai":          "openai",
	"paddle":          "paddle",
	"paypal":          "paypal",
	"plaid":           "plaid",
	"posthog":         "posthog",
	"postmark":        "postmark",
	"resend":          "resend",
	"s3":              "aws-s3",
	"segment":         "segment",
	"sendgrid":        "sendgrid",
	"sentry":          "sentry",
	"ses":             "aws-ses",
	"slack":           "slack",
	"square":          "square",
	"stripe":          "stripe",
	"supabase":        "supabase",
	"twilio":          "twilio",
	"typesense":       "typesense",
	"vonage":          "vonage",
	"zendesk":         "zendesk",
}

// IntegrationDoc returns the embedded API reference doc for the
// given service name (canonical, lowercase). Returns empty string
// when no bundled doc exists.
func IntegrationDoc(name string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	base, ok := integrationAliases[key]
	if !ok {
		return ""
	}
	data, err := integrationsFS.ReadFile("builtin/integrations/" + base + ".md")
	if err != nil {
		return ""
	}
	return string(data)
}

// HasIntegrationDoc reports whether we ship a bundled reference doc
// for the service name.
func HasIntegrationDoc(name string) bool {
	_, ok := integrationAliases[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// IntegrationDocNames returns every canonical service name that has
// a bundled reference doc. Useful for diagnostics and tests.
func IntegrationDocNames() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(integrationAliases))
	for _, base := range integrationAliases {
		if seen[base] {
			continue
		}
		seen[base] = true
		out = append(out, base)
	}
	return out
}
