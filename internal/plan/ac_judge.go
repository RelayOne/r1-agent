// Package plan — semantic AC judge.
//
// The grep-based content_match / command-based acceptance checks tell us
// "the bytes match a pattern" or "the command exited 0". They cannot tell
// us "the code actually implements the feature the SOW asked for". That
// gap is what this file fills: when a mechanical check fails, the judge
// consults the LLM with the task spec, the AC, and the relevant code,
// and returns a semantic verdict: "implemented but pattern-differed" or
// "genuinely missing".
//
// The judge is the LAST line of defense, not the first. Mechanical
// checks still run and pass the cheap case. Only when they fail does
// the judge get invoked.

package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/perflog"
	"github.com/ericmacdougall/stoke/internal/provider"
)

// SemanticVerdict is the LLM judge's ruling on a failed mechanical check.
type SemanticVerdict struct {
	// ImplementsRequirement is true when the judge finds the code
	// actually implements what the AC was trying to measure, even
	// though the mechanical check failed (e.g. grep pattern mismatch).
	ImplementsRequirement bool `json:"implements_requirement"`

	// Reasoning is the judge's natural-language explanation. Always
	// populated for audit trail.
	Reasoning string `json:"reasoning"`

	// Evidence is specific file references and identifiers the judge
	// used to make the decision. Lets the operator verify the ruling.
	Evidence []string `json:"evidence,omitempty"`

	// SuggestedACRewrite, when ImplementsRequirement is false, may
	// contain a concrete rewritten AC that would correctly detect
	// the genuine gap in the code. When ImplementsRequirement is
	// true, may contain a better AC that would pass against this
	// code without losing the intent.
	SuggestedACRewrite string `json:"suggested_ac_rewrite,omitempty"`
}

// SemanticJudgeInput bundles everything the judge needs.
type SemanticJudgeInput struct {
	// TaskDescription is what the task was asked to build.
	TaskDescription string

	// SOWSpec is the relevant excerpt from the SOW covering this area.
	SOWSpec string

	// Criterion is the AC that failed its mechanical check.
	Criterion AcceptanceCriterion

	// FailureOutput is what the mechanical check said when it failed.
	FailureOutput string

	// CodeExcerpts maps file path -> content for files the judge
	// should read. Populated by the caller with files most relevant
	// to the criterion.
	CodeExcerpts map[string]string

	// RepoRoot is the workspace root (for logging only).
	RepoRoot string

	// UniversalPromptBlock carries the universal coding-standards +
	// known-gotchas block for injection into the semantic-judge
	// prompt. Empty is fine.
	UniversalPromptBlock string
}

// JudgeAC consults the LLM to decide whether a failed mechanical AC
// check represents a real implementation gap or just a pattern
// mismatch on correctly-implemented code.
//
// Returns nil verdict + nil error when no provider is configured —
// callers should treat that as "no semantic judgment available, treat
// mechanical result as authoritative".
func JudgeAC(ctx context.Context, prov provider.Provider, model string, in SemanticJudgeInput) (*SemanticVerdict, error) {
	if prov == nil {
		return nil, nil // no judge available
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	span := perflog.Start("llm.judge_ac", "ac="+in.Criterion.ID, "model="+model)
	defer func() { span.End() }()

	var body strings.Builder
	body.WriteString(semanticJudgePrompt)
	if strings.TrimSpace(in.UniversalPromptBlock) != "" {
		body.WriteString("\n\n")
		body.WriteString(in.UniversalPromptBlock)
	}
	body.WriteString("\n\n")

	body.WriteString("TASK THE WORKER WAS ASKED TO BUILD:\n")
	body.WriteString(in.TaskDescription)
	body.WriteString("\n\n")

	if strings.TrimSpace(in.SOWSpec) != "" {
		body.WriteString("SOW SPEC EXCERPT (authoritative requirements for this area):\n")
		body.WriteString(truncateForJudge(in.SOWSpec, 6000))
		body.WriteString("\n\n")
	}

	body.WriteString("ACCEPTANCE CRITERION THAT FAILED ITS MECHANICAL CHECK:\n")
	fmt.Fprintf(&body, "  id: %s\n  description: %s\n", in.Criterion.ID, in.Criterion.Description)
	if in.Criterion.Command != "" {
		fmt.Fprintf(&body, "  command: %s\n", in.Criterion.Command)
	}
	if in.Criterion.FileExists != "" {
		fmt.Fprintf(&body, "  file_exists: %s\n", in.Criterion.FileExists)
	}
	if in.Criterion.ContentMatch != nil && in.Criterion.ContentMatch.File != "" {
		fmt.Fprintf(&body, "  content_match: file=%q pattern=%q\n", in.Criterion.ContentMatch.File, in.Criterion.ContentMatch.Pattern)
	}
	body.WriteString("\n")

	body.WriteString("MECHANICAL CHECK OUTPUT (why it failed):\n")
	body.WriteString(truncateForJudge(in.FailureOutput, 4000))
	body.WriteString("\n\n")

	if len(in.CodeExcerpts) > 0 {
		body.WriteString("RELEVANT CODE (what the worker actually produced):\n")
		paths := sortedKeys(in.CodeExcerpts)
		for _, p := range paths {
			fmt.Fprintf(&body, "\n--- %s ---\n", p)
			body.WriteString(truncateForJudge(in.CodeExcerpts[p], 4000))
			body.WriteString("\n")
		}
		body.WriteString("\n")
	}

	body.WriteString("Output the JSON verdict described in the system prompt.")

	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": body.String()}})
	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 8000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, fmt.Errorf("semantic judge chat: %w", err)
	}
	raw, _ := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("semantic judge returned no content")
	}
	// Dump for post-mortem.
	_ = os.WriteFile("/tmp/stoke-semantic-judge-raw.txt", []byte(raw), 0o644)

	var verdict SemanticVerdict
	if _, err := jsonutil.ExtractJSONInto(raw, &verdict); err != nil {
		return nil, fmt.Errorf("parse semantic judge verdict: %w", err)
	}
	return &verdict, nil
}

