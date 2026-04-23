// Package plan — externaldocs.go
//
// External-service detection and documentation requirement. Stoke's
// shippability contract refuses to build code against an external
// service (Guesty, Stripe, Mews, etc.) without real documentation
// — the harness does not synthesize mocks. This file is the part
// of the feasibility gate that enforces that contract.
//
// Responsibilities:
//
//  1. Detect which external services the SOW references. Uses a
//     curated alias table for common names (keeps precision high)
//     plus a generic "integrates with <X>" pattern for unknown
//     services (lower precision but catches the long tail).
//
//  2. Check whether the SOW itself supplies sufficient documentation
//     for each referenced service. A sufficient doc carries enough
//     to build a real integration: endpoint URLs, request/response
//     shape, auth mechanism. A one-liner "integrates with Stripe"
//     does not count.
//
//  3. When the SOW is insufficient, consult a websearch.Searcher.
//     Any non-empty result set (title + excerpt or body) is treated
//     as attempted doc retrieval; the caller can accept or reject
//     based on policy. When no searcher is configured, the gap
//     stays open and the gate refuses the run.

package plan

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/ericmacdougall/stoke/internal/skill"
	"github.com/ericmacdougall/stoke/internal/websearch"
)

// ExternalService is one third-party service the SOW references.
type ExternalService struct {
	// Name is the service's canonical short name (e.g. "guesty",
	// "stripe", "mews"). Lowercase.
	Name string
	// Aliases are the strings the detector matched against. The
	// canonical name is included.
	Aliases []string
	// MentionedInTaskIDs lists the task IDs that reference this
	// service. Empty when only the session-level text mentions it
	// but no task explicitly depends on it.
	MentionedInTaskIDs []string
}

// ExternalServiceDocs is the documentation-coverage verdict for one
// service. Produced by CheckExternalDocs.
type ExternalServiceDocs struct {
	Service ExternalService
	// SOWProvides is true when the SOW itself contains enough doc
	// content (endpoint URLs + schema hints + auth instructions).
	SOWProvides bool
	// SOWEvidence is the substring from the SOW that satisfied the
	// check, when SOWProvides is true.
	SOWEvidence string
	// WebResults are any documentation pages the Searcher returned
	// when the SOW was insufficient. Callers treat a non-empty set
	// as "docs are reachable; inject them into the task briefing."
	WebResults []websearch.Result
	// WebQuery is the query we sent to the searcher (for audit).
	WebQuery string
	// BundledDoc is the embedded API reference shipped inside stoke
	// for well-known services. When non-empty, the doc is authored +
	// verified by the stoke team and should be preferred over web
	// search results for briefing injection.
	BundledDoc string
	// Covered is true when BundledDoc OR SOWProvides OR
	// len(WebResults) > 0. When Covered is false after every check,
	// the gate refuses.
	Covered bool
}

// knownExternalServices maps canonical name → alternate aliases used
// in SOW prose. Keeps precision high for common cases so the gate
// doesn't false-fire on in-codebase tokens that happen to match a
// vendor name.
//
// Tier classification is orthogonal, enforced via wellKnownServices
// below: the detector sees every entry here the same way — as a
// "there's a service referenced" signal — but the documentation
// requirement is only applied to NICHE services. Well-known services
// have stable, widely-documented APIs that every modern code model
// has training-data coverage for; the harness does not demand
// operator-supplied docs to build against them.
var knownExternalServices = map[string][]string{
	// Niche / proprietary vendor APIs (documentation required):
	"guesty":         {"guesty"},
	"hostaway":       {"hostaway"},
	"mews":           {"mews"},
	"pointclickcare": {"pointclickcare", "point click care"},
	"yardi":          {"yardi"},
	"realpage":       {"realpage"},
	// Well-known ubiquitous services (model training covers these):
	"stripe":    {"stripe"},
	"sendgrid":  {"sendgrid"},
	"twilio":    {"twilio"},
	"slack":     {"slack webhook", "slack api", "slack bot"},
	"fcm":       {"fcm", "firebase cloud messaging"},
	"apns":      {"apns", "apple push notification"},
	"expo":      {"expo push notifications"},
	"okta":      {"okta"},
	"auth0":     {"auth0"},
	"openai":    {"openai api"},
	"anthropic": {"anthropic api"},
	"github":    {"github api", "github webhook"},
	"gitlab":    {"gitlab api"},
	"datadog":   {"datadog api"},
	"segment":   {"segment analytics"},
	"sentry":    {"sentry dsn", "sentry api"},
	"postmark":  {"postmark"},
	"mailgun":   {"mailgun"},
}

