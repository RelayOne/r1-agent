// critique.go — one-shot critique verb.
//
// Lighter-weight structured review of a draft artifact. CloudSwarm
// posts {draft, criteria?, target_audience?} and receives
// {critique: {strengths, issues, recommended_revisions}}.
//
// Rationale: the R1 critique path on SOWs (plan.CritiqueSOW) is
// structural — it requires a typed SOW and a live provider. For a
// free-form draft the spec allows either (a) a primitive critique
// function (none exists for prose drafts) or (b) a direct LLM call.
// We land the deterministic path: a rule-based review over the draft
// that is fast, LLM-free, and produces the full strengths / issues /
// revisions triad. When a future LLM-backed prose critic lands, it
// plugs in behind the same Response shape.
//
// Checks the draft for:
//
//   - length thresholds (too short → issue, reasonable → strength)
//   - criterion coverage (each supplied criterion scanned for
//     keyword presence; missing → issue w/ revision suggestion)
//   - audience calibration hints (target_audience mentioned by name
//     or the draft addresses the audience directly → strength)
//   - structural signals (headings, paragraphs, examples)
package oneshot

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// critiqueRequest is the verb payload shape per spec §5.6.1.
type critiqueRequest struct {
	Draft          string   `json:"draft"`
	Criteria       []string `json:"criteria,omitempty"`
	TargetAudience string   `json:"target_audience,omitempty"`
}

// critiqueIssue is one issue entry in the response.
type critiqueIssue struct {
	Severity   string `json:"severity"` // info | warn | error
	Text       string `json:"text"`
	Suggestion string `json:"suggestion,omitempty"`
}

// critiqueBody is the structured body inside Response.Data.
type critiqueBody struct {
	Strengths            []string        `json:"strengths"`
	Issues               []critiqueIssue `json:"issues"`
	RecommendedRevisions []string        `json:"recommended_revisions"`
}

// critiqueResponse is the full {verb, status, critique} envelope.
type critiqueResponse struct {
	Verb     string       `json:"verb"`
	Status   string       `json:"status"`
	Critique critiqueBody `json:"critique"`
	Error    string       `json:"error,omitempty"`
}

// handleCritique is invoked by Dispatch when verb=="critique".
func handleCritique(payload json.RawMessage) (Response, error) {
	req := critiqueRequest{}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &req); err != nil {
			return critiqueScaffoldResponse(fmt.Sprintf("invalid request payload: %v", err)), nil
		}
	}
	// Legacy probe path: nil / empty / draft-less payload returns the
	// pre-wiring scaffold shape ({critique:"scaffold-response"}) so
	// CloudSwarm's long-standing probe parser keeps working. A real
	// draft unlocks the structured strengths/issues/revisions triad.
	if strings.TrimSpace(req.Draft) == "" {
		return critiqueScaffoldResponse(
			"scaffold — critique called without draft field"), nil
	}

	body := runCritique(req)
	resp := critiqueResponse{Verb: "critique", Status: "ok", Critique: body}
	data, err := json.Marshal(resp)
	if err != nil {
		return Response{}, fmt.Errorf("oneshot: marshal critique: %w", err)
	}
	return Response{
		Verb:            "critique",
		Status:          StatusOK,
		ProviderUsed:    "r1_core",
		CostEstimateUSD: 0,
		Data:            data,
	}, nil
}

// critiqueScaffoldResponse returns the legacy pre-wiring scaffold shape
// ({critique:"scaffold-response"}) so CloudSwarm's long-standing probe
// parser keeps working. Used when the caller posts a nil / empty / or
// draft-less payload, analogous to decomposeScaffoldResponse.
func critiqueScaffoldResponse(note string) Response {
	body := struct {
		Critique string `json:"critique"`
	}{
		Critique: "scaffold-response",
	}
	data, _ := json.Marshal(body)
	return Response{
		Verb:   "critique",
		Status: StatusScaffold,
		Data:   data,
		Note:   note,
	}
}

