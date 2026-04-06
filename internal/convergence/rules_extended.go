package convergence

import (
	"regexp"
	"strings"
)

// ExtendedRules returns rules for architecture, concurrency, config, database,
// observability, performance, error handling, and AI-specific failure modes.
// These supplement DefaultRules() to enforce the full engineering checklist.
func ExtendedRules() []Rule {
	return []Rule{
		// Concurrency safety
		goroutineWithoutRecoverRule(),
		mutexWithoutDeferUnlockRule(),
		channelWithoutTimeoutRule(),

		// Configuration & secrets
		hardcodedURLRule(),
		hardcodedPortRule(),
		directEnvAccessRule(),

		// Database & data safety
		destructiveMigrationRule(),
		sqlInLoopRule(),

		// Observability
		rawLogRule(),

		// Error handling
		contextNotPropagatedRule(),
		httpWithoutTimeoutRule(),

		// AI-specific failure modes
		hallucinatedGoImportRule(),
		deferredHardProblemRule(),
	}
}

// --- Concurrency safety rules ---

func goroutineWithoutRecoverRule() Rule {
	goStmt := regexp.MustCompile(`go\s+func\s*\(`)
	recoverCall := regexp.MustCompile(`recover\(\)`)
	return Rule{
		ID: "goroutine-recover", Name: "Goroutines must have panic recovery", Category: CatReliability,
		Severity: SevBlocking, Enabled: true,
		Description: "Goroutines without defer/recover crash the entire process on panic",
		Check: func(file string, content []byte) []Finding {
			if !isGoFile(file) || isTestFile(file) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				if goStmt.MatchString(line) {
					found := false
					end := i + 6
					if end > len(lines) {
						end = len(lines)
					}
					for j := i; j < end; j++ {
						if recoverCall.MatchString(lines[j]) {
							found = true
							break
						}
					}
					if !found {
						findings = append(findings, Finding{
							RuleID:      "goroutine-recover",
							Category:    CatReliability,
							Severity:    SevBlocking,
							File:        file,
							Line:        i + 1,
							Description: "Goroutine launched without defer/recover — panics will crash the process",
							Suggestion:  "Add defer func() { if r := recover(); r != nil { log.Error(...) } }() at goroutine start",
							Evidence:    strings.TrimSpace(line),
						})
					}
				}
			}
			return findings
		},
	}
}

func mutexWithoutDeferUnlockRule() Rule {
	lockCall := regexp.MustCompile(`\.(Lock|RLock)\(\)`)
	deferUnlock := regexp.MustCompile(`defer\s+\w+\.(Unlock|RUnlock)\(\)`)
	return Rule{
		ID: "mutex-defer-unlock", Name: "Mutex Lock must have defer Unlock", Category: CatReliability,
		Severity: SevBlocking, Enabled: true,
		Description: "Mutex Lock without corresponding defer Unlock risks deadlock on panic or early return",
		Check: func(file string, content []byte) []Finding {
			if !isGoFile(file) || isTestFile(file) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				if lockCall.MatchString(line) {
					if i+1 < len(lines) && deferUnlock.MatchString(lines[i+1]) {
						continue
					}
					findings = append(findings, Finding{
						RuleID:      "mutex-defer-unlock",
						Category:    CatReliability,
						Severity:    SevBlocking,
						File:        file,
						Line:        i + 1,
						Description: "Lock() without defer Unlock() on the next line — deadlock risk",
						Suggestion:  "Add defer mu.Unlock() immediately after Lock()",
						Evidence:    strings.TrimSpace(line),
					})
				}
			}
			return findings
		},
	}
}

