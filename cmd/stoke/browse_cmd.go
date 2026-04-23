package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ericmacdougall/stoke/internal/browser"
	"github.com/ericmacdougall/stoke/internal/executor"
)

// browseCmd handles `stoke browse <url> [--expected TEXT] [--regex PATTERN]`.
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
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: stoke browse <url> [--expected TEXT] [--regex PATTERN]")
		os.Exit(1)
	}
	url := fs.Arg(0)

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
	defer cancel()
	d, err := ex.Execute(ctx, plan, executor.EffortStandard)
	if err != nil {
		fmt.Fprintf(os.Stderr, "browse: %v\n", err)
		os.Exit(1)
	}
	bd := d.(executor.BrowserDeliverable)
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
		text = text[:*textLimit] + "... [truncated at " + fmt.Sprint(*textLimit) + " chars]"
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
	if r.Status < 200 || r.Status >= 300 {
		os.Exit(2)
	}
	if failed > 0 {
		os.Exit(3)
	}

	_ = browser.NewClient // silence unused import if plan Extra is empty
}
