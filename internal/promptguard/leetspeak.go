package promptguard

import (
	"regexp"
	"unicode"
)

// leetMap is the classical digit-for-letter substitution table used in
// community jailbreak corpora. We only decode digits that unambiguously map
// to a single letter; 0 is excluded to avoid false positives on hex literals
// and version strings (e.g. 0xDEADBEEF, v2.0.1).
var leetMap = map[rune]rune{
	'1': 'i',
	'3': 'e',
	'4': 'a',
	'5': 's',
	'7': 't',
}

// injectionKeywordRe matches the injection keyword families that are relevant
// after leet-normalisation. This is applied to the normalised form, not the
// original — so the regex stays simple plain-text, as intended by the design.
var injectionKeywordRe = regexp.MustCompile(
	`(?i)(shift|share|reveal|bypass|include|override|ignore|disregard|forget|exfil|instruct|focus|output|print|return)`,
)

// leetSubstitutionRe matches any of our leet digits inside an alphabetic word.
// Used to check that a string actually contains leet substitutions (not just
// ordinary numbers).
var leetSubstitutionRe = regexp.MustCompile(`[13457]`)

// normalizeLeet returns s with the digit-for-letter substitutions reversed.
// Non-leet characters are passed through unchanged.
func normalizeLeet(s string) string {
	runes := []rune(s)
	out := make([]rune, len(runes))
	for i, r := range runes {
		if letter, ok := leetMap[r]; ok {
			out[i] = letter
		} else {
			out[i] = r
		}
	}
	return string(out)
}

// significantLeetDensity returns true when s contains at least minSubs leet
// digit substitutions occurring inside mixed letter+digit tokens of length ≥
// minWordLen. Pure-digit tokens (e.g. "15437", "0xDEAD") are excluded because
// they appear naturally in source code and version strings.
func significantLeetDensity(s string, minSubs, minWordLen int) bool {
	subsFound := 0
	inWord := false
	wordStart := 0
	wordHasLeet := 0
	wordHasLetter := false // requires at least one non-digit letter in the token

	for i, r := range s {
		isWordChar := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
		if isWordChar {
			if !inWord {
				inWord = true
				wordStart = i
				wordHasLeet = 0
				wordHasLetter = false
			}
			if unicode.IsLetter(r) {
				wordHasLetter = true
			}
			if _, ok := leetMap[r]; ok {
				wordHasLeet++
			}
		} else {
			if inWord {
				wordLen := i - wordStart
				if wordLen >= minWordLen && wordHasLeet > 0 && wordHasLetter {
					subsFound += wordHasLeet
				}
				inWord = false
				wordHasLeet = 0
				wordStart = 0
				wordHasLetter = false
			}
		}
	}
	if inWord {
		// trailing word — use rune count for length estimate (close enough for ASCII)
		wordLen := len(s) - wordStart
		if wordLen >= minWordLen && wordHasLeet > 0 && wordHasLetter {
			subsFound += wordHasLeet
		}
	}
	return subsFound >= minSubs
}

// scanLeetspeak checks s for leet-encoded injection phrases and returns any
// threats found. The offset in each Threat is mapped back to the original
// string because normaliseLeet performs a 1:1 rune substitution (no length
// change), so offsets are byte-identical between the original and normalised
// forms.
func scanLeetspeak(s string) []Threat {
	// Quick skip: if there are no leet substitution digits in the string at
	// all, return immediately without allocating the normalised copy.
	if !leetSubstitutionRe.MatchString(s) {
		return nil
	}
	if !significantLeetDensity(s, 3, 4) {
		return nil
	}
	norm := normalizeLeet(s)
	var out []Threat
	for _, loc := range injectionKeywordRe.FindAllStringIndex(norm, -1) {
		out = append(out, Threat{
			PatternName: "leetspeak-instruction-rewrite",
			Start:       loc[0],
			End:         loc[1],
			Excerpt:     excerpt(s, loc[0], loc[1]),
		})
	}
	return out
}
