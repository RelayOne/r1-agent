// Convergence override judge: VP Eng proposes ignore entries for findings
// that are flagging valid code as broken, CTO reviews and signs off.
//
// The flow:
//
//	1. Regex/semantic rules flag findings. Build + tests still pass.
//	2. Supervisor's RepeatTracker counts the same flag appearing N times.
//	3. Once a signature crosses threshold, the supervisor asks the
//	   OverrideJudge to review it.
//	4. VP Eng stance reads findings + file excerpts + build/test outcomes
//	   + the SOW's acceptance criteria, and returns a proposal listing
//	   which findings to ignore (with reasons) and any continuation items
//	   it thinks are actually missing.
//	5. CTO stance reads the VP Eng proposal and either approves, denies,
//	   or amends it. Only CTO-approved items land in the ignore list.
//	6. Convergence re-runs and filters findings against the new ignores.
//
// This matches the user's instruction: "let vp eng propose the overrides to
// cto to sign off on" with "supervisor counting convergence blocked
// attempts/repeat flags" as the trigger.
package convergence

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/provider"
)

// JudgeContext is what the override judge needs to make a decision.
type JudgeContext struct {
	// MissionID is used for audit trail / logging.
	MissionID string
	// Findings is the subset of a convergence report the supervisor wants
	// overrides considered for. Typically these are findings that have
	// blocked convergence for >= threshold iterations.
	Findings []Finding
	// FileSnippets maps file path → relevant excerpt (e.g. ±5 lines around
	// each flagged line). The judge needs this to assess whether the
	// regex match is a real issue or a false positive.
	FileSnippets map[string]string
	// SOWCriteria are the acceptance criteria the mission is trying to
	// satisfy. The judge cross-references findings against these so it
	// can distinguish "regex noise" from "the criterion genuinely isn't
	// met yet".
	SOWCriteria []string
	// BuildPassed is true if the build step exited 0 on the last run.
	BuildPassed bool
	// TestsPassed is true if the test step exited 0 on the last run.
	TestsPassed bool
	// LintPassed is true if the lint step exited 0 on the last run (or
	// was not configured).
	LintPassed bool
	// ProjectRoot is used by the mock judge for audit writes.
	ProjectRoot string
}

// JudgeProposal is the VP Eng's output: a list of findings to ignore
// (with justifications) and a continuation note describing work that is
// actually missing and should be fed back into the build loop.
type JudgeProposal struct {
	Ignores []IgnoreEntry `json:"ignores"`
	// Continuations are free-form items the VP Eng thinks the SOW missed
	// or that should become the next iteration's focus. These do NOT
	// become ignores; they're surfaced to the supervisor for escalation
	// (e.g. generating a continuation SOW).
	Continuations []string `json:"continuations"`
	// Rationale is a one-paragraph summary from the VP Eng explaining the
	// overall judgment (what's being ignored and why, what's left to do).
	Rationale string `json:"rationale"`
}

// JudgeDecision is the CTO's sign-off on a VP Eng proposal. Approved
// entries are added to the persistent IgnoreList; denied entries are
// dropped with a note in the audit log.
type JudgeDecision struct {
	Approved      []IgnoreEntry `json:"approved"`
	Denied        []IgnoreEntry `json:"denied"`
	Continuations []string      `json:"continuations"` // propagated from VP Eng
	Rationale     string        `json:"rationale"`     // CTO's decision reasoning
}

// OverrideJudge is the two-role (VP Eng → CTO) decision engine.
// Implementations:
//   - LLMOverrideJudge: real provider-backed implementation
//   - MockOverrideJudge: deterministic test implementation
type OverrideJudge interface {
	// Propose is the VP Eng pass: examine findings + context, return a
	// proposal of ignores + continuations.
	Propose(ctx JudgeContext) (*JudgeProposal, error)
	// Approve is the CTO pass: review a proposal, return a decision.
	Approve(ctx JudgeContext, proposal *JudgeProposal) (*JudgeDecision, error)
}

// LLMOverrideJudge drives the two-role flow via a provider.Provider. It
// makes two sequential API calls (VP Eng prompt, then CTO prompt) and
// parses the JSON responses.
type LLMOverrideJudge struct {
	Provider provider.Provider
	Model    string
}

