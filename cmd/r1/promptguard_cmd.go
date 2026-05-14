package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/ledger/nodes"
	"github.com/RelayOne/r1/internal/promptguard"
	"github.com/RelayOne/r1/internal/r1dir"
)

// promptguardCmd dispatches `r1 promptguard <verb> ...`. The only verb
// today is `verify-system-prompt` (spec §T3 item 14); future verbs
// (refresh-corpus, list-patterns) extend the switch below.
func promptguardCmd(args []string) {
	os.Exit(runPromptguardCmd(args, os.Stdout, os.Stderr))
}

func runPromptguardCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: r1 promptguard <verify-system-prompt> [flags]")
		return 2
	}
	switch args[0] {
	case "verify-system-prompt":
		return runVerifySystemPromptCmd(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "promptguard: unknown verb %q\n", args[0])
		return 2
	}
}

// runVerifySystemPromptCmd implements `r1 promptguard verify-system-prompt`.
//
// Exit codes:
//
//	0 — Verified (or Modified with --accept-modified)
//	0 — Modified without --accept-modified (operator-decision; stdout
//	    carries "STATUS: Modified — diff at <path>" so a follow-up
//	    operator script can branch)
//	2 — Tampered (operator must investigate before proceeding)
//	1 — operational failures (ledger open error, no fingerprint
//	    persisted, no verify key configured)
func runVerifySystemPromptCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("promptguard verify-system-prompt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "repository root")
	keySource := fs.String("key-source", "env", "key source: env (default)")
	accept := fs.Bool("accept-modified", false, "write a new fingerprint accepting the current prompt")
	promptPath := fs.String("prompt-file", "", "path to the assembled system prompt to verify (required for prod)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *keySource != "env" {
		fmt.Fprintf(stderr, "promptguard: --key-source %q unsupported; only 'env' is implemented\n", *keySource)
		return 2
	}
	// Read live-assembled prompt. In daemon-startup the orchestrator
	// captures the assembled prompt to disk under .stoke/promptguard/
	// (see internal/app/orchestrator.go boot path); operators can
	// also pass --prompt-file with a freshly re-assembled prompt.
	var promptText string
	if *promptPath != "" {
		raw, err := os.ReadFile(*promptPath)
		if err != nil {
			fmt.Fprintf(stderr, "promptguard: read prompt: %v\n", err)
			return 1
		}
		promptText = string(raw)
	} else {
		// Default location written by daemon boot. If missing, surface
		// a clear error rather than verifying an empty string.
		defaultPath := r1dir.CanonicalPathFor(*repo, "promptguard/system_prompt.txt")
		raw, err := os.ReadFile(defaultPath)
		if err != nil {
			fmt.Fprintf(stderr, "promptguard: no --prompt-file and default %s missing: %v\n", defaultPath, err)
			return 1
		}
		promptText = string(raw)
	}

	lg, err := ledger.New(r1dir.CanonicalPathFor(*repo, "ledger"))
	if err != nil {
		fmt.Fprintf(stderr, "promptguard: open ledger: %v\n", err)
		return 1
	}
	defer lg.Close()

	fp, fpNode, err := loadLatestFP(lg)
	if err != nil {
		fmt.Fprintf(stderr, "promptguard: load fingerprint: %v\n", err)
		return 1
	}
	_ = fpNode // reserved for future audit-trail surfacing.

	status, vErr := promptguard.VerifySystemPrompt(promptText, fp)
	if vErr != nil {
		fmt.Fprintf(stderr, "promptguard: verify: %v\n", vErr)
		return 1
	}
	switch status {
	case promptguard.Verified:
		fmt.Fprintln(stdout, "STATUS: Verified")
		return 0
	case promptguard.Modified:
		diffPath := writeDiffArtifact(*repo, promptText, fp)
		if *accept {
			if werr := writeFPNode(lg, promptText); werr != nil {
				fmt.Fprintf(stderr, "promptguard: write accepted fingerprint: %v\n", werr)
				return 1
			}
			fmt.Fprintln(stdout, "STATUS: Verified (accepted modification)")
			return 0
		}
		fmt.Fprintf(stdout, "STATUS: Modified - diff at %s\n", diffPath)
		return 0
	case promptguard.Tampered:
		fmt.Fprintln(stdout, "STATUS: Tampered - refuse to start")
		return 2
	default:
		fmt.Fprintf(stderr, "promptguard: unknown status %v\n", status)
		return 1
	}
}

