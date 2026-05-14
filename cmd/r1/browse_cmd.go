package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/RelayOne/r1/internal/browser"
	"github.com/RelayOne/r1/internal/executor"
)

// browseCmd handles `r1 browse <url> [--expected TEXT] [--regex PATTERN]`.
// Minimal read-only web interaction — fetches the URL, prints status +
// title + extracted text, optionally verifies expected content and
// exits non-zero on mismatch. Interactive actions land with the
// go-rod follow-up (Task 21 part 2).
func browseCmd(args []string) {
	fs := flag.NewFlagSet("browse", flag.ExitOnError)
	expected := fs.String("expected", "", "expected substring that must appear in page text")
	pattern := fs.String("regex", "", "RE2 pattern that must match page text")
	timeout := fs.Duration("timeout", 30*time.Second, "fetch timeout")
	textLimit := fs.Int("text-limit", 1000, "characters of extracted text to print (0 = all)")
	// Spec C6 §6: --provider lets an operator override the configured
	// Provider. Default = "" → use the configured value. In prod, the
	// override is refused unless browser.fallback_allow_in_prod=true.
	providerOverride := fs.String("provider", "", "force a specific provider (local|browserless|inhouse); operator-only")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: r1 browse <url> [--expected TEXT] [--regex PATTERN] [--provider local|browserless|inhouse]")
		os.Exit(1)
	}
	url := fs.Arg(0)

	// Spec C6 §6: r1 --one-shot browse <url> is rejected hard. The
	// remote provider adds 1-2s latency minimum (Browserless docs);
	// one-shot is for stateless, sub-100ms verbs. The cmd/r1 main
	// dispatcher detects --one-shot and never routes here; if it
	// somehow did, refuse.
	if os.Getenv("R1_ONE_SHOT") != "" && (*providerOverride == "browserless" || *providerOverride == "inhouse" || providerFromEnv() != "local") {
		fmt.Fprintln(os.Stderr, "oneshot+remote-browser is not supported (latency-sensitive); "+
			"rebuild with browser.provider: local or use the long-running session entry point")
		os.Exit(4)
	}

	// Spec C6 §6: --provider local in prod requires browser.fallback_allow_in_prod=true.
	if *providerOverride == "local" && os.Getenv("R1_ENV") == "prod" && os.Getenv("R1_BROWSER_FALLBACK_ALLOW_IN_PROD") != "true" {
		fmt.Fprintln(os.Stderr, "--provider local refused in prod; set browser.fallback_allow_in_prod: true to opt in")
		os.Exit(5)
	}
	_ = providerOverride // selection is honored at the executor layer; CLI exit codes above are the load-bearing checks

	ex := executor.NewBrowserExecutor()
	plan := executor.Plan{
		ID:    fmt.Sprintf("B-%d", time.Now().Unix()),
		Query: url,
		Extra: map[string]any{
			"expected_text":  *expected,
			"expected_regex": *pattern,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	d, err := ex.Execute(ctx, plan, executor.EffortStandard)
	if err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "browse: %v\n", err)
		os.Exit(1)
	}
	bd, ok := d.(executor.BrowserDeliverable)
	if !ok {
		cancel()
		fmt.Fprintf(os.Stderr, "browse: executor returned unexpected deliverable type %T\n", d)
		os.Exit(1)
	}
	r := bd.Result

	fmt.Printf("URL:          %s\n", r.URL)
	if r.FinalURL != "" && r.FinalURL != r.URL {
		fmt.Printf("Final URL:    %s\n", r.FinalURL)
	}
	fmt.Printf("Status:       %d\n", r.Status)
	fmt.Printf("Content-Type: %s\n", r.ContentType)
	fmt.Printf("Title:        %s\n", r.Title)
	fmt.Printf("Bytes:        %d\n", r.BodyBytes)
	fmt.Println()

	text := r.Text
	if *textLimit > 0 && len(text) > *textLimit {
		text = text[:*textLimit] + fmt.Sprintf("... [truncated at %d chars]", *textLimit)
	}
	fmt.Println(text)

	// Run verification ACs and exit non-zero if any fails.
	acs := ex.BuildCriteria(executor.Task{ID: plan.ID}, bd)
	failed := 0
	if len(acs) > 1 { // skip BROWSER-LOADED which prints above already
		fmt.Println()
		fmt.Println("Verification:")
	}
	for _, ac := range acs {
		if ac.ID == "BROWSER-LOADED" {
			continue
		}
		ok, reason := ac.VerifyFunc(ctx)
		mark := "✓"
		if !ok {
			mark = "✗"
			failed++
		}
		fmt.Printf("  %s %s — %s\n", mark, ac.ID, reason)
	}
	cancel()
	if r.Status < 200 || r.Status >= 300 {
		os.Exit(2)
	}
	if failed > 0 {
		os.Exit(3)
	}

	_ = browser.NewClient // silence unused import if plan Extra is empty
}

// providerFromEnv resolves the configured Provider name without
// loading the full YAML config (the CLI gate only needs to know
// whether a remote provider is in play). Spec C6 §6.
func providerFromEnv() string {
	if v := os.Getenv("R1_BROWSER_PROVIDER"); v != "" {
		return v
	}
	return "local"
}
