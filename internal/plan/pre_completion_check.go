// Package plan — pre_completion_check.go
//
// PreEndTurnCheckFn implementation for descent-hardening spec-1 item 3.
//
// Parses the <pre_completion>...</pre_completion> XML block the worker
// must emit before claiming "done", "complete", "finished", or "ready
// for review" (per preCompletionGate prompt block in sow_native.go).
// Cross-checks two claims against observable ground truth:
//
//   1. FILES_MODIFIED: every path claimed "created|modified|deleted"
//      must show up in `git status --porcelain <path>` inside the
//      session cwd. A claim with empty git output is a mismatch.
//   2. AC_VERIFICATION: every AC claiming `exit_code: 0` must have
//      the exact command string appear in the session transcript as a
//      bash tool input. No match = fabrication.
//
// On mismatch the function emits a bus event `descent.pre_completion_gate_failed`
// and returns a short reason string — the agentloop's PreEndTurnCheckFn
// seam will force another turn, giving the model a chance to correct.
// The caller (descent_bridge.go) may additionally flag the descent
// run with analysisCategory="code_bug" so T4 runs again.
//
// The parser is lenient about formatting (YAML-ish indentation, blank
// lines) because the block is human-drafted and exact schema drift
// would be worse than a false-negative. It IS strict about the
// AC command string match: the whole purpose is to catch paraphrase
// fabrications where the worker wrote "I ran the tests" without
// actually invoking the SOW's `go test ./...` command.
package plan

