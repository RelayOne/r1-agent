package convergence

import (
	"path/filepath"
	"regexp"
	"strings"
)

// PostmortemRules returns rules derived from real production incidents.
// These catch bugs that only show up in postmortems: numerical precision,
// resource leaks, distributed system correctness, time handling, and
// AI agent operational safety.
func PostmortemRules() []Rule {
	return []Rule{
		// Numerical correctness
		floatMoneyRule(),
		floatEqualityRule(),
		intOverflowRule(),
		jsonBigIntRule(),

		// Resource lifecycle
		resourceLeakRule(),
		goroutineContextRule(),

		// Distributed systems
		retryWithoutJitterRule(),
		timeoutHierarchyRule(),

		// Time & clock
		wallClockDurationRule(),
		naiveTimeComparisonRule(),

		// Build & version discipline
		generatedCodeEditRule(),
		latestTagRule(),

		// Agent operational safety
		agentDiffSizeRule(),
		agentScopeMarkerRule(),

		// Graceful shutdown
		mainWithoutSignalRule(),

		// Serialization
		sensitiveFieldSerializationRule(),
	}
}

// --- Numerical correctness (the "money bug" family) ---

func floatMoneyRule() Rule {
	// Detect float64 used with money/price/cost/amount/balance/currency variables
	re := regexp.MustCompile(`(?i)(price|amount|balance|cost|total|subtotal|tax|fee|charge|payment|revenue|salary|wage|discount|refund)\s*\w*\s*(float32|float64|number)`)
	// Also catch: var price float64, amount := 0.0
	reVar := regexp.MustCompile(`(?i)(price|amount|balance|cost|total|subtotal|tax|fee|charge|payment|revenue|salary|wage|discount|refund)\w*\s*:?=\s*\d+\.\d+`)
	return Rule{
		ID: "no-float-money", Name: "No floating point for money", Category: CatReliability,
		Severity: SevBlocking, Enabled: true,
		Description: "Floating point arithmetic produces rounding errors in financial calculations — use integer cents or decimal library",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				if re.MatchString(line) || reVar.MatchString(line) {
					findings = append(findings, Finding{
						RuleID:      "no-float-money",
						Category:    CatReliability,
						Severity:    SevBlocking,
						File:        file,
						Line:        i + 1,
						Description: "Float used for money — produces rounding errors ($0.1 + $0.2 != $0.3)",
						Suggestion:  "Use integer cents (amount_cents int64) or shopspring/decimal",
						Evidence:    strings.TrimSpace(line),
					})
				}
			}
			return findings
		},
	}
}

func floatEqualityRule() Rule {
	re := regexp.MustCompile(`\bfloat(32|64)\b`)
	eqCheck := regexp.MustCompile(`==\s*\d+\.\d+|\d+\.\d+\s*==`)
	return Rule{
		ID: "no-float-equality", Name: "No float equality comparison", Category: CatReliability,
		Severity: SevBlocking, Enabled: true,
		Description: "Float equality comparison (==) fails due to precision — use epsilon comparison",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil
			}
			s := string(content)
			if !re.MatchString(s) {
				return nil // no floats in file
			}
			var findings []Finding
			lines := strings.Split(s, "\n")
			for i, line := range lines {
				if eqCheck.MatchString(line) {
					findings = append(findings, Finding{
						RuleID:      "no-float-equality",
						Category:    CatReliability,
						Severity:    SevBlocking,
						File:        file,
						Line:        i + 1,
						Description: "Float equality comparison — use math.Abs(a-b) < epsilon",
						Suggestion:  "Compare with epsilon: math.Abs(a-b) < 1e-9",
						Evidence:    strings.TrimSpace(line),
					})
				}
			}
			return findings
		},
	}
}