// vpEngPrompt is the strict prompt for the VP Eng role.
const vpEngPrompt = `You are the VP of Engineering reviewing a set of convergence findings that have been flagging a code change as incomplete. Your job: decide which findings are regex/semantic noise (false positives) and which represent real work that is still missing.

Context: the build and test suites have been run. Their outcomes are provided below. If a finding insists the code is broken but the tests pass AND you can verify from the file snippet that the code is correct, the finding is a false positive — propose an ignore entry with a specific reason.

If a finding reflects a genuine gap, do NOT propose an ignore. Instead, add a continuation item describing the work that's still needed.

Output ONLY a JSON object matching this schema. No prose, no markdown fences.

{
  "ignores": [
    {
      "rule_id": "must match finding.rule_id exactly",
      "file": "must match finding.file exactly, or use a glob",
      "line_start": int (0 = any line),
      "line_end": int (0 = same as line_start),
      "pattern": "optional substring of finding.evidence for extra specificity",
      "reason": "REQUIRED: one sentence explaining why this flag is a false positive"
    }
  ],
  "continuations": ["short strings describing work that is actually missing"],
  "rationale": "one paragraph explaining your overall judgment"
}

RULES:
1. Be specific. Ignore entries must target concrete findings; do not propose blanket ignores that could hide new issues.
2. Every ignore entry needs a Reason that references the file + test evidence.
3. If build failed or tests failed, do NOT propose any ignores — the regex flags are likely correct.
4. Continuations must be short, actionable work items, not commentary.

CONTEXT:
`

// ctoPrompt is the strict prompt for the CTO review role.
const ctoPrompt = `You are the CTO reviewing a VP of Engineering proposal to ignore some convergence findings. Your job: approve each entry only if you agree the finding is a false positive AND the VP Eng's reason is specific enough to be auditable.

Deny any entry where:
- The reason is vague ("it's fine", "not important", "the model knows best")
- The entry pattern is too broad (would hide future real issues)
- The build or tests are failing (in which case nothing should be ignored)
- The ignore would suppress a security or reliability finding without concrete evidence the specific case is safe

Output ONLY a JSON object. No prose.

{
  "approved": [{ ... same schema as IgnoreEntry ... }],
  "denied":   [{ ... same schema as IgnoreEntry ... }],
  "continuations": ["carried over from VP Eng proposal"],
  "rationale": "paragraph explaining your approve/deny decisions"
}

RULES:
1. Copy approved entries verbatim from the proposal. Do not edit the reasons.
2. Amended entries go in approved with a note appended to Reason: " [CTO: ...]"
3. Denied entries preserve the original reason and carry a separate CTO note.
4. Continuations are always carried forward — that's a VP Eng decision.

VP ENG PROPOSAL:
`

// Propose invokes the VP Eng LLM pass.
func (j *LLMOverrideJudge) Propose(ctx JudgeContext) (*JudgeProposal, error) {
	if j.Provider == nil {
		return nil, fmt.Errorf("no provider configured")
	}
	model := j.Model
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	userText := vpEngPrompt + buildJudgeContextBlob(ctx)
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": userText}})

	resp, err := j.Provider.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 8000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, fmt.Errorf("vp eng chat: %w", err)
	}

	raw := extractText(resp)
	var prop JudgeProposal
	if _, err := jsonutil.ExtractJSONInto(raw, &prop); err != nil {
		return nil, fmt.Errorf("parse vp eng proposal: %w", err)
	}
	// Stamp proposer on every entry.
	for i := range prop.Ignores {
		prop.Ignores[i].ProposedBy = "vp-eng"
	}
	return &prop, nil
}

// Approve invokes the CTO LLM pass.
func (j *LLMOverrideJudge) Approve(ctx JudgeContext, proposal *JudgeProposal) (*JudgeDecision, error) {
	if proposal == nil {
		return &JudgeDecision{}, nil
	}
	if j.Provider == nil {
		return nil, fmt.Errorf("no provider configured")
	}
	model := j.Model
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	proposalJSON, _ := json.MarshalIndent(proposal, "", "  ")
	userText := ctoPrompt + string(proposalJSON) + "\n\nCONTEXT:\n" + buildJudgeContextBlob(ctx)
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": userText}})

	resp, err := j.Provider.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 8000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, fmt.Errorf("cto chat: %w", err)
	}

	raw := extractText(resp)
	var dec JudgeDecision
	if _, err := jsonutil.ExtractJSONInto(raw, &dec); err != nil {
		return nil, fmt.Errorf("parse cto decision: %w", err)
	}
	// Stamp approver on every entry.
	for i := range dec.Approved {
		dec.Approved[i].ApprovedBy = "cto"
		if dec.Approved[i].ProposedBy == "" {
			dec.Approved[i].ProposedBy = "vp-eng"
		}
	}
	return &dec, nil
}

