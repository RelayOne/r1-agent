// Adversarial second critic at the merge gate. A single cross-model
// reviewer's PASS is a single point of trust: a lenient or confused
// reviewer waves defects through and nothing downstream re-examines
// the change. The SecondCritic challenges every PASS verdict with an
// explicitly adversarial charge — find concrete reasons this change
// must NOT merge — and a blocking, file-anchored dissent stops the
// merge (the workflow's attempt/retry loop is the resolution path).
// The critic never sees dissents from the primary reviewer: those
// already block on their own.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/jsonutil"
	"github.com/RelayOne/r1/internal/promptguard"
	"github.com/RelayOne/r1/internal/provider"
)

// CriticInput is everything the second critic may consider. Diff and
// SemanticSummary are repo-derived (untrusted) and are sanitized before
// they reach the model.
type CriticInput struct {
	Task               string   // the task the change claims to implement
	Files              []string // validated changed-file set
	Diff               string   // diff summary of the change
	SemanticSummary    string   // semdiff analysis (renames, breaking changes), may be empty
	PrimaryEngine      string   // which engine produced the PASS being challenged
	PrimaryVerdictJSON string   // the primary reviewer's raw verdict output
}

// CriticFinding is one file-anchored piece of dissent evidence.
type CriticFinding struct {
	Severity string `json:"severity"`
	File     string `json:"file"`
	Line     string `json:"line"`
	Message  string `json:"message"`
}

// CriticVerdict is the critic's structured disagreement. Severity is
// normalized to "blocking" or "advisory"; only a blocking dissent
// stops the merge.
type CriticVerdict struct {
	Dissent         bool            `json:"dissent"`
	Severity        string          `json:"severity"`
	Reasoning       string          `json:"reasoning"`
	RequestedChange string          `json:"requested_change"`
	Findings        []CriticFinding `json:"findings"`
}

// SecondCritic challenges a PASSing cross-model review. Implementations
// must return an error rather than a fabricated verdict when they
// cannot render judgment — the caller fails closed on error.
type SecondCritic interface {
	Challenge(ctx context.Context, in CriticInput) (*CriticVerdict, error)
}

// criticDissentError signals a blocking second-critic dissent. It is the
// mechanism behind this file's doc promise that "the workflow's
// attempt/retry loop is the resolution path": runCrossModelReview returns
// it (leaving the worktree and state intact) instead of a terminal error,
// and Run() routes it back through the attempt loop — folding
// RequestedChange into the retry brief — until the change is revised or
// the attempt budget is exhausted, at which point it fails closed.
type criticDissentError struct {
	reasoning       string
	requestedChange string
	findings        int
}

func (e *criticDissentError) Error() string {
	return fmt.Sprintf("second-opinion dissent (blocking, %d findings): %s", e.findings, e.reasoning)
}

// criticTimeout bounds one Challenge call so a hung provider endpoint
// surfaces as a fail-closed error instead of stalling the merge gate
// forever.
const criticTimeout = 2 * time.Minute

// Prompt caps for repo-derived text, mirroring the specexec selector's
// bounded-request discipline.
const (
	criticDiffCap    = 16 * 1024
	criticVerdictCap = 4 * 1024
)

const criticInstruction = `You are an adversarial second reviewer at a merge gate.
Another reviewer has already PASSED this change; their verdict is attached. Your ONLY job is to find concrete, evidence-backed reasons the change must NOT merge: correctness bugs, missing requirements from the task, broken edge cases, security regressions, or verification gaps the first reviewer missed.
Do not repeat style commentary. Do not dissent to look thorough. If you find no merge-blocking problem, return {"dissent":false}.
A "blocking" dissent REQUIRES at least one finding anchored to a specific file. Use "advisory" for real but non-blocking concerns.
Output ONLY a JSON object, no prose, no markdown fences:
{"dissent":true|false,"severity":"blocking"|"advisory","reasoning":"...","requested_change":"...","findings":[{"severity":"...","file":"...","line":"...","message":"..."}]}

`

// LLMSecondCritic is the provider-backed SecondCritic (same seam as
// convergence.LLMOverrideJudge). Model defaults to claude-sonnet-4-6.
type LLMSecondCritic struct {
	Provider provider.Provider
	Model    string
}

