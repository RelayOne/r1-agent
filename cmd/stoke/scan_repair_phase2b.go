// scan_repair_phase2b.go — H-17 Phase 2b security vector scan.
//
// For each security vector in .claude/scripts/security/vectors.md (or
// the built-in fallback list), dispatch ONE LLM call that reviews the
// repo's security CSV outputs + relevant source excerpts for that
// vector only. Findings go to audit/security/vector-<N>-<slug>.md.
//
// Parallelized via the same cfg.Workers pool as Phase 2a. Individual
// call timeouts are 2 min — a timeout records a "(call failed)" stanza
// and keeps the phase moving.

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// securityVector is one entry from vectors.md: an ordered number, a
// human name, and the full prose body the reviewer sees in its prompt.
type securityVector struct {
	Num  int
	Name string
	Slug string
	Body string
}

// fallbackSecurityVectors is used when .claude/scripts/security/vectors.md
// is missing or unparseable. The list mirrors the spec's bullet-list
// of default vectors so the phase degrades gracefully instead of
// skipping entirely. Keep in sync with the scan-and-repair spec.
var fallbackSecurityVectors = []securityVector{
	{Num: 1, Name: "auth-bypass", Slug: "auth-bypass", Body: "Authentication and authorization bypasses. Role checks, tenant isolation, session validation."},
	{Num: 2, Name: "injection", Slug: "injection", Body: "Injection (SQL/XSS/command/path). Raw query interpolation, unsanitized shell args, path traversal."},
	{Num: 3, Name: "secret-exposure", Slug: "secret-exposure", Body: "Secret exposure: hardcoded tokens, leaked keys, unencrypted storage, env var handling."},
	{Num: 4, Name: "cors-wildcarding", Slug: "cors-wildcarding", Body: "CORS wildcarding on credentialed endpoints. Permissive Access-Control-Allow-Origin."},
	{Num: 5, Name: "rate-limiting-gaps", Slug: "rate-limiting-gaps", Body: "Rate limiting gaps on auth/billing endpoints. Missing throttle on password reset, login, webhook."},
	{Num: 6, Name: "csrf", Slug: "csrf", Body: "CSRF gaps: state-changing endpoints without token validation or SameSite cookies."},
	{Num: 7, Name: "crypto-weaknesses", Slug: "crypto-weaknesses", Body: "Crypto weaknesses: weak algorithms, hardcoded IVs, insufficient key derivation, pinning gaps."},
	{Num: 8, Name: "session-management", Slug: "session-management", Body: "Session management: regeneration on auth, expiration, secure cookie flags, session fixation."},
	{Num: 9, Name: "toctou-race", Slug: "toctou-race", Body: "TOCTOU races: file-system/lock-free patterns where state changes between check and use."},
	{Num: 10, Name: "concurrent-data-integrity", Slug: "concurrent-data-integrity", Body: "Concurrent data integrity: unsafe concurrent access, missing locks, lost updates."},
	{Num: 11, Name: "input-validation", Slug: "input-validation", Body: "Input validation gaps: missing schema checks, unbounded inputs, type coercion bugs."},
	{Num: 12, Name: "output-encoding", Slug: "output-encoding", Body: "Output encoding: HTML/JSON/logs — unescaped user data reaching the output surface."},
}

// parseSecurityVectors extracts vectors from a vectors.md file. The
// format is ordered-heading-per-vector ("## N. NAME") followed by a
// short prose description. On parse failure or missing file we return
// fallbackSecurityVectors so Phase 2b still runs end-to-end.
func parseSecurityVectors(path string) []securityVector {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return fallbackSecurityVectors
	}
	// The parser reuses the ## pattern but the heading form is
	// "## N. Human Name" (vectors.md uses sentence-case rather than
	// the ALL_CAPS slug used in semantic-patterns.md). We split on
	// headings and capture the trailing body.
	var out []securityVector
	lines := strings.Split(string(b), "\n")
	var cur securityVector
	var body strings.Builder
	flush := func() {
		if cur.Name == "" {
			return
		}
		cur.Body = strings.TrimSpace(body.String())
		cur.Slug = slugify(cur.Name)
		if cur.Slug == "pattern" || cur.Slug == "" {
			cur.Slug = fmt.Sprintf("vector-%d", cur.Num)
		}
		out = append(out, cur)
		cur = securityVector{}
		body.Reset()
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Match "## N. Rest" — capture the trailing name free-form.
		if strings.HasPrefix(trimmed, "## ") {
			rest := strings.TrimPrefix(trimmed, "## ")
			// Extract leading number: "1. Auth Control".
			dot := strings.Index(rest, ".")
			if dot > 0 {
				numStr := rest[:dot]
				num := 0
				for i := 0; i < len(numStr); i++ {
					if numStr[i] < '0' || numStr[i] > '9' {
						num = 0
						break
					}
					num = num*10 + int(numStr[i]-'0')
				}
				if num > 0 {
					flush()
					cur.Num = num
					cur.Name = strings.TrimSpace(rest[dot+1:])
					continue
				}
			}
		}
		if cur.Name != "" {
			body.WriteString(line)
			body.WriteByte('\n')
		}
	}
	flush()
	if len(out) == 0 {
		return fallbackSecurityVectors
	}
	return out
}