// wellKnownServices are services whose APIs are stable enough AND
// documented in enough training data that modern code models can
// build against them correctly without operator-provided docs. The
// feasibility gate treats references to these as Covered by default.
//
// Criteria for inclusion:
//   - Public docs portal indexed by every major LLM training set
//   - API shape stable for ≥2 years (webhooks, REST, SDK)
//   - Widely-used — at least hundreds of open-source repos integrate
//     against it so model training has seen many correct examples
//
// Services NOT in this set (Guesty, Hostaway, Mews, PointClickCare,
// Yardi, RealPage, etc.) are proprietary vendor platforms where
// model training is thin and operator-provided docs are needed to
// avoid hallucinated endpoints.
var wellKnownServices = map[string]bool{
	"stripe":    true,
	"sendgrid":  true,
	"twilio":    true,
	"slack":     true,
	"fcm":       true,
	"apns":      true,
	"expo":      true,
	"okta":      true,
	"auth0":     true,
	"openai":    true,
	"anthropic": true,
	"github":    true,
	"gitlab":    true,
	"datadog":   true,
	"segment":   true,
	"sentry":    true,
	"postmark":  true,
	"mailgun":   true,
}

// IsWellKnownService returns true when the service is covered by
// model training data strongly enough that the feasibility gate
// trusts the worker to build against it without additional docs.
func IsWellKnownService(name string) bool {
	return wellKnownServices[strings.ToLower(strings.TrimSpace(name))]
}

// genericIntegrationHint catches "integrates with X", "calls the X
// API", "X SDK", "X webhook" patterns for services not in the known
// list. Lower precision; treated as "worth a web search" rather
// than "definitely external."
var genericIntegrationHint = regexp.MustCompile(`(?i)\b(?:integrates?\s+with|calls?\s+the|connects?\s+to|uses?\s+the)\s+([A-Z][A-Za-z0-9]{2,}(?:\s+[A-Z][a-z]+)?)\s+(?:API|SDK|webhook|service|platform)`)

