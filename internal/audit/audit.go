package audit

import (
	"fmt"
	"strings"

	"stoke/internal/scan"
)

// Persona represents an expert perspective for code review.
type Persona struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Focus       string `json:"focus"`
	Prompt      string `json:"-"` // generated review prompt
}

// ReviewRequest is what gets sent to an AI model for review.
type ReviewRequest struct {
	Persona     Persona  `json:"persona"`
	Files       []string `json:"files"`
	DiffSummary string   `json:"diff_summary"`
	ScanResult  *scan.ScanResult  `json:"scan_result,omitempty"`
	SecurityMap *scan.SecurityMap `json:"security_map,omitempty"`
}

// ReviewFinding is one issue from a persona review.
type ReviewFinding struct {
	PersonaID string `json:"persona_id"`
	Severity  string `json:"severity"` // critical, high, medium, low
	File      string `json:"file,omitempty"`
	Line      int    `json:"line,omitempty"`
	Issue     string `json:"issue"`
	Suggestion string `json:"suggestion,omitempty"`
}

// AuditReport collects findings from all persona reviews.
type AuditReport struct {
	Personas []Persona       `json:"personas"`
	Findings []ReviewFinding `json:"findings"`
}

// DefaultPersonas returns the 17 built-in review personas.
// 5 core + 12 specialized, matching the enforcer's full persona set.
func DefaultPersonas() []Persona {
	return []Persona{
		// --- Core 5 (always available) ---
		{ID: "security", Name: "Security Reviewer",
			Focus: "Authentication, authorization, injection, secrets, cryptography, input validation, CSRF, XSS, SSRF"},
		{ID: "performance", Name: "Performance Reviewer",
			Focus: "N+1 queries, unbounded allocations, missing pagination, blocking I/O, cache misses, hot paths, memory leaks"},
		{ID: "reliability", Name: "Reliability Reviewer",
			Focus: "Error handling, edge cases, race conditions, resource leaks, graceful degradation, idempotency, retry logic"},
		{ID: "maintainability", Name: "Maintainability Reviewer",
			Focus: "Code clarity, naming, abstraction levels, test coverage, documentation, coupling, API design, DRY violations"},
		{ID: "ops", Name: "Operations Reviewer",
			Focus: "Observability, logging, metrics, health checks, deployment safety, rollback, configuration, feature flags"},

		// --- Specialized 12 (selected by context) ---
		{ID: "api-design", Name: "API Design Reviewer",
			Focus: "REST conventions, versioning, error responses, pagination, rate limiting, backward compatibility, OpenAPI compliance"},
		{ID: "data-integrity", Name: "Data Integrity Reviewer",
			Focus: "Schema migrations, data validation, transaction boundaries, constraint enforcement, backup/restore, eventual consistency"},
		{ID: "concurrency", Name: "Concurrency Reviewer",
			Focus: "Race conditions, deadlocks, mutex usage, channel patterns, goroutine leaks, atomic operations, lock ordering"},
		{ID: "testing", Name: "Testing Reviewer",
			Focus: "Test coverage gaps, assertion quality, test isolation, flaky test patterns, missing edge cases, test data management"},
		{ID: "dependency", Name: "Dependency Reviewer",
			Focus: "Version pinning, known vulnerabilities, license compliance, transitive dependencies, minimal dependency surface"},
		{ID: "accessibility", Name: "Accessibility Reviewer",
			Focus: "WCAG compliance, ARIA attributes, keyboard navigation, screen reader support, color contrast, focus management"},
		{ID: "i18n", Name: "Internationalization Reviewer",
			Focus: "Hardcoded strings, locale handling, date/number formatting, RTL support, character encoding, translation readiness"},
		{ID: "privacy", Name: "Privacy Reviewer",
			Focus: "PII handling, data retention, consent management, GDPR/CCPA compliance, logging of sensitive data, anonymization"},
		{ID: "cost", Name: "Cost Reviewer",
			Focus: "Cloud resource provisioning, query costs, storage growth, API call volume, caching opportunities, compute right-sizing"},
		{ID: "dx", Name: "Developer Experience Reviewer",
			Focus: "Onboarding friction, README accuracy, error messages, CLI UX, debug-ability, local development setup"},
		{ID: "migration", Name: "Migration Reviewer",
			Focus: "Breaking changes, rollback safety, data migration scripts, feature flag gating, blue-green compatibility, version skew"},
		{ID: "compliance", Name: "Compliance Reviewer",
			Focus: "Audit logging, access controls, data classification, regulatory requirements, change management, approval workflows"},
	}
}

