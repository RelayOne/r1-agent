// scan_repair_phase2c.go — H-17 Phase 2c multi-perspective persona audit.
//
// Dispatches one LLM call per selected persona from
// .claude/scripts/audit-personas.md. Writes to
// audit/perspectives/<slug>.qa.md.
//
// Selection is controlled by cfg.PersonasSelection:
//   - "all"     → every persona parsed from the file
//   - "core"    → 8 canonical personas (see corePersonaSlugs)
//   - CSV list  → exactly those slugs (trimmed)
//
// Security-critical personas (lead-security, sneaky-finder,
// scaling-consultant, build-deploy) prefer opus when --opus-bin is
// available; every other persona uses the configured worker model.

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

// persona is one entry from audit-personas.md. Slug is the "###" header
// slug ("lead-eng"); Body is the full paragraph describing the role.
type persona struct {
	Slug       string
	Name       string // "Lead Engineer" style name extracted from the body prefix
	Body       string
	PreferOpus bool
}

// corePersonaSlugs is the 8-persona "core" set from audit-personas.md.
// Used when --personas=core.
var corePersonaSlugs = []string{
	"lead-eng",
	"lead-qa",
	"lead-security",
	"vp-eng-completeness",
	"vp-eng-types",
	"sneaky-finder",
	"build-deploy",
	"picky-reviewer",
}

// opusPreferredSlugs is the subset of persona slugs where Phase 2c
// prefers opus when --opus-bin is available. Security-critical roles
// benefit most from the deeper reasoning capacity; others use the
// regular worker model for cost.
var opusPreferredSlugs = map[string]bool{
	"lead-security":      true,
	"sneaky-finder":      true,
	"scaling-consultant": true,
	"build-deploy":       true,
}

// parseAuditPersonas extracts personas from audit-personas.md. Each
// persona is a level-3 heading "### N. slug-name" followed by a short
// prose body. Returns a fallback single-persona list if the file is
// missing or unparseable so Phase 2c still produces SOMETHING.
func parseAuditPersonas(path string) []persona {
	b, err := os.ReadFile(path)
	if err != nil {
		return fallbackPersonas()
	}
	var out []persona
	lines := strings.Split(string(b), "\n")
	var cur persona
	var body strings.Builder
	flush := func() {
		if cur.Slug == "" {
			return
		}
		cur.Body = strings.TrimSpace(body.String())
		cur.PreferOpus = opusPreferredSlugs[cur.Slug]
		out = append(out, cur)
		cur = persona{}
		body.Reset()
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Heading form: "### N. slug-name"
		if strings.HasPrefix(trimmed, "### ") {
			rest := strings.TrimPrefix(trimmed, "### ")
			// Strip "N. " prefix.
			dot := strings.Index(rest, ".")
			if dot > 0 {
				leading := rest[:dot]
				isNum := true
				for i := 0; i < len(leading); i++ {
					if leading[i] < '0' || leading[i] > '9' {
						isNum = false
						break
					}
				}
				if isNum {
					rest = strings.TrimSpace(rest[dot+1:])
				}
			}
			// rest is now something like "lead-eng" or "vp-eng-types".
			if rest != "" {
				flush()
				cur.Slug = strings.ToLower(rest)
				cur.Name = rest
				continue
			}
		}
		if cur.Slug != "" {
			body.WriteString(line)
			body.WriteByte('\n')
		}
	}
	flush()
	if len(out) == 0 {
		return fallbackPersonas()
	}
	return out
}

// fallbackPersonas is used when audit-personas.md is missing. Provides
// the 17 slug stubs with minimal descriptions so the phase still runs.
func fallbackPersonas() []persona {
	allSlugs := []string{
		"lead-eng", "lead-qa", "lead-security", "lead-ux", "lead-compliance",
		"product-owner", "vp-eng-completeness", "vp-eng-idempotency",
		"vp-eng-scaling", "vp-eng-types", "vp-eng-tests", "vp-eng-docs",
		"vp-eng-comments", "sneaky-finder", "scaling-consultant",
		"build-deploy", "picky-reviewer",
	}
	out := make([]persona, 0, len(allSlugs))
	for _, s := range allSlugs {
		out = append(out, persona{
			Slug:       s,
			Name:       s,
			Body:       fmt.Sprintf("You are the %s. Audit the repo for issues in your domain of responsibility.", s),
			PreferOpus: opusPreferredSlugs[s],
		})
	}
	return out
}

