// Package sandattr implements sandbox failure attribution.
// Inspired by Codex CLI's sandbox-aware retry logic:
//
// When a tool call fails inside a sandbox, was it:
// a) A real code bug (agent wrote bad code)
// b) A sandbox restriction (network blocked, file write denied)
// c) A resource limit (OOM, timeout, disk full)
//
// This distinction is critical for retry strategy:
// - Code bugs → retry with error context
// - Sandbox restrictions → retry with adjusted permissions or skip
// - Resource limits → retry with reduced scope
package sandattr

import (
	"regexp"
	"strings"
)

// Cause classifies why a failure occurred.
type Cause string

const (
	CauseCodeBug       Cause = "code_bug"       // agent's code has an error
	CauseSandboxDeny   Cause = "sandbox_deny"    // sandbox blocked the operation
	CauseNetworkBlock  Cause = "network_block"   // network access denied
	CauseFileBlock     Cause = "file_block"      // file write/read denied
	CauseExecBlock     Cause = "exec_block"      // process execution denied
	CauseResourceLimit Cause = "resource_limit"  // OOM, timeout, disk full
	CauseTimeout       Cause = "timeout"         // operation timed out
	CausePermission    Cause = "permission"      // OS permission denied
	CauseUnknown       Cause = "unknown"
)

// Attribution describes the attributed cause of a failure.
type Attribution struct {
	Cause       Cause    `json:"cause"`
	Confidence  float64  `json:"confidence"` // 0-1
	Evidence    []string `json:"evidence"`   // matching patterns
	Suggestion  string   `json:"suggestion"` // what to do about it
	Retryable   bool     `json:"retryable"`
	NeedsEscalation bool `json:"needs_escalation"` // requires permission change
}

// RetryStrategy recommends how to handle the failure.
type RetryStrategy struct {
	ShouldRetry     bool     `json:"should_retry"`
	AdjustSandbox   bool     `json:"adjust_sandbox"`   // relax sandbox rules
	ReduceScope     bool     `json:"reduce_scope"`      // try smaller task
	AddContext       bool     `json:"add_context"`       // add error context to prompt
	SkipTool        bool     `json:"skip_tool"`         // skip the failing tool
	SuggestedPerms  []string `json:"suggested_perms,omitempty"`
}

type pattern struct {
	cause      Cause
	confidence float64
	regex      *regexp.Regexp
	suggestion string
}

var patterns = []pattern{
	// Sandbox/Seatbelt denials
	{CauseSandboxDeny, 0.95, regexp.MustCompile(`(?i)sandbox.*denied|seatbelt.*denied|operation not permitted.*sandbox`), "Adjust sandbox configuration to allow this operation"},
	{CauseSandboxDeny, 0.90, regexp.MustCompile(`(?i)bubblewrap.*error|bwrap.*failed`), "Check Bubblewrap sandbox configuration"},
	{CauseSandboxDeny, 0.85, regexp.MustCompile(`(?i)seccomp.*blocked|syscall.*not allowed`), "The syscall is blocked by seccomp policy"},

	// Network blocks (must be checked before generic sandbox patterns)
	{CauseNetworkBlock, 0.96, regexp.MustCompile(`(?i)network.*unreachable|could not resolve.*sandbox`), "Network access is blocked by sandbox — use offline alternatives"},
	{CauseNetworkBlock, 0.90, regexp.MustCompile(`(?i)ENETUNREACH|connect.*refused.*sandbox`), "Network access denied by sandbox policy"},
	{CauseNetworkBlock, 0.80, regexp.MustCompile(`(?i)dial tcp.*connection refused|no such host`), "Network request failed — may be sandbox or actual network issue"},

	// File access blocks
	{CauseFileBlock, 0.95, regexp.MustCompile(`(?i)read-only file system`), "File system is read-only in sandbox — use writable paths"},
	{CauseFileBlock, 0.90, regexp.MustCompile(`(?i)permission denied.*open|cannot create.*permission`), "File access denied — check sandbox file permissions"},
	{CauseFileBlock, 0.85, regexp.MustCompile(`(?i)EACCES|EPERM.*file`), "File permission error — may be sandbox restriction"},

	// Exec blocks
	{CauseExecBlock, 0.90, regexp.MustCompile(`(?i)exec.*not permitted|cannot execute.*denied`), "Process execution blocked by sandbox"},
	{CauseExecBlock, 0.85, regexp.MustCompile(`(?i)command not found.*sandbox|exec format error`), "Executable not available in sandbox environment"},

	// Resource limits
	{CauseResourceLimit, 0.95, regexp.MustCompile(`(?i)out of memory|OOM|killed.*signal 9|cannot allocate`), "Out of memory — reduce scope or increase memory limit"},
	{CauseResourceLimit, 0.90, regexp.MustCompile(`(?i)no space left on device|disk quota exceeded`), "Disk space exhausted"},
	{CauseResourceLimit, 0.85, regexp.MustCompile(`(?i)too many open files|EMFILE|ENFILE`), "File descriptor limit reached"},

	// Timeouts
	{CauseTimeout, 0.90, regexp.MustCompile(`(?i)context deadline exceeded|timed? ?out|timeout`), "Operation timed out — increase timeout or reduce scope"},

	// OS permissions (not sandbox)
	{CausePermission, 0.80, regexp.MustCompile(`(?i)permission denied|EACCES|EPERM`), "OS-level permission denied"},

	// Code bugs (lower confidence, these are fallbacks)
	{CauseCodeBug, 0.70, regexp.MustCompile(`(?i)syntax error|unexpected token|parse error`), "Syntax error in generated code — fix the code"},
	{CauseCodeBug, 0.70, regexp.MustCompile(`(?i)undefined[:\s]|undeclared|not declared`), "Undeclared variable — fix the code"},
	{CauseCodeBug, 0.70, regexp.MustCompile(`(?i)type mismatch|cannot convert|incompatible types`), "Type error in generated code — fix the code"},
	{CauseCodeBug, 0.70, regexp.MustCompile(`(?i)nil pointer|null pointer|segmentation fault`), "Nil pointer dereference — fix the code"},
	{CauseCodeBug, 0.60, regexp.MustCompile(`(?i)import cycle|circular dependency`), "Import cycle — restructure the code"},
	{CauseCodeBug, 0.60, regexp.MustCompile(`(?i)compilation failed|build failed|does not compile`), "Build failure — fix the code errors"},
}

