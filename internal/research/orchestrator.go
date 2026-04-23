// orchestrator.go coordinates the fetch+verify+store pipeline for a
// single research run. It sits above the MVP primitives already in
// this package (Planner / Fetcher / VerifyClaim) and wraps them in a
// parallel fan-out layer whose public contract is one call — Run — in
// and one Report out.
//
// Why this lives in a new file instead of a refactor of research.go:
// the MVP primitives already carry their own weight and are consumed
// directly by internal/executor.ResearchExecutor for the single-agent
// path. The orchestrator is the MULTI-agent path — it re-uses those
// primitives via composition so the single-agent call site continues
// to work byte-for-byte (per the spec's "Backward compatibility"
// section).
//
// What the orchestrator actually does:
//
//  1. Decompose the query into SubQuestions via the configured
//     Planner (defaults to the stdlib heuristic; a future LLM-backed
//     decomposer is drop-in).
//  2. Derive one SubObjective per SubQuestion, bounded by effort:
//     Effort=Minimal collapses to 1 subagent; Thorough scales to
//     min(MaxParallel, len(subs)). Extras fold into the last
//     subagent's scope so nothing is dropped.
//  3. Fan out subagent workers via golang.org/x/sync/errgroup with
//     SetLimit(MaxParallel). Each worker produces a Findings value
//     containing the Sources it consulted and the top Sentences.
//  4. Optionally persist the run artefacts to RunRoot on disk
//     (plan.md + subagent-<i>/{objective.md,findings.md,sources.jsonl}
//     + synthesis.md). This is the "filesystem as communication"
//     surface from RT-07 §1.5 — it is NOT required for the orchestrator
//     to function, so RunRoot="" is valid and skips all file IO.
//  5. Synthesize a Report body from the collected Findings via the
//     configured Synthesize func (default: deterministic markdown).
//  6. Return a fully-populated research.Report. Claims are assigned
//     stable C-<N> IDs across subagents so the descent engine's AC
//     set is deterministic under a fixed Fetcher.
//
// What this file intentionally does NOT do:
//
//   - It does not import internal/provider. Orchestrator accepts two
//     opaque "agent" hook functions (LeadFn / SubFn) so callers that
//     want to wire a real provider can do so without this package
//     taking on that dependency. A nil hook means "use the built-in
//     deterministic path". This keeps the research package dep-free
//     and makes the orchestrator testable with zero mocks.
//   - It does not modify ResearchExecutor. The spec's §"Modified
//     files" calls for Config-flag plumbing inside the executor; that
//     is a separate, larger change and is explicitly out of scope
//     here (tracked on the spec checklist as items 1-4, 21, 27-29).
//   - It does not implement an LLM decomposer or browser fetcher.
//     Those are their own files in the spec's checklist (items 7-9,
//     22-23) and are independent of this coordinator.
//
// The orchestrator is the "pipeline wrapper" that routes research
// requests through the existing primitives. Everything below is
// stdlib + golang.org/x/sync/errgroup (already vendored; used by the
// rest of the codebase).

package research

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// Effort is the local effort ladder used by the orchestrator to size
// the fan-out. Kept as a string rather than an int enum so callers
// pass the ladder position explicitly and a bogus value falls into
// the default bucket (EffortStandard) rather than silently meaning
// "zero subagents". Matches the effort names used elsewhere in the
// codebase without importing the executor package (avoids a cycle).
type Effort string

const (
	EffortMinimal  Effort = "minimal"
	EffortStandard Effort = "standard"
	EffortThorough Effort = "thorough"
	EffortCritical Effort = "critical"
)