// loadLatestFP scans the ledger for system_prompt_fingerprint
// nodes and returns the most-recently-signed one. Returns a clear
// error when none exists so the CLI can prompt the operator to start
// the daemon first.
func loadLatestFP(lg *ledger.Ledger) (promptguard.SignedFingerprint, *ledger.Node, error) {
	ctx := context.Background()
	nodes, err := lg.Query(ctx, ledger.QueryFilter{Type: "system_prompt_fingerprint"})
	if err != nil {
		return promptguard.SignedFingerprint{}, nil, err
	}
	if len(nodes) == 0 {
		return promptguard.SignedFingerprint{}, nil, fmt.Errorf("no system_prompt_fingerprint node found (has the daemon ever started with a signing key?)")
	}
	// Sort by CreatedAt desc and use the most recent.
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].CreatedAt.After(nodes[j].CreatedAt)
	})
	n := nodes[0]
	var fp promptguard.SignedFingerprint
	if uerr := json.Unmarshal(n.Content, &fp); uerr != nil {
		return promptguard.SignedFingerprint{}, &n, fmt.Errorf("unmarshal fingerprint content: %w", uerr)
	}
	return fp, &n, nil
}

// writeFPNode appends a new SystemPromptFingerprint to the
// ledger, signing the supplied prompt text with the env-configured
// key. Used by the --accept-modified branch.
func writeFPNode(lg *ledger.Ledger, promptText string) error {
	fp, err := promptguard.SignSystemPrompt(promptText)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	node := nodes.SystemPromptFingerprint{
		PromptSHA256:   fp.PromptSHA256,
		KeyFingerprint: fp.KeyFingerprint,
		SignedAt:       fp.SignedAt,
		Signature:      fp.Signature,
		AssemblyTrace:  "operator: r1 promptguard verify-system-prompt --accept-modified",
		Version:        1,
	}
	if vErr := node.Validate(); vErr != nil {
		return vErr
	}
	content, err := json.Marshal(node)
	if err != nil {
		return err
	}
	_, err = lg.AddNode(context.Background(), ledger.Node{
		Type:          "system_prompt_fingerprint",
		SchemaVersion: 1,
		CreatedBy:     "r1 promptguard accept-modified",
		Content:       content,
		CreatedAt:     time.Now().UTC(),
	})
	return err
}

// writeDiffArtifact writes a small diff stub under /tmp so operators can
// inspect the change between the on-disk prompt and the signed
// fingerprint. We deliberately do NOT compute a full unified diff
// (the source-of-truth prompt at sign-time is not retained — only its
// SHA-256), so the artifact carries the live prompt + the expected
// SHA + the actual SHA. An operator armed with the previous release
// can produce the real diff.
func writeDiffArtifact(repo, promptText string, fp promptguard.SignedFingerprint) string {
	ts := time.Now().UTC().Format("20060102T150405Z")
	path := fmt.Sprintf("/tmp/r1-prompt-diff-%s.txt", ts)
	body := fmt.Sprintf(
		"# r1 promptguard system-prompt diff\n# repo: %s\n# expected_sha256: %s\n# signed_at: %s\n# key_fp: %s\n# live_len: %d\n# === live prompt ===\n%s\n",
		repo, fp.PromptSHA256, fp.SignedAt.UTC().Format(time.RFC3339Nano), fp.KeyFingerprint, len(promptText), promptText,
	)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return fmt.Sprintf("(diff write failed: %v)", err)
	}
	return path
}
