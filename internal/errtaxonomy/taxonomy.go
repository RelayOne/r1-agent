// Package errtaxonomy implements a structured error taxonomy for retry strategies.
// Inspired by Stoke's existing failure/analyzer.go and claw-code's error handling:
//
// Different error types need different responses:
// - Transient (network timeout): retry immediately
// - Rate limit: retry after backoff
// - Auth failure: stop and alert
// - Syntax error: fix code, then retry
// - Resource exhaustion: reduce scope, then retry
// - Logic error: needs human intervention
//
// This taxonomy enables smart retry policies: don't waste retries on
// permanent failures, and don't give up on transient ones.
package errtaxonomy

import (
	"regexp"
	"strings"
)

// Class is an error classification.
type Class string

const (
	ClassTransient   Class = "transient"   // network, timeout, temporary unavailability
	ClassRateLimit   Class = "rate_limit"  // API rate limits, throttling
	ClassAuth        Class = "auth"        // authentication/authorization failure
	ClassSyntax      Class = "syntax"      // code syntax/compilation errors
	ClassType        Class = "type"        // type errors, wrong arguments
	ClassRuntime     Class = "runtime"     // runtime panics, nil dereference
	ClassResource    Class = "resource"    // out of memory, disk full, context too large
	ClassLogic       Class = "logic"       // test failures, assertion errors
	ClassPermission  Class = "permission"  // file permission, sandbox restriction
	ClassNotFound    Class = "not_found"   // file/module/function not found
	ClassConflict    Class = "conflict"    // merge conflict, lock contention
	ClassUnknown     Class = "unknown"
)

// RetryStrategy describes how to handle an error class.
type RetryStrategy struct {
	ShouldRetry   bool   `json:"should_retry"`
	MaxRetries    int    `json:"max_retries"`
	BackoffMs     int    `json:"backoff_ms"`
	RequiresFix   bool   `json:"requires_fix"`   // needs code change before retry
	Escalate      bool   `json:"escalate"`       // needs human attention
	ReduceScope   bool   `json:"reduce_scope"`   // try with smaller scope
	Description   string `json:"description"`
}

// Strategies maps error classes to retry strategies.
var Strategies = map[Class]RetryStrategy{
	ClassTransient: {
		ShouldRetry: true, MaxRetries: 3, BackoffMs: 1000,
		Description: "Retry with exponential backoff",
	},
	ClassRateLimit: {
		ShouldRetry: true, MaxRetries: 5, BackoffMs: 5000,
		Description: "Wait for rate limit reset, then retry",
	},
	ClassAuth: {
		ShouldRetry: false, Escalate: true,
		Description: "Authentication failure - check credentials",
	},
	ClassSyntax: {
		ShouldRetry: true, MaxRetries: 3, RequiresFix: true,
		Description: "Fix syntax error, then retry",
	},
	ClassType: {
		ShouldRetry: true, MaxRetries: 2, RequiresFix: true,
		Description: "Fix type error, then retry",
	},
	ClassRuntime: {
		ShouldRetry: true, MaxRetries: 2, RequiresFix: true,
		Description: "Fix runtime error, then retry",
	},
	ClassResource: {
		ShouldRetry: true, MaxRetries: 2, ReduceScope: true, BackoffMs: 2000,
		Description: "Reduce scope or free resources, then retry",
	},
	ClassLogic: {
		ShouldRetry: true, MaxRetries: 3, RequiresFix: true,
		Description: "Fix test/logic failure, then retry",
	},
	ClassPermission: {
		ShouldRetry: false, Escalate: true,
		Description: "Permission denied - check access rights",
	},
	ClassNotFound: {
		ShouldRetry: true, MaxRetries: 1, RequiresFix: true,
		Description: "Fix missing reference, then retry",
	},
	ClassConflict: {
		ShouldRetry: true, MaxRetries: 2, BackoffMs: 1000,
		Description: "Resolve conflict, then retry",
	},
	ClassUnknown: {
		ShouldRetry: true, MaxRetries: 1,
		Description: "Unknown error - retry once, then escalate",
	},
}