// SubObjective is the 4-field brief handed to one subagent per
// RT-07 §1.4. We keep this local to orchestrator.go rather than in
// its own file so the whole multi-agent surface is reviewable in one
// place. Empty Objective is rejected by Validate.
type SubObjective struct {
	// Objective states the single question this subagent is
	// responsible for answering.
	Objective string `json:"objective"`
	// OutputFormat names the shape the subagent should return
	// (e.g. "bullet list of top sentences").
	OutputFormat string `json:"output_format"`
	// ToolGuidance lists which tools the subagent may use. For the
	// stdlib orchestrator this is descriptive-only; a future LLM
	// subagent may honour it programmatically.
	ToolGuidance string `json:"tool_guidance,omitempty"`
	// TaskBoundaries is the "what not to do" clause. When the effort
	// ladder collapses N sub-questions into one subagent, the extras
	// are appended here so the subagent still knows it owns them.
	TaskBoundaries string `json:"task_boundaries,omitempty"`
	// Hints carries forward the decomposer's per-sub-question keyword
	// list so the orchestrator's built-in fetcher can route URLs the
	// same way the single-agent executor does.
	Hints []string `json:"hints,omitempty"`
}

// Validate rejects empty Objective. Called once per SubObjective
// before fan-out so a bug in decomposition surfaces immediately
// rather than silently producing a no-op subagent.
func (s SubObjective) Validate() error {
	if strings.TrimSpace(s.Objective) == "" {
		return fmt.Errorf("subobjective: empty Objective")
	}
	return nil
}

// Findings is what one subagent returns. Kept concrete (no interface)
// because callers always consume all fields — no benefit to
// polymorphism and a real struct is easier to serialise.
type Findings struct {
	// SubObjective is the brief this Findings answers (echoed back
	// so the Lead can correlate without reconstructing from order).
	SubObjective SubObjective `json:"sub_objective"`
	// Summary is a one-paragraph writeup for the Lead. The
	// deterministic path sets this to a bullet list of Sentences.
	Summary string `json:"summary"`
	// Sources is the deduplicated list of URLs this subagent
	// consulted. Duplicate URLs across subagents are allowed — the
	// Orchestrator dedups at the Report level.
	Sources []Source `json:"sources"`
	// Sentences are claim-candidate excerpts ranked by overlap with
	// the SubObjective's Objective text. Capped at MaxClaimsPerSub.
	Sentences []string `json:"sentences"`
	// SourceForSentence[i] is the URL the ith sentence came from.
	// Parallel to Sentences so the orchestrator can build Claim
	// records without re-computing source routing.
	SourceForSentence []string `json:"source_for_sentence,omitempty"`
	// Err records a per-subagent failure. Populated when the
	// subagent's work failed but the orchestrator decided to
	// continue (subagent failures do not fail the whole run per
	// RT-07 §1). Nil on success.
	Err error `json:"-"`
}

// LeadFn is the optional hook the orchestrator calls to synthesise
// the final Report body from per-subagent Findings. When nil, the
// orchestrator falls back to a deterministic markdown synthesiser.
// Implementations MUST be safe to call from Orchestrator.Run's
// goroutine and MUST honour ctx cancellation.
type LeadFn func(ctx context.Context, query string, findings []Findings) (string, error)

// SubFn is the optional hook the orchestrator calls per subagent.
// When nil, the orchestrator runs a deterministic subagent that
// fetches each URL routed by the SubObjective's Hints and extracts
// the top overlap-ranked sentences. Implementations MUST honour ctx
// cancellation and return a populated Findings — including Err =
// nil on success.
type SubFn func(ctx context.Context, obj SubObjective, fetch Fetcher, urls []string) (Findings, error)