import (
	"encoding/xml"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

// ToolCall is a single bash tool invocation captured from the session
// transcript. Populated by the agentloop wiring in descent_bridge.go.
// Only the input command matters for pre-completion cross-checking.
type ToolCall struct {
	Name  string // tool name (e.g. "bash", "edit")
	Input string // raw input command / path
}

// PreCheckContext bundles everything the PreEndTurnCheck needs from the
// caller's context. Each field is optional but the check becomes
// progressively weaker as fields are omitted.
type PreCheckContext struct {
	// RepoRoot is the absolute path where `git status` runs. Empty
	// disables FILES_MODIFIED cross-check (returns neutral).
	RepoRoot string

	// SowACs is the canonical list of acceptance criteria from the
	// SOW session. Used to recognize AC ids. Empty list disables
	// AC_VERIFICATION cross-check.
	SowACs []AcceptanceCriterion

	// SessionTranscript is the tool-call history for the current
	// session. The check grepss bash tool inputs for exact AC command
	// matches. Empty list means AC verification always fails the
	// cross-check (conservative — no evidence = fabrication).
	SessionTranscript []ToolCall

	// OnMismatch is called when a cross-check fails. Typically wired
	// to bus.Publish for descent.pre_completion_gate_failed events.
	// Nil = no emission (check still returns reason).
	OnMismatch func(mismatchKind, claim, observed string)
}

// PreCompletionResult is the machine-readable parse of the
// <pre_completion> block. Exposed for test introspection.
type PreCompletionResult struct {
	Present      bool
	FilesClaims  []FileClaim
	ACClaims     []ACClaim
	SelfAssessAllACsPass  *bool
	SelfAssessClaimingSuccess *bool
}

// FileClaim represents one FILES_MODIFIED line inside the block.
type FileClaim struct {
	Path   string // repo-relative or absolute
	Action string // "created" | "modified" | "deleted"
	Reason string
}

// ACClaim represents one AC_VERIFICATION stanza inside the block.
type ACClaim struct {
	ACID             string
	Command          string
	RanThisSession   string // "yes" | "no" | ""
	ExitCode         string // integer or "not run"
	FirstErrorLine   string
	Verdict          string // "PASS" | "FAIL" | "NOT_RUN"
}

// completionTriggerRE detects words in the final assistant message
// that the gate prompt instructs the worker to NEVER emit without a
// <pre_completion> block preceding.
var completionTriggerRE = regexp.MustCompile(`(?i)\b(done|complete|completed|finished|ready for review)\b`)

// preCompletionBlockRE captures the raw XML block for parsing. The
// prompt specifies a single `<pre_completion>...</pre_completion>` —
// we accept at most one. Lenient about surrounding whitespace.
var preCompletionBlockRE = regexp.MustCompile(`(?s)<pre_completion>\s*(.*?)\s*</pre_completion>`)

// NewPreEndTurnCheck builds a PreEndTurnCheckFn-style closure given the
// check context. Returns a function with the signature
// func(finalText string) (retry bool, reason string) that the caller
// wires into its agentloop via PreEndTurnCheckFn.
//
// Semantics:
//   - finalText does NOT trigger completion words → (false, "") — no gate required
//   - finalText contains completion words AND no <pre_completion> block → (false, "pre_completion_gate missing")
//     and emits OnMismatch("missing_block", ..., ...)
//   - Block present → run cross-checks; any mismatch → (false, reason) and emit OnMismatch
//   - Everything matches → (false, "")
//
// The returned retry bool is ALWAYS false because this spec-1 item
// forces the descent engine to treat a gate failure as a code_bug
// (see item 3 description). The reason string is non-empty iff a
// mismatch was detected, giving the caller enough information to
// force T4 retry without needing to re-parse.
func NewPreEndTurnCheck(ctx PreCheckContext) func(finalText string) (retry bool, reason string) {
	return func(finalText string) (bool, string) {
		if !completionTriggerRE.MatchString(finalText) {
			return false, ""
		}

		// Completion trigger hit — a <pre_completion> block is mandatory.
		m := preCompletionBlockRE.FindStringSubmatch(finalText)
		if len(m) < 2 {
			if ctx.OnMismatch != nil {
				ctx.OnMismatch("missing_block",
					"claimed completion without pre_completion block",
					truncateForPreCheck(finalText, 200))
			}
			return false, "pre_completion_gate missing"
		}

		result, err := ParsePreCompletionBlock(m[1])
		if err != nil {
			if ctx.OnMismatch != nil {
				ctx.OnMismatch("parse_error", err.Error(), "")
			}
			return false, fmt.Sprintf("pre_completion_gate parse error: %v", err)
		}

		// SELF_ASSESSMENT consistency: if "all ACs pass" == false but
		// "claiming success" == true, that's explicit self-contradiction.
		if result.SelfAssessAllACsPass != nil && result.SelfAssessClaimingSuccess != nil {
			if !*result.SelfAssessAllACsPass && *result.SelfAssessClaimingSuccess {
				if ctx.OnMismatch != nil {
					ctx.OnMismatch("self_assessment_inconsistent",
						"claiming_success=yes with all_acs_pass=no",
						"")
				}
				return false, "pre_completion_gate self-assessment inconsistent"
			}
		}

		// FILES_MODIFIED cross-check: every claim must show up in
		// git status --porcelain. A claim with empty git output means
		// the worker either never wrote that file or it was already
		// committed (in which case "modified this session" is a fib).
		if ctx.RepoRoot != "" {
			for _, fc := range result.FilesClaims {
				if fc.Action == "" || strings.EqualFold(fc.Action, "unchanged") {
					continue
				}
				if !pathShowsInGitStatus(ctx.RepoRoot, fc.Path) {
					if ctx.OnMismatch != nil {
						ctx.OnMismatch("files_missing",
							fmt.Sprintf("%s (%s)", fc.Path, fc.Action),
							"git status --porcelain empty")
					}
					return false, fmt.Sprintf("pre_completion_gate: claimed %s %s but git shows no change", fc.Action, fc.Path)
				}
			}
		}

		// AC_VERIFICATION cross-check: any AC claiming exit_code=0 must
		// have its exact command appear in the session transcript as a
		// bash tool input. Extra whitespace + boolean-ish exit codes are
		// tolerated; substring match on trimmed command.
		if len(ctx.SessionTranscript) > 0 {
			for _, ac := range result.ACClaims {
				if !claimsZeroExit(ac) {
					continue
				}
				cmd := strings.TrimSpace(ac.Command)
				if cmd == "" {
					continue
				}
				if !commandAppearsInTranscript(ctx.SessionTranscript, cmd) {
					if ctx.OnMismatch != nil {
						ctx.OnMismatch("ac_no_evidence",
							fmt.Sprintf("AC %s: %s", ac.ACID, cmd),
							"no matching bash tool input")
					}
					return false, fmt.Sprintf("pre_completion_gate: AC %s claims exit_code:0 but no transcript evidence", ac.ACID)
				}
			}
		}

		return false, ""
	}
}

// ParsePreCompletionBlock parses the inner text of a <pre_completion>
// block into structured data. The block uses YAML-ish markup that
// encoding/xml can't hack, so we parse line-by-line. Exported for
// test introspection.
func ParsePreCompletionBlock(body string) (PreCompletionResult, error) {
	var out PreCompletionResult
	out.Present = true

	// First try strict XML in case a worker actually emits nested tags.
	// This catches the edge case where the model over-structures the block.
	type xmlForm struct {
		XMLName xml.Name `xml:"pre_completion"`
		Raw     string   `xml:",innerxml"`
	}
	if strings.HasPrefix(strings.TrimSpace(body), "<") {
		var x xmlForm
		_ = xml.Unmarshal([]byte("<pre_completion>"+body+"</pre_completion>"), &x)
		// If xml parse worked and produced something, parse its inner
		// text line-by-line same as plain form.
		body = x.Raw + body // fall through to line-by-line
	}

	lines := strings.Split(body, "\n")
	section := ""
	var curAC *ACClaim
	flushAC := func() {
		if curAC != nil && curAC.ACID != "" {
			out.ACClaims = append(out.ACClaims, *curAC)
		}
		curAC = nil
	}

	boolPtr := func(b bool) *bool { return &b }

	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t\r")
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		// Detect section headers (top-level keys ending in ":").
		upper := strings.ToUpper(trim)
		switch {
		case strings.HasPrefix(upper, "FILES_MODIFIED:"):
			flushAC()
			section = "files"
			continue
		case strings.HasPrefix(upper, "AC_VERIFICATION:"):
			flushAC()
			section = "ac"
			continue
		case strings.HasPrefix(upper, "TODO_SCAN:"):
			flushAC()
			section = "todo"
			continue
		case strings.HasPrefix(upper, "DEPENDENCIES:"):
			flushAC()
			section = "deps"
			continue
		case strings.HasPrefix(upper, "OUTSTANDING:"):
			flushAC()
			section = "outstanding"
			continue
		case strings.HasPrefix(upper, "SELF_ASSESSMENT:"):
			flushAC()
			section = "self"
			continue
		}

		switch section {
		case "files":
			// Pattern: "  - <path> (action) — reason"
			if strings.HasPrefix(trim, "-") {
				rest := strings.TrimSpace(strings.TrimPrefix(trim, "-"))
				claim := parseFileClaim(rest)
				if claim.Path != "" {
					out.FilesClaims = append(out.FilesClaims, claim)
				}
			}
		case "ac":
			// Pattern:
			//   - AC-id: X
			//     command: Y
			//     exit_code: 0
			//     verdict: PASS
			if strings.HasPrefix(trim, "-") || strings.HasPrefix(strings.ToLower(trim), "ac-id:") {
				flushAC()
				curAC = &ACClaim{}
				rest := strings.TrimSpace(strings.TrimPrefix(trim, "-"))
				kv := splitKV(rest)
				if strings.EqualFold(kv.key, "AC-id") {
					curAC.ACID = kv.val
				}
				continue
			}
			if curAC != nil {
				kv := splitKV(trim)
				switch strings.ToLower(kv.key) {
				case "ac-id":
					curAC.ACID = kv.val
				case "command":
					curAC.Command = kv.val
				case "ran_this_session":
					curAC.RanThisSession = kv.val
				case "exit_code":
					curAC.ExitCode = kv.val
				case "first_error_line":
					curAC.FirstErrorLine = kv.val
				case "verdict":
					curAC.Verdict = kv.val
				}
			}
		case "self":
			if strings.HasPrefix(trim, "-") {
				rest := strings.TrimSpace(strings.TrimPrefix(trim, "-"))
				lower := strings.ToLower(rest)
				if strings.Contains(lower, "every ac report pass") || strings.Contains(lower, "did every ac") {
					if v, ok := yesNo(rest); ok {
						out.SelfAssessAllACsPass = boolPtr(v)
					}
				}
				if strings.Contains(lower, "claiming success") {
					if v, ok := yesNo(rest); ok {
						out.SelfAssessClaimingSuccess = boolPtr(v)
					}
				}
			}
		}
	}
	flushAC()

	return out, nil
}