func intOverflowRule() Rule {
	// Go silently wraps on integer overflow. Flag unchecked arithmetic on int32/uint32.
	re := regexp.MustCompile(`\b(int32|uint32|int16|uint16|int8|uint8)\b`)
	arith := regexp.MustCompile(`\*=|\+=`)
	return Rule{
		ID: "int-overflow-risk", Name: "Integer overflow risk on narrow types", Category: CatReliability,
		Severity: SevMajor, Enabled: true,
		Description: "Arithmetic on narrow integer types silently wraps in Go — use int64 or check bounds",
		Check: func(file string, content []byte) []Finding {
			if !isGoFile(file) || isTestFile(file) {
				return nil
			}
			s := string(content)
			if !re.MatchString(s) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(s, "\n")
			for i, line := range lines {
				if arith.MatchString(line) && re.MatchString(line) {
					findings = append(findings, Finding{
						RuleID:      "int-overflow-risk",
						Category:    CatReliability,
						Severity:    SevMajor,
						File:        file,
						Line:        i + 1,
						Description: "Arithmetic on narrow int type — Go silently wraps on overflow",
						Suggestion:  "Use int64, or add bounds checking before arithmetic",
						Evidence:    strings.TrimSpace(line),
					})
				}
			}
			return findings
		},
	}
}

func jsonBigIntRule() Rule {
	// JSON number → float64 loses precision above 2^53. Flag JSON marshaling of int64/uint64.
	re := regexp.MustCompile(`json:"[^"]*"\s*$`)
	bigInt := regexp.MustCompile(`(int64|uint64)`)
	return Rule{
		ID: "json-bigint", Name: "JSON int64 precision loss", Category: CatReliability,
		Severity: SevMajor, Enabled: true,
		Description: "JSON serializes int64 as number — JavaScript loses precision above 2^53",
		Check: func(file string, content []byte) []Finding {
			if !isGoFile(file) || isTestFile(file) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				if re.MatchString(line) && bigInt.MatchString(line) {
					// Check if it has ,string tag
					if strings.Contains(line, `,string"`) {
						continue // already serialized as string
					}
					findings = append(findings, Finding{
						RuleID:      "json-bigint",
						Category:    CatReliability,
						Severity:    SevMajor,
						File:        file,
						Line:        i + 1,
						Description: "int64/uint64 in JSON struct loses precision above 2^53 in JavaScript",
						Suggestion:  "Add ,string to json tag: `json:\"id,string\"` to serialize as string",
						Evidence:    strings.TrimSpace(line),
					})
				}
			}
			return findings
		},
	}
}

// --- Resource lifecycle ---

func resourceLeakRule() Rule {
	// Detect Open/Dial/Connect without defer Close
	openCall := regexp.MustCompile(`\b(Open|Dial|Connect|NewClient|Acquire)\(`)
	deferClose := regexp.MustCompile(`defer\s+\w+\.Close\(\)`)
	return Rule{
		ID: "resource-leak", Name: "Resources must be closed", Category: CatReliability,
		Severity: SevBlocking, Enabled: true,
		Description: "Resource opened without defer Close — file descriptor / connection leak",
		Check: func(file string, content []byte) []Finding {
			if !isGoFile(file) || isTestFile(file) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				if openCall.MatchString(line) && strings.Contains(line, ":=") {
					// Check next 3 lines for defer Close
					found := false
					end := i + 4
					if end > len(lines) {
						end = len(lines)
					}
					for j := i + 1; j < end; j++ {
						if deferClose.MatchString(lines[j]) || strings.Contains(lines[j], "defer") {
							found = true
							break
						}
					}
					if !found {
						findings = append(findings, Finding{
							RuleID:      "resource-leak",
							Category:    CatReliability,
							Severity:    SevBlocking,
							File:        file,
							Line:        i + 1,
							Description: "Resource opened without defer Close — leak risk",
							Suggestion:  "Add defer resource.Close() immediately after error check",
							Evidence:    strings.TrimSpace(line),
						})
					}
				}
			}
			return findings
		},
	}
}

