package main

// policy_cmd.go — `stoke policy …` subcommand group (POL-9, POL-10, POL-11).
//
// Three verbs:
//
//	stoke policy validate <file.yaml>
//	    Load and compile the YAML policy file. Print the compiled-rule
//	    count and "ok" on success; on parse/compile error, print to
//	    stderr and exit non-zero. (POL-9)
//
//	stoke policy test <file.yaml> "principal=X action=Y resource=Z [k=v…]"
//	    Build a policy.Request from the k=v tokens and run Check against
//	    the compiled YAML client. Print the decision + reasons. Exit 0
//	    on Allow, 1 on Deny, 2 on engine error. (POL-10)
//
//	stoke policy trace [--last-N int] [--log path]
//	    Tail the recent stoke.policy.check / stoke.policy.denied events
//	    from the stream NDJSON log (default ./.stoke/stream.jsonl) and
//	    pretty-print the last N. Missing log file is not an error —
//	    print a message to stderr and exit 0. (POL-11)
//
// Entry point: runPolicyCmd(args []string) int returns an exit code.
// main.go routes `stoke policy …` here passing os.Args[2:].

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ericmacdougall/stoke/internal/policy"
	"github.com/ericmacdougall/stoke/internal/r1dir"
)

// policyCmd is the public entry wired into main.go's subcommand switch.
// Thin shim that delegates to runPolicyCmd so tests can invoke the
// logic without os.Exit side effects.
func policyCmd(args []string) {
	code := runPolicyCmd(args, os.Stdout, os.Stderr)
	if code != 0 {
		os.Exit(code)
	}
}

// runPolicyCmd dispatches to the verb-specific runner and returns an
// exit code (0 = success, non-zero = error per the verb's contract).
// Writers are threaded through so tests can capture output.
func runPolicyCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		policyUsage(stderr)
		return 1
	}
	switch args[0] {
	case "validate":
		return runPolicyValidate(args[1:], stdout, stderr)
	case "test":
		return runPolicyTest(args[1:], stdout, stderr)
	case "trace":
		return runPolicyTrace(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		policyUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown policy subcommand: %s\n\n", args[0])
		policyUsage(stderr)
		return 1
	}
}

// policyUsage writes the verb help to w.
func policyUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: stoke policy <verb> [args]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "verbs:")
	fmt.Fprintln(w, "  validate <file.yaml>")
	fmt.Fprintln(w, "      Compile the YAML policy and report rule count.")
	fmt.Fprintln(w, "  test <file.yaml> \"principal=X action=Y resource=Z [k=v…]\"")
	fmt.Fprintln(w, "      Evaluate a single request and print decision + reasons.")
	fmt.Fprintln(w, "  trace [--last-N int] [--log path]")
	fmt.Fprintln(w, "      Tail recent stoke.policy.* events from the stream log.")
}

// runPolicyValidate implements POL-9.
//
// On success: print "compiled N rules: ok" to stdout and return 0.
// On error: print the error to stderr and return 1.
//
// The compiled-rule count is derived by re-reading the YAML at the CLI
// layer (YAMLClient.rules is unexported). This is honest duplication —
// NewYAMLClient is what validates rule syntax; we only count.
func runPolicyValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("policy validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	rest := fs.Args()
	if len(rest) < 1 {
		fmt.Fprintln(stderr, "usage: stoke policy validate <file.yaml>")
		return 1
	}
	path := rest[0]

	if _, err := policy.NewYAMLClient(path); err != nil {
		fmt.Fprintf(stderr, "policy validate: %v\n", err)
		return 1
	}

	count, err := countPolicyRules(path)
	if err != nil {
		// The file parsed fine under NewYAMLClient, so a re-read
		// failure here is genuinely surprising — surface it but
		// still treat the compile step as authoritative "ok".
		fmt.Fprintf(stderr, "policy validate: warning counting rules: %v\n", err)
		fmt.Fprintln(stdout, "compiled rules: ok")
		return 0
	}
	fmt.Fprintf(stdout, "compiled %d rule(s): ok\n", count)
	return 0
}

