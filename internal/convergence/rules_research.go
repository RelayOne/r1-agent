package convergence

import (
	"regexp"
	"strings"
)

// ResearchRules returns rules derived from analysis of 100+ research documents
// covering AI agent failure modes, caching pitfalls, UX completeness, and
// concurrency safety. These supplement PostmortemRules with insights from
// systematic study of AI-generated code failures.
func ResearchRules() []Rule {
	return []Rule{
		// AI agent failure modes (from wf-228f06ee research)
		testAssertionWeakeningRule(),
		agentSelfReportUnverifiedRule(),

		// Concurrency safety (from wf-c8170573 research)
		unboundedGoroutineSpawnRule(),

		// Caching correctness (from wf-44e469d3 research)
		cacheNoTTLRule(),

		// UX completeness (from wf-c8343ed0 research)
		missingErrorStateUIRule(),

		// Retry correctness
		retryWithoutBackoffRule(),
	}
}

// --- AI agent failure mode rules ---

// testAssertionWeakeningRule detects assertion removals or downgrades in test files.
// Research found agents "delete tests to make them pass" — AI tests score 30-40%
// on mutation testing despite 90%+ line coverage.
func testAssertionWeakeningRule() Rule {
	// Patterns that indicate weakened assertions
	tautology := regexp.MustCompile(`assert\.True\(\s*true\s*\)|expect\(\s*true\s*\)\.toBe\(\s*true\s*\)|assert\.Equal\(\s*1\s*,\s*1\s*\)`)
	// Test function that only calls t.Log or has no assertions
	emptyBody := regexp.MustCompile(`func Test\w+\(t \*testing\.T\)\s*\{\s*(t\.Log|t\.Skip)\(`)
	return Rule{
		ID: "test-assertion-weakening", Name: "No weakened test assertions", Category: CatTestCoverage,
		Severity: SevBlocking, Enabled: true,
		Description: "Test contains tautological assertions or has been gutted — possible agent deception",
		Check: func(file string, content []byte) []Finding {
			if !isTestFile(file) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				if tautology.MatchString(line) {
					findings = append(findings, Finding{
						RuleID:      "test-assertion-weakening",
						Category:    CatTestCoverage,
						Severity:    SevBlocking,
						File:        file,
						Line:        i + 1,
						Description: "Tautological assertion (always passes) — test proves nothing",
						Suggestion:  "Replace with meaningful assertion that tests actual behavior",
						Evidence:    strings.TrimSpace(line),
					})
				}
				if emptyBody.MatchString(line) {
					findings = append(findings, Finding{
						RuleID:      "test-assertion-weakening",
						Category:    CatTestCoverage,
						Severity:    SevBlocking,
						File:        file,
						Line:        i + 1,
						Description: "Test function has no assertions — was it gutted to pass?",
						Suggestion:  "Add real assertions that verify behavior, not just existence",
						Evidence:    strings.TrimSpace(line),
					})
				}
			}
			return findings
		},
	}
}

// agentSelfReportUnverifiedRule flags completion markers without evidence.
// Research found LLMs are overconfident in 84.3% of scenarios. Users spend
// 30-40% of time re-verifying agent claims.
func agentSelfReportUnverifiedRule() Rule {
	completionMarker := regexp.MustCompile(`(?i)\b(DONE|COMPLETED|FINISHED)\b`)
	evidenceRef := regexp.MustCompile(`(?i)(ok\s+\w+/|coverage:\s*\d|diff\s|commit\s[0-9a-f]|✓\s+\d|PASS\s+\d)`)
	return Rule{
		ID: "agent-self-report", Name: "Agent completion claims need evidence", Category: CatConsistency,
		Severity: SevMajor, Enabled: true,
		Description: "File contains completion claims without test/diff evidence — possible agent overconfidence",
		Check: func(file string, content []byte) []Finding {
			// Only check agent output/log files, not source code
			if !strings.Contains(file, "output") && !strings.Contains(file, "log") &&
				!strings.Contains(file, "report") && !strings.Contains(file, "result") {
				return nil
			}
			if isGoFile(file) || isTestFile(file) {
				return nil
			}
			s := string(content)
			if !completionMarker.MatchString(s) {
				return nil
			}
			if evidenceRef.MatchString(s) {
				return nil // has evidence references
			}
			return []Finding{{
				RuleID:      "agent-self-report",
				Category:    CatConsistency,
				Severity:    SevMajor,
				File:        file,
				Line:        1,
				Description: "Completion claim without test/diff evidence — verify manually",
				Suggestion:  "Cross-check agent claims against actual test results and git diff",
			}}
		},
	}
}