// runCritique is the deterministic core; unit-tested directly.
func runCritique(req critiqueRequest) critiqueBody {
	draft := req.Draft
	lower := strings.ToLower(draft)
	body := critiqueBody{
		Strengths:            []string{},
		Issues:               []critiqueIssue{},
		RecommendedRevisions: []string{},
	}

	// Length signals. Very short drafts rarely survive critique;
	// moderate / long drafts earn a strength.
	words := wordCount(draft)
	switch {
	case words < 20:
		body.Issues = append(body.Issues, critiqueIssue{
			Severity:   "error",
			Text:       fmt.Sprintf("draft is very short (%d words); unlikely to satisfy acceptance criteria", words),
			Suggestion: "expand to cover the core claims with 2-3 supporting sentences each",
		})
		body.RecommendedRevisions = append(body.RecommendedRevisions,
			"expand the draft substantially before another review pass")
	case words < 60:
		body.Issues = append(body.Issues, critiqueIssue{
			Severity:   "warn",
			Text:       fmt.Sprintf("draft is brief (%d words); may lack depth for the intended audience", words),
			Suggestion: "consider adding concrete examples or rationale",
		})
	default:
		body.Strengths = append(body.Strengths,
			fmt.Sprintf("draft has adequate length (%d words) for substantive review", words))
	}

	// Structural signals. A draft with headings/lists reads better
	// for most audiences; pure wall-of-text drafts earn a warn.
	if hasHeadings(draft) {
		body.Strengths = append(body.Strengths, "uses headings or section markers for structure")
	}
	if hasList(draft) {
		body.Strengths = append(body.Strengths, "uses lists to organize multiple items")
	}
	if words >= 80 && !hasHeadings(draft) && !hasList(draft) {
		body.Issues = append(body.Issues, critiqueIssue{
			Severity:   "info",
			Text:       "longer draft lacks headings or lists; readability suffers for scan-readers",
			Suggestion: "add 2-4 section headings and at least one bulleted list",
		})
		body.RecommendedRevisions = append(body.RecommendedRevisions,
			"restructure with headings and bullets so readers can scan")
	}

	// Criterion coverage. For each supplied criterion, check whether
	// any of its distinctive keywords (len>=4, not stopwords) appear
	// in the draft. Missing → error-severity issue with a pointed
	// revision suggestion.
	for _, crit := range req.Criteria {
		crit = strings.TrimSpace(crit)
		if crit == "" {
			continue
		}
		if criterionCovered(lower, crit) {
			body.Strengths = append(body.Strengths,
				"addresses criterion: "+truncate(crit, 80))
		} else {
			body.Issues = append(body.Issues, critiqueIssue{
				Severity:   "error",
				Text:       "criterion not evident in draft: " + truncate(crit, 120),
				Suggestion: "add a section or paragraph that explicitly addresses: " + truncate(crit, 80),
			})
			body.RecommendedRevisions = append(body.RecommendedRevisions,
				"ensure the draft directly addresses: "+truncate(crit, 80))
		}
	}

	// Audience calibration. If target_audience is set, look for it
	// (or first distinctive word of it) in the draft. Missing →
	// a warn rather than error since audience names aren't always
	// mentioned verbatim.
	if ta := strings.TrimSpace(req.TargetAudience); ta != "" {
		if audienceReferenced(lower, ta) {
			body.Strengths = append(body.Strengths,
				"draft names or speaks to the target audience ("+truncate(ta, 60)+")")
		} else {
			body.Issues = append(body.Issues, critiqueIssue{
				Severity:   "warn",
				Text:       "draft does not explicitly reference the target audience: " + truncate(ta, 80),
				Suggestion: "open with framing that orients the draft to the intended audience",
			})
			body.RecommendedRevisions = append(body.RecommendedRevisions,
				"calibrate tone and framing for: "+truncate(ta, 60))
		}
	}

	return body
}

// wordCount returns a cheap whitespace-split word count.
func wordCount(s string) int {
	return len(strings.Fields(s))
}

// hasHeadings detects Markdown-style headings or SHOUT-case single
// lines that look like section markers.
var headingRe = regexp.MustCompile(`(?m)^(#{1,6}\s+\S|[A-Z][A-Z0-9 ]{3,}:?$)`)

func hasHeadings(s string) bool { return headingRe.MatchString(s) }

var listRe = regexp.MustCompile(`(?m)^\s*([-*]|\d+[\.\)])\s+\S`)

func hasList(s string) bool { return listRe.MatchString(s) }

// criterionCovered checks whether the distinctive keywords of a
// criterion appear in the draft. Considered "covered" when at least
// half of the criterion's non-stopword tokens (len>=4) appear, or
// the full criterion phrase appears as a substring.
func criterionCovered(lowerDraft, criterion string) bool {
	c := strings.ToLower(criterion)
	if len(c) < 40 && strings.Contains(lowerDraft, c) {
		return true
	}
	toks := distinctiveTokens(c)
	if len(toks) == 0 {
		// Degenerate criterion (all stopwords / too short) — fall
		// back to substring match on the original.
		return strings.Contains(lowerDraft, c)
	}
	hits := 0
	for _, t := range toks {
		if strings.Contains(lowerDraft, t) {
			hits++
		}
	}
	// "half or more" bar keeps us honest on two-keyword criteria
	// without letting a three-token criterion sneak through on one
	// keyword alone.
	return hits*2 >= len(toks)
}

// audienceReferenced is a loose check — either the full audience
// string appears, or the first distinctive token does.
func audienceReferenced(lowerDraft, audience string) bool {
	a := strings.ToLower(audience)
	if strings.Contains(lowerDraft, a) {
		return true
	}
	toks := distinctiveTokens(a)
	for _, t := range toks {
		if strings.Contains(lowerDraft, t) {
			return true
		}
	}
	return false
}

// stopwords is a small English stopword set. Not exhaustive — just
// enough to keep "the / and / with / must / should" from dominating
// criterion-coverage decisions.
var stopwords = map[string]struct{}{
	"the": {}, "and": {}, "for": {}, "with": {}, "that": {},
	"this": {}, "have": {}, "must": {}, "should": {}, "will": {},
	"when": {}, "what": {}, "which": {}, "from": {}, "into": {},
	"been": {}, "were": {}, "they": {}, "them": {}, "your": {},
	"each": {}, "some": {}, "more": {}, "than": {}, "also": {},
	"their": {}, "there": {}, "about": {}, "after": {}, "before": {},
}

// distinctiveTokens returns tokens len>=4 that aren't stopwords,
// lowercased.
func distinctiveTokens(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !isWordRune(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) < 4 {
			continue
		}
		if _, ok := stopwords[f]; ok {
			continue
		}
		out = append(out, f)
	}
	return out
}

func isWordRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9')
}
