package lifecycle

import (
	"log"
	"strings"
)

// Region identifiers recognised by the Customer.io Track API. Anything
// else falls back to RegionUS with a warning logged at startup.
const (
	RegionUS = "us"
	RegionEU = "eu"
)

// regionURLs maps the recognised region tags to the Customer.io Track
// API base URL. The path /api/v1 is appended by the SDK on every
// request — the value here is the host root only, matching what
// customerio.NewTrackClient expects.
var regionURLs = map[string]string{
	RegionUS: "https://track.customer.io",
	RegionEU: "https://track-eu.customer.io",
}

// RegionURL returns the Customer.io Track API base URL for the given
// region tag. Empty / unknown inputs return the US URL — the SDK's
// own default — and emit a one-line warning so operators see the
// fallback in their logs without the call failing.
func RegionURL(region string) string {
	switch normalizeRegion(region) {
	case RegionEU:
		return regionURLs[RegionEU]
	default:
		return regionURLs[RegionUS]
	}
}

// normalizeRegion lowercases and trims the operator input, defaulting
// blanks to "us" and logging a one-line warning for anything that
// isn't on the recognised list. Mirrors specs §6.2.
func normalizeRegion(region string) string {
	cleaned := strings.ToLower(strings.TrimSpace(region))
	switch cleaned {
	case "", RegionUS:
		return RegionUS
	case RegionEU:
		return RegionEU
	default:
		log.Printf("lifecycle: unknown CUSTOMERIO_REGION=%q, falling back to %q", region, RegionUS)
		return RegionUS
	}
}