// Orchestrator coordinates a single research run end-to-end. All
// fields are optional: zero-value is a deterministic, provider-free
// orchestrator wired to the heuristic planner, a nil-tolerant fetcher
// (injected via NewOrchestrator), and the default markdown
// synthesiser. Hot-path configuration (MaxParallel, RunRoot) is tuned
// per-call via field assignment on the struct returned from
// NewOrchestrator.
type Orchestrator struct {
	// Planner decomposes the query. Defaults to research.NewPlanner
	// (heuristic). Swap for an LLM-backed Planner by injecting a
	// Decomposer into the Planner, not here.
	Planner *Planner

	// Fetcher is handed to the default SubFn and to any verifier
	// that runs inside Run. Required. NewOrchestrator sets this.
	Fetcher Fetcher

	// MaxParallel caps concurrent subagents. Default 5 (RT-07 §7
	// open question 3). Values <= 0 are promoted to 1.
	MaxParallel int

	// RunRoot is the filesystem directory where run artefacts are
	// written (plan.md, subagent-<i>/..., synthesis.md). Empty
	// string skips all file IO — useful for tests and for callers
	// that persist elsewhere (e.g. a ledger node).
	RunRoot string

	// Clock is used for the "fetched_at" timestamps on sources.jsonl
	// entries. Default time.Now; tests inject a fixed time to keep
	// golden files deterministic.
	Clock func() time.Time

	// MaxClaimsPerSub caps the Sentences extracted from any single
	// subagent. Default 3 — matches the single-agent executor so
	// claim counts are comparable between paths.
	MaxClaimsPerSub int

	// URLsByHint routes candidate URLs to subagents whose Hints
	// match. Keyed like executor.ResearchExecutor consumes
	// Plan.Extra["urls_by_hint"]. Optional.
	URLsByHint map[string][]string

	// GlobalURLs is the fallback URL set handed to every subagent
	// when URLsByHint has no entry for that subagent's hints.
	// Keyed like Plan.Extra["urls"].
	GlobalURLs []string

	// Lead is the optional LLM synthesiser hook. Nil → deterministic
	// markdown synthesis.
	Lead LeadFn

	// Sub is the optional per-subagent hook. Nil → deterministic
	// fetch-and-rank loop that uses Fetcher directly.
	Sub SubFn

	// OnEvent is an optional callback fired at each pipeline stage
	// boundary ("plan.ready", "subagent.started",
	// "subagent.completed", "synthesis.ready", "completed"). Keeps
	// the orchestrator observable without coupling it to a specific
	// bus. Nil is a no-op.
	OnEvent func(event string, payload map[string]any)
}

// NewOrchestrator returns an Orchestrator with the stdlib defaults
// wired. Fetcher must not be nil — the default SubFn reads it
// directly. All other fields carry sensible zero-value defaults.
func NewOrchestrator(f Fetcher) *Orchestrator {
	return &Orchestrator{
		Planner:         NewPlanner(),
		Fetcher:         f,
		MaxParallel:     5,
		Clock:           time.Now,
		MaxClaimsPerSub: 3,
	}
}

