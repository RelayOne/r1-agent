// Package main — inspect.go
//
// Standalone codebase inspection (audit) command. Runs the hygiene
// scanner + integration reviewer WITHOUT a Statement of Work. Safe
// to run on any repository; read-only by default, applies hygiene
// auto-fixes only when --fix is passed.
//
// The inspect command reuses:
//   - plan.DetectExecutors     — list ecosystems present
//   - plan.ScanOnly            — read-only hygiene sweep
//   - plan.ScanAndAutoFix      — hygiene sweep + auto-fix (with --fix)
//   - plan.RunIntegrationReview — cross-file gap reviewer (with --agent)
//
// Name note: the `audit` subcommand is already taken (17-persona
// multi-perspective review via the internal/audit package) and the
// `scan` subcommand is also taken (deterministic secrets/eval/exec
// scanner via internal/scan). We register this non-SOW codebase
// inspection path under the fresh verb `inspect` to avoid collision.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/RelayOne/r1-agent/internal/litellm"
	"github.com/RelayOne/r1-agent/internal/plan"
	"github.com/RelayOne/r1-agent/internal/provider"
	"github.com/RelayOne/r1-agent/internal/skill"
)

// inspectOutput is the JSON shape emitted by `stoke inspect --json`.
type inspectOutput struct {
	RepoRoot    string                    `json:"repo_root"`
	Ecosystems  []string                  `json:"ecosystems"`
	Hygiene     *plan.HygieneReport       `json:"hygiene"`
	Integration *plan.IntegrationReport   `json:"integration"`
	Summary     string                    `json:"summary"`
}

// inspectCmd implements `stoke inspect` — the standalone, SOW-free
// audit entry point. Prints a structured report of hygiene findings
// and (when a provider is configured and --agent=true) cross-file
// integration gaps.
func inspectCmd(args []string) {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	repo := fs.String("repo", ".", "Repository root")
	jsonOut := fs.Bool("json", false, "Emit a single JSON object on stdout")
	agent := fs.Bool("agent", true, "Run the integration reviewer (cross-file gaps)")
	fix := fs.Bool("fix", false, "Apply auto-fixable hygiene findings in place")
	cfgPath := fs.String("config", "", "Config file path (reserved; currently unused)")
	apiKey := fs.String("api-key", "", "API key for integration reviewer (or LITELLM_API_KEY / LITELLM_MASTER_KEY / ANTHROPIC_API_KEY)")
	baseURL := fs.String("base-url", "", "Base URL for integration reviewer provider (e.g. LiteLLM proxy)")
	modelName := fs.String("model", "", "Model name for integration reviewer (default: claude-sonnet-4-6)")
	timeout := fs.Duration("timeout", 10*time.Minute, "Overall timeout for the inspect run")
	fs.Parse(args)
	_ = cfgPath // reserved for future config-driven provider resolution

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fatal("resolve repo: %v", err)
	}
	if st, statErr := os.Stat(absRepo); statErr != nil || !st.IsDir() {
		fatal("repo root is not a directory: %s", absRepo)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// 1. Ecosystem detection.
	execs := plan.DetectExecutors(absRepo)
	ecoStrs := make([]string, len(execs))
	for i, e := range execs {
		ecoStrs[i] = string(e)
	}

	// 2. Hygiene sweep — scan-only by default, auto-fix with --fix.
	var hygiene *plan.HygieneReport
	if *fix {
		hygiene, err = plan.ScanAndAutoFix(ctx, absRepo)
	} else {
		hygiene, err = plan.ScanOnly(ctx, absRepo)
	}
	if err != nil {
		fatal("hygiene: %v", err)
	}

	// 3. Integration reviewer — only when --agent=true and a provider
	//    can be resolved. No provider means we skip this phase with a
	//    warning; hygiene-only inspect is a valid mode.
	var integration *plan.IntegrationReport
	if *agent {
		prov, resolvedModel, provNote := resolveInspectProvider(*apiKey, *baseURL, *modelName)
		if prov == nil {
			if !*jsonOut {
				fmt.Fprintf(os.Stderr, "  warning: no reasoning provider available (%s) — skipping integration review\n", provNote)
			}
		} else {
			if !*jsonOut {
				fmt.Printf("  integration reviewer: %s\n", provNote)
			}
			// Use the chunked variant so large repos don't hit the
			// 10-minute/40-turn budget and silently return empty —
			// the SOW path already switched to chunked for this
			// reason; inspect deserves the same resilience.
			universalCtx := skill.LoadUniversalContext(absRepo)
			integration, err = plan.RunIntegrationReviewChunked(ctx, prov, resolvedModel, plan.IntegrationReviewInput{
				RepoRoot:             absRepo,
				UniversalPromptBlock: universalCtx.PromptBlock(),
			}, 10*time.Minute)
			if err != nil {
				if !*jsonOut {
					fmt.Fprintf(os.Stderr, "  integration review failed: %v\n", err)
				}
				integration = nil
			}
		}
	}

	summary := buildInspectSummary(hygiene, integration, *fix)

	if *jsonOut {
		out := inspectOutput{
			RepoRoot:    absRepo,
			Ecosystems:  ecoStrs,
			Hygiene:     hygiene,
			Integration: integration,
			Summary:     summary,
		}
		data, mErr := json.MarshalIndent(out, "", "  ")
		if mErr != nil {
			fatal("marshal inspect output: %v", mErr)
		}
		fmt.Println(string(data))
		return
	}

	renderInspectHuman(absRepo, ecoStrs, hygiene, integration, summary, *fix)
}