// countPolicyRules reads the YAML file at path and returns the number
// of top-level entries in the `rules:` list. Kept narrow on purpose so
// a schema drift in the policy package doesn't silently skew the count.
func countPolicyRules(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var doc struct {
		Rules []map[string]any `yaml:"rules"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return 0, err
	}
	return len(doc.Rules), nil
}

// runPolicyTest implements POL-10.
//
// Usage: stoke policy test <file.yaml> "principal=X action=Y resource=Z [k=v…]"
//
// The kv string is a single quoted argument of whitespace-separated
// key=value tokens. principal / action / resource are the three named
// keys; every other key is stored in Request.Context as a string.
//
// Exit codes:
//
//	0 = Allow
//	1 = Deny
//	2 = engine/usage error
func runPolicyTest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("policy test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) < 2 {
		fmt.Fprintln(stderr, "usage: stoke policy test <file.yaml> \"principal=X action=Y resource=Z [k=v…]\"")
		return 2
	}
	path := rest[0]
	kvString := strings.Join(rest[1:], " ")

	req, err := parsePolicyKVRequest(kvString)
	if err != nil {
		fmt.Fprintf(stderr, "policy test: %v\n", err)
		return 2
	}

	yc, err := policy.NewYAMLClient(path)
	if err != nil {
		fmt.Fprintf(stderr, "policy test: %v\n", err)
		return 2
	}

	res, err := yc.Check(context.Background(), req)
	if err != nil {
		fmt.Fprintf(stderr, "policy test: check error: %v\n", err)
		return 2
	}

	decisionLabel := "Deny"
	if res.Decision == policy.DecisionAllow {
		decisionLabel = "Allow"
	}
	fmt.Fprintf(stdout, "decision: %s\n", decisionLabel)
	fmt.Fprintln(stdout, "reasons:")
	if len(res.Reasons) == 0 {
		fmt.Fprintln(stdout, "  - no-match")
	} else {
		for _, r := range res.Reasons {
			fmt.Fprintf(stdout, "  - %s\n", r)
		}
	}

	if res.Decision == policy.DecisionAllow {
		return 0
	}
	return 1
}

// parsePolicyKVRequest turns "principal=X action=Y resource=Z k=v …"
// into a policy.Request. Unknown keys land in Context as string values
// per the task spec (the YAML engine coerces strings to ints for
// trust_level comparisons when possible).
func parsePolicyKVRequest(kv string) (policy.Request, error) {
	req := policy.Request{Context: map[string]any{}}
	tokens := strings.Fields(kv)
	if len(tokens) == 0 {
		return req, fmt.Errorf("empty request string (need principal=… action=… resource=…)")
	}
	for _, tok := range tokens {
		eq := strings.IndexByte(tok, '=')
		if eq < 0 {
			return req, fmt.Errorf("bad token %q: expected key=value", tok)
		}
		k := strings.TrimSpace(tok[:eq])
		v := strings.TrimSpace(tok[eq+1:])
		switch k {
		case "principal":
			req.Principal = v
		case "action":
			req.Action = v
		case "resource":
			req.Resource = v
		default:
			req.Context[k] = v
		}
	}
	if req.Principal == "" || req.Action == "" || req.Resource == "" {
		return req, fmt.Errorf("missing one of principal/action/resource (got principal=%q action=%q resource=%q)",
			req.Principal, req.Action, req.Resource)
	}
	return req, nil
}

// runPolicyTrace implements POL-11.
//
// Reads the stream NDJSON log (default ./.stoke/stream.jsonl), filters
// to events whose top-level "type" equals "stoke.policy.check" or
// "stoke.policy.denied", and pretty-prints the tail-N of those.
//
// Missing log file is *not* an error — operators may run `policy trace`
// on a fresh repo; we print an informational note to stderr and exit 0.
//
// Output format (one line per event):
//
//	ts  decision  principal→action  resource  reasons
func runPolicyTrace(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("policy trace", flag.ContinueOnError)
	fs.SetOutput(stderr)
	lastN := fs.Int("last-N", 20, "number of recent policy events to show")
	// Default stream path resolves via r1dir: prefers `./.r1/stream.jsonl`
	// when the canonical layout exists, falls back to `./.stoke/stream.jsonl`
	// for pre-rename sessions (work-r1-rename.md §S1-5).
	defaultLog := "./" + filepath.Join(r1dir.Root(), "stream.jsonl")
	logPath := fs.String("log", defaultLog, "path to the NDJSON stream log")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *lastN <= 0 {
		*lastN = 20
	}

	f, err := os.Open(*logPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(stderr, "policy trace: no log at %s (no history yet)\n", *logPath)
			return 0
		}
		fmt.Fprintf(stderr, "policy trace: open %s: %v\n", *logPath, err)
		return 1
	}
	defer f.Close()

	matches, err := filterPolicyEvents(f, *lastN)
	if err != nil {
		fmt.Fprintf(stderr, "policy trace: scan %s: %v\n", *logPath, err)
		return 1
	}
	if len(matches) == 0 {
		fmt.Fprintln(stderr, "policy trace: no stoke.policy.* events found")
		return 0
	}
	for _, line := range matches {
		fmt.Fprintln(stdout, line)
	}
	return 0
}

// filterPolicyEvents scans r line-by-line (NDJSON) and returns up to
// lastN pretty-printed lines for events whose "type" is a policy event.
// The scan keeps a ring buffer of size lastN so we never hold the
// whole file in memory — important for long-running instances.
func filterPolicyEvents(r io.Reader, lastN int) ([]string, error) {
	// Use a generous buffer: stream events can legitimately be large
	// (thinking blocks, tool results). 1 MiB per line is enough for
	// any realistic policy event but still bounds memory.
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)

	ring := make([]string, 0, lastN)
	for sc.Scan() {
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var evt map[string]any
		if err := json.Unmarshal(raw, &evt); err != nil {
			// Tolerate malformed lines — the stream may be mid-write.
			continue
		}
		t, _ := evt["type"].(string)
		if t != "stoke.policy.check" && t != "stoke.policy.denied" {
			continue
		}
		line := formatPolicyEvent(evt)
		if len(ring) == lastN {
			ring = append(ring[1:], line)
		} else {
			ring = append(ring, line)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return ring, nil
}

// formatPolicyEvent renders one NDJSON event as a single pretty line.
// Shape:  ts  decision  principal→action  resource  reasons
// Missing fields collapse to "-" so the columnar layout is preserved
// for human scanning; callers interested in the raw shape can jq the
// source log directly.
func formatPolicyEvent(evt map[string]any) string {
	ts := strOrDash(evt["ts"])
	typ := strOrDash(evt["type"])

	// Policy fields live under the "_stoke.dev/policy.*" namespace
	// on the assistant-visible events, but may also appear at top
	// level on pure stoke.* events — check both.
	decision := firstString(evt, "_stoke.dev/policy.decision", "decision")
	if decision == "" {
		if typ == "stoke.policy.denied" {
			decision = "deny"
		} else {
			decision = "allow"
		}
	}
	principal := firstString(evt, "_stoke.dev/policy.principal", "principal")
	action := firstString(evt, "_stoke.dev/policy.action", "action")
	resource := firstString(evt, "_stoke.dev/policy.resource", "resource")
	reasons := firstStringSlice(evt, "_stoke.dev/policy.reasons", "reasons")

	pa := dashIfEmpty(principal) + "->" + dashIfEmpty(action)
	return fmt.Sprintf("%s  %s  %s  %s  %s",
		ts, decision, pa, dashIfEmpty(resource), reasonsString(reasons))
}

// strOrDash coerces v to a string, falling back to "-" on nil/non-string.
func strOrDash(v any) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return "-"
}

// dashIfEmpty returns "-" for empty strings, passthrough otherwise.
func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// firstString returns the first non-empty string value found at any of
// the given keys in evt. Used to paper over field layout differences
// between system events (namespaced keys) and stoke.* events.
func firstString(evt map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := evt[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// firstStringSlice returns the first []string-convertible value found
// at any of the given keys in evt. JSON unmarshal delivers []any, so
// we coerce element-by-element.
func firstStringSlice(evt map[string]any, keys ...string) []string {
	for _, k := range keys {
		v, ok := evt[k]
		if !ok {
			continue
		}
		switch xs := v.(type) {
		case []any:
			out := make([]string, 0, len(xs))
			for _, x := range xs {
				if s, ok := x.(string); ok {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				return out
			}
		case []string:
			if len(xs) > 0 {
				return xs
			}
		}
	}
	return nil
}

// reasonsString joins reasons with commas, returning "-" for empty.
func reasonsString(xs []string) string {
	if len(xs) == 0 {
		return "-"
	}
	return strings.Join(xs, ",")
}