// selectPersonas filters the persona list based on cfg.PersonasSelection.
func selectPersonas(all []persona, selection string) []persona {
	sel := strings.TrimSpace(strings.ToLower(selection))
	if sel == "" || sel == "all" {
		return all
	}
	if sel == "core" {
		out := make([]persona, 0, len(corePersonaSlugs))
		lookup := map[string]persona{}
		for _, p := range all {
			lookup[p.Slug] = p
		}
		for _, slug := range corePersonaSlugs {
			if p, ok := lookup[slug]; ok {
				out = append(out, p)
			} else {
				// Fall back to a stub persona so the requested slug
				// still gets dispatched even when the repo's copy of
				// audit-personas.md is missing it.
				out = append(out, persona{
					Slug:       slug,
					Name:       slug,
					Body:       fmt.Sprintf("You are the %s.", slug),
					PreferOpus: opusPreferredSlugs[slug],
				})
			}
		}
		return out
	}
	// Comma-separated slug list. Case-insensitive. Unknown slugs become
	// stub personas so the CLI never silently drops a user request.
	want := map[string]bool{}
	for _, s := range strings.Split(sel, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			want[s] = true
		}
	}
	out := make([]persona, 0, len(want))
	seen := map[string]bool{}
	for _, p := range all {
		if want[p.Slug] {
			out = append(out, p)
			seen[p.Slug] = true
		}
	}
	for slug := range want {
		if !seen[slug] {
			out = append(out, persona{
				Slug:       slug,
				Name:       slug,
				Body:       fmt.Sprintf("You are the %s.", slug),
				PreferOpus: opusPreferredSlugs[slug],
			})
		}
	}
	return out
}

// runPhase2cPersonas dispatches one LLM call per persona. Each call
// is bounded by a 2-min timeout; a timeout records a "(call failed)"
// stanza and keeps the phase moving.
func runPhase2cPersonas(ctx context.Context, cfg *scanRepairConfig, p1 *phase1Result) error {
	all := parseAuditPersonas(filepath.Join(cfg.Repo, ".claude", "scripts", "audit-personas.md"))
	personas := selectPersonas(all, cfg.PersonasSelection)
	if len(personas) == 0 {
		return fmt.Errorf("no personas selected")
	}

	outDir := filepath.Join(cfg.Repo, "audit", "perspectives")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("mkdir perspectives: %w", err)
	}

	// Section blob shared across personas. For specialist personas
	// (security etc.) we could narrow to security-only files, but the
	// sections are already decomposed by the project-mapper, and the
	// persona's role-description carries enough signal for a strong
	// reviewer to filter on its own.
	filesBlob := buildTopSectionsBlob(cfg.Repo, p1, 20)

	jobs := make(chan persona, len(personas))
	workers := cfg.Workers
	if workers < 1 {
		workers = 2
	}
	var wg sync.WaitGroup
	var calls, findings int64

	call := cfg.personaCaller
	if call == nil {
		call = func(ctx context.Context, dir, prompt string, preferOpus bool) string {
			return personaWorkerCall(ctx, cfg, prompt, preferOpus)
		}
	}

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for per := range jobs {
				cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				prompt := fmt.Sprintf(personaPromptTemplate, per.Name, per.Body) +
					"\n\n## Codebase sections to review\n" + filesBlob
				reply := call(cctx, cfg.Repo, prompt, per.PreferOpus)
				cancel()
				atomic.AddInt64(&calls, 1)
				n := writePersonaFinding(outDir, per, reply)
				atomic.AddInt64(&findings, int64(n))
				result := fmt.Sprintf("%d findings", n)
				if n == 0 {
					result = "None."
				}
				fmt.Printf("🎭 Phase 2c persona %s: %s\n", per.Slug, result)
			}
		}()
	}
	for _, p := range personas {
		jobs <- p
	}
	close(jobs)
	wg.Wait()

	fmt.Printf("  Phase 2c complete: %d personas, %d total findings\n\n",
		atomic.LoadInt64(&calls), atomic.LoadInt64(&findings))
	return nil
}

// writePersonaFinding writes the persona's reply verbatim to
// audit/perspectives/<slug>.qa.md and returns the finding count (lines
// matching the persona checkbox finding format).
func writePersonaFinding(outDir string, per persona, reply string) int {
	body := strings.TrimSpace(reply)
	if body == "" {
		body = "(no response — call failed or timed out)"
	}
	out := filepath.Join(outDir, per.Slug+".qa.md")
	content := fmt.Sprintf("# %s\n\n%s\n", per.Name, body)
	if err := os.WriteFile(out, []byte(content), 0644); err != nil { // #nosec G306 -- CLI output artefact; user-readable.
		fmt.Fprintf(os.Stderr, "  [Phase 2c] write %s: %v\n", out, err)
	}
	return countCheckboxFindings(body)
}

// countCheckboxFindings counts lines that look like the persona
// finding format ("- [ ] [SEVERITY] ..." or "- [SEVERITY] ...").
func countCheckboxFindings(body string) int {
	t := strings.TrimSpace(body)
	if t == "" || strings.EqualFold(t, "None.") || strings.EqualFold(t, "None") {
		return 0
	}
	n := 0
	for _, line := range strings.Split(t, "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "- [ ]") || strings.HasPrefix(s, "- [") {
			n++
		}
	}
	return n
}

// personaWorkerCall routes a Phase 2c prompt through opus if the
// operator provided --opus-bin AND this persona preferOpus,
// otherwise through the regular worker.
func personaWorkerCall(ctx context.Context, cfg *scanRepairConfig, prompt string, preferOpus bool) string {
	select {
	case <-ctx.Done():
		return ""
	default:
	}
	if preferOpus && cfg.OpusBin != "" {
		return claudeReviewCall(cfg.OpusBin, cfg.Repo, prompt, "opus")
	}
	return workerSemanticCall(ctx, cfg, prompt)
}