// yesNo extracts a yes/no verdict from free text. Case-insensitive.
// Returns (true, true) for "yes", (false, true) for "no", (_, false)
// for anything else.
func yesNo(s string) (bool, bool) {
	lower := strings.ToLower(s)
	// Strip trailing punctuation + surrounding whitespace.
	lower = strings.TrimRight(lower, " ?.!")
	if strings.HasSuffix(lower, "yes") {
		return true, true
	}
	if strings.HasSuffix(lower, "no") {
		return false, true
	}
	// Some variants: " — yes", ": yes"
	if strings.Contains(lower, " yes") {
		return true, true
	}
	if strings.Contains(lower, " no") {
		return false, true
	}
	return false, false
}

// kvPair holds a parsed key:value line.
type kvPair struct {
	key string
	val string
}

// splitKV splits "key: value" handling leading/trailing whitespace.
func splitKV(s string) kvPair {
	idx := strings.Index(s, ":")
	if idx < 0 {
		return kvPair{key: s}
	}
	return kvPair{
		key: strings.TrimSpace(s[:idx]),
		val: strings.TrimSpace(s[idx+1:]),
	}
}

// parseFileClaim parses "path (action) — reason" into FileClaim.
var fileClaimRE = regexp.MustCompile(`^(\S.*?)\s*\(([^)]+)\)`)

