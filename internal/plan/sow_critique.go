package plan

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ericmacdougall/stoke/internal/provider"
)

// sowCritiquePrompt asks the model to grade a candidate SOW and return a
// list of structured issues. The critic pass runs AFTER prose conversion
// so we can catch problems (vague criteria, missing foundation, bad
// decomposition) before any code is written.
const sowCritiquePrompt = `You are a senior engineering reviewer grading a draft Statement of Work (SOW) before it's handed to an autonomous build agent. Your goal: find concrete issues that would cause the build to fail or produce unmaintainable code.

Score the SOW 0-100 on each dimension:
  - foundation: does the first session establish the repo layout, dependencies, and config?
  - decomposition: are sessions appropriately sized (3-12 total; each runnable in one focused work block)?
  - criteria: is EVERY session's acceptance_criteria verifiable mechanically (command / file_exists / content_match, not "manual review")?
  - stack: is the stack inferred correctly from the prose (language, framework, monorepo, infra)?
  - dependencies: are task dependencies declared properly (no cycles, no missing refs)?
  - specificity: are task descriptions single-sentence specific statements (not bullet lists or vague goals)?

Output ONLY a JSON object. No prose, no markdown fences.

{
  "overall_score": int 0-100,
  "dimensions": {
    "foundation": int,
    "decomposition": int,
    "criteria": int,
    "stack": int,
    "dependencies": int,
    "specificity": int
  },
  "issues": [
    {
      "severity": "blocking|major|minor",
      "session_id": "optional — which session",
      "task_id": "optional — which task",
      "description": "what's wrong",
      "fix": "specific suggestion"
    }
  ],
  "verdict": "ship|refine|reject",
  "summary": "one-paragraph explanation of your verdict"
}

VERDICT RULES:
  - "ship": overall score >= 80, no blocking issues
  - "refine": any blocking issue OR overall score < 80 OR any dimension < 60
  - "reject": the SOW is fundamentally broken (no sessions, all criteria manual, etc.)

DRAFT SOW:
`

// sowRefinePrompt asks the model to produce a fixed SOW given a set of
// critique issues. The result must be a complete replacement SOW, not a
// patch.
const sowRefinePrompt = `You are refining a Statement of Work (SOW) based on specific reviewer issues. Your job: produce a complete, improved replacement SOW that addresses EVERY issue below while preserving the parts of the original that were correct.

Output ONLY a JSON SOW in the same schema as the original — no prose, no markdown fences, no commentary.

RULES:
1. Preserve the original SOW's id, name, and any sessions the review didn't flag.
2. Rewrite or replace only what's flagged.
3. Every session MUST have at least one mechanically-verifiable acceptance_criterion (command / file_exists / content_match).
4. Task IDs stay unique across the whole SOW.
5. Do NOT introduce new sessions just for the sake of it — fix what's broken.

CRITIQUE ISSUES:
`

// SOWCritique is the structured output of a critic pass.
type SOWCritique struct {
	OverallScore int            `json:"overall_score"`
	Dimensions   map[string]int `json:"dimensions"`
	Issues       []CritiqueIssue `json:"issues"`
	Verdict      string         `json:"verdict"` // ship | refine | reject
	Summary      string         `json:"summary"`
}