func goroutineContextRule() Rule {
	goStmt := regexp.MustCompile(`go\s+func\s*\(`)
	ctxParam := regexp.MustCompile(`\bctx\b`)
	return Rule{
		ID: "goroutine-context", Name: "Goroutines should respect context cancellation", Category: CatReliability,
		Severity: SevMajor, Enabled: true,
		Description: "Goroutine not using context — won't stop when parent is cancelled",
		Check: func(file string, content []byte) []Finding {
			if !isGoFile(file) || isTestFile(file) {
				return nil
			}
			// Only flag if the enclosing function has a ctx parameter
			s := string(content)
			if !strings.Contains(s, "context.Context") {
				return nil
			}
			var findings []Finding
			lines := strings.Split(s, "\n")
			for i, line := range lines {
				if goStmt.MatchString(line) {
					// Check if ctx is passed or used in the goroutine
					end := i + 8
					if end > len(lines) {
						end = len(lines)
					}
					hasCtx := false
					for j := i; j < end; j++ {
						if ctxParam.MatchString(lines[j]) {
							hasCtx = true
							break
						}
					}
					if !hasCtx {
						findings = append(findings, Finding{
							RuleID:      "goroutine-context",
							Category:    CatReliability,
							Severity:    SevMajor,
							File:        file,
							Line:        i + 1,
							Description: "Goroutine doesn't use context — won't stop on cancellation",
							Suggestion:  "Pass ctx to goroutine and select on ctx.Done()",
							Evidence:    strings.TrimSpace(line),
						})
					}
				}
			}
			return findings
		},
	}
}

// --- Distributed systems ---

func retryWithoutJitterRule() Rule {
	retrySleep := regexp.MustCompile(`time\.Sleep\(.*[*].*attempt`)
	jitter := regexp.MustCompile(`rand\.|jitter|Jitter`)
	return Rule{
		ID: "retry-jitter", Name: "Retry backoff must include jitter", Category: CatReliability,
		Severity: SevBlocking, Enabled: true,
		Description: "Exponential backoff without jitter causes thundering herd on recovery",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil
			}
			s := string(content)
			if !retrySleep.MatchString(s) {
				return nil
			}
			if jitter.MatchString(s) {
				return nil // has jitter
			}
			var findings []Finding
			lines := strings.Split(s, "\n")
			for i, line := range lines {
				if retrySleep.MatchString(line) {
					findings = append(findings, Finding{
						RuleID:      "retry-jitter",
						Category:    CatReliability,
						Severity:    SevBlocking,
						File:        file,
						Line:        i + 1,
						Description: "Retry backoff without jitter — causes thundering herd on recovery",
						Suggestion:  "Add random jitter: sleep(baseDelay * 2^attempt + rand(0, baseDelay))",
						Evidence:    strings.TrimSpace(line),
					})
				}
			}
			return findings
		},
	}
}

func timeoutHierarchyRule() Rule {
	// Inner timeout must be less than outer timeout
	// Detect context.WithTimeout values that could violate hierarchy
	re := regexp.MustCompile(`context\.WithTimeout\([^,]+,\s*(\d+)\s*\*\s*time\.(Second|Minute)`)
	return Rule{
		ID: "timeout-hierarchy", Name: "Timeout hierarchy must be correct", Category: CatReliability,
		Severity: SevMajor, Enabled: true,
		Description: "Inner timeouts must be shorter than outer timeouts — otherwise inner never fires",
		Check: func(file string, content []byte) []Finding {
			// Advisory rule — can't fully validate without type analysis
			// Flag when multiple timeouts exist in same function as a review hint
			if !isGoFile(file) || isTestFile(file) {
				return nil
			}
			s := string(content)
			matches := re.FindAllStringIndex(s, -1)
			if len(matches) <= 1 {
				return nil
			}
			// Multiple timeouts in same file — advisory
			return []Finding{{
				RuleID:      "timeout-hierarchy",
				Category:    CatReliability,
				Severity:    SevMajor,
				File:        file,
				Line:        1,
				Description: "Multiple context.WithTimeout in file — verify inner < outer",
				Suggestion:  "Document timeout hierarchy: HTTP handler (30s) > DB query (5s) > cache (1s)",
			}}
		},
	}
}

// --- Time & clock ---