// runPhase2bSecurityVectors dispatches one LLM call per vector. Each
// call: 2-min ceiling, worker routes via opus-preferred binary if
// cfg.OpusBin is set, otherwise through the regular worker model.
func runPhase2bSecurityVectors(ctx context.Context, cfg *scanRepairConfig, p1 *phase1Result) error {
	vectors := parseSecurityVectors(filepath.Join(cfg.Repo, ".claude", "scripts", "security", "vectors.md"))
	if len(vectors) == 0 {
		return fmt.Errorf("no security vectors to scan")
	}
	outDir := filepath.Join(cfg.Repo, "audit", "security")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("mkdir security: %w", err)
	}

	// Gather the input context ONCE: concat of audit/security/*.csv +
	// top-20 section files. The prompt is large but bounded — we trim
	// each section file to ~40 lines so we don't blow past the
	// context window on a large repo.
	csvBlob := readAllCSVs(outDir)
	filesBlob := buildTopSectionsBlob(cfg.Repo, p1, 20)

	jobs := make(chan securityVector, len(vectors))
	workers := cfg.Workers
	if workers < 1 {
		workers = 2
	}
	var wg sync.WaitGroup
	var calls, findings int64

	call := cfg.vectorCaller
	if call == nil {
		call = func(ctx context.Context, dir, prompt string, preferOpus bool) string {
			return vectorWorkerCall(ctx, cfg, prompt, preferOpus)
		}
	}

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for v := range jobs {
				cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				prompt := fmt.Sprintf(vectorScanPromptTemplate, v.Num, v.Name, v.Body, csvBlob+"\n\n"+filesBlob)
				reply := call(cctx, cfg.Repo, prompt, true)
				cancel()
				atomic.AddInt64(&calls, 1)
				n := writeVectorFinding(outDir, v, reply)
				atomic.AddInt64(&findings, int64(n))
				result := fmt.Sprintf("%d findings", n)
				if n == 0 {
					result = "None."
				}
				fmt.Printf("⚡ Phase 2b vector %d: %s\n", v.Num, result)
			}
		}()
	}
	for _, v := range vectors {
		jobs <- v
	}
	close(jobs)
	wg.Wait()

	fmt.Printf("  Phase 2b complete: %d vectors, %d total findings\n\n",
		atomic.LoadInt64(&calls), atomic.LoadInt64(&findings))
	return nil
}

// writeVectorFinding persists the worker reply to
// audit/security/vector-<N>-<slug>.md and returns the finding count.
func writeVectorFinding(outDir string, v securityVector, reply string) int {
	body := strings.TrimSpace(reply)
	if body == "" {
		body = "(no response — call failed or timed out)"
	}
	out := filepath.Join(outDir, fmt.Sprintf("vector-%d-%s.md", v.Num, v.Slug))
	content := fmt.Sprintf("# Vector %d: %s\n\n%s\n", v.Num, v.Name, body)
	if err := os.WriteFile(out, []byte(content), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "  [Phase 2b] write %s: %v\n", out, err)
	}
	return countFindingLines(body)
}

// countFindingLines approximates the number of distinct findings. We
// count lines beginning with "- [" (our standard finding format).
// A "None." reply returns 0.
func countFindingLines(body string) int {
	t := strings.TrimSpace(body)
	if t == "" || strings.EqualFold(t, "None.") || strings.EqualFold(t, "None") {
		return 0
	}
	n := 0
	for _, line := range strings.Split(t, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- [") {
			n++
		}
	}
	return n
}

// readAllCSVs concatenates every *.csv file in dir into a single
// prompt-friendly blob with per-file headers. Non-existent dir → "".
func readAllCSVs(dir string) string {
	var buf strings.Builder
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".csv") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		fmt.Fprintf(&buf, "### %s\n```csv\n%s\n```\n\n", e.Name(), string(b))
	}
	return buf.String()
}

// buildTopSectionsBlob concatenates the top-N section files (first N
// by sorted order) into a prompt-friendly blob. Each section file is
// truncated to ~80 lines so the prompt stays bounded.
func buildTopSectionsBlob(repo string, p1 *phase1Result, topN int) string {
	if p1 == nil {
		return ""
	}
	var buf strings.Builder
	for i, sf := range p1.Sections {
		if i >= topN {
			break
		}
		b, err := os.ReadFile(sf)
		if err != nil {
			continue
		}
		lines := strings.Split(string(b), "\n")
		if len(lines) > 80 {
			lines = lines[:80]
		}
		fmt.Fprintf(&buf, "### %s\n%s\n\n", filepath.Base(sf), strings.Join(lines, "\n"))
	}
	_ = repo
	return buf.String()
}

// vectorWorkerCall routes a Phase 2b prompt through opus if the
// operator provided --opus-bin, otherwise through the regular worker
// model via claudeReviewCall.
func vectorWorkerCall(ctx context.Context, cfg *scanRepairConfig, prompt string, preferOpus bool) string {
	select {
	case <-ctx.Done():
		return ""
	default:
	}
	if preferOpus && cfg.OpusBin != "" {
		// Treat --opus-bin as a Claude-style binary that accepts
		// standard claude args. Routing to claudeReviewCall with an
		// explicit "opus" model alias keeps the call consistent with
		// the rest of the phase.
		return claudeReviewCall(cfg.OpusBin, cfg.Repo, prompt, "opus")
	}
	return workerSemanticCall(ctx, cfg, prompt)
}
