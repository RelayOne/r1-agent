package main

// cicd_cmd.go — `r1 cicd` subcommand.
//
// GitHub Actions integration (historically mis-tagged T-R1P-020; that
//   ticket is the multi-language LSP client, internal/lsp/client)
// T-R1P-021: GitLab CI integration
// T-R1P-022: CircleCI integration
// C5:       BitBucket Pipelines integration
//
// Usage:
//
//	r1 cicd --provider github --mode review   [--output .github/workflows/r1.yml]
//	r1 cicd --provider gitlab --mode autofix
//	r1 cicd --provider circleci --mode mission --plan plans/my-plan.json --workers 4
//	r1 cicd --provider bitbucket --mode review
//	r1 cicd init bitbucket [--workspace .]    sugar for --provider bitbucket --mode review
//	r1 cicd --list                            list all supported providers and modes

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/cicd"
	cicdbitbucket "github.com/RelayOne/r1/internal/cicd/bitbucket"
	cicdgithub "github.com/RelayOne/r1/internal/cicd/github"
	cicdgitlab "github.com/RelayOne/r1/internal/cicd/gitlab"
)

func cicdCmd(args []string) {
	// Runtime verbs (audit A056): `r1 cicd trigger|status|logs` drive
	// the provider REST adapters in internal/cicd/{github,gitlab,
	// bitbucket}, which were library-only until this wiring.
	if len(args) >= 1 {
		switch args[0] {
		case "trigger", "status", "logs":
			os.Exit(runCicdRuntime(args, os.Stdout, os.Stderr))
		}
	}

	// `r1 cicd init bitbucket [--workspace .]` is sugar that rewrites args to
	// `--provider bitbucket --mode review` while leaving any other flags
	// the caller supplied intact.
	if len(args) >= 2 && args[0] == "init" && args[1] == "bitbucket" {
		rest := args[2:]
		rewritten := []string{"--provider", "bitbucket", "--mode", "review"}
		// `--workspace .` is accepted as a flag here but is informational
		// only — the generator detects the project type from the CWD.
		for i := 0; i < len(rest); i++ {
			if rest[i] == "--workspace" || rest[i] == "-workspace" {
				if i+1 < len(rest) {
					i++ // skip the value
				}
				continue
			}
			rewritten = append(rewritten, rest[i])
		}
		args = rewritten
	}

	fs := flag.NewFlagSet("cicd", flag.ExitOnError)
	provider := fs.String("provider", "github", "CI/CD provider: github | gitlab | circleci | bitbucket")
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
		fmt.Fprintln(os.Stderr, "Generate R1 CI/CD integration recipes for GitHub Actions, GitLab CI, CircleCI, or BitBucket Pipelines.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Examples:")
		fmt.Fprintln(os.Stderr, "  r1 cicd --provider github --mode review")
		fmt.Fprintln(os.Stderr, "  r1 cicd --provider gitlab --mode autofix")
		fmt.Fprintln(os.Stderr, "  r1 cicd --provider circleci --mode mission --plan plans/sprint.json --workers 4")
		fmt.Fprintln(os.Stderr, "  r1 cicd --provider bitbucket --mode review")
		fmt.Fprintln(os.Stderr, "  r1 cicd init bitbucket --workspace .")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Runtime verbs (drive the provider REST APIs from env tokens):")
		fmt.Fprintln(os.Stderr, "  r1 cicd trigger --provider github --repo owner/repo --workflow ci.yml --ref main")
		fmt.Fprintln(os.Stderr, "  r1 cicd status  --provider github --repo owner/repo --run-id 42 [--wait]")
		fmt.Fprintln(os.Stderr, "  r1 cicd logs    --provider gitlab --project 123 --job-id 9")
		fmt.Fprintln(os.Stderr, "  Tokens: GITHUB_TOKEN/GH_TOKEN, GITLAB_TOKEN/CI_JOB_TOKEN,")
		fmt.Fprintln(os.Stderr, "          BITBUCKET_API_TOKEN (+BITBUCKET_USERNAME for basic auth)")
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

// --- runtime verbs (audit A056) ---
//
// runCicdRuntime implements `r1 cicd trigger|status|logs`, constructing
// the provider REST adapters (internal/cicd/{github,gitlab,bitbucket})
// from env tokens and driving TriggerWorkflow / GetRunStatus /
// WaitForCompletion / log fetching. Split from cicdCmd and given
// injectable writers so the flows are unit-testable against
// httptest servers via --base-url.
func runCicdRuntime(args []string, stdout, stderr io.Writer) int {
	verb := args[0]
	fs := flag.NewFlagSet("cicd "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	providerName := fs.String("provider", "github", "CI provider: github | gitlab | bitbucket")
	repo := fs.String("repo", "", "github owner/repo, or bitbucket repo slug")
	project := fs.String("project", "", "gitlab project id or namespace/project")
	workspace := fs.String("workspace", "", "bitbucket workspace")
	workflow := fs.String("workflow", "", "github workflow file name or numeric id (trigger)")
	ref := fs.String("ref", "main", "git ref to run against (trigger)")
	runID := fs.Int64("run-id", 0, "github workflow run id / gitlab pipeline id")
	jobID := fs.Int64("job-id", 0, "gitlab job id (logs)")
	pipelineUUID := fs.String("uuid", "", "bitbucket pipeline uuid")
	stepUUID := fs.String("step-uuid", "", "bitbucket step uuid (logs; empty = all steps)")
	pattern := fs.String("pattern", "", "bitbucket custom pipeline pattern (trigger)")
	wait := fs.Bool("wait", false, "block until the run reaches a terminal state (status)")
	timeout := fs.Duration("timeout", 15*time.Minute, "max wait when --wait is set")
	baseURL := fs.String("base-url", "", "override the provider API base URL (self-hosted / tests)")
	vars := map[string]string{}
	fs.Func("var", "KEY=VALUE pipeline variable / dispatch input (repeatable)", func(s string) error {
		k, v, ok := strings.Cut(s, "=")
		if !ok || k == "" {
			return fmt.Errorf("--var wants KEY=VALUE, got %q", s)
		}
		vars[k] = v
		return nil
	})
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	ctx := context.Background()
	switch *providerName {
	case "github":
		return cicdRuntimeGitHub(ctx, verb, stdout, stderr, cicdRuntimeOpts{
			repo: *repo, workflow: *workflow, ref: *ref, runID: *runID,
			wait: *wait, timeout: *timeout, baseURL: *baseURL, vars: vars,
		})
	case "gitlab":
		return cicdRuntimeGitLab(ctx, verb, stdout, stderr, cicdRuntimeOpts{
			project: *project, ref: *ref, runID: *runID, jobID: *jobID,
			wait: *wait, timeout: *timeout, baseURL: *baseURL, vars: vars,
		})
	case "bitbucket":
		return cicdRuntimeBitbucket(ctx, verb, stdout, stderr, cicdRuntimeOpts{
			workspace: *workspace, repo: *repo, ref: *ref, uuid: *pipelineUUID,
			stepUUID: *stepUUID, pattern: *pattern,
			wait: *wait, timeout: *timeout, baseURL: *baseURL, vars: vars,
		})
	default:
		fmt.Fprintf(stderr, "r1 cicd %s: unsupported --provider %q (github | gitlab | bitbucket)\n", verb, *providerName)
		return 2
	}
}

// cicdRuntimeOpts carries the parsed runtime-verb flags.
type cicdRuntimeOpts struct {
	repo, project, workspace string
	workflow, ref            string
	runID, jobID             int64
	uuid, stepUUID, pattern  string
	wait                     bool
	timeout                  time.Duration
	baseURL                  string
	vars                     map[string]string
}

// cicdEnvToken returns the first non-empty env var, warning when none
// is set (public-repo reads still work unauthenticated).
func cicdEnvToken(stderr io.Writer, names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	fmt.Fprintf(stderr, "r1 cicd: warning: none of %s set; proceeding unauthenticated\n", strings.Join(names, "/"))
	return ""
}

func cicdPrintJSON(stdout io.Writer, v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(stdout, "%v\n", v)
		return
	}
	fmt.Fprintf(stdout, "%s\n", data)
}

func cicdRuntimeGitHub(ctx context.Context, verb string, stdout, stderr io.Writer, o cicdRuntimeOpts) int {
	owner, name, ok := strings.Cut(o.repo, "/")
	if !ok || owner == "" || name == "" {
		fmt.Fprintf(stderr, "r1 cicd %s: --repo owner/repo is required for github\n", verb)
		return 2
	}
	c := cicdgithub.New(cicdgithub.Config{
		Token:   cicdEnvToken(stderr, "GITHUB_TOKEN", "GH_TOKEN"),
		BaseURL: o.baseURL,
	})
	switch verb {
	case "trigger":
		if o.workflow == "" {
			fmt.Fprintf(stderr, "r1 cicd trigger: --workflow (file name or id) is required for github\n")
			return 2
		}
		inputs := map[string]interface{}{}
		for k, v := range o.vars {
			inputs[k] = v
		}
		if err := c.TriggerWorkflow(ctx, owner, name, o.workflow, o.ref, inputs); err != nil {
			fmt.Fprintf(stderr, "r1 cicd trigger: %v\n", err)
			return 1
		}
		cicdPrintJSON(stdout, map[string]any{
			"provider": "github", "triggered": true,
			"repo": o.repo, "workflow": o.workflow, "ref": o.ref,
		})
		return 0
	case "status":
		if o.runID == 0 {
			fmt.Fprintf(stderr, "r1 cicd status: --run-id is required for github\n")
			return 2
		}
		var (
			run *cicdgithub.WorkflowRun
			err error
		)
		if o.wait {
			run, err = c.WaitForCompletion(ctx, owner, name, o.runID, o.timeout)
		} else {
			run, err = c.GetRunStatus(ctx, owner, name, o.runID)
		}
		if err != nil {
			fmt.Fprintf(stderr, "r1 cicd status: %v\n", err)
			return 1
		}
		cicdPrintJSON(stdout, run)
		if o.wait && run.Conclusion != "" && run.Conclusion != "success" {
			return 1
		}
		return 0
	case "logs":
		if o.runID == 0 {
			fmt.Fprintf(stderr, "r1 cicd logs: --run-id is required for github\n")
			return 2
		}
		logsURL, err := c.GetJobLogs(ctx, owner, name, o.runID)
		if err != nil {
			fmt.Fprintf(stderr, "r1 cicd logs: %v\n", err)
			return 1
		}
		// GitHub answers with a short-lived signed archive URL.
		fmt.Fprintln(stdout, logsURL)
		return 0
	}
	return 2
}

func cicdRuntimeGitLab(ctx context.Context, verb string, stdout, stderr io.Writer, o cicdRuntimeOpts) int {
	if o.project == "" {
		fmt.Fprintf(stderr, "r1 cicd %s: --project is required for gitlab\n", verb)
		return 2
	}
	c := cicdgitlab.New(cicdgitlab.Config{
		Token:   cicdEnvToken(stderr, "GITLAB_TOKEN", "CI_JOB_TOKEN"),
		BaseURL: o.baseURL,
	})
	switch verb {
	case "trigger":
		p, err := c.TriggerPipeline(ctx, o.project, o.ref, o.vars)
		if err != nil {
			fmt.Fprintf(stderr, "r1 cicd trigger: %v\n", err)
			return 1
		}
		cicdPrintJSON(stdout, p)
		return 0
	case "status":
		if o.runID == 0 {
			fmt.Fprintf(stderr, "r1 cicd status: --run-id (pipeline id) is required for gitlab\n")
			return 2
		}
		var (
			p   *cicdgitlab.Pipeline
			err error
		)
		if o.wait {
			p, err = c.WaitForCompletion(ctx, o.project, o.runID, o.timeout)
		} else {
			p, err = c.GetPipelineStatus(ctx, o.project, o.runID)
		}
		if err != nil {
			fmt.Fprintf(stderr, "r1 cicd status: %v\n", err)
			return 1
		}
		cicdPrintJSON(stdout, p)
		if o.wait && p.IsTerminal() && p.Status != "success" {
			return 1
		}
		return 0
	case "logs":
		if o.jobID == 0 {
			fmt.Fprintf(stderr, "r1 cicd logs: --job-id is required for gitlab\n")
			return 2
		}
		log, err := c.GetJobLog(ctx, o.project, o.jobID)
		if err != nil {
			fmt.Fprintf(stderr, "r1 cicd logs: %v\n", err)
			return 1
		}
		fmt.Fprint(stdout, log)
		return 0
	}
	return 2
}

func cicdRuntimeBitbucket(ctx context.Context, verb string, stdout, stderr io.Writer, o cicdRuntimeOpts) int {
	if o.workspace == "" || o.repo == "" {
		fmt.Fprintf(stderr, "r1 cicd %s: --workspace and --repo are required for bitbucket\n", verb)
		return 2
	}
	username := os.Getenv("BITBUCKET_USERNAME")
	c := cicdbitbucket.New(cicdbitbucket.Config{
		Token:     cicdEnvToken(stderr, "BITBUCKET_API_TOKEN"),
		Username:  username,
		BasicAuth: username != "",
		BaseURL:   o.baseURL,
	})
	switch verb {
	case "trigger":
		var (
			p   *cicdbitbucket.Pipeline
			err error
		)
		if o.pattern != "" {
			p, err = c.TriggerCustomPipeline(ctx, o.workspace, o.repo, o.ref, o.pattern, o.vars)
		} else {
			p, err = c.TriggerPipeline(ctx, o.workspace, o.repo, o.ref, o.vars)
		}
		if err != nil {
			fmt.Fprintf(stderr, "r1 cicd trigger: %v\n", err)
			return 1
		}
		cicdPrintJSON(stdout, p)
		return 0
	case "status":
		if o.uuid == "" {
			fmt.Fprintf(stderr, "r1 cicd status: --uuid is required for bitbucket\n")
			return 2
		}
		var (
			p   *cicdbitbucket.Pipeline
			err error
		)
		if o.wait {
			p, err = c.WaitForCompletion(ctx, o.workspace, o.repo, o.uuid, o.timeout)
		} else {
			p, err = c.GetPipelineStatus(ctx, o.workspace, o.repo, o.uuid)
		}
		if err != nil {
			fmt.Fprintf(stderr, "r1 cicd status: %v\n", err)
			return 1
		}
		cicdPrintJSON(stdout, p)
		return 0
	case "logs":
		if o.uuid == "" {
			fmt.Fprintf(stderr, "r1 cicd logs: --uuid is required for bitbucket\n")
			return 2
		}
		if o.stepUUID != "" {
			log, err := c.GetStepLog(ctx, o.workspace, o.repo, o.uuid, o.stepUUID)
			if err != nil {
				fmt.Fprintf(stderr, "r1 cicd logs: %v\n", err)
				return 1
			}
			fmt.Fprint(stdout, log)
			return 0
		}
		steps, err := c.ListPipelineSteps(ctx, o.workspace, o.repo, o.uuid)
		if err != nil {
			fmt.Fprintf(stderr, "r1 cicd logs: %v\n", err)
			return 1
		}
		for _, st := range steps {
			log, err := c.GetStepLog(ctx, o.workspace, o.repo, o.uuid, st.UUID)
			if err != nil {
				fmt.Fprintf(stderr, "r1 cicd logs: step %s: %v\n", st.UUID, err)
				continue
			}
			fmt.Fprintf(stdout, "=== step %s (%s) ===\n%s\n", st.Name, st.UUID, log)
		}
		return 0
	}
	return 2
}