func wallClockDurationRule() Rule {
	return Rule{
		ID: "wall-clock-duration", Name: "Use monotonic clock for durations", Category: CatReliability,
		Severity: SevMajor, Enabled: true,
		Description: "time.Since/Sub in Go uses monotonic clock — safe. But explicit wall-clock subtraction is not.",
		Check: func(file string, content []byte) []Finding {
			// Go's time.Now() embeds monotonic reading, so time.Since is actually safe.
			// Flag explicit wall-clock patterns only.
			if !isGoFile(file) || isTestFile(file) {
				return nil
			}
			wallSub := regexp.MustCompile(`\.Unix\(\)\s*-`)
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				if wallSub.MatchString(line) {
					findings = append(findings, Finding{
						RuleID:      "wall-clock-duration",
						Category:    CatReliability,
						Severity:    SevMinor,
						File:        file,
						Line:        i + 1,
						Description: "Wall-clock subtraction for duration — use time.Since() which uses monotonic clock",
						Suggestion:  "Replace with time.Since(start) or end.Sub(start)",
						Evidence:    strings.TrimSpace(line),
					})
				}
			}
			return findings
		},
	}
}

func naiveTimeComparisonRule() Rule {
	// Comparing time.Time with == instead of .Equal()
	re := regexp.MustCompile(`time\.Time.*==|==.*time\.Time`)
	return Rule{
		ID: "time-comparison", Name: "Use time.Equal() not ==", Category: CatReliability,
		Severity: SevMajor, Enabled: true,
		Description: "time.Time == comparison considers monotonic clock reading — use .Equal() for wall-clock comparison",
		Check: func(file string, content []byte) []Finding {
			if !isGoFile(file) || isTestFile(file) {
				return nil
			}
			return regexCheck(re, file, content, "time-comparison", CatReliability, SevMajor,
				"time.Time == comparison includes monotonic reading — use .Equal()",
				"Replace a == b with a.Equal(b) for correct time comparison")
		},
	}
}

// --- Build & version discipline ---

func generatedCodeEditRule() Rule {
	generatedMarker := regexp.MustCompile(`(?i)(DO NOT EDIT|generated by|auto-generated|@generated|code generated)`)
	return Rule{
		ID: "no-edit-generated", Name: "Generated code must not be hand-edited", Category: CatConsistency,
		Severity: SevBlocking, Enabled: true,
		Description: "File is marked as generated — hand edits will be overwritten on next generation",
		Check: func(file string, content []byte) []Finding {
			// This rule flags generated files that appear in the diff.
			// The convergence gate only sees modified files, so if a generated
			// file is in the set, it was hand-edited.
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				if i > 10 {
					break // generated markers are in the first few lines
				}
				if generatedMarker.MatchString(line) {
					return []Finding{{
						RuleID:      "no-edit-generated",
						Category:    CatConsistency,
						Severity:    SevBlocking,
						File:        file,
						Line:        i + 1,
						Description: "Generated file was hand-edited — changes will be lost on regeneration",
						Suggestion:  "Edit the generator input or template instead, then regenerate",
						Evidence:    strings.TrimSpace(line),
					}}
				}
			}
			return nil
		},
	}
}

func latestTagRule() Rule {
	re := regexp.MustCompile(`:latest\b|image:\s*\w+/\w+\s*$`)
	return Rule{
		ID: "no-latest-tag", Name: "No :latest tag in production", Category: CatReliability,
		Severity: SevBlocking, Enabled: true,
		Description: "Using :latest tag makes builds non-reproducible — pin to specific version",
		Check: func(file string, content []byte) []Finding {
			if !strings.Contains(file, "Dockerfile") && !strings.Contains(file, "docker-compose") &&
				!strings.HasSuffix(file, ".yaml") && !strings.HasSuffix(file, ".yml") {
				return nil
			}
			if isTestFile(file) {
				return nil
			}
			return regexCheck(re, file, content, "no-latest-tag", CatReliability, SevBlocking,
				"Container image with :latest tag — non-reproducible build",
				"Pin to specific version: image:1.2.3 or image@sha256:...")
		},
	}
}

// --- Agent operational safety ---