func channelWithoutTimeoutRule() Rule {
	return Rule{
		ID: "channel-timeout", Name: "Channel operations should have timeout/select", Category: CatReliability,
		Severity: SevMajor, Enabled: true,
		Description: "Bare channel receive without select/timeout can leak goroutines",
		Check: func(file string, content []byte) []Finding {
			if !isGoFile(file) || isTestFile(file) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			inSelect := false
			for i, line := range lines {
				trimmed := strings.TrimSpace(line)
				if strings.Contains(trimmed, "select {") || strings.Contains(trimmed, "select{") {
					inSelect = true
				}
				if inSelect {
					if trimmed == "}" {
						inSelect = false
					}
					continue
				}
				if strings.Contains(trimmed, "range") || strings.HasPrefix(trimmed, "for") {
					continue
				}
				if strings.HasPrefix(trimmed, "<-") && !strings.Contains(trimmed, ":=") && !strings.Contains(trimmed, "=") {
					findings = append(findings, Finding{
						RuleID:      "channel-timeout",
						Category:    CatReliability,
						Severity:    SevMajor,
						File:        file,
						Line:        i + 1,
						Description: "Bare channel receive without select — goroutine leak if sender never sends",
						Suggestion:  "Use select with context.Done() or time.After() for timeout",
						Evidence:    trimmed,
					})
				}
			}
			return findings
		},
	}
}

// --- Configuration & secrets rules ---

func hardcodedURLRule() Rule {
	re := regexp.MustCompile(`https?://[a-zA-Z0-9][-a-zA-Z0-9]*\.(com|io|net|org|dev|app|co)[/a-zA-Z0-9._-]*`)
	allowedHosts := []string{"example.com", "localhost", "127.0.0.1", "schemas.openxmlformats", "www.w3.org",
		"json-schema.org", "golang.org", "github.com", "pkg.go.dev", "docs.", "spec.", "rfc-editor.org"}
	return Rule{
		ID: "no-hardcoded-url", Name: "No hardcoded URLs", Category: CatConsistency,
		Severity: SevMajor, Enabled: true,
		Description: "Hardcoded URLs break when environments change — use configuration",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				if strings.Contains(line, "// ") || strings.Contains(line, "/* ") {
					continue
				}
				matches := re.FindAllString(line, -1)
				for _, m := range matches {
					allowed := false
					for _, a := range allowedHosts {
						if strings.Contains(m, a) {
							allowed = true
							break
						}
					}
					if !allowed {
						findings = append(findings, Finding{
							RuleID:      "no-hardcoded-url",
							Category:    CatConsistency,
							Severity:    SevMajor,
							File:        file,
							Line:        i + 1,
							Description: "Hardcoded URL — use config or environment variable",
							Suggestion:  "Move URL to configuration and inject at runtime",
							Evidence:    m,
						})
					}
				}
			}
			return findings
		},
	}
}

func hardcodedPortRule() Rule {
	// Match patterns like ":8080", ":3000" in string literals
	re := regexp.MustCompile(`":[0-9]{4,5}"`)
	return Rule{
		ID: "no-hardcoded-port", Name: "No hardcoded ports", Category: CatConsistency,
		Severity: SevMajor, Enabled: true,
		Description: "Hardcoded port numbers break in different environments",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil
			}
			return regexCheck(re, file, content, "no-hardcoded-port", CatConsistency, SevMajor,
				"Hardcoded port number — use configuration",
				"Move port to configuration or environment variable")
		},
	}
}

func directEnvAccessRule() Rule {
	re := regexp.MustCompile(`os\.Getenv\(`)
	return Rule{
		ID: "no-direct-env", Name: "No direct os.Getenv in business logic", Category: CatConsistency,
		Severity: SevMajor, Enabled: true,
		Description: "Direct os.Getenv scatters config access — centralize in config layer",
		Check: func(file string, content []byte) []Finding {
			if !isGoFile(file) || isTestFile(file) {
				return nil
			}
			if strings.Contains(file, "config") || strings.Contains(file, "cmd/") || strings.HasSuffix(file, "main.go") {
				return nil
			}
			return regexCheck(re, file, content, "no-direct-env", CatConsistency, SevMajor,
				"Direct os.Getenv() in business logic — centralize config access",
				"Read environment variables in config layer and inject via struct fields")
		},
	}
}

// --- Database & data safety rules ---