// DetectExternalServices returns every external service the SOW
// references, combining the curated alias table with the generic
// hint regex. Tasks that explicitly mention a service are recorded
// in MentionedInTaskIDs so the operator sees blast radius.
func DetectExternalServices(sow *SOW, rawSOW string) []ExternalService {
	if sow == nil && rawSOW == "" {
		return nil
	}
	found := map[string]*ExternalService{}

	addMatch := func(canon, alias string, taskID string) {
		if s, ok := found[canon]; ok {
			s.Aliases = appendUnique(s.Aliases, alias)
			if taskID != "" {
				s.MentionedInTaskIDs = appendUnique(s.MentionedInTaskIDs, taskID)
			}
			return
		}
		out := &ExternalService{Name: canon, Aliases: []string{alias}}
		if taskID != "" {
			out.MentionedInTaskIDs = []string{taskID}
		}
		found[canon] = out
	}

	scan := func(text, taskID string) {
		lower := strings.ToLower(text)
		for canon, aliases := range knownExternalServices {
			for _, a := range aliases {
				idx := strings.Index(lower, a)
				if idx < 0 {
					continue
				}
				if mentionIsPlaceholderContext(lower, idx, len(a)) {
					// Service appears in a "Coming Soon" / "placeholder" /
					// "v2" context — operator has signaled no integration
					// code is being written. Skip to avoid false-positive
					// refusals on SOWs that have future-work notes.
					break
				}
				addMatch(canon, a, taskID)
				break
			}
		}
		for _, m := range genericIntegrationHint.FindAllStringSubmatch(text, -1) {
			if len(m) < 2 {
				continue
			}
			canon := strings.ToLower(strings.TrimSpace(m[1]))
			if _, isKnown := knownExternalServices[canon]; isKnown {
				continue
			}
			// Also suppress generic-hint matches in placeholder contexts.
			idx := strings.Index(lower, strings.ToLower(m[0]))
			if idx >= 0 && mentionIsPlaceholderContext(lower, idx, len(m[0])) {
				continue
			}
			addMatch(canon, m[1], taskID)
		}
	}

	if rawSOW != "" {
		scan(rawSOW, "")
	}
	if sow != nil {
		for _, s := range sow.Sessions {
			scan(s.Title+"\n"+s.Description, "")
			for _, t := range s.Tasks {
				scan(t.Description, t.ID)
			}
		}
	}

	out := make([]ExternalService, 0, len(found))
	for _, s := range found {
		sort.Strings(s.MentionedInTaskIDs)
		sort.Strings(s.Aliases)
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// placeholderMarkers are substrings that indicate a service mention
// is a UI-only card, future-work note, or explicit non-integration
// reference. When one of these appears within a short window of the
// service mention, the detector suppresses the match so the gate
// doesn't refuse builds that explicitly scope OUT a service as
// placeholder-only.
var placeholderMarkers = []string{
	"coming soon", "placeholder", "ui only", "ui-only",
	"not in v1", "not implemented in v1", "deferred to v2",
	"future work", "future integration", "stub card",
	"not functional", "not yet implemented", "out of scope",
	"will be added", "v2 scope",
}

// mentionIsPlaceholderContext returns true when a placeholder marker
// appears in the SAME clause as the service mention — not just the
// same paragraph. Clause boundaries are period, newline, em-dash,
// semicolon. This matters because SOWs commonly list one real
// integration and several placeholders in the same paragraph, e.g.
// "Guesty is functional — wire it up. Hostaway, Mews are placeholder
// cards with 'Coming Soon' overlay." The real integration (Guesty)
// and the placeholder list (Hostaway, Mews) are in the same
// paragraph but different clauses; a per-paragraph window would
// suppress Guesty too. We look at the clause around each mention
// only.
func mentionIsPlaceholderContext(lower string, mentionStart, mentionLen int) bool {
	clauseStart := mentionStart
	for clauseStart > 0 {
		c := lower[clauseStart-1]
		if c == '.' || c == '\n' || c == ';' {
			break
		}
		// Em-dash (—) is three UTF-8 bytes: 0xE2 0x80 0x94. Check the
		// last byte since we're scanning backward byte-by-byte.
		if clauseStart >= 3 &&
			lower[clauseStart-3] == 0xE2 && lower[clauseStart-2] == 0x80 && lower[clauseStart-1] == 0x94 {
			break
		}
		clauseStart--
	}
	clauseEnd := mentionStart + mentionLen
	for clauseEnd < len(lower) {
		c := lower[clauseEnd]
		if c == '.' || c == '\n' || c == ';' {
			break
		}
		if clauseEnd+2 < len(lower) &&
			lower[clauseEnd] == 0xE2 && lower[clauseEnd+1] == 0x80 && lower[clauseEnd+2] == 0x94 {
			break
		}
		clauseEnd++
	}
	slice := lower[clauseStart:clauseEnd]
	for _, m := range placeholderMarkers {
		if strings.Contains(slice, m) {
			return true
		}
	}
	return false
}

// sowProvidesDocumentation is the deterministic first-pass check.
// It looks for three signals that the SOW ships usable API docs for
// the service:
//
//  1. A vendor-domain URL (e.g. "docs.guesty.com", "api.stripe.com")
//  2. At least one endpoint-shaped line (HTTP verb + path)
//  3. Some schema hint (field names in backticks or fence blocks)
//
// Returns (true, evidence-string) when at least two of the three
// signals are present for the service name. Not perfect — an LLM
// judge catches the ambiguous cases in feasibility.go — but
// deterministic enough to skip the LLM call when the SOW clearly
// does or doesn't carry docs.
func sowProvidesDocumentation(rawSOW, serviceName string) (bool, string) {
	if rawSOW == "" || serviceName == "" {
		return false, ""
	}
	lower := strings.ToLower(rawSOW)
	snameLower := strings.ToLower(serviceName)
	if !strings.Contains(lower, snameLower) {
		return false, ""
	}

	signals := 0
	evidence := []string{}

	// Signal 1: vendor-domain URL
	urlRE := regexp.MustCompile(`(?i)https?://[a-z0-9.-]*` + regexp.QuoteMeta(snameLower) + `[a-z0-9./-]*`)
	if m := urlRE.FindString(rawSOW); m != "" {
		signals++
		evidence = append(evidence, "URL: "+m)
	}

	// Signal 2: endpoint-shaped line
	endpointRE := regexp.MustCompile(`(?i)(?:GET|POST|PUT|PATCH|DELETE)\s+/[A-Za-z0-9_/{}?-]+`)
	if m := endpointRE.FindString(rawSOW); m != "" {
		signals++
		evidence = append(evidence, "endpoint: "+m)
	}

	// Signal 3: schema hint — a code fence or tick-quoted field list
	// near the service name
	if i := strings.Index(lower, snameLower); i >= 0 {
		start := i - 500
		if start < 0 {
			start = 0
		}
		end := i + 1500
		if end > len(rawSOW) {
			end = len(rawSOW)
		}
		window := rawSOW[start:end]
		if strings.Contains(window, "```") || strings.Count(window, "`") >= 4 {
			signals++
			evidence = append(evidence, "schema fence near mention")
		}
	}

	// Require at least 2 of 3 signals — ensures the SOW genuinely
	// carries doc content, not just a passing mention.
	if signals < 2 {
		return false, ""
	}
	return true, strings.Join(evidence, " | ")
}

// CheckExternalDocs runs the full documentation-coverage check:
// deterministic SOW scan first, web search when the SOW is
// insufficient. Returns one ExternalServiceDocs per service.
//
// When searcher is nil, web-search path is skipped and any
// service lacking SOW documentation has Covered=false; the gate
// must refuse.
//
// When searcher is non-nil, each uncovered service gets one
// search query of the form "<service> API documentation
// endpoints authentication". The caller can inspect WebResults
// and fold them into task briefings.
func CheckExternalDocs(ctx context.Context, services []ExternalService, rawSOW string, searcher websearch.Searcher) []ExternalServiceDocs {
	out := make([]ExternalServiceDocs, 0, len(services))
	for _, svc := range services {
		r := ExternalServiceDocs{Service: svc}
		// Tier-1: well-known services (Stripe, FCM, SendGrid, etc.).
		// Model training has broad, stable coverage; no operator docs
		// required. The SOW can still supply custom specifics
		// (different webhook signing key shape, non-default endpoint)
		// and those would be picked up by the sowProvidesDocumentation
		// check below — so a well-known service with SOW-provided
		// overrides STILL records SOWProvides=true for audit trail.
		if IsWellKnownService(svc.Name) {
			r.Covered = true
			if doc := skill.IntegrationDoc(svc.Name); doc != "" {
				r.BundledDoc = doc
			}
			if ok, ev := sowProvidesDocumentation(rawSOW, svc.Name); ok {
				r.SOWProvides = true
				r.SOWEvidence = ev
			} else if r.BundledDoc != "" {
				r.SOWEvidence = "bundled reference doc ships with stoke"
			} else {
				r.SOWEvidence = "well-known service; trusted to model's training-data coverage"
			}
			out = append(out, r)
			continue
		}
		// Tier-2 fallback: check for a bundled doc even on niche
		// services — when one exists, it counts as coverage.
		if doc := skill.IntegrationDoc(svc.Name); doc != "" {
			r.BundledDoc = doc
			r.Covered = true
			r.SOWEvidence = "bundled reference doc ships with stoke"
			out = append(out, r)
			continue
		}
		// Tier-2: niche / proprietary vendor. Require real docs.
		if ok, ev := sowProvidesDocumentation(rawSOW, svc.Name); ok {
			r.SOWProvides = true
			r.SOWEvidence = ev
			r.Covered = true
			out = append(out, r)
			continue
		}
		if searcher == nil {
			out = append(out, r)
			continue
		}
		query := fmt.Sprintf("%s API documentation endpoints authentication", svc.Name)
		r.WebQuery = query
		if results, err := searcher.Search(ctx, query, 5); err == nil && len(results) > 0 {
			r.WebResults = results
			r.Covered = true
		}
		out = append(out, r)
	}
	return out
}

// RefusalReason builds the operator-facing message for uncovered
// services. One line per uncovered service with actionable
// suggestions: paste docs, set TAVILY_API_KEY, pass --docs-dir.
func RefusalReason(uncovered []ExternalServiceDocs) string {
	if len(uncovered) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "SOW references %d external service(s) without usable documentation:\n", len(uncovered))
	for _, u := range uncovered {
		fmt.Fprintf(&b, "  - %s", u.Service.Name)
		if len(u.Service.MentionedInTaskIDs) > 0 {
			fmt.Fprintf(&b, " (tasks: %s)", strings.Join(u.Service.MentionedInTaskIDs, ", "))
		}
		b.WriteString("\n")
		if u.WebQuery != "" {
			fmt.Fprintf(&b, "      web-search query tried: %q — no results or searcher unavailable\n", u.WebQuery)
		}
	}
	b.WriteString("\nTo proceed, either:\n")
	b.WriteString("  1. Paste the API reference for each service into the SOW (endpoints, schemas, auth).\n")
	b.WriteString("  2. Set TAVILY_API_KEY or WEBSEARCH_COMMAND so stoke can fetch docs itself.\n")
	b.WriteString("  3. Supply docs on disk via --docs-dir <path>.\n")
	b.WriteString("  4. Pass --force to proceed without docs (stoke will NOT synthesize mocks).\n")
	return b.String()
}

func appendUnique(s []string, v string) []string {
	for _, e := range s {
		if e == v {
			return s
		}
	}
	return append(s, v)
}