// --- Concurrency safety ---

// unboundedGoroutineSpawnRule flags goroutine creation inside loops without
// a semaphore or worker pool. Research on workspace isolation found this is
// a leading cause of OOM and resource exhaustion in production.
func unboundedGoroutineSpawnRule() Rule {
	goStmt := regexp.MustCompile(`\bgo\s+(func\s*\(|\w+\()`)
	semaphore := regexp.MustCompile(`(sem|semaphore|limiter|pool|workers?|tokens?)\s*(chan|<-|\.Acquire)`)
	return Rule{
		ID: "unbounded-goroutine", Name: "No unbounded goroutine spawning in loops", Category: CatReliability,
		Severity: SevBlocking, Enabled: true,
		Description: "Goroutines spawned in loop without rate limiting — causes OOM under load",
		Check: func(file string, content []byte) []Finding {
			if !isGoFile(file) || isTestFile(file) {
				return nil
			}
			s := string(content)
			// If file has semaphore/pool patterns, it's likely bounded
			if semaphore.MatchString(s) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(s, "\n")
			forDepth := 0
			for i, line := range lines {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "for ") || trimmed == "for {" {
					forDepth++
				}
				if forDepth > 0 {
					forDepth += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
					if forDepth < 0 {
						forDepth = 0
					}
					if goStmt.MatchString(trimmed) {
						// Check if there's a WaitGroup (acceptable with bounded input)
						hasWG := false
						start := i - 10
						if start < 0 {
							start = 0
						}
						for j := start; j < i; j++ {
							if strings.Contains(lines[j], "WaitGroup") || strings.Contains(lines[j], "errgroup") {
								hasWG = true
								break
							}
						}
						// WaitGroup alone doesn't bound concurrency, but it's a signal
						// the developer thought about it. Still flag but as major.
						sev := SevBlocking
						if hasWG {
							sev = SevMajor
						}
						findings = append(findings, Finding{
							RuleID:      "unbounded-goroutine",
							Category:    CatReliability,
							Severity:    sev,
							File:        file,
							Line:        i + 1,
							Description: "Goroutine spawned inside loop without concurrency limit — OOM risk",
							Suggestion:  "Use a semaphore channel or worker pool: sem := make(chan struct{}, maxWorkers)",
							Evidence:    strings.TrimSpace(line),
						})
					}
				}
			}
			return findings
		},
	}
}

// --- Caching correctness ---

// cacheNoTTLRule flags cache set operations without a TTL. Research on caching
// documents thundering herd failures and stale data incidents from missing TTLs.
func cacheNoTTLRule() Rule {
	// Cache set without expiry
	cacheSet := regexp.MustCompile(`\.(Set|Put|Add|Store)\([^)]*\)`)
	cacheSetWithTTL := regexp.MustCompile(`\.(Set|Put|Add|Store)\([^)]*,\s*(time\.|duration|ttl|expir|timeout)`)
	cacheSetEx := regexp.MustCompile(`\.(SetEx|SetNX|PutWithTTL|SetWithExpiry)\(`)
	return Rule{
		ID: "cache-ttl", Name: "Cache entries must have TTL", Category: CatReliability,
		Severity: SevMajor, Enabled: true,
		Description: "Cache set without TTL causes unbounded memory growth and stale data",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil
			}
			s := string(content)
			// Only check files that look cache-related
			if !strings.Contains(s, "cache") && !strings.Contains(s, "Cache") &&
				!strings.Contains(s, "redis") && !strings.Contains(s, "memcache") {
				return nil
			}
			var findings []Finding
			lines := strings.Split(s, "\n")
			for i, line := range lines {
				if cacheSet.MatchString(line) && !cacheSetWithTTL.MatchString(line) && !cacheSetEx.MatchString(line) {
					// Skip if it's clearly not a cache operation
					if !strings.Contains(strings.ToLower(line), "cache") {
						continue
					}
					findings = append(findings, Finding{
						RuleID:      "cache-ttl",
						Category:    CatReliability,
						Severity:    SevMajor,
						File:        file,
						Line:        i + 1,
						Description: "Cache set without TTL — unbounded growth and stale data risk",
						Suggestion:  "Add TTL with jitter: ttl := baseTTL + rand.Duration(jitter)",
						Evidence:    strings.TrimSpace(line),
					})
				}
			}
			return findings
		},
	}
}