func destructiveMigrationRule() Rule {
	re := regexp.MustCompile(`(?i)\b(DROP\s+(TABLE|COLUMN|INDEX|DATABASE)|TRUNCATE\s+TABLE|ALTER\s+TABLE\s+\w+\s+DROP)\b`)
	return Rule{
		ID: "no-destructive-migration", Name: "No destructive migrations", Category: CatReliability,
		Severity: SevBlocking, Enabled: true,
		Description: "DROP TABLE/COLUMN destroys data irreversibly — requires multi-step migration",
		Check: func(file string, content []byte) []Finding {
			if !strings.Contains(file, "migration") && !strings.HasSuffix(file, ".sql") {
				return nil
			}
			return regexCheck(re, file, content, "no-destructive-migration", CatReliability, SevBlocking,
				"Destructive migration — DROP/TRUNCATE destroys data irreversibly",
				"Use multi-step migration: add new, migrate data, then drop old in a later release")
		},
	}
}

func sqlInLoopRule() Rule {
	sqlCall := regexp.MustCompile(`\.(Query|Exec|QueryRow|Get|Select)\(`)
	forLoop := regexp.MustCompile(`^\s*for\s`)
	return Rule{
		ID: "no-sql-in-loop", Name: "No SQL queries inside loops (N+1)", Category: CatReliability,
		Severity: SevBlocking, Enabled: true,
		Description: "SQL query inside a loop causes N+1 query problem — use batch queries",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			depth := 0
			for i, line := range lines {
				trimmed := strings.TrimSpace(line)
				if forLoop.MatchString(line) {
					depth++
				}
				if depth > 0 {
					depth += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
					if depth < 0 {
						depth = 0
					}
					if sqlCall.MatchString(trimmed) {
						findings = append(findings, Finding{
							RuleID:      "no-sql-in-loop",
							Category:    CatReliability,
							Severity:    SevBlocking,
							File:        file,
							Line:        i + 1,
							Description: "SQL query inside loop — N+1 query problem",
							Suggestion:  "Batch the query: collect IDs, then use WHERE id IN (...)",
							Evidence:    strings.TrimSpace(line),
						})
					}
				}
			}
			return findings
		},
	}
}

// --- Observability rules ---

func rawLogRule() Rule {
	re := regexp.MustCompile(`\blog\.(Print|Fatal|Panic)(ln|f)?\(`)
	return Rule{
		ID: "no-raw-log", Name: "Use structured logging", Category: CatCodeQuality,
		Severity: SevMajor, Enabled: true,
		Description: "Raw log.Print lacks structure — use slog or structured logger",
		Check: func(file string, content []byte) []Finding {
			if !isGoFile(file) || isTestFile(file) {
				return nil
			}
			return regexCheck(re, file, content, "no-raw-log", CatCodeQuality, SevMajor,
				"Raw log.Print — use structured logging (slog, zerolog, zap)",
				"Replace with structured logger: log.Info(\"msg\", \"key\", value)")
		},
	}
}

// --- Error handling rules ---

func contextNotPropagatedRule() Rule {
	funcWithCtx := regexp.MustCompile(`func\s+\w+\([^)]*ctx\s+context\.Context`)
	ctxUsage := regexp.MustCompile(`\bctx\b`)
	return Rule{
		ID: "context-propagation", Name: "context.Context must be propagated", Category: CatReliability,
		Severity: SevMajor, Enabled: true,
		Description: "Function accepts context.Context but never uses it — timeout/cancellation won't propagate",
		Check: func(file string, content []byte) []Finding {
			if !isGoFile(file) || isTestFile(file) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				if funcWithCtx.MatchString(line) {
					braces := 0
					used := false
					started := false
					for j := i; j < len(lines); j++ {
						braces += strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
						if braces > 0 {
							started = true
						}
						if started && j > i && ctxUsage.MatchString(lines[j]) {
							used = true
							break
						}
						if started && braces <= 0 {
							break
						}
					}
					if !used && started {
						findings = append(findings, Finding{
							RuleID:      "context-propagation",
							Category:    CatReliability,
							Severity:    SevMajor,
							File:        file,
							Line:        i + 1,
							Description: "Function accepts context.Context but never uses it — cancellation won't propagate",
							Suggestion:  "Pass ctx to downstream calls or remove the parameter",
							Evidence:    strings.TrimSpace(line),
						})
					}
				}
			}
			return findings
		},
	}
}

