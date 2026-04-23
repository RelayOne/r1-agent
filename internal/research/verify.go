// verify.go implements the claim-verification primitive the
// ResearchExecutor hands to the descent engine via
// AcceptanceCriterion.VerifyFunc. The algorithm is deterministic and
// stdlib-only so the descent 8-tier ladder can run VerifyClaim
// without any LLM dependency.
//
// The MVP algorithm:
//  1. Fetch the claim's SourceURL. Fetch errors → unsupported with
//     reason "fetch error: <message>".
//  2. Strip HTML tags to get plain text (see stripHTML).
//  3. Tokenize both the claim and the page into lowercase word sets,
//     ignoring stopwords and tokens shorter than 3 characters.
//  4. Require keyword overlap |claim_tokens ∩ page_tokens| /
//     |claim_tokens| >= 0.5.
//  5. Require at least one 3-word consecutive phrase from the claim
//     to appear verbatim (case-insensitive) in the page text.
//
// Future: swap VerifyClaim for an LLM-entailment judge. The caller
// surface stays identical — descent only sees (bool, reason).

package research

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// VerifyClaim confirms that claim c is supported by the page at
// c.SourceURL according to the MVP heuristic. Returns supported=true
// iff both the keyword-overlap and phrase-presence gates pass.
// reason is always populated (success or failure) so descent has a
// human-readable note for its failure log.
func VerifyClaim(ctx context.Context, c Claim, f Fetcher) (supported bool, reason string) {
	if strings.TrimSpace(c.Text) == "" {
		return false, "empty claim text"
	}
	if strings.TrimSpace(c.SourceURL) == "" {
		return false, "claim has no source URL"
	}
	if f == nil {
		return false, "no fetcher configured"
	}

	body, err := f.Fetch(ctx, c.SourceURL)
	if err != nil {
		return false, fmt.Sprintf("fetch error: %v", err)
	}
	page := stripHTML(body)
	pageLower := strings.ToLower(page)

	claimTokens := tokenize(c.Text)
	if len(claimTokens) == 0 {
		return false, "claim has no content words"
	}
	pageTokens := tokenSet(page)

	matched := 0
	for _, t := range claimTokens {
		if pageTokens[t] {
			matched++
		}
	}
	overlap := float64(matched) / float64(len(claimTokens))

	// Phrase-presence gate: any 3-word consecutive span from the
	// (non-stopword-filtered) claim must appear in the page text.
	// Using the original-order tokens for the phrase check means we
	// actually detect "the quick brown fox" as a phrase, not just a
	// bag of overlapping words.
	claimPhrases := phrases(c.Text, 3)
	phraseHit := false
	for _, p := range claimPhrases {
		if strings.Contains(pageLower, p) {
			phraseHit = true
			break
		}
	}

	if overlap >= 0.5 && phraseHit {
		return true, fmt.Sprintf("supported: overlap=%.2f, phrase match found", overlap)
	}

	switch {
	case overlap < 0.5 && !phraseHit:
		return false, fmt.Sprintf("unsupported: overlap=%.2f (need 0.5) and no 3-word phrase match", overlap)
	case overlap < 0.5:
		return false, fmt.Sprintf("unsupported: overlap=%.2f (need 0.5)", overlap)
	default:
		return false, fmt.Sprintf("unsupported: overlap=%.2f passes but no 3-word phrase match", overlap)
	}
}

// tagRE strips HTML tags (<...>); scriptRE / styleRE drop script and
// style blocks before tags are cleaned so we don't surface JS / CSS
// tokens as page content.
var (
	scriptRE = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	styleRE  = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`)
	tagRE    = regexp.MustCompile(`<[^>]+>`)
	wsRE     = regexp.MustCompile(`\s+`)
)

// stripHTML returns a reasonable plain-text form of an HTML document.
// It removes <script> and <style> blocks, strips remaining tags, and
// collapses whitespace. Not a full readable-text extractor — just
// enough signal for the keyword / phrase heuristic.
func stripHTML(body string) string {
	if body == "" {
		return ""
	}
	s := scriptRE.ReplaceAllString(body, " ")
	s = styleRE.ReplaceAllString(s, " ")
	s = tagRE.ReplaceAllString(s, " ")
	// Common entity decoding — the signal-critical ones only.
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = wsRE.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// stopwords are dropped from the claim's token set before overlap is
// computed. A short, conservative list — we don't want to over-prune
// and make overlap trivially high.
var stopwords = map[string]bool{
	"the": true, "and": true, "but": true, "for": true, "nor": true,
	"yet": true, "with": true, "from": true, "that": true, "this": true,
	"these": true, "those": true, "have": true, "has": true, "had": true,
	"was": true, "were": true, "are": true, "been": true, "being": true,
	"its": true, "their": true, "his": true, "her": true, "not": true,
	"into": true, "onto": true, "upon": true, "about": true, "over": true,
	"under": true, "such": true, "than": true, "then": true, "also": true,
	"just": true, "only": true, "very": true, "much": true, "many": true,
}

// wordRE matches contiguous alphanumeric spans; apostrophes and
// hyphens are dropped so "state-of-the-art" tokenizes to ["state",
// "of", "the", "art"] — matching conventional English search
// tokenization.
var wordRE = regexp.MustCompile(`[A-Za-z0-9]+`)

// tokenize returns the lowercase content tokens from s, dropping
// stopwords and tokens shorter than 3 chars. Order is preserved.
// Duplicates are preserved — tokenize is used for phrase extraction
// where order matters. For set-style overlap use tokenSet.
func tokenize(s string) []string {
	matches := wordRE.FindAllString(strings.ToLower(s), -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) < 3 || stopwords[m] {
			continue
		}
		out = append(out, m)
	}
	return out
}

// tokenSet returns the distinct content tokens from s as a set. Uses
// tokenize for consistent filtering rules.
func tokenSet(s string) map[string]bool {
	set := make(map[string]bool)
	for _, t := range tokenize(s) {
		set[t] = true
	}
	return set
}

// StripHTMLPublic is the exported wrapper over stripHTML. It exists
// for cross-package helpers (e.g. the ResearchExecutor's sentence
// ranker) that need the same HTML-cleaning behavior VerifyClaim uses;
// keeping the wrapper separate lets the internal name stay concise.
func StripHTMLPublic(body string) string { return stripHTML(body) }

// TokenSetPublic is the exported wrapper over tokenSet for the same
// reason as StripHTMLPublic.
func TokenSetPublic(s string) map[string]bool { return tokenSet(s) }

// phrases returns all n-length consecutive word phrases from s,
// lowercased and space-joined. Uses the ORIGINAL word sequence (no
// stopword filtering) so "the quick brown fox" produces "the quick
// brown" / "quick brown fox" etc.
func phrases(s string, n int) []string {
	if n <= 0 {
		return nil
	}
	words := wordRE.FindAllString(strings.ToLower(s), -1)
	if len(words) < n {
		return nil
	}
	out := make([]string, 0, len(words)-n+1)
	for i := 0; i+n <= len(words); i++ {
		out = append(out, strings.Join(words[i:i+n], " "))
	}
	return out
}