// Run executes the full pipeline. It is safe to call Run multiple
// times on the same Orchestrator concurrently provided the caller
// uses distinct RunRoot values per call (so on-disk writes do not
// collide) — the orchestrator itself carries no per-run mutable
// state.
//
// Errors: Run returns an error only when the pipeline cannot produce
// ANY output (e.g. Fetcher is nil, query is empty after trim, RunRoot
// mkdir fails). Individual subagent failures are captured on their
// Findings.Err and logged via OnEvent; they do not propagate.
func (o *Orchestrator) Run(ctx context.Context, query string, effort Effort) (Report, error) {
	if o == nil {
		return Report{}, fmt.Errorf("orchestrator: nil receiver")
	}
	if o.Fetcher == nil {
		return Report{}, fmt.Errorf("orchestrator: no Fetcher configured")
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return Report{}, fmt.Errorf("orchestrator: empty query")
	}
	planner := o.Planner
	if planner == nil {
		planner = NewPlanner()
	}
	maxPar := o.MaxParallel
	if maxPar <= 0 {
		maxPar = 1
	}
	maxClaims := o.MaxClaimsPerSub
	if maxClaims <= 0 {
		maxClaims = 3
	}
	clock := o.Clock
	if clock == nil {
		clock = time.Now
	}

	// Stage 1: decompose the query into SubQuestions, then collapse
	// into SubObjectives bounded by effort.
	subQs := planner.Plan(q)
	subObjs := buildSubObjectives(subQs, effort, maxPar)
	for i := range subObjs {
		if err := subObjs[i].Validate(); err != nil {
			// A validation failure here means the decomposer
			// returned garbage; fail loudly rather than silently
			// fanning out to no-op workers.
			return Report{}, fmt.Errorf("orchestrator: %w (sub %d)", err, i)
		}
	}

	// Stage 1b: persist plan.md + per-subagent objectives if RunRoot
	// is set. Missing directory is created; existing directory is
	// tolerated.
	if o.RunRoot != "" {
		if err := os.MkdirAll(o.RunRoot, 0o755); err != nil {
			return Report{}, fmt.Errorf("orchestrator: mkdir RunRoot: %w", err)
		}
		if err := writePlanFile(o.RunRoot, q, subObjs); err != nil {
			return Report{}, fmt.Errorf("orchestrator: write plan.md: %w", err)
		}
	}
	o.emit("plan.ready", map[string]any{
		"query":     q,
		"subagents": len(subObjs),
	})

	// Stage 2: fan-out. Pre-allocate the findings slice at full
	// length so workers write into a stable index (no append race).
	findings := make([]Findings, len(subObjs))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxPar)

	subFn := o.Sub
	if subFn == nil {
		subFn = o.deterministicSubagent
	}

	for i := range subObjs {
		i := i
		obj := subObjs[i]
		urls := o.routeURLs(obj)
		g.Go(func() error {
			o.emit("subagent.started", map[string]any{
				"index":     i,
				"objective": obj.Objective,
				"urls":      len(urls),
			})
			f, err := subFn(gctx, obj, o.Fetcher, urls)
			// Always echo the SubObjective so callers that ignore
			// ordering can still correlate.
			f.SubObjective = obj
			if err != nil {
				// Subagent errors are captured, not propagated — per
				// RT-07 §1 ("subagents never fail the whole run").
				f.Err = err
				o.emit("subagent.completed", map[string]any{
					"index": i,
					"error": err.Error(),
				})
			} else {
				o.emit("subagent.completed", map[string]any{
					"index":     i,
					"sources":   len(f.Sources),
					"sentences": len(f.Sentences),
				})
			}
			// Cap sentences defensively in case SubFn ignored the
			// orchestrator's MaxClaimsPerSub contract.
			if len(f.Sentences) > maxClaims {
				f.Sentences = f.Sentences[:maxClaims]
				if len(f.SourceForSentence) > maxClaims {
					f.SourceForSentence = f.SourceForSentence[:maxClaims]
				}
			}
			findings[i] = f
			// Stage 2b: persist per-subagent files. Errors here are
			// non-fatal — the run still produces an in-memory Report.
			if o.RunRoot != "" {
				_ = writeSubagentDir(o.RunRoot, i, obj, f, clock())
			}
			return nil
		})
	}
	// Wait intentionally ignores the group error: individual
	// subagents capture their error on Findings.Err; the only way
	// g.Wait returns non-nil here is if a subagent panicked inside
	// errgroup, which is already a process-killing bug.
	_ = g.Wait()

	// Stage 3: synthesise the Report body. Prefer the LeadFn hook;
	// fall back to the deterministic markdown synth.
	lead := o.Lead
	var body string
	if lead != nil {
		// Re-read findings from disk when RunRoot is set — enforces
		// the "filesystem as communication" contract (RT-07 §1.5)
		// that the Lead consumes what the subagents wrote, not
		// in-memory state. When RunRoot is empty, we hand the
		// in-memory slice directly.
		rf := findings
		if o.RunRoot != "" {
			if loaded, err := readFindingsFromDisk(o.RunRoot, subObjs); err == nil {
				rf = loaded
			}
		}
		b, err := lead(ctx, q, rf)
		if err != nil || strings.TrimSpace(b) == "" {
			body = deterministicSynthesize(q, findings)
		} else {
			body = b
		}
	} else {
		body = deterministicSynthesize(q, findings)
	}
	if o.RunRoot != "" {
		_ = os.WriteFile(filepath.Join(o.RunRoot, "synthesis.md"), []byte(body), 0o644)
	}
	o.emit("synthesis.ready", map[string]any{"bytes": len(body)})

	// Stage 4: flatten Findings into Claims + deduplicated Sources.
	claims, sources := claimsFromFindings(findings)

	rep := Report{
		Query:   q,
		Body:    body,
		Claims:  claims,
		Sources: sources,
	}
	o.emit("completed", map[string]any{
		"claims":  len(claims),
		"sources": len(sources),
	})
	return rep, nil
}