func httpWithoutTimeoutRule() Rule {
	re := regexp.MustCompile(`http\.Client\{\s*\}|&http\.Client\{\s*\}`)
	return Rule{
		ID: "http-timeout", Name: "HTTP clients must have timeouts", Category: CatReliability,
		Severity: SevBlocking, Enabled: true,
		Description: "HTTP client without timeout will hang indefinitely on slow/dead servers",
		Check: func(file string, content []byte) []Finding {
			if !isGoFile(file) || isTestFile(file) {
				return nil
			}
			return regexCheck(re, file, content, "http-timeout", CatReliability, SevBlocking,
				"HTTP client created without timeout — will hang on slow servers",
				"Set Timeout: &http.Client{Timeout: 30 * time.Second}")
		},
	}
}

// --- AI-specific failure mode rules ---

func hallucinatedGoImportRule() Rule {
	importLine := regexp.MustCompile(`^\s*"([^"]+)"`)
	return Rule{
		ID: "hallucinated-import", Name: "No hallucinated import paths", Category: CatConsistency,
		Severity: SevBlocking, Enabled: true,
		Description: "Import path doesn't match known patterns — possible AI hallucination",
		Check: func(file string, content []byte) []Finding {
			if !isGoFile(file) || isTestFile(file) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			inImport := false
			var modulePath string
			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "\"") && strings.Contains(trimmed, "/") {
					parts := strings.Split(strings.Trim(trimmed, "\""), "/")
					if len(parts) >= 3 && strings.Contains(parts[0], ".") {
						modulePath = strings.Join(parts[:3], "/")
						break
					}
				}
			}
			for i, line := range lines {
				trimmed := strings.TrimSpace(line)
				if trimmed == "import (" {
					inImport = true
					continue
				}
				if inImport && trimmed == ")" {
					inImport = false
					continue
				}
				if !inImport {
					continue
				}
				matches := importLine.FindStringSubmatch(trimmed)
				if len(matches) < 2 {
					continue
				}
				path := matches[1]
				if !strings.Contains(path, ".") {
					continue
				}
				if modulePath != "" && strings.HasPrefix(path, modulePath) {
					continue
				}
				// Flag imports with suspicious structural patterns
				if strings.Contains(path, " ") || strings.Contains(path, "..") || strings.Count(path, "/") > 8 {
					findings = append(findings, Finding{
						RuleID:      "hallucinated-import",
						Category:    CatConsistency,
						Severity:    SevBlocking,
						File:        file,
						Line:        i + 1,
						Description: "Suspicious import path — possible AI hallucination",
						Suggestion:  "Verify this import exists in go.mod or is a valid standard library package",
						Evidence:    path,
					})
				}
			}
			return findings
		},
	}
}

func deferredHardProblemRule() Rule {
	returnNil := regexp.MustCompile(`return\s+nil\s*$`)
	funcDecl := regexp.MustCompile(`^func\s+`)
	commentDefer := regexp.MustCompile(`(?i)(implement later|for now|placeholder|stub|skip|TODO)`)
	return Rule{
		ID: "deferred-hard-problem", Name: "No deferring hard problems", Category: CatConsistency,
		Severity: SevBlocking, Enabled: true,
		Description: "Function body suggests the hard part was deferred — return nil with deferral comment",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				trimmed := strings.TrimSpace(line)
				if !returnNil.MatchString(trimmed) {
					continue
				}
				start := i - 3
				if start < 0 {
					start = 0
				}
				for j := start; j < i; j++ {
					if commentDefer.MatchString(lines[j]) {
						for k := j; k >= 0; k-- {
							if funcDecl.MatchString(lines[k]) {
								findings = append(findings, Finding{
									RuleID:      "deferred-hard-problem",
									Category:    CatConsistency,
									Severity:    SevBlocking,
									File:        file,
									Line:        i + 1,
									Description: "Function defers the hard problem — return nil with deferral comment",
									Suggestion:  "Implement the actual logic instead of deferring",
									Evidence:    strings.TrimSpace(lines[j]) + " ... " + trimmed,
								})
								break
							}
						}
						break
					}
				}
			}
			return findings
		},
	}
}