// CritiqueIssue is a single concern the critic flagged.
type CritiqueIssue struct {
	Severity    string `json:"severity"` // blocking | major | minor
	SessionID   string `json:"session_id,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	Description string `json:"description"`
	Fix         string `json:"fix"`
}

// HasBlocking reports whether the critique includes any blocking issues.
func (c *SOWCritique) HasBlocking() bool {
	for _, i := range c.Issues {
		if i.Severity == "blocking" {
			return true
		}
	}
	return false
}

// CritiqueSOW runs the critic pass against a candidate SOW. Returns the
// structured critique or an error if the provider call fails / the
// response can't be parsed.
func CritiqueSOW(sow *SOW, prov provider.Provider, model string) (*SOWCritique, error) {
	if sow == nil {
		return nil, fmt.Errorf("nil SOW")
	}
	if prov == nil {
		return nil, fmt.Errorf("no provider configured")
	}
	sowBlob, err := json.MarshalIndent(sow, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal sow: %w", err)
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	userText := sowCritiquePrompt + string(sowBlob)
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": userText}})

	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 8000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, fmt.Errorf("critic chat: %w", err)
	}
	raw := ""
	for _, c := range resp.Content {
		if c.Type == "text" {
			raw += c.Text
		}
	}
	cleaned := stripMarkdownFences(raw)
	start := strings.Index(cleaned, "{")
	end := strings.LastIndex(cleaned, "}")
	if start < 0 || end < start {
		return nil, fmt.Errorf("critic response had no JSON (first 200 chars: %s)", truncateForError(raw, 200))
	}
	var crit SOWCritique
	if err := json.Unmarshal([]byte(cleaned[start:end+1]), &crit); err != nil {
		return nil, fmt.Errorf("parse critique: %w", err)
	}
	return &crit, nil
}

// RefineSOW rewrites a SOW to address the issues in a critique. Returns a
// new SOW that has been re-validated against the schema.
func RefineSOW(original *SOW, crit *SOWCritique, prov provider.Provider, model string) (*SOW, error) {
	if original == nil || crit == nil {
		return nil, fmt.Errorf("nil SOW or critique")
	}
	if prov == nil {
		return nil, fmt.Errorf("no provider configured")
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	origBlob, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal original: %w", err)
	}
	critBlob, err := json.MarshalIndent(crit, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal critique: %w", err)
	}

	userText := sowRefinePrompt + string(critBlob) + "\n\nORIGINAL SOW:\n" + string(origBlob)
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": userText}})

	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 16000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, fmt.Errorf("refine chat: %w", err)
	}

	raw := ""
	for _, c := range resp.Content {
		if c.Type == "text" {
			raw += c.Text
		}
	}
	cleaned := stripMarkdownFences(raw)
	first := strings.Index(cleaned, "{")
	last := strings.LastIndex(cleaned, "}")
	if first < 0 || last < first {
		return nil, fmt.Errorf("refine response had no JSON (first 200 chars: %s)", truncateForError(raw, 200))
	}
	refined, err := ParseSOW([]byte(cleaned[first:last+1]), "refined.json")
	if err != nil {
		return nil, fmt.Errorf("parse refined SOW: %w", err)
	}
	if errs := ValidateSOW(refined); len(errs) > 0 {
		return nil, fmt.Errorf("refined SOW failed validation: %s", strings.Join(errs, "; "))
	}
	return refined, nil
}

// CritiqueAndRefine runs up to maxPasses critique→refine cycles until the
// SOW's verdict is "ship" or the pass budget is exhausted. Returns the
// best SOW produced and the final critique. If the budget runs out with
// issues remaining, the last refined SOW is still returned (caller can
// choose to proceed or abort).
//
// This is the "smart" entry point called from sowCmd after prose
// conversion: turn a rough LLM-generated SOW into something the build
// agent can actually execute against.
func CritiqueAndRefine(sow *SOW, prov provider.Provider, model string, maxPasses int) (*SOW, *SOWCritique, error) {
	if maxPasses < 1 {
		maxPasses = 2
	}
	current := sow
	var lastCrit *SOWCritique
	for pass := 1; pass <= maxPasses; pass++ {
		crit, err := CritiqueSOW(current, prov, model)
		if err != nil {
			return current, lastCrit, fmt.Errorf("critique pass %d: %w", pass, err)
		}
		lastCrit = crit
		if crit.Verdict == "ship" && !crit.HasBlocking() {
			return current, crit, nil
		}
		if crit.Verdict == "reject" {
			return current, crit, fmt.Errorf("critic rejected SOW: %s", crit.Summary)
		}
		// Refine and loop.
		refined, err := RefineSOW(current, crit, prov, model)
		if err != nil {
			// Refinement failed but we have a critique — return the
			// current SOW and let the caller decide.
			return current, crit, fmt.Errorf("refine pass %d: %w", pass, err)
		}
		current = refined
	}
	return current, lastCrit, nil
}
