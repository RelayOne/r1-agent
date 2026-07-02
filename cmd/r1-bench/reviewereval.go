// r1-bench reviewereval — the S-U-017 cross-model review FP/FN
// measurement runner. Loads a labeled ReviewEvalCase corpus, routes
// the reviewer role through internal/modelsource (litellm / openrouter
// / direct), runs reviewereval.RunPair, and emits the Report table
// plus machine-readable PairResult JSON.
//
// The corpus files are the builder's pre-baked output; --builder is
// attribution recorded in the result, not a live call. Note the
// --builder-model/--reviewer-model flags on `r1 sow` serve the SOW
// runner, not this harness.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/modelsource"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/reviewereval"
)

// buildReviewerFunc resolves the reviewer provider via modelsource and
// adapts provider.Chat to reviewereval.ReviewFunc, returning the
// concrete model ID the requests will carry. Package var so tests can
// stub the paid LLM call (same pattern as run_cmd.go's runCostTracker).
var buildReviewerFunc = func(source, model, url, apiKey string, timeout time.Duration) (reviewereval.ReviewFunc, string, error) {
	cfg := modelsource.Config{
		Role:   modelsource.RoleReviewer,
		Source: modelsource.Source(strings.ToLower(strings.TrimSpace(source))),
		Model:  model,
		URL:    url,
		APIKey: apiKey,
	}
	if cfg.Source == "" {
		cfg.Source = modelsource.SourceLiteLLM
	}
	resolved, err := cfg.Build()
	if err != nil {
		return nil, "", err
	}
	modelID := resolved.Model
	if modelID == "" {
		modelID = model
	}
	fmt.Fprintf(os.Stderr, "r1-bench reviewereval: reviewer=%s source=%s endpoint=%s\n", modelID, resolved.Source, resolved.Endpoint)

	fn := func(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
		userContent, err := json.Marshal([]map[string]any{{"type": "text", "text": userPrompt}})
		if err != nil {
			return "", err
		}
		req := provider.ChatRequest{
			Model:     modelID,
			System:    systemPrompt,
			MaxTokens: 2000,
			Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
		}
		// provider.Chat has no ctx parameter; run it on a goroutine so
		// the per-case timeout/cancel still bounds the wait.
		type chatOut struct {
			resp *provider.ChatResponse
			err  error
		}
		ch := make(chan chatOut, 1)
		go func() {
			resp, cerr := resolved.Provider.Chat(req)
			ch <- chatOut{resp, cerr}
		}()
		select {
		case out := <-ch:
			if out.err != nil {
				return "", out.err
			}
			return chatResponseText(out.resp), nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return fn, modelID, nil
}

// chatResponseText concatenates the text blocks of a ChatResponse,
// falling back to thinking blocks when the model emitted only those.
func chatResponseText(resp *provider.ChatResponse) string {
	if resp == nil {
		return ""
	}
	var text, thinking strings.Builder
	for _, c := range resp.Content {
		text.WriteString(c.Text)
		thinking.WriteString(c.Thinking)
	}
	if text.Len() > 0 {
		return text.String()
	}
	return thinking.String()
}

// runReviewerEval implements the `r1-bench reviewereval` subcommand:
// LoadCorpus → RunPair → Grade/Score (inside RunPair) → Report + JSON.
func runReviewerEval(args []string) error {
	fs := flag.NewFlagSet("r1-bench reviewereval", flag.ContinueOnError)
	var (
		corpus         = fs.String("corpus", "internal/reviewereval/corpus", "directory of flat ReviewEvalCase *.json files (NOT the repo-root corpus/, which is the r1-bench task format)")
		builder        = fs.String("builder", "", "builder model recorded for attribution (required; the corpus files are its pre-baked output)")
		reviewer       = fs.String("reviewer", "", "reviewer model alias or exact vendor ID (required)")
		reviewerSource = fs.String("reviewer-source", "", "reviewer source: litellm (default) | openrouter | direct")
		reviewerURL    = fs.String("reviewer-url", "", "reviewer endpoint URL override (source=direct)")
		reviewerAPIKey = fs.String("reviewer-api-key", "", "reviewer API key (defaults to the source's env var)")
		output         = fs.String("output", "", "write the []PairResult JSON here (default: print JSON to stdout after the table)")
		caseTimeout    = fs.Duration("case-timeout", 2*time.Minute, "per-case reviewer call timeout")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *builder == "" {
		return errors.New("reviewereval: --builder is required (attribution for the corpus implementations)")
	}
	if *reviewer == "" {
		return errors.New("reviewereval: --reviewer is required")
	}

	cases, err := reviewereval.LoadCorpus(*corpus)
	if err != nil {
		return err
	}
	if len(cases) == 0 {
		return fmt.Errorf("reviewereval: corpus %s contains no parsable cases", *corpus)
	}

	review, reviewerModel, err := buildReviewerFunc(*reviewerSource, *reviewer, *reviewerURL, *reviewerAPIKey, *caseTimeout)
	if err != nil {
		return fmt.Errorf("reviewereval: resolve reviewer: %w", err)
	}
	perCase := func(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
		cctx, cancel := context.WithTimeout(ctx, *caseTimeout)
		defer cancel()
		return review(cctx, systemPrompt, userPrompt)
	}

	res, err := reviewereval.RunPair(context.Background(), *builder, reviewerModel, cases, perCase)
	if err != nil {
		return err
	}

	results := []reviewereval.PairResult{res}
	fmt.Print(reviewereval.Report(results))
	if len(res.Skipped) > 0 {
		fmt.Printf("\n  skipped %d case(s): %s\n", len(res.Skipped), strings.Join(res.Skipped, ", "))
	}

	body, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("reviewereval: marshal results: %w", err)
	}
	body = append(body, '\n')
	if *output == "" {
		fmt.Println()
		_, werr := os.Stdout.Write(body)
		return werr
	}
	if err := os.WriteFile(*output, body, 0o644); err != nil {
		return err
	}
	fmt.Printf("\n  results written to %s\n", *output)
	return nil
}