func agentDiffSizeRule() Rule {
	return Rule{
		ID: "agent-diff-size", Name: "Agent diff size limit", Category: CatConsistency,
		Severity: SevMajor, Enabled: true,
		Description: "Changed file is excessively large — may indicate agent generating boilerplate instead of solving the problem",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil
			}
			lines := len(strings.Split(string(content), "\n"))
			if lines > 800 {
				return []Finding{{
					RuleID:      "agent-diff-size",
					Category:    CatConsistency,
					Severity:    SevMajor,
					File:        file,
					Line:        1,
					Description: "File exceeds 800 lines — may indicate generated boilerplate or scope creep",
					Suggestion:  "Split into smaller, focused files or verify the size is justified",
				}}
			}
			return nil
		},
	}
}

func agentScopeMarkerRule() Rule {
	// Detect files that shouldn't be modified by agents
	protectedPatterns := []string{
		".env", "credentials", "secrets", ".pem", ".key",
		"go.sum", "package-lock.json", "yarn.lock", "Cargo.lock",
	}
	return Rule{
		ID: "agent-scope", Name: "Agent must not modify protected files", Category: CatSecurity,
		Severity: SevBlocking, Enabled: true,
		Description: "Protected file modified — lockfiles, secrets, and credentials require human review",
		Check: func(file string, content []byte) []Finding {
			base := filepath.Base(file)
			for _, p := range protectedPatterns {
				if strings.Contains(base, p) || strings.HasSuffix(file, p) {
					return []Finding{{
						RuleID:      "agent-scope",
						Category:    CatSecurity,
						Severity:    SevBlocking,
						File:        file,
						Line:        1,
						Description: "Protected file modified — requires human review",
						Suggestion:  "Lockfiles, secrets, and credentials must not be modified by agents",
					}}
				}
			}
			return nil
		},
	}
}

// --- Graceful shutdown ---

func mainWithoutSignalRule() Rule {
	mainFunc := regexp.MustCompile(`^func main\(\)`)
	signalNotify := regexp.MustCompile(`signal\.Notify|signal\.NotifyContext`)
	return Rule{
		ID: "graceful-shutdown", Name: "Main must handle OS signals", Category: CatReliability,
		Severity: SevMajor, Enabled: true,
		Description: "Main function without signal handling — SIGTERM will kill in-flight work",
		Check: func(file string, content []byte) []Finding {
			if !isGoFile(file) || isTestFile(file) || !strings.HasSuffix(file, "main.go") {
				return nil
			}
			s := string(content)
			if !mainFunc.MatchString(s) {
				return nil
			}
			if signalNotify.MatchString(s) {
				return nil
			}
			lines := strings.Split(s, "\n")
			for i, line := range lines {
				if mainFunc.MatchString(line) {
					return []Finding{{
						RuleID:      "graceful-shutdown",
						Category:    CatReliability,
						Severity:    SevMajor,
						File:        file,
						Line:        i + 1,
						Description: "Main function without signal.Notify — SIGTERM kills in-flight work",
						Suggestion:  "Add signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT) for graceful shutdown",
					}}
				}
			}
			return nil
		},
	}
}

// --- Serialization ---

func sensitiveFieldSerializationRule() Rule {
	jsonTag := regexp.MustCompile(`json:"[^"]*"`)
	sensitiveField := regexp.MustCompile(`(?i)(password|secret|token|apikey|api_key|private_key|ssn|credit_card|cvv)`)
	dashTag := regexp.MustCompile(`json:"-"`)
	return Rule{
		ID: "sensitive-serialization", Name: "Sensitive fields must be excluded from JSON", Category: CatSecurity,
		Severity: SevBlocking, Enabled: true,
		Description: "Sensitive field has JSON tag — will be serialized in API responses",
		Check: func(file string, content []byte) []Finding {
			if !isGoFile(file) || isTestFile(file) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				if sensitiveField.MatchString(line) && jsonTag.MatchString(line) && !dashTag.MatchString(line) {
					findings = append(findings, Finding{
						RuleID:      "sensitive-serialization",
						Category:    CatSecurity,
						Severity:    SevBlocking,
						File:        file,
						Line:        i + 1,
						Description: "Sensitive field has JSON tag — will appear in API responses",
						Suggestion:  "Use `json:\"-\"` to exclude from serialization",
						Evidence:    strings.TrimSpace(line),
					})
				}
			}
			return findings
		},
	}
}