// Attribute analyzes an error message and attributes its cause.
func Attribute(stderr string) *Attribution {
	if stderr == "" {
		return &Attribution{Cause: CauseUnknown, Confidence: 0}
	}

	var best *Attribution
	for _, p := range patterns {
		if p.regex.MatchString(stderr) {
			if best == nil || p.confidence > best.Confidence {
				match := p.regex.FindString(stderr)
				best = &Attribution{
					Cause:      p.cause,
					Confidence: p.confidence,
					Evidence:   []string{match},
					Suggestion: p.suggestion,
				}
			}
		}
	}

	if best == nil {
		return &Attribution{
			Cause:      CauseUnknown,
			Confidence: 0.5,
			Suggestion: "Unknown failure — check full error output",
		}
	}

	// Set retryable and escalation flags
	switch best.Cause {
	case CauseCodeBug:
		best.Retryable = true
	case CauseSandboxDeny, CauseNetworkBlock, CauseFileBlock, CauseExecBlock:
		best.Retryable = true
		best.NeedsEscalation = true
	case CauseResourceLimit:
		best.Retryable = true
	case CauseTimeout:
		best.Retryable = true
	case CausePermission:
		best.Retryable = false
		best.NeedsEscalation = true
	}

	return best
}

// SuggestRetry returns a retry strategy based on the attribution.
func SuggestRetry(attr *Attribution) *RetryStrategy {
	rs := &RetryStrategy{}

	switch attr.Cause {
	case CauseCodeBug:
		rs.ShouldRetry = true
		rs.AddContext = true
	case CauseSandboxDeny, CauseFileBlock, CauseExecBlock:
		rs.ShouldRetry = true
		rs.AdjustSandbox = true
		rs.SuggestedPerms = suggestPermissions(attr)
	case CauseNetworkBlock:
		rs.ShouldRetry = true
		rs.SkipTool = true // try without the network-dependent tool
	case CauseResourceLimit:
		rs.ShouldRetry = true
		rs.ReduceScope = true
	case CauseTimeout:
		rs.ShouldRetry = true
		rs.ReduceScope = true
	case CausePermission:
		rs.ShouldRetry = false
	default:
		rs.ShouldRetry = attr.Retryable
		rs.AddContext = true
	}

	return rs
}

// IsSandboxCaused returns true if the failure is from sandbox restrictions.
func IsSandboxCaused(stderr string) bool {
	attr := Attribute(stderr)
	switch attr.Cause {
	case CauseSandboxDeny, CauseNetworkBlock, CauseFileBlock, CauseExecBlock:
		return true
	}
	return false
}

// IsCodeBug returns true if the failure appears to be from agent code.
func IsCodeBug(stderr string) bool {
	attr := Attribute(stderr)
	return attr.Cause == CauseCodeBug
}

func suggestPermissions(attr *Attribution) []string {
	var perms []string
	for _, ev := range attr.Evidence {
		lower := strings.ToLower(ev)
		if strings.Contains(lower, "network") || strings.Contains(lower, "connect") {
			perms = append(perms, "network_access")
		}
		if strings.Contains(lower, "file") || strings.Contains(lower, "write") || strings.Contains(lower, "read-only") {
			perms = append(perms, "file_write")
		}
		if strings.Contains(lower, "exec") {
			perms = append(perms, "process_exec")
		}
	}
	if len(perms) == 0 {
		perms = append(perms, "sandbox_relaxed")
	}
	return perms
}