// BuildPrompt generates the review prompt for a persona given context.
func BuildPrompt(p Persona, req ReviewRequest) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("You are a %s performing a focused code review.\n\n", p.Name))
	sb.WriteString(fmt.Sprintf("YOUR FOCUS AREAS: %s\n\n", p.Focus))
	sb.WriteString("REVIEW THESE CHANGES:\n")

	if req.DiffSummary != "" {
		sb.WriteString("```\n" + req.DiffSummary + "\n```\n\n")
	}

	if len(req.Files) > 0 {
		sb.WriteString("FILES MODIFIED: " + strings.Join(req.Files, ", ") + "\n\n")
	}

	// Include scan findings relevant to this persona
	if req.ScanResult != nil && len(req.ScanResult.Findings) > 0 {
		relevant := filterFindings(req.ScanResult.Findings, p.ID)
		if len(relevant) > 0 {
			sb.WriteString("AUTOMATED SCAN FOUND THESE ISSUES:\n")
			for _, f := range relevant {
				sb.WriteString(fmt.Sprintf("  [%s] %s:%d -- %s\n", f.Severity, f.File, f.Line, f.Message))
			}
			sb.WriteString("\n")
		}
	}

	// Include security surface for security persona
	if p.ID == "security" && req.SecurityMap != nil && len(req.SecurityMap.Surfaces) > 0 {
		sb.WriteString("SECURITY SURFACE MAP:\n")
		for _, s := range req.SecurityMap.Surfaces {
			if s.Risk == "high" {
				sb.WriteString(fmt.Sprintf("  [%s] %s:%d -- %s (%s)\n", s.Category, s.File, s.Line, s.Note, s.Risk))
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString(`OUTPUT FORMAT: Return ONLY a JSON array of findings:
[
  {"severity": "critical|high|medium|low", "file": "path", "line": 0, "issue": "description", "suggestion": "how to fix"}
]

If no issues found, return: []

RULES:
- Only flag concrete, actionable issues
- No style nitpicks unless they cause bugs
- Be specific: file, line, what's wrong, how to fix
- Severity guide: critical=security/data loss, high=correctness, medium=reliability, low=improvement
`)
	return sb.String()
}

// filterFindings returns scan findings relevant to a persona.
func filterFindings(findings []scan.Finding, personaID string) []scan.Finding {
	var out []scan.Finding
	for _, f := range findings {
		switch personaID {
		case "security", "privacy":
			if f.Rule == "no-hardcoded-secret" || f.Rule == "no-eval" || f.Rule == "no-innerhtml" || f.Rule == "no-exec" {
				out = append(out, f)
			}
		case "reliability", "concurrency":
			if f.Severity == "critical" || f.Severity == "high" {
				out = append(out, f)
			}
		case "maintainability", "dx":
			if f.Rule == "no-todo-fixme" || f.Rule == "no-console-log" || f.Rule == "no-fmt-println" {
				out = append(out, f)
			}
		case "testing":
			if f.Rule == "no-test-only" || f.Rule == "no-test-skip" {
				out = append(out, f)
			}
		case "performance", "cost":
			if f.Severity == "high" || f.Severity == "medium" {
				out = append(out, f)
			}
		}
	}
	return out
}

// SelectPersonas picks the most relevant personas for a set of changes.
// Always includes the core 5. Adds specialized personas based on context.
func SelectPersonas(allPersonas []Persona, securityMap *scan.SecurityMap, scanResult *scan.ScanResult) []Persona {
	if securityMap == nil && scanResult == nil {
		// No context -- return core 5 only
		var core []Persona
		coreIDs := map[string]bool{"security": true, "performance": true, "reliability": true, "maintainability": true, "ops": true}
		for _, p := range allPersonas {
			if coreIDs[p.ID] { core = append(core, p) }
		}
		return core
	}

	selected := map[string]bool{
		"maintainability": true, // always
		"reliability":     true, // always
	}

	if securityMap != nil {
		for _, s := range securityMap.Surfaces {
			switch s.Category {
			case "auth", "crypto", "injection", "secrets":
				selected["security"] = true
				selected["privacy"] = true
			case "network":
				selected["performance"] = true
				selected["api-design"] = true
			case "file":
				selected["data-integrity"] = true
			}
		}
	}

	if scanResult != nil {
		if scanResult.HasBlocking() {
			selected["security"] = true
		}
		for _, f := range scanResult.Findings {
			if f.Rule == "no-todo-fixme" { selected["dx"] = true }
			if f.Rule == "no-test-only" { selected["testing"] = true }
		}
	}

	var out []Persona
	for _, p := range allPersonas {
		if selected[p.ID] { out = append(out, p) }
	}
	if len(out) < 3 {
		// Minimum useful set
		coreIDs := map[string]bool{"security": true, "performance": true, "reliability": true, "maintainability": true, "ops": true}
		for _, p := range allPersonas {
			if coreIDs[p.ID] && !selected[p.ID] { out = append(out, p) }
		}
	}
	return out
}