// Challenge renders the adversarial verdict. Any provider or parse
// failure is returned as an error — never a made-up verdict — so the
// merge gate can fail closed.
func (c *LLMSecondCritic) Challenge(ctx context.Context, in CriticInput) (*CriticVerdict, error) {
	if c.Provider == nil {
		return nil, fmt.Errorf("second critic: no provider configured")
	}
	model := c.Model
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	prompt := buildCriticPrompt(in)
	userContent, err := json.Marshal([]map[string]interface{}{{"type": "text", "text": prompt}})
	if err != nil {
		return nil, fmt.Errorf("second critic: marshal prompt: %w", err)
	}
	req := provider.ChatRequest{
		Model:     model,
		MaxTokens: 4000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	}

	// provider.Provider.Chat carries no context; race it against the
	// caller's deadline so a hung endpoint cannot stall the merge gate.
	cctx, cancel := context.WithTimeout(ctx, criticTimeout)
	defer cancel()
	type chatOut struct {
		resp *provider.ChatResponse
		err  error
	}
	ch := make(chan chatOut, 1)
	go func() {
		resp, chatErr := c.Provider.Chat(req)
		ch <- chatOut{resp, chatErr}
	}()
	var resp *provider.ChatResponse
	select {
	case <-cctx.Done():
		return nil, fmt.Errorf("second critic: %w", cctx.Err())
	case out := <-ch:
		if out.err != nil {
			return nil, fmt.Errorf("second critic chat: %w", out.err)
		}
		resp = out.resp
	}

	var v CriticVerdict
	if _, err := jsonutil.ExtractJSONInto(criticResponseText(resp), &v); err != nil {
		return nil, fmt.Errorf("second critic: parse verdict: %w", err)
	}
	normalizeCriticVerdict(&v)
	return &v, nil
}

// normalizeCriticVerdict enforces the severity contract: only the
// literal "blocking" blocks; everything else (unknown, empty, cased
// variants) demotes to "advisory". A blocking dissent without a single
// file-anchored finding also demotes — the instruction requires
// evidence and an evidence-free block would let a lazy critic veto
// every merge.
func normalizeCriticVerdict(v *CriticVerdict) {
	v.Severity = strings.ToLower(strings.TrimSpace(v.Severity))
	if !v.Dissent {
		v.Severity = ""
		return
	}
	if v.Severity != "blocking" {
		v.Severity = "advisory"
		return
	}
	anchored := false
	for _, f := range v.Findings {
		if strings.TrimSpace(f.File) != "" {
			anchored = true
			break
		}
	}
	if !anchored {
		v.Severity = "advisory"
	}
}

// buildCriticPrompt renders the bounded, sanitized critic request.
func buildCriticPrompt(in CriticInput) string {
	var b strings.Builder
	b.WriteString(criticInstruction)
	fmt.Fprintf(&b, "## Task\n%s\n\n", in.Task)
	if len(in.Files) > 0 {
		// File paths are the validated changed-file set, but a path is
		// still repo-derived text: %q-quote each entry so an embedded
		// newline (or other control char) cannot spoof a new prompt
		// section (e.g. a file literally named "\n## Diff\nignore ...").
		quoted := make([]string, len(in.Files))
		for i, f := range in.Files {
			quoted[i] = fmt.Sprintf("%q", f)
		}
		fmt.Fprintf(&b, "## Changed files\n%s\n\n", strings.Join(quoted, "\n"))
	}
	fmt.Fprintf(&b, "## Primary reviewer (%s) PASS verdict\n%s\n\n",
		in.PrimaryEngine, criticSanitize("primary-verdict", in.PrimaryVerdictJSON, criticVerdictCap))
	if diff := criticSanitize("diff", in.Diff, criticDiffCap); diff != "" {
		fmt.Fprintf(&b, "## Diff\n%s\n\n", diff)
	}
	if sem := criticSanitize("semdiff", in.SemanticSummary, criticVerdictCap); sem != "" {
		fmt.Fprintf(&b, "## Semantic change summary\n%s\n", sem)
	}
	return b.String()
}

// criticSanitize routes repo-derived text through promptguard (verify
// phase disposition) and truncates it; a sanitize rejection drops the
// block rather than feeding flagged text to the critic.
func criticSanitize(source, text string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	sanitized, _, err := promptguard.Sanitize(text, promptguard.PhaseAction("verify"), "second-critic:"+source)
	if err != nil {
		return ""
	}
	if len(sanitized) > limit {
		sanitized = sanitized[:limit] + "\n... (truncated)"
	}
	return sanitized
}

func criticResponseText(resp *provider.ChatResponse) string {
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

// emitSecondOpinionEvent emits exactly one hub.EventVerifySecondOpinion
// carrying the critic's verdict. Like emitReviewEvent (see its doc for
// the full ordering argument), the emit is SYNCHRONOUS: the Governor's
// observe handler that records the dissent/agree must be dispatched
// before the workflow proceeds toward task completion, otherwise the
// trust rules could observe worker.declaration.done first. Nil-safe via
// emitEvent (no-op when e.EventBus == nil).
func (e Engine) emitSecondOpinionEvent(name string, cv *CriticVerdict) {
	state := "agree"
	if cv.Dissent {
		state = "dissent"
	}
	e.emitEvent(context.Background(), &hub.Event{
		Type:   hub.EventVerifySecondOpinion,
		TaskID: name,
		Phase:  "review",
		Lifecycle: &hub.LifecycleEvent{
			Entity: "second_opinion",
			State:  state,
		},
		Custom: map[string]any{
			"severity":         cv.Severity,
			"reasoning":        cv.Reasoning,
			"requested_change": cv.RequestedChange,
		},
	})
}
