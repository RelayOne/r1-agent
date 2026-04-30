package convergence

import (
	"fmt"
	"regexp"
	"strings"
)

// Promise is a spec promise and the diff evidence that does or does not satisfy it.
type Promise struct {
	Sentence  string
	Keywords  []string
	Missing   []string
	Evidence  string
	Satisfied bool
}

// PromiseChecker extracts delivery promises from spec text and reconciles them with a diff.
type PromiseChecker struct {
	Spec string
	Diff string
}

var promiseVerbPattern = regexp.MustCompile(`(?i)\b(ship|build|add|implement)\b`)

var promiseStopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "be": {}, "by": {}, "for": {}, "from": {}, "if": {},
	"in": {}, "into": {}, "of": {}, "on": {}, "or": {}, "the": {}, "then": {}, "this": {},
	"to": {}, "with": {}, "we": {}, "will": {}, "must": {}, "should": {}, "that": {},
}

// Check returns every extracted promise together with whether the diff satisfies it.
func (c PromiseChecker) Check() ([]Promise, error) {
	sentences := splitSpecSentences(c.Spec)
	addedLines := diffAddedLines(c.Diff)
	promises := make([]Promise, 0, len(sentences))

	for _, sentence := range sentences {
		promise, ok := extractPromise(sentence, addedLines)
		if !ok {
			continue
		}
		promises = append(promises, promise)
	}

	return promises, nil
}

func splitSpecSentences(spec string) []string {
	spec = strings.ReplaceAll(spec, "\n", " ")
	raw := strings.FieldsFunc(spec, func(r rune) bool {
		return r == '.' || r == '!' || r == '?'
	})
	out := make([]string, 0, len(raw))
	for _, sentence := range raw {
		sentence = strings.Join(strings.Fields(sentence), " ")
		if sentence != "" {
			out = append(out, sentence)
		}
	}
	return out
}

func diffAddedLines(diff string) []string {
	var lines []string
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++") || !strings.HasPrefix(line, "+") {
			continue
		}
		lines = append(lines, strings.TrimSpace(strings.TrimPrefix(line, "+")))
	}
	return lines
}

func extractPromise(sentence string, addedLines []string) (Promise, bool) {
	loc := promiseVerbPattern.FindStringIndex(sentence)
	if loc == nil {
		return Promise{}, false
	}

	keywords := extractPromiseKeywords(sentence[loc[1]:])
	if len(keywords) == 0 {
		return Promise{}, false
	}

	matched, evidence := matchedKeywords(keywords, addedLines)
	missing := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		if _, ok := matched[keyword]; !ok {
			missing = append(missing, keyword)
		}
	}

	required := 1
	if len(keywords) > 1 {
		required = 2
	}

	return Promise{
		Sentence:  sentence,
		Keywords:  keywords,
		Missing:   missing,
		Evidence:  evidence,
		Satisfied: len(matched) >= required,
	}, true
}

func extractPromiseKeywords(fragment string) []string {
	fragment = strings.ToLower(fragment)
	words := strings.FieldsFunc(fragment, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	seen := make(map[string]struct{})
	keywords := make([]string, 0, len(words))
	for _, word := range words {
		word = normalizePromiseToken(word)
		if word == "" {
			continue
		}
		if _, stop := promiseStopwords[word]; stop {
			continue
		}
		if _, seenAlready := seen[word]; seenAlready {
			continue
		}
		seen[word] = struct{}{}
		keywords = append(keywords, word)
	}
	return keywords
}

func normalizePromiseToken(token string) string {
	token = strings.TrimSpace(strings.ToLower(token))
	token = strings.Trim(token, "-_")
	if len(token) < 3 {
		return ""
	}
	switch {
	case strings.HasSuffix(token, "ies") && len(token) > 4:
		token = strings.TrimSuffix(token, "ies") + "y"
	case strings.HasSuffix(token, "es") && len(token) > 4:
		token = strings.TrimSuffix(token, "es")
	case strings.HasSuffix(token, "s") && len(token) > 3:
		token = strings.TrimSuffix(token, "s")
	}
	return token
}

func matchedKeywords(keywords []string, addedLines []string) (map[string]struct{}, string) {
	matched := make(map[string]struct{})
	var evidence string
	for _, line := range addedLines {
		normalized := normalizeLine(line)
		for _, keyword := range keywords {
			if strings.Contains(normalized, keyword) {
				matched[keyword] = struct{}{}
				if evidence == "" {
					evidence = line
				}
			}
		}
	}
	return matched, evidence
}

func normalizeLine(line string) string {
	parts := strings.FieldsFunc(strings.ToLower(line), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	for i := range parts {
		parts[i] = normalizePromiseToken(parts[i])
	}
	return strings.Join(parts, " ")
}

var _ = fmt.Sprintf