// --- UX completeness ---

// missingErrorStateUIRule ensures data-fetching UI components handle error states.
// Research: "every shipped-but-broken UI shares the same root cause: someone forgot a state."
func missingErrorStateUIRule() Rule {
	fetchPattern := regexp.MustCompile(`(useQuery|useSWR|useFetch|fetch\(|axios\.|\.get\(|\.post\()`)
	errorHandling := regexp.MustCompile(`(\berror\b.*return|isError|error\s*&&|error\s*\?|\.catch\(|onError|ErrorBoundary|fallback|if\s*\(\s*error\s*\))`)
	return Rule{
		ID: "missing-error-state", Name: "Data-fetching UI must handle error state", Category: CatUXQuality,
		Severity: SevMajor, Enabled: true,
		Description: "Component fetches data but has no error handling UI — blank screen on failure",
		Check: func(file string, content []byte) []Finding {
			if !isFrontendFile(file) || isTestFile(file) {
				return nil
			}
			s := string(content)
			if !fetchPattern.MatchString(s) {
				return nil // no data fetching
			}
			if errorHandling.MatchString(s) {
				return nil // has error handling
			}
			return []Finding{{
				RuleID:      "missing-error-state",
				Category:    CatUXQuality,
				Severity:    SevMajor,
				File:        file,
				Line:        1,
				Description: "Component fetches data but never renders an error state",
				Suggestion:  "Add error handling: if (error) return <ErrorMessage error={error} />",
			}}
		},
	}
}

// --- Retry correctness ---

// retryWithoutBackoffRule flags retry loops that use constant delays.
// Constant-delay retry causes thundering herd and wastes resources.
func retryWithoutBackoffRule() Rule {
	retryLoop := regexp.MustCompile(`for\s+.*(?:retry|attempt|tries|i\s*:?=)`)
	sleepCall := regexp.MustCompile(`time\.Sleep\((\d+\s*\*\s*time\.\w+|time\.\w+)\)`)
	backoff := regexp.MustCompile(`(backoff|Backoff|exponential|2\s*\*|<<|attempt\s*\*|tries\s*\*|pow|math\.Pow)`)
	return Rule{
		ID: "retry-backoff", Name: "Retry loops must use backoff", Category: CatReliability,
		Severity: SevMajor, Enabled: true,
		Description: "Retry with constant delay causes thundering herd — use exponential backoff",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			inRetry := false
			retryStart := 0
			depth := 0
			for i, line := range lines {
				trimmed := strings.TrimSpace(line)
				if retryLoop.MatchString(trimmed) {
					inRetry = true
					retryStart = i
					depth = 0
				}
				if inRetry {
					depth += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
					if depth <= 0 && i > retryStart {
						inRetry = false
						continue
					}
					if sleepCall.MatchString(trimmed) {
						// Check surrounding context for backoff
						start := retryStart
						end := i + 3
						if end > len(lines) {
							end = len(lines)
						}
						hasBackoff := false
						for j := start; j < end; j++ {
							if backoff.MatchString(lines[j]) {
								hasBackoff = true
								break
							}
						}
						if !hasBackoff {
							findings = append(findings, Finding{
								RuleID:      "retry-backoff",
								Category:    CatReliability,
								Severity:    SevMajor,
								File:        file,
								Line:        i + 1,
								Description: "Retry loop with constant sleep — use exponential backoff",
								Suggestion:  "Use: delay := baseDelay * time.Duration(1<<attempt) + jitter",
								Evidence:    strings.TrimSpace(line),
							})
						}
					}
				}
			}
			return findings
		},
	}
}