// emit is the nil-safe helper around OnEvent so each call site stays
// a one-liner rather than a three-line conditional.
func (o *Orchestrator) emit(event string, payload map[string]any) {
	if o == nil || o.OnEvent == nil {
		return
	}
	o.OnEvent(event, payload)
}

// routeURLs returns the candidate URLs for one SubObjective,
// prioritising per-hint matches over the GlobalURLs fallback. Mirrors
// the single-agent executor's collectURLs so both paths consume the
// same Plan.Extra shape. Duplicates are removed; order preserved.
func (o *Orchestrator) routeURLs(obj SubObjective) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, u)
	}
	if o.URLsByHint != nil {
		for _, h := range obj.Hints {
			for _, u := range o.URLsByHint[h] {
				add(u)
			}
		}
	}
	for _, u := range o.GlobalURLs {
		add(u)
	}
	return out
}

// deterministicSubagent is the built-in SubFn. It fetches each URL
// sequentially (inside a single subagent goroutine — the parallelism
// is across subagents, not inside one) and ranks sentences by
// keyword overlap with the SubObjective's Objective. Uses only the
// exported primitives from this package so it has no dependency on
// the executor package.
func (o *Orchestrator) deterministicSubagent(ctx context.Context, obj SubObjective, f Fetcher, urls []string) (Findings, error) {
	out := Findings{SubObjective: obj}
	if f == nil {
		return out, fmt.Errorf("subagent: nil Fetcher")
	}
	maxClaims := o.MaxClaimsPerSub
	if maxClaims <= 0 {
		maxClaims = 3
	}
	type scored struct {
		sent string
		url  string
		sc   float64
	}
	// Score sentences against the combined objective+hints token
	// set. Hints carry the real entity names (e.g. "Postgres",
	// "MySQL") that the heuristic decomposer strips from the
	// Objective prose; without them, short operand names tokenise
	// out entirely and no sentence would score above zero.
	scoreQuery := obj.Objective
	if len(obj.Hints) > 0 {
		scoreQuery = scoreQuery + " " + strings.Join(obj.Hints, " ")
	}
	var ranked []scored
	seenURL := map[string]bool{}
	for _, u := range urls {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		body, err := f.Fetch(ctx, u)
		if err != nil || body == "" {
			continue
		}
		if !seenURL[u] {
			seenURL[u] = true
			out.Sources = append(out.Sources, Source{URL: u, Title: extractHTMLTitle(body)})
		}
		text := StripHTMLPublic(body)
		if text == "" {
			continue
		}
		for _, s := range splitSentencesPlain(text) {
			st := strings.TrimSpace(s)
			if len(st) < 20 || len(st) > 400 {
				continue
			}
			sc := overlapScoreLocal(scoreQuery, st)
			if sc <= 0 {
				continue
			}
			ranked = append(ranked, scored{sent: st, url: u, sc: sc})
		}
	}
	// Stable sort by score desc, then by sentence for tie-break so
	// the output is deterministic across runs.
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].sc != ranked[j].sc {
			return ranked[i].sc > ranked[j].sc
		}
		return ranked[i].sent < ranked[j].sent
	})
	if len(ranked) > maxClaims {
		ranked = ranked[:maxClaims]
	}
	for _, r := range ranked {
		out.Sentences = append(out.Sentences, r.sent)
		out.SourceForSentence = append(out.SourceForSentence, r.url)
	}
	// Summary is a bullet list — stable, deterministic, and useful
	// to a human reader of subagent-<i>/findings.md.
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", obj.Objective)
	if len(out.Sentences) == 0 {
		b.WriteString("_No relevant sentences extracted._\n")
	} else {
		for _, s := range out.Sentences {
			fmt.Fprintf(&b, "- %s\n", s)
		}
	}
	out.Summary = b.String()
	return out, nil
}