func extractText(resp *provider.ChatResponse) string {
	if resp == nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range resp.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

// buildJudgeContextBlob serializes the JudgeContext in a compact human/model-
// readable format. It's deliberately not JSON so the model can read it
// without parsing.
func buildJudgeContextBlob(ctx JudgeContext) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Mission: %s\n\n", ctx.MissionID)
	fmt.Fprintf(&b, "Build:  %s\nTests:  %s\nLint:   %s\n\n",
		boolLabel(ctx.BuildPassed, "PASS", "FAIL"),
		boolLabel(ctx.TestsPassed, "PASS", "FAIL"),
		boolLabel(ctx.LintPassed, "PASS", "FAIL"))

	if len(ctx.SOWCriteria) > 0 {
		b.WriteString("Acceptance criteria (from SOW):\n")
		for i, c := range ctx.SOWCriteria {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, c)
		}
		b.WriteString("\n")
	}

	b.WriteString("Findings under review:\n")
	for i, f := range ctx.Findings {
		fmt.Fprintf(&b, "  [%d] rule=%s severity=%s file=%s:%d\n", i+1, f.RuleID, f.Severity, f.File, f.Line)
		fmt.Fprintf(&b, "       %s\n", f.Description)
		if f.Evidence != "" {
			fmt.Fprintf(&b, "       evidence: %s\n", f.Evidence)
		}
	}
	b.WriteString("\n")

	if len(ctx.FileSnippets) > 0 {
		b.WriteString("File snippets:\n")
		for path, snippet := range ctx.FileSnippets {
			fmt.Fprintf(&b, "--- %s ---\n%s\n\n", path, snippet)
		}
	}
	return b.String()
}

func boolLabel(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// RunOverrideFlow drives the full VP Eng → CTO → IgnoreList pipeline for a
// set of triggered findings. Call this from the supervisor when
// RepeatTracker indicates a finding has crossed the threshold.
//
// Returns (updated ignore list, decision record, error). The caller is
// responsible for persisting the ignore list via list.Save(projectRoot).
func RunOverrideFlow(judge OverrideJudge, list *IgnoreList, ctx JudgeContext) (*JudgeDecision, error) {
	if judge == nil {
		return nil, fmt.Errorf("no judge configured")
	}
	if list == nil {
		return nil, fmt.Errorf("no ignore list")
	}
	if len(ctx.Findings) == 0 {
		return &JudgeDecision{}, nil
	}
	proposal, err := judge.Propose(ctx)
	if err != nil {
		return nil, fmt.Errorf("vp eng propose: %w", err)
	}
	decision, err := judge.Approve(ctx, proposal)
	if err != nil {
		return nil, fmt.Errorf("cto approve: %w", err)
	}
	// Stamp the block count so auditors can see how many iterations this
	// flag blocked before being overridden.
	blockCountByKey := make(map[string]int, len(ctx.Findings))
	for _, f := range ctx.Findings {
		blockCountByKey[signatureKey(FlagSignature{RuleID: f.RuleID, File: f.File, Line: f.Line})]++
	}
	for i := range decision.Approved {
		e := decision.Approved[i]
		key := signatureKey(FlagSignature{RuleID: e.RuleID, File: e.File, Line: e.LineStart})
		if n, ok := blockCountByKey[key]; ok {
			decision.Approved[i].BlockCount = n
		}
	}
	// Add approved entries to the persistent list. Add errors (missing
	// reason, etc.) are surfaced but don't abort the whole decision.
	for _, e := range decision.Approved {
		if addErr := list.Add(e); addErr != nil {
			return decision, fmt.Errorf("add ignore entry (rule=%s file=%s): %w", e.RuleID, e.File, addErr)
		}
	}
	return decision, nil
}