const semanticJudgePrompt = `You are a senior code reviewer acting as a semantic acceptance judge. A mechanical acceptance check (grep pattern, file existence, shell command) has failed for a specific criterion. Your job is to decide whether the code actually implements what the criterion was trying to measure, regardless of whether the mechanical check pattern matched.

Think carefully:

  1. Read the task spec and the SOW excerpt. What did the worker need to build?
  2. Read the failing AC. What was the AC actually trying to measure? (The pattern is usually a proxy for some semantic property — "this file includes JWT handling", "this page renders a login form", etc.)
  3. Read the code. Does the code implement what the AC was trying to measure, even if the specific pattern doesn't match?
     - A grep for "createContext|useAuth" might fail because the worker used "React.createContext" in one place and exported a custom hook named "useAuthContext" — but the FEATURE is implemented correctly.
     - A grep for "jwt|JWT" might fail because the worker used the "jose" library with "jwtVerify" imported under an alias — but JWT verification IS happening.
     - Conversely, a file_exists check for "apps/web/app/login/page.tsx" can pass on a 20-line empty shell — the check passed but the feature is NOT implemented.
  4. If the code genuinely does NOT implement what the AC measured — the feature is actually missing or broken — return implements_requirement: false.
  5. If the code DOES implement it, just with different identifiers or structure than the AC's pattern, return implements_requirement: true and suggest a better AC that would pass against this code while preserving the intent.

Ground rules:

  - Be strict about "implements the requirement". A login page that renders nothing does NOT implement a login page. A file that exists but re-exports an empty module does NOT count.

  - Be lenient about pattern matching. Same feature + different identifier names / import paths / file organization = implements.

  - If the failure output suggests a build/compile/runtime error (not a pattern mismatch), that's a real failure. implements_requirement must be false.

  - If you can't tell from the code provided, return implements_requirement: false and request more context via the reasoning field.

Output ONLY a single JSON object in this schema — no prose, no backticks:

{
  "implements_requirement": true | false,
  "reasoning": "one paragraph explaining the verdict with specific file/line/identifier references",
  "evidence": ["file_path:function_or_identifier", ...],
  "suggested_ac_rewrite": "optional rewritten AC command if the current one is bad"
}

`

func truncateForJudge(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	half := (maxLen - 32) / 2
	if half < 100 {
		return s[:maxLen]
	}
	return s[:half] + "\n... (truncated in middle) ...\n" + s[len(s)-half:]
}

// CollectCodeExcerptsForAC picks files most likely relevant to a
// failing acceptance criterion: the file_exists path, the
// content_match file, and any path mentioned in the failure output.
func CollectCodeExcerptsForAC(repoRoot string, ac AcceptanceCriterion, failureOutput string, taskFiles []string, maxFiles, maxBytesPerFile int) map[string]string {
	if maxFiles <= 0 {
		maxFiles = 8
	}
	if maxBytesPerFile <= 0 {
		maxBytesPerFile = 6000
	}

	seen := map[string]bool{}
	var paths []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		paths = append(paths, p)
	}

	if ac.FileExists != "" {
		add(ac.FileExists)
	}
	if ac.ContentMatch != nil && ac.ContentMatch.File != "" {
		add(ac.ContentMatch.File)
	}
	for _, f := range taskFiles {
		add(f)
	}
	// Scan failure output for paths that look like source files.
	for _, word := range strings.Fields(failureOutput) {
		w := strings.Trim(word, "'\"`:,;()[]{}")
		if strings.HasSuffix(w, ".ts") || strings.HasSuffix(w, ".tsx") ||
			strings.HasSuffix(w, ".js") || strings.HasSuffix(w, ".jsx") ||
			strings.HasSuffix(w, ".go") || strings.HasSuffix(w, ".rs") ||
			strings.HasSuffix(w, ".py") {
			add(w)
		}
	}

	out := map[string]string{}
	for _, p := range paths {
		if len(out) >= maxFiles {
			break
		}
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(repoRoot, p)
		}
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		if len(data) > maxBytesPerFile {
			data = append([]byte{}, data[:maxBytesPerFile]...)
			data = append(data, []byte("\n... (truncated)\n")...)
		}
		out[p] = string(data)
	}
	return out
}