// buildSubObjectives collapses a SubQuestion slice into a
// SubObjective slice sized by effort. Extras (when effort caps below
// len(subs)) are folded into the last SubObjective's TaskBoundaries
// so they still get worked on.
func buildSubObjectives(subs []SubQuestion, effort Effort, maxPar int) []SubObjective {
	if len(subs) == 0 {
		return nil
	}
	cap := effortCap(effort, maxPar, len(subs))
	if cap <= 0 {
		cap = 1
	}
	if cap >= len(subs) {
		out := make([]SubObjective, len(subs))
		for i, s := range subs {
			out[i] = subObjectiveFrom(s)
		}
		return out
	}
	// Take the first `cap` directly; fold the rest into the last
	// SubObjective's TaskBoundaries.
	out := make([]SubObjective, cap)
	for i := 0; i < cap-1; i++ {
		out[i] = subObjectiveFrom(subs[i])
	}
	// Last subagent inherits cap-1 through end.
	last := subObjectiveFrom(subs[cap-1])
	extras := subs[cap:]
	if len(extras) > 0 {
		var b strings.Builder
		b.WriteString("Also cover the following scope (merged due to effort budget):\n")
		for _, e := range extras {
			fmt.Fprintf(&b, "- %s\n", e.Text)
			last.Hints = append(last.Hints, e.Hints...)
		}
		if last.TaskBoundaries != "" {
			last.TaskBoundaries += "\n\n"
		}
		last.TaskBoundaries += b.String()
	}
	out[cap-1] = last
	return out
}

// effortCap returns how many subagents this effort level allows for
// a decomposition of length n. The table mirrors RT-07 §1.3:
//
//   - Minimal  → 1 (all scope folded into a single subagent)
//   - Standard → min(4, n) — concurrency (MaxParallel) is a
//     separate concern handled by errgroup.SetLimit
//   - Thorough → min(MaxParallel, n) — operators scale fan-out by
//     increasing MaxParallel
//   - Critical → same as Thorough
//
// Keeping MaxParallel OUT of the Standard cap means a test can set
// MaxParallel=1 to force serial execution of ≥2 subagents — the
// decomposition still produces multiple subagents, but errgroup
// runs them one at a time.
func effortCap(effort Effort, maxPar, n int) int {
	switch effort {
	case EffortMinimal:
		return 1
	case EffortThorough, EffortCritical:
		if n < maxPar {
			return n
		}
		return maxPar
	case EffortStandard, "":
		m := 4
		if n < m {
			m = n
		}
		return m
	default:
		m := 4
		if n < m {
			m = n
		}
		return m
	}
}

// subObjectiveFrom builds a SubObjective from one SubQuestion with
// default OutputFormat / ToolGuidance text. Hints are carried
// through so URL routing keeps working identically to the single-
// agent path.
func subObjectiveFrom(sq SubQuestion) SubObjective {
	return SubObjective{
		Objective:      sq.Text,
		OutputFormat:   "Bullet list of 1-3 verifiable one-sentence claims, each with a citable source URL.",
		ToolGuidance:   "Use the provided Fetcher to read candidate pages. Prefer primary sources.",
		TaskBoundaries: "",
		Hints:          append([]string(nil), sq.Hints...),
	}
}

// writePlanFile persists plan.md at the RunRoot so a human (or a
// Lead agent) can review the decomposition independently of any
// in-memory state. Deterministic output order per SubObjective index.
func writePlanFile(runRoot, query string, objs []SubObjective) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Research Plan\n\n")
	fmt.Fprintf(&b, "Query: %s\n\n", query)
	fmt.Fprintf(&b, "Subagents: %d\n\n", len(objs))
	for i, o := range objs {
		fmt.Fprintf(&b, "## Subagent %d\n\n", i+1)
		fmt.Fprintf(&b, "Objective: %s\n\n", o.Objective)
		if o.OutputFormat != "" {
			fmt.Fprintf(&b, "Output Format: %s\n\n", o.OutputFormat)
		}
		if o.ToolGuidance != "" {
			fmt.Fprintf(&b, "Tool Guidance: %s\n\n", o.ToolGuidance)
		}
		if o.TaskBoundaries != "" {
			fmt.Fprintf(&b, "Task Boundaries:\n%s\n\n", o.TaskBoundaries)
		}
		if len(o.Hints) > 0 {
			fmt.Fprintf(&b, "Hints: %s\n\n", strings.Join(o.Hints, ", "))
		}
	}
	return os.WriteFile(filepath.Join(runRoot, "plan.md"), []byte(b.String()), 0o644)
}

