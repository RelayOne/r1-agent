package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RelayOne/r1-agent/internal/jsonutil"
	"github.com/RelayOne/r1-agent/internal/provider"
)

// JudgeRelevance filters a list of candidate skill matches through the
// LLM to keep only the ones genuinely relevant to the session's task
// scope. TF-IDF ranking surfaces keyword-related skills, but keyword
// relatedness != task relatedness. A "Web Operator Dashboard"
// session matches "kubernetes-operations" via the word "operator" even
// though k8s has nothing to do with a Next.js dashboard.
//
// The judge reads the task descriptions + skill summaries and returns
// the filtered list. Errors fall back to returning the input unchanged
// so a flaky LLM doesn't break skill injection entirely.
func JudgeRelevance(ctx context.Context, prov provider.Provider, model string, sessionTitle string, taskDescriptions []string, candidates []SkillMatch) []SkillMatch {
	if prov == nil || len(candidates) == 0 {
		return candidates
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	var b strings.Builder
	b.WriteString(skillRelevanceJudgePrompt)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "SESSION: %s\n\n", sessionTitle)
	b.WriteString("TASKS IN THIS SESSION:\n")
	for i, td := range taskDescriptions {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, td)
	}
	b.WriteString("\n")
	b.WriteString("CANDIDATE SKILLS (picked by keyword matching — you decide which apply):\n")
	for _, c := range candidates {
		fmt.Fprintf(&b, "\n  📋 %s — %s\n", c.Skill.Name, c.Skill.Description)
	}
	b.WriteString("\nOutput the JSON verdict described in the system prompt.")

	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": b.String()}})
	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 4000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return candidates // fallback: no filter on error
	}
	raw := ""
	for _, c := range resp.Content {
		if c.Type == "text" {
			raw += c.Text
		}
	}
	if strings.TrimSpace(raw) == "" {
		return candidates
	}

	var verdict struct {
		Keep    []string `json:"keep"`
		Reasoning string `json:"reasoning"`
	}
	if _, err := jsonutil.ExtractJSONInto(raw, &verdict); err != nil {
		return candidates
	}

	keep := map[string]bool{}
	for _, name := range verdict.Keep {
		keep[strings.TrimSpace(name)] = true
	}
	var filtered []SkillMatch
	for _, c := range candidates {
		if keep[c.Skill.Name] {
			filtered = append(filtered, c)
		}
	}
	// If the judge returned an empty list or something went sideways,
	// fall back to the original list so we don't accidentally remove
	// all skills. Conservative bias toward having some skill context.
	if len(filtered) == 0 {
		return candidates
	}
	return filtered
}

const skillRelevanceJudgePrompt = `You are a tech lead filtering a list of candidate skills/playbooks for relevance to a specific session's work. Keyword matching surfaced these candidates, but keyword relatedness isn't the same as task relatedness. Your job: decide which skills genuinely apply to the tasks described below, and discard the rest.

A skill is GENUINELY RELEVANT when:
  - Its topic matches the actual work being done (a pnpm-monorepo-discipline skill is relevant to a monorepo setup task, not to a single-file React component task)
  - Its gotchas would prevent a real mistake on THIS session's work
  - It's in the language/framework/stack being used

A skill is NOT RELEVANT when:
  - The match was incidental keyword overlap (a "kubernetes-operations" skill matching "operator" in "Web Operator Dashboard")
  - The skill is for a different stack (a "react-native-*" skill in a Next.js web session)
  - The skill is for a concern outside this session's scope (a "payment-integration" skill in a session about user profile pages)

Be selective. Fewer relevant skills > many tangential ones. If NO candidates are relevant, return an empty keep list — the runner will fall back gracefully.

Output ONLY a single JSON object — no prose, no backticks:

{
  "keep": ["skill-name-1", "skill-name-2"],
  "reasoning": "one sentence about why these and not others"
}

`