func parseFileClaim(line string) FileClaim {
	m := fileClaimRE.FindStringSubmatch(line)
	if len(m) < 3 {
		// Fallback: just take the first whitespace-delimited token
		// as the path.
		fields := strings.Fields(line)
		if len(fields) > 0 {
			return FileClaim{Path: fields[0]}
		}
		return FileClaim{}
	}
	return FileClaim{
		Path:   strings.TrimSpace(m[1]),
		Action: strings.ToLower(strings.TrimSpace(m[2])),
		Reason: strings.TrimSpace(strings.TrimPrefix(line[len(m[0]):], "—")),
	}
}

// claimsZeroExit returns true when an AC_VERIFICATION entry asserts
// exit_code 0 (or "pass" verdict with numeric 0).
func claimsZeroExit(ac ACClaim) bool {
	if strings.TrimSpace(ac.ExitCode) == "0" {
		return true
	}
	// "verdict: PASS" with a numeric exit code that's not "not run"
	// also counts as zero-exit evidence claim.
	if strings.EqualFold(strings.TrimSpace(ac.Verdict), "PASS") {
		ec := strings.ToLower(strings.TrimSpace(ac.ExitCode))
		if ec == "0" || ec == "" {
			return true
		}
	}
	return false
}

// commandAppearsInTranscript returns true when the exact command
// (or a trimmed variant) appears in any transcript entry's input.
// Substring match after trimming to accommodate shells wrapping
// commands in `bash -lc "..."` or quoting.
func commandAppearsInTranscript(transcript []ToolCall, cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	// Also check a whitespace-normalized form so "go test ./..." and
	// "go  test  ./..." both match.
	normalized := strings.Join(strings.Fields(cmd), " ")
	for _, tc := range transcript {
		input := tc.Input
		if input == "" {
			continue
		}
		if strings.Contains(input, cmd) {
			return true
		}
		if strings.Contains(strings.Join(strings.Fields(input), " "), normalized) {
			return true
		}
	}
	return false
}

// pathShowsInGitStatus invokes `git status --porcelain -- <path>` in
// repoRoot and returns true when the output contains any reference to
// path. Absent git, or empty path, returns true (neutral — don't fail
// the check on infrastructure we can't observe).
var gitStatusOnce sync.Once
var gitStatusAvailable bool

func pathShowsInGitStatus(repoRoot, path string) bool {
	if repoRoot == "" || strings.TrimSpace(path) == "" {
		return true
	}
	gitStatusOnce.Do(func() {
		if _, err := exec.LookPath("git"); err == nil {
			gitStatusAvailable = true
		}
	})
	if !gitStatusAvailable {
		return true
	}
	cmd := exec.Command("git", "status", "--porcelain", "--", path)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return true // git error ≠ evidence of fabrication
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return false
	}
	return true
}

// truncateForPreCheck trims a string for log display.
func truncateForPreCheck(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