// writeSubagentDir persists one subagent's artefacts as three files
// under <runRoot>/subagent-<i+1>/:
//
//	objective.md  — SubObjective in markdown
//	findings.md   — Summary + Sentences
//	sources.jsonl — one {url,title,fetched_at} per line
//
// The 1-based directory index (subagent-1, subagent-2, ...) matches
// the plan.md section headers for easy cross-referencing.
func writeSubagentDir(runRoot string, idx int, obj SubObjective, f Findings, fetchedAt time.Time) error {
	dir := filepath.Join(runRoot, fmt.Sprintf("subagent-%d", idx+1))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// objective.md
	var ob strings.Builder
	fmt.Fprintf(&ob, "# Objective\n\n%s\n\n", obj.Objective)
	if obj.OutputFormat != "" {
		fmt.Fprintf(&ob, "## Output Format\n\n%s\n\n", obj.OutputFormat)
	}
	if obj.ToolGuidance != "" {
		fmt.Fprintf(&ob, "## Tool Guidance\n\n%s\n\n", obj.ToolGuidance)
	}
	if obj.TaskBoundaries != "" {
		fmt.Fprintf(&ob, "## Task Boundaries\n\n%s\n", obj.TaskBoundaries)
	}
	if err := os.WriteFile(filepath.Join(dir, "objective.md"), []byte(ob.String()), 0o644); err != nil {
		return err
	}
	// findings.md
	if err := os.WriteFile(filepath.Join(dir, "findings.md"), []byte(f.Summary), 0o644); err != nil {
		return err
	}
	// sources.jsonl
	var sb strings.Builder
	for _, s := range f.Sources {
		rec := map[string]any{
			"url":        s.URL,
			"title":      s.Title,
			"fetched_at": fetchedAt.UTC().Format(time.RFC3339Nano),
		}
		line, err := json.Marshal(rec)
		if err != nil {
			continue
		}
		sb.Write(line)
		sb.WriteByte('\n')
	}
	return os.WriteFile(filepath.Join(dir, "sources.jsonl"), []byte(sb.String()), 0o644)
}

// readFindingsFromDisk reconstructs a []Findings by reading each
// subagent-<i>/findings.md + sources.jsonl. Used by the LeadFn path
// to honour the filesystem-as-communication contract. Missing files
// yield an empty Findings entry at that index so the LeadFn still
// sees the full SubObjective list.
func readFindingsFromDisk(runRoot string, objs []SubObjective) ([]Findings, error) {
	out := make([]Findings, len(objs))
	for i, obj := range objs {
		dir := filepath.Join(runRoot, fmt.Sprintf("subagent-%d", i+1))
		f := Findings{SubObjective: obj}
		if body, err := os.ReadFile(filepath.Join(dir, "findings.md")); err == nil {
			f.Summary = string(body)
		}
		if raw, err := os.ReadFile(filepath.Join(dir, "sources.jsonl")); err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				var rec struct {
					URL   string `json:"url"`
					Title string `json:"title"`
				}
				if err := json.Unmarshal([]byte(line), &rec); err == nil && rec.URL != "" {
					f.Sources = append(f.Sources, Source{URL: rec.URL, Title: rec.Title})
				}
			}
		}
		out[i] = f
	}
	return out, nil
}

// claimsFromFindings flattens per-subagent Findings into Claims and
// a deduplicated Sources list. Stable IDs (C-1, C-2, ...) are
// assigned in fan-out order so the descent engine's AC set is
// reproducible under a fixed Fetcher.
func claimsFromFindings(findings []Findings) ([]Claim, []Source) {
	var claims []Claim
	var sources []Source
	seenURL := map[string]bool{}
	n := 0
	for _, f := range findings {
		for i, s := range f.Sentences {
			url := ""
			if i < len(f.SourceForSentence) {
				url = f.SourceForSentence[i]
			}
			if url == "" && len(f.Sources) > 0 {
				url = f.Sources[0].URL
			}
			if url == "" {
				continue
			}
			n++
			claims = append(claims, Claim{
				ID:        fmt.Sprintf("C-%d", n),
				Text:      s,
				SourceURL: url,
			})
		}
		for _, s := range f.Sources {
			if seenURL[s.URL] {
				continue
			}
			seenURL[s.URL] = true
			sources = append(sources, s)
		}
	}
	return claims, sources
}

