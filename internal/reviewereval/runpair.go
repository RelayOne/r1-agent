package reviewereval

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/RelayOne/r1/internal/jsonutil"
)

// ReviewFunc is the minimal LLM surface RunPair needs: send one
// reviewer prompt pair, receive the model's raw text reply. Transport
// adapters (modelsource → provider.Provider) live with the caller
// (cmd/r1-bench) so this package stays offline-testable.
type ReviewFunc func(ctx context.Context, systemPrompt, userPrompt string) (string, error)

// reviewSystemPrompt frames the reviewer's task. The reply contract is
// a single JSON object so parseReviewerVerdict stays deterministic.
const reviewSystemPrompt = `You are a rigorous code reviewer measuring implementation authenticity.
Given a spec and the submitted implementation files, decide whether the
implementation genuinely satisfies the spec ("real") or is a
plausible-looking placeholder ("fake" — stubs, hardcoded expected
values, TODO bodies, panics, or unconditional success paths).
Reply with exactly one JSON object and nothing else:
{"verdict":"real"|"fake","reasoning":"<one short paragraph>"}`

// buildReviewPrompts renders one case into the (system, user) prompt
// pair fed to the reviewer. File paths are sorted for reproducible
// prompts across runs.
func buildReviewPrompts(c Case) (systemPrompt, userPrompt string) {
	var b strings.Builder
	fmt.Fprintf(&b, "Case ID: %s\n", c.ID)
	if c.Description != "" {
		fmt.Fprintf(&b, "Task: %s\n", c.Description)
	}
	fmt.Fprintf(&b, "\nSpec:\n%s\n", c.Spec)
	paths := make([]string, 0, len(c.Files))
	for p := range c.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	b.WriteString("\nSubmitted implementation:\n")
	for _, p := range paths {
		fmt.Fprintf(&b, "\n--- %s ---\n%s\n", p, c.Files[p])
	}
	b.WriteString("\nIs this implementation real or fake?")
	return reviewSystemPrompt, b.String()
}

// reviewerReply is the JSON shape the reviewer is instructed to emit.
type reviewerReply struct {
	Verdict   string `json:"verdict"`
	Reasoning string `json:"reasoning"`
}

// parseReviewerVerdict extracts the reviewer's real/fake call from a
// raw model reply. Primary path: robust JSON extraction (handles
// fenced/prose-wrapped objects). Fallback: unambiguous bare-word scan.
// Returns an error when the reply is undecidable so RunPair can count
// the case as skipped instead of guessing.
func parseReviewerVerdict(reply string) (matchesReal bool, reasoning string, err error) {
	var rr reviewerReply
	if _, jerr := jsonutil.ExtractJSONInto(reply, &rr); jerr == nil {
		switch strings.ToLower(strings.TrimSpace(rr.Verdict)) {
		case string(LabelReal):
			return true, rr.Reasoning, nil
		case string(LabelFake):
			return false, rr.Reasoning, nil
		}
	}
	low := strings.ToLower(reply)
	hasReal := strings.Contains(low, string(LabelReal))
	hasFake := strings.Contains(low, string(LabelFake))
	switch {
	case hasReal && !hasFake:
		return true, strings.TrimSpace(reply), nil
	case hasFake && !hasReal:
		return false, strings.TrimSpace(reply), nil
	}
	return false, "", fmt.Errorf("reviewereval: undecidable reviewer reply (%d bytes)", len(reply))
}

// RunPair evaluates one (builder, reviewer) pairing over the labeled
// corpus and returns a fully populated PairResult. The corpus files
// ARE the builder's pre-baked output — builderModel is attribution
// metadata recorded in the result, not a live call; re-generating the
// implementations would invalidate the ground-truth labels.
//
// Per-case review failures (transport errors, undecidable replies) are
// recorded in PairResult.Skipped and warned to stderr — a flaky case
// must not sink the whole evaluation — but if every case fails, RunPair
// returns an error rather than an empty "insufficient data" score.
func RunPair(ctx context.Context, builderModel, reviewerModel string, cases []Case, review ReviewFunc) (PairResult, error) {
	res := PairResult{BuilderModel: builderModel, ReviewerModel: reviewerModel}
	if review == nil {
		return res, fmt.Errorf("reviewereval: RunPair requires a ReviewFunc")
	}
	if len(cases) == 0 {
		return res, fmt.Errorf("reviewereval: RunPair requires a non-empty corpus")
	}

	for _, c := range cases {
		if err := ctx.Err(); err != nil {
			return res, fmt.Errorf("reviewereval: RunPair canceled after %d/%d cases: %w", len(res.Decisions)+len(res.Skipped), len(cases), err)
		}
		systemPrompt, userPrompt := buildReviewPrompts(c)
		reply, err := review(ctx, systemPrompt, userPrompt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reviewereval: skip case %s: review call: %v\n", c.ID, err)
			res.Skipped = append(res.Skipped, c.ID)
			continue
		}
		matchesReal, reasoning, err := parseReviewerVerdict(reply)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reviewereval: skip case %s: %v\n", c.ID, err)
			res.Skipped = append(res.Skipped, c.ID)
			continue
		}
		res.Decisions = append(res.Decisions, Decision{
			CaseID:      c.ID,
			MatchesReal: matchesReal,
			Reasoning:   reasoning,
		})
	}

	if len(res.Decisions) == 0 {
		return res, fmt.Errorf("reviewereval: all %d case reviews failed", len(cases))
	}

	res.Confusion = Grade(cases, res.Decisions)
	res.Precision, res.Recall, res.Accuracy = res.Confusion.Score()
	return res, nil
}
