package main

// cicd_cmd.go — `r1 cicd` subcommand.
//
// T-R1P-020: GitHub Actions integration
// T-R1P-021: GitLab CI integration
// T-R1P-022: CircleCI integration
//
// Usage:
//
//	r1 cicd --provider github --mode review   [--output .github/workflows/r1.yml]
//	r1 cicd --provider gitlab --mode autofix
//	r1 cicd --provider circleci --mode mission --plan plans/my-plan.json --workers 4
//	r1 cicd --list                            list all supported providers and modes

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/RelayOne/r1/internal/cicd"
)

func cicdCmd(args []string) {
	fs := flag.NewFlagSet("cicd", flag.ExitOnError)
	provider := fs.String("provider", "github", "CI/CD provider: github | gitlab | circleci")
	mode := fs.String("mode", "review", "Integration mode: review | autofix | mission")
	planFile := fs.String("plan", "", "Plan file path (mission mode; default: stoke-plan.json)")
	workers := fs.Int("workers", 1, "Number of parallel R1 workers (mission mode)")
	output := fs.String("output", "", "Output file path (default: provider-canonical path)")
	r1Version := fs.String("r1-version", "latest", "R1 binary version to install in CI")
	policyPath := fs.String("policy", "r1.policy.yaml", "R1 policy file path")
	branch := fs.String("branch", "main", "Branch filter for push triggers")
	list := fs.Bool("list", false, "List supported providers and modes, then exit")
	stdout := fs.Bool("stdout", false, "Write to stdout instead of a file")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: r1 cicd [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Generate R1 CI/CD integration recipes for GitHub Actions, GitLab CI, or CircleCI.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Examples:")
		fmt.Fprintln(os.Stderr, "  r1 cicd --provider github --mode review")
		fmt.Fprintln(os.Stderr, "  r1 cicd --provider gitlab --mode autofix")
		fmt.Fprintln(os.Stderr, "  r1 cicd --provider circleci --mode mission --plan plans/sprint.json --workers 4")
	}
	_ = fs.Parse(args)

	if *list {
		fmt.Println("Providers:")
		for _, p := range cicd.AllProviders() {
			fmt.Printf("  %s\n", p)
		}
		fmt.Println("")
		fmt.Println("Modes:")
		for _, m := range cicd.AllModes() {
			fmt.Printf("  %-10s  %s\n", m, modeDescription(m))
		}
		return
	}

	opts := cicd.Options{
		Mode:       cicd.Mode(*mode),
		PlanFile:   *planFile,
		R1Version:  *r1Version,
		Workers:    *workers,
		PolicyPath: *policyPath,
		Branch:     *branch,
	}

	yaml, defaultPath, err := cicd.GenerateConfig(cicd.Provider(*provider), opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "r1 cicd: %v\n", err)
		os.Exit(1)
	}

	// Validate.
	if warns := cicd.ValidateConfig(cicd.Provider(*provider), yaml); len(warns) > 0 {
		fmt.Fprintf(os.Stderr, "r1 cicd: validation warnings:\n")
		for _, w := range warns {
			fmt.Fprintf(os.Stderr, "  - %s\n", w)
		}
	}

	if *stdout {
		fmt.Print(yaml)
		return
	}

	outPath := *output
	if outPath == "" {
		outPath = defaultPath
	}

	// Ensure parent dirs exist.
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "r1 cicd: mkdir %s: %v\n", filepath.Dir(outPath), err)
		os.Exit(1)
	}

	if err := os.WriteFile(outPath, []byte(yaml), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "r1 cicd: write %s: %v\n", outPath, err)
		os.Exit(1)
	}

	fmt.Printf("r1 cicd: wrote %s (%d bytes)\n", outPath, len(yaml))
	fmt.Println("")
	fmt.Println("Next steps:")
	fmt.Printf("  1. Set the ANTHROPIC_API_KEY secret in your %s repository settings.\n", *provider)
	fmt.Printf("  2. Commit and push %s.\n", outPath)
	fmt.Println("  3. R1 will run automatically on the configured trigger.")
}

func modeDescription(m cicd.Mode) string {
	switch m {
	case cicd.ModeReview:
		return "R1 reviews every PR and posts findings as a comment"
	case cicd.ModeAutoFix:
		return "R1 fixes failing lint/test issues and commits the changes"
	case cicd.ModeMission:
		return "R1 executes a plan file and opens a PR when all tasks pass"
	default:
		return string(m)
	}
}