// deterministicSynthesize is the orchestrator's built-in markdown
// synthesiser. Identical in spirit to the executor's
// DeterministicSynthesize (but duplicated here to avoid an import
// cycle research <- executor — the executor depends on research, not
// the other way around). Given the same findings, produces the same
// body byte-for-byte.
func deterministicSynthesize(query string, findings []Findings) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Research report: %s\n\n", query)
	for i, f := range findings {
		fmt.Fprintf(&b, "## Subagent %d: %s\n\n", i+1, f.SubObjective.Objective)
		if f.Err != nil {
			fmt.Fprintf(&b, "_Subagent failed: %s_\n\n", f.Err.Error())
			continue
		}
		if len(f.Sentences) == 0 {
			b.WriteString("_No sentences extracted._\n\n")
		} else {
			for _, s := range f.Sentences {
				fmt.Fprintf(&b, "- %s\n", s)
			}
			b.WriteString("\n")
		}
		if len(f.Sources) > 0 {
			b.WriteString("Sources:\n")
			for _, s := range f.Sources {
				if s.Title != "" {
					fmt.Fprintf(&b, "- [%s](%s)\n", s.Title, s.URL)
				} else {
					fmt.Fprintf(&b, "- %s\n", s.URL)
				}
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// overlapScoreLocal returns a rough keyword-overlap score between
// query and body — same shape as the executor's overlapScore but
// re-implemented on the exported TokenSetPublic so this package
// stays importable from tests without executor.
func overlapScoreLocal(query, body string) float64 {
	q := TokenSetPublic(query)
	if len(q) == 0 {
		return 0
	}
	b := TokenSetPublic(body)
	if len(b) == 0 {
		return 0
	}
	matched := 0
	for t := range q {
		if b[t] {
			matched++
		}
	}
	return float64(matched) / float64(len(q))
}

// splitSentencesPlain is a minimal sentence splitter on . ! ?
// boundaries. Mirrors executor.splitSentences so the orchestrator
// produces comparable sentence sets to the single-agent path.
func splitSentencesPlain(text string) []string {
	text = strings.ReplaceAll(text, "\n", " ")
	var out []string
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		c := text[i]
		b.WriteByte(c)
		if c == '.' || c == '!' || c == '?' {
			if i+1 >= len(text) || text[i+1] == ' ' {
				s := strings.TrimSpace(b.String())
				if s != "" {
					out = append(out, s)
				}
				b.Reset()
			}
		}
	}
	if rest := strings.TrimSpace(b.String()); rest != "" {
		out = append(out, rest)
	}
	return out
}

// extractHTMLTitle returns the first <title>…</title> content if
// present. Duplicated from executor.extractTitle to avoid the
// import cycle — this package must not import executor.
func extractHTMLTitle(body string) string {
	low := strings.ToLower(body)
	start := strings.Index(low, "<title")
	if start < 0 {
		return ""
	}
	gt := strings.Index(body[start:], ">")
	if gt < 0 {
		return ""
	}
	open := start + gt + 1
	end := strings.Index(strings.ToLower(body[open:]), "</title>")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(body[open : open+end])
}

// compile-time sanity: SubObjective.Validate returns an error so
// callers can treat Validate in table-driven tests uniformly with
// other Validate-bearing types.
var _ = func() error { return (SubObjective{}).Validate() }

// sortInterfaceCheck documents that we depend on sort.SliceStable
// being available in the stdlib. Unused at runtime; kept so a stdlib
// removal (extremely unlikely) surfaces as a build error here with
// a clear pointer to the orchestrator's ranking contract.
var _ = sort.SliceStable

// syncInterfaceCheck similarly documents sync.Mutex as a potential
// future concurrency primitive when we add a shared rate-limiter
// around SubFn calls. Not used today.
var _ sync.Mutex