// Classify determines the error class from an error message.
func Classify(errMsg string) Class {
	lower := strings.ToLower(errMsg)

	for _, rule := range classificationRules {
		if rule.pattern.MatchString(lower) {
			return rule.class
		}
	}

	// Keyword fallbacks
	for _, kw := range keywordRules {
		if strings.Contains(lower, kw.keyword) {
			return kw.class
		}
	}

	return ClassUnknown
}

// Strategy returns the retry strategy for an error message.
func Strategy(errMsg string) RetryStrategy {
	class := Classify(errMsg)
	if s, ok := Strategies[class]; ok {
		return s
	}
	return Strategies[ClassUnknown]
}

// ShouldRetry is a convenience function.
func ShouldRetry(errMsg string) bool {
	return Strategy(errMsg).ShouldRetry
}

// NeedsHuman returns true if the error needs human intervention.
func NeedsHuman(errMsg string) bool {
	return Strategy(errMsg).Escalate
}

// ClassifyMultiple classifies multiple error messages and returns the most severe.
func ClassifyMultiple(messages []string) Class {
	severity := map[Class]int{
		ClassAuth: 10, ClassPermission: 9, ClassResource: 8,
		ClassConflict: 7, ClassRuntime: 6, ClassLogic: 5,
		ClassType: 4, ClassSyntax: 3, ClassNotFound: 2,
		ClassRateLimit: 1, ClassTransient: 0, ClassUnknown: -1,
	}

	maxSev := -2
	maxClass := ClassUnknown

	for _, msg := range messages {
		c := Classify(msg)
		if sev, ok := severity[c]; ok && sev > maxSev {
			maxSev = sev
			maxClass = c
		}
	}
	return maxClass
}

type rule struct {
	pattern *regexp.Regexp
	class   Class
}

type kwRule struct {
	keyword string
	class   Class
}

var classificationRules = []rule{
	// Rate limits
	{regexp.MustCompile(`rate.?limit|429|too many requests|throttl`), ClassRateLimit},
	// Auth
	{regexp.MustCompile(`401|403|unauthorized|forbidden|invalid.*token|invalid.*key|auth.*fail`), ClassAuth},
	// Network/transient
	{regexp.MustCompile(`timeout|timed?\s*out|connection\s*(refused|reset)|econnrefused|econnreset|enetunreach|503|502|504`), ClassTransient},
	// Syntax
	{regexp.MustCompile(`syntax\s*error|unexpected\s*token|parse\s*error|unterminated|expected\s*[;:{}\[\])]`), ClassSyntax},
	// Type errors
	{regexp.MustCompile(`type\s*error|cannot\s*use.*as|incompatible\s*type|undefined:\s*\w+|not\s*assignable`), ClassType},
	// Runtime
	{regexp.MustCompile(`panic|nil\s*pointer|segfault|stack\s*overflow|index\s*out\s*of\s*(range|bounds)`), ClassRuntime},
	// Resource
	{regexp.MustCompile(`out\s*of\s*memory|oom|disk\s*(full|space)|context.*too\s*large|token\s*limit|no\s*space`), ClassResource},
	// Logic/test failures
	{regexp.MustCompile(`test.*fail|assert|expected.*got|mismatch|fail:`), ClassLogic},
	// Permission
	{regexp.MustCompile(`permission\s*denied|access\s*denied|eperm|eacces|sandbox`), ClassPermission},
	// Not found
	{regexp.MustCompile(`not\s*found|no\s*such\s*file|enoent|module.*not\s*found|cannot\s*find|import.*not\s*found`), ClassNotFound},
	// Conflict
	{regexp.MustCompile(`conflict|merge.*fail|lock.*held|deadlock`), ClassConflict},
}

var keywordRules = []kwRule{
	{"timeout", ClassTransient},
	{"refused", ClassTransient},
	{"syntax", ClassSyntax},
	{"undefined", ClassType},
	{"panic", ClassRuntime},
	{"permission", ClassPermission},
	{"not found", ClassNotFound},
}