// resolveInspectProvider follows the same precedence the native SOW
// runner uses: explicit --api-key, then env (LITELLM_API_KEY,
// LITELLM_MASTER_KEY, ANTHROPIC_API_KEY), then auto-discovery of a
// local LiteLLM proxy. Returns (nil, "", reason) when no provider
// can be resolved — callers should treat that as a soft skip.
func resolveInspectProvider(apiKey, baseURL, modelName string) (provider.Provider, string, string) {
	key := apiKey
	if key == "" {
		for _, k := range []string{"LITELLM_API_KEY", "LITELLM_MASTER_KEY", "ANTHROPIC_API_KEY"} {
			if v := os.Getenv(k); v != "" {
				key = v
				break
			}
		}
	}
	if baseURL == "" {
		if d := litellm.Discover(); d != nil {
			baseURL = d.BaseURL
			if key == "" && d.APIKey != "" {
				key = d.APIKey
			}
		}
	}
	if key == "" && baseURL != "" {
		key = provider.LocalLiteLLMStub
	}
	if key == "" {
		return nil, "", "no API key (set --api-key, ANTHROPIC_API_KEY, or run a LiteLLM proxy)"
	}
	if modelName == "" {
		modelName = "claude-sonnet-4-6"
	}
	prov := provider.NewAnthropicProvider(key, baseURL)
	note := fmt.Sprintf("model=%s", modelName)
	if baseURL != "" {
		note += " via " + baseURL
	}
	return prov, modelName, note
}

// buildInspectSummary produces the one-sentence summary line shown
// at the bottom of human output and in the JSON payload.
func buildInspectSummary(hyg *plan.HygieneReport, integ *plan.IntegrationReport, fixApplied bool) string {
	var hygCount, autoFixable, autoFixed int
	if hyg != nil {
		if fixApplied {
			hygCount = len(hyg.Remaining)
			autoFixed = len(hyg.AutoFixed)
		} else {
			hygCount = len(hyg.PreFix)
		}
		for _, f := range hyg.PreFix {
			if f.AutoFixable {
				autoFixable++
			}
		}
	}
	integCount := 0
	if integ != nil {
		integCount = len(integ.Gaps)
	}
	total := hygCount + integCount
	if fixApplied {
		return fmt.Sprintf("%d finding(s) after fix; %d hygiene auto-fix(es) applied.", total, autoFixed)
	}
	return fmt.Sprintf("%d total findings, %d auto-fixable. Run with --fix to apply hygiene auto-fixes, or dispatch a stoke sow repair for integration gaps.", total, autoFixable)
}

// renderInspectHuman prints the human-readable report.
func renderInspectHuman(repo string, ecos []string, hyg *plan.HygieneReport, integ *plan.IntegrationReport, summary string, fixApplied bool) {
	fmt.Printf("stoke inspect — %s\n", repo)
	if len(ecos) == 0 {
		fmt.Println("ecosystems detected: (none)")
	} else {
		fmt.Printf("ecosystems detected: %s\n", joinStrings(ecos, ", "))
	}
	fmt.Println()

	// Hygiene section.
	fmt.Println("workspace hygiene:")
	switch {
	case hyg == nil:
		fmt.Println("  (no report)")
	case fixApplied:
		fmt.Printf("  auto-fixed: %d\n", len(hyg.AutoFixed))
		for _, f := range hyg.AutoFixed {
			fmt.Printf("    + [%s] %s — %s\n", f.Kind, f.Package, f.Detail)
		}
		fmt.Printf("  remaining: %d\n", len(hyg.Remaining))
		for _, f := range hyg.Remaining {
			fmt.Printf("    - [%s] %s — %s\n", f.Kind, f.Package, f.Detail)
		}
	default:
		pre := hyg.PreFix
		if len(pre) == 0 {
			fmt.Println("  clean (0 findings)")
		} else {
			fmt.Printf("  %d finding(s):\n", len(pre))
			for _, f := range pre {
				tag := ""
				if f.AutoFixable {
					tag = " [auto-fixable]"
				}
				fmt.Printf("    - [%s] %s — %s%s\n", f.Kind, f.Package, f.Detail, tag)
			}
		}
	}
	fmt.Println()

	// Integration section.
	fmt.Println("integration review:")
	switch {
	case integ == nil:
		fmt.Println("  (skipped — no provider or --agent=false)")
	case len(integ.Gaps) == 0:
		fmt.Println("  clean (0 cross-file gaps)")
		if integ.Summary != "" {
			fmt.Printf("  %s\n", integ.Summary)
		}
	default:
		fmt.Printf("  %d cross-file gap(s):\n", len(integ.Gaps))
		for i, g := range integ.Gaps {
			fmt.Printf("    %d. [%s] %s\n", i+1, g.Kind, g.Location)
			if g.Detail != "" {
				fmt.Printf("       detail: %s\n", g.Detail)
			}
			if g.SuggestedFollowup != "" {
				fmt.Printf("       suggested: %s\n", g.SuggestedFollowup)
			}
		}
	}
	fmt.Println()

	fmt.Printf("summary: %s\n", summary)
}

// joinStrings is a tiny helper to avoid pulling strings.Join into
// this file's import set when most of the file uses only fmt + os.
func joinStrings(xs []string, sep string) string {
	out := ""
	for i, s := range xs {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
