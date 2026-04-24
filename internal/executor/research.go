// research.go: real ResearchExecutor.
//
// This is the claim-gated research backend. The deliverable is a
// research.Report with per-claim SourceURL; BuildCriteria produces
// one AcceptanceCriterion per claim whose VerifyFunc fetches the
// cited URL and confirms the page supports the claim. The descent
// engine then runs its standard 8-tier ladder over the AC set — a
// missed claim can be repaired (re-search) at T4/T7 or soft-passed
// at T8 when the source is consistently uncooperative.
//
// Architecture seams on ResearchExecutor (each is an injectable
// field; swapping one does not require touching the others):
//
//   - Decomposer (LLMDecomposer) — LANDED. When non-nil (or when
//     Provider is set and Decomposer is auto-constructed) the
//     executor asks the LLM for 1..N sub-questions sized by effort.
//     On any failure the heuristic Planner takes over — no call to
//     a live provider is ever load-bearing for availability.
//   - Fetcher — upgrade to a browser-backed fetcher for JS-rendered
//     pages; Track B Task 21 wires the go-rod implementation.
//   - Synthesize — replace DeterministicSynthesize with an LLM
//     synthesis over SubQuestionAnswers. In fan-out mode the answers
//     are reconstructed from disk (see executeFanOut) so the lead
//     honors the filesystem-as-channel contract.
//   - Subagent fan-out (SessionDir + MaxParallel) — LANDED. When
//     SessionDir is non-empty, Execute spawns one subagent per
//     SubQuestion via errgroup, each writes a JSON finding file
//     under <SessionDir>/<session>/<subq-id>.json, and the lead
//     reads those files back before synthesis. When SessionDir is
//     empty the executor keeps the legacy inline behaviour for
//     callers that haven't opted in.

package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/RelayOne/r1/internal/plan"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/research"
)

// ResearchExecutor satisfies Executor for TaskResearch. It runs
// lead-and-subagents when Decomposer is wired to an LLM-backed
// provider, and falls back to the stdlib-only single-agent path
// (HeuristicDecomposer + sequential fetch) otherwise. Subagents fan
// out via errgroup and write per-sub-question findings to disk under
// SessionDir; the lead synthesis then reads those files back so the
// communication contract is "filesystem as channel", not raw in-
// memory state.
type ResearchExecutor struct {
	// Planner drives query decomposition via the classic
	// research.Decomposer interface. When Decomposer (below) is also
	// set, Decomposer takes priority and Planner is only consulted
	// as the fallback path when the LLM call fails.
	Planner *research.Planner

	// Decomposer is the LLM-backed decomposer. Optional; when nil
	// the executor falls back to Planner. On a non-nil Decomposer
	// whose Decompose call returns an error, the executor still
	// falls back to Planner so a provider outage does not take the
	// whole research path down.
	Decomposer *LLMDecomposer

	// Fetcher is used for BOTH the subagent search phase (fetching
	// candidate pages the executor knows about via the Plan's Extra
	// map) and by the descent verifier (fetching cited URLs to
	// confirm claim support). In the MVP, Execute only verifies
	// URLs the caller pre-supplied via Plan.Extra["urls"]; the full
	// search-provider integration lands with Track B Task 22.
	Fetcher research.Fetcher

	// Synthesize produces the Report body from the sub-question
	// answers. Defaults to DeterministicSynthesize (bullet points
	// per sub-question with cited sources); operators wire an LLM
	// synthesizer by setting this field. When a non-nil Provider
	// is also wired and SessionDir is non-empty, the synthesizer
	// receives answers reconstructed from disk rather than the raw
	// in-memory fan-out output — honouring the "lead reads from
	// filesystem refs" contract.
	Synthesize func(ctx context.Context, query string, answers []research.SubQuestionAnswer) string

	// MaxClaimsPerSubQ caps how many claims are extracted from any
	// single sub-question's top source. Default 3. Keeps the AC set
	// small enough that descent can iterate across all claims
	// within a reasonable budget.
	MaxClaimsPerSubQ int

	// SessionDir is the directory under which per-sub-question
	// findings are written:
	//
	//	<SessionDir>/<subq-id>.json
	//
	// A per-Execute call resolves the final path as
	// <SessionDir>/<session>/ where <session> is Plan.ID (falling
	// back to a sanitized query slug). Empty SessionDir disables
	// filesystem fan-out and the executor runs the in-memory path
	// (preserves the old behavior for callers that don't set it).
	SessionDir string

	// MaxParallel caps the number of subagents running concurrently
	// during fan-out. 0 → 4. Only applies when SessionDir is set
	// (filesystem fan-out path).
	MaxParallel int

	// Provider is the LLM provider used both by the default
	// LLMDecomposer (when Decomposer is nil) and by the lead
	// synthesis path. Optional — nil keeps the executor on the
	// deterministic path end-to-end.
	Provider provider.Provider

	// Model is the model name handed to Provider.Chat. Defaults to
	// "claude-sonnet-4-6" when empty. Shared by the decomposer and
	// the lead synthesis call.
	Model string
}

// NewResearchExecutor returns a ResearchExecutor wired with the given
// Fetcher and default Planner + Synthesize. Fetcher must not be nil;
// the caller decides between HTTPFetcher (production) and
// StubFetcher (tests).
func NewResearchExecutor(f research.Fetcher) *ResearchExecutor {
	return &ResearchExecutor{
		Planner:          research.NewPlanner(),
		Fetcher:          f,
		Synthesize:       DeterministicSynthesize,
		MaxClaimsPerSubQ: 3,
	}
}

// TaskType implements Executor.
func (e *ResearchExecutor) TaskType() TaskType { return TaskResearch }

// Execute performs the research task end-to-end and returns a
// ResearchDeliverable. The pipeline has two modes, selected by the
// executor's configuration:
//
//  1. FAN-OUT (SessionDir set): decompose via LLMDecomposer (falling
//     back to the heuristic Planner on error), fan out N subagents
//     via errgroup — each searches, reads, evaluates, and writes a
//     per-subq findings JSON to <SessionDir>/<session>/<subq-id>.json.
//     The lead then synthesizes from the filesystem refs (not the
//     raw in-memory output), producing the Report body.
//
//  2. INLINE (SessionDir empty): preserve the original single-pass
//     behavior — decompose via Planner, fetch candidate URLs
//     sequentially per SubQuestion, extract claims, and synthesize.
//     This is the fallback for callers that haven't opted into the
//     filesystem-backed fan-out.
//
// In both modes, the set of candidate URLs comes from Plan.Extra —
// either Extra["urls_by_hint"] (map[string][]string keyed by
// SubQuestion hint) or Extra["urls"] (global []string). Without any
// URLs the Report still renders; Claims will be empty and descent's
// AC set soft-passes.
func (e *ResearchExecutor) Execute(ctx context.Context, p Plan, effort EffortLevel) (Deliverable, error) {
	if e.Fetcher == nil {
		return nil, fmt.Errorf("research executor: no Fetcher configured")
	}
	syn := e.Synthesize
	if syn == nil {
		syn = DeterministicSynthesize
	}
	maxClaims := e.MaxClaimsPerSubQ
	if maxClaims <= 0 {
		maxClaims = 3
	}

	// Stage 1: decompose. LLM path first; fall back to the
	// heuristic planner on any failure so a provider outage never
	// stalls research entirely.
	subs := e.decompose(ctx, p.Query, effort)

	// Resolve candidate URLs from Plan.Extra. Two shapes are
	// supported:
	//   Extra["urls_by_hint"] map[string][]string  — per-hint URLs
	//   Extra["urls"]         []string             — global URLs
	byHint, _ := p.Extra["urls_by_hint"].(map[string][]string)
	globalURLs, _ := p.Extra["urls"].([]string)

	// Stage 2: fan-out if SessionDir is set, else run inline.
	if strings.TrimSpace(e.SessionDir) != "" {
		return e.executeFanOut(ctx, p, subs, byHint, globalURLs, syn, maxClaims)
	}
	return e.executeInline(ctx, p, subs, byHint, globalURLs, syn, maxClaims)
}

// decompose chooses between the LLM decomposer and the heuristic
// planner. The LLM path wins when available; any error falls back to
// the planner so the executor remains usable without a provider.
func (e *ResearchExecutor) decompose(ctx context.Context, query string, effort EffortLevel) []research.SubQuestion {
	// When a Provider is wired but no explicit Decomposer, build a
	// default LLMDecomposer so callers don't have to set both fields.
	dec := e.Decomposer
	if dec == nil && e.Provider != nil {
		dec = &LLMDecomposer{Provider: e.Provider, Model: e.Model}
	}
	if dec != nil {
		subs, err := dec.Decompose(ctx, query, effort)
		if err == nil && len(subs) > 0 {
			return subs
		}
		// Fall through to the heuristic path.
	}
	planner := e.Planner
	if planner == nil {
		planner = research.NewPlanner()
	}
	return planner.Plan(query)
}

// executeInline runs the original single-pass path: one sequential
// loop over SubQuestions, returning the aggregated Report. Preserved
// so callers that don't opt into SessionDir get byte-for-byte the
// prior behavior.
func (e *ResearchExecutor) executeInline(
	ctx context.Context,
	p Plan,
	subs []research.SubQuestion,
	byHint map[string][]string,
	globalURLs []string,
	syn func(ctx context.Context, query string, answers []research.SubQuestionAnswer) string,
	maxClaims int,
) (Deliverable, error) {
	var (
		claims  []research.Claim
		sources []research.Source
		seenURL = map[string]bool{}
		claimN  int
	)
	answers := make([]research.SubQuestionAnswer, 0, len(subs))

	for _, sq := range subs {
		urls := collectURLs(sq, byHint, globalURLs)
		ans := research.SubQuestionAnswer{Question: sq}

		var bestBody, bestURL string
		bestScore := -1.0
		for _, u := range urls {
			body, err := e.Fetcher.Fetch(ctx, u)
			if err != nil || body == "" {
				continue
			}
			score := overlapScore(sq.Text, body)
			if score > bestScore {
				bestScore = score
				bestBody = body
				bestURL = u
			}
			if !seenURL[u] {
				seenURL[u] = true
				sources = append(sources, research.Source{URL: u, Title: extractTitle(body)})
				ans.Sources = append(ans.Sources, research.Source{URL: u, Title: extractTitle(body)})
			}
		}

		if bestURL != "" {
			sents := topSentences(bestBody, sq.Text, maxClaims)
			ans.Sentences = sents
			for _, s := range sents {
				claimN++
				claims = append(claims, research.Claim{
					ID:        fmt.Sprintf("C-%d", claimN),
					Text:      s,
					SourceURL: bestURL,
				})
			}
		}

		answers = append(answers, ans)
	}

	body := syn(ctx, p.Query, answers)
	rep := research.Report{
		Query:   p.Query,
		Body:    body,
		Claims:  claims,
		Sources: sources,
	}
	return ResearchDeliverable{Report: rep, Sources: sources}, nil
}

// executeFanOut runs subagents in parallel via errgroup. Each worker
// does its own search → read → evaluate pass over the candidate URLs
// for its SubQuestion and writes the resulting findings to
// <SessionDir>/<session>/<subq-id>.json. Once all workers complete,
// the lead reads the findings BACK from disk (filesystem-as-channel)
// and synthesizes the Report body.
func (e *ResearchExecutor) executeFanOut(
	ctx context.Context,
	p Plan,
	subs []research.SubQuestion,
	byHint map[string][]string,
	globalURLs []string,
	syn func(ctx context.Context, query string, answers []research.SubQuestionAnswer) string,
	maxClaims int,
) (Deliverable, error) {
	sessionDir, err := e.resolveSessionDir(p)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return nil, fmt.Errorf("research executor: mkdir session dir: %w", err)
	}

	maxPar := e.MaxParallel
	if maxPar <= 0 {
		maxPar = 4
	}

	// Fan-out: each subagent writes its finding file; errgroup
	// bounds concurrency. We DO propagate ctx cancel via
	// errgroup.WithContext, but we never let a per-subagent fetch
	// error fail the whole run — the finding file still lands (with
	// Err populated) so the lead has a deterministic input set.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxPar)
	var mu sync.Mutex
	paths := make([]string, len(subs))
	for i := range subs {
		i := i
		sq := subs[i]
		urls := collectURLs(sq, byHint, globalURLs)
		g.Go(func() error {
			f := runSubagentFindings(gctx, e.Fetcher, sq, urls, maxClaims)
			path := filepath.Join(sessionDir, sanitizeID(sq.ID)+".json")
			if werr := writeFindingJSON(path, f); werr != nil {
				return fmt.Errorf("subagent %s: write finding: %w", sq.ID, werr)
			}
			mu.Lock()
			paths[i] = path
			mu.Unlock()
			return nil
		})
	}
	if werr := g.Wait(); werr != nil {
		return nil, werr
	}

	// Stage 3: lead synthesis from filesystem refs. Read each
	// finding file back; build SubQuestionAnswer + Claim lists;
	// hand the answers to the Synthesize func.
	answers, claims, sources := assembleFromFindings(paths, subs)
	body := syn(ctx, p.Query, answers)
	rep := research.Report{
		Query:   p.Query,
		Body:    body,
		Claims:  claims,
		Sources: sources,
	}
	return ResearchDeliverable{Report: rep, Sources: sources}, nil
}

// subagentFinding is the on-disk payload one subagent writes after
// its search+read+evaluate pass. Kept JSON-stable (explicit field
// tags, no embedded interfaces) so the lead reading the file back
// does not depend on runtime type identity.
type subagentFinding struct {
	Question  research.SubQuestion `json:"question"`
	Sources   []research.Source    `json:"sources"`
	Sentences []string             `json:"sentences"`
	BestURL   string               `json:"best_url,omitempty"`
	Err       string               `json:"err,omitempty"`
}

// runSubagentFindings is the single-subagent pipeline: fetch each
// candidate URL, pick the highest-overlap body, extract the top
// sentences. Returns a subagentFinding — the caller persists it.
// Never returns an error: per-URL fetch failures are tolerated so
// other sources still contribute to Sources, and a total failure
// yields an empty Sentences slice (Err is populated).
func runSubagentFindings(
	ctx context.Context,
	f research.Fetcher,
	sq research.SubQuestion,
	urls []string,
	maxClaims int,
) subagentFinding {
	out := subagentFinding{Question: sq}
	if f == nil {
		out.Err = "nil Fetcher"
		return out
	}
	var (
		bestBody, bestURL string
		bestScore         = -1.0
		seen              = map[string]bool{}
	)
	for _, u := range urls {
		if err := ctx.Err(); err != nil {
			out.Err = err.Error()
			return out
		}
		body, err := f.Fetch(ctx, u)
		if err != nil || body == "" {
			continue
		}
		score := overlapScore(sq.Text, body)
		if score > bestScore {
			bestScore = score
			bestBody = body
			bestURL = u
		}
		if !seen[u] {
			seen[u] = true
			out.Sources = append(out.Sources, research.Source{URL: u, Title: extractTitle(body)})
		}
	}
	if bestURL != "" {
		out.Sentences = topSentences(bestBody, sq.Text, maxClaims)
		out.BestURL = bestURL
	}
	return out
}

// writeFindingJSON marshals a subagentFinding and writes it to path.
// Directory is expected to exist (the fan-out caller already ran
// MkdirAll). Uses 0o644 so the file is readable by the lead without
// elevated privileges; findings are never secrets.
func writeFindingJSON(path string, f subagentFinding) error {
	blob, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, blob, 0o644) // #nosec G306 -- executor research artefact; user-readable.
}

// readFindingJSON reads one finding file back. Missing / unparseable
// files yield an empty-Sentences finding so the lead's synthesis input
// is always the full subagent slice length — the lead may surface a
// "subagent N produced no findings" note downstream.
func readFindingJSON(path string) (subagentFinding, error) {
	var out subagentFinding
	raw, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	if uerr := json.Unmarshal(raw, &out); uerr != nil {
		return out, uerr
	}
	return out, nil
}

// assembleFromFindings reads each finding file and flattens the
// results into the shape the lead synthesizer expects: a parallel
// SubQuestionAnswer slice for the prompt template and a flat Claim
// slice for descent's AC set. Duplicate source URLs across subagents
// collapse into a single entry in the final Sources list.
func assembleFromFindings(paths []string, subs []research.SubQuestion) (
	[]research.SubQuestionAnswer, []research.Claim, []research.Source,
) {
	answers := make([]research.SubQuestionAnswer, 0, len(paths))
	var claims []research.Claim
	var sources []research.Source
	seenURL := map[string]bool{}
	claimN := 0
	for i, pth := range paths {
		var sq research.SubQuestion
		if i < len(subs) {
			sq = subs[i]
		}
		ans := research.SubQuestionAnswer{Question: sq}
		if pth == "" {
			answers = append(answers, ans)
			continue
		}
		f, err := readFindingJSON(pth)
		if err != nil {
			answers = append(answers, ans)
			continue
		}
		// Prefer the finding's own Question (populated by the
		// subagent) over the caller's parallel slice, in case the
		// decomposer re-ordered or the caller drifted.
		if strings.TrimSpace(f.Question.Text) != "" {
			ans.Question = f.Question
		}
		ans.Sources = append(ans.Sources, f.Sources...)
		ans.Sentences = append(ans.Sentences, f.Sentences...)
		for _, s := range f.Sources {
			if seenURL[s.URL] {
				continue
			}
			seenURL[s.URL] = true
			sources = append(sources, s)
		}
		if f.BestURL != "" {
			for _, s := range f.Sentences {
				claimN++
				claims = append(claims, research.Claim{
					ID:        fmt.Sprintf("C-%d", claimN),
					Text:      s,
					SourceURL: f.BestURL,
				})
			}
		}
		answers = append(answers, ans)
	}
	return answers, claims, sources
}

// resolveSessionDir returns the per-Execute directory. It is
// <SessionDir>/<session>/ where <session> is Plan.ID (when set) or a
// sanitized slug derived from Plan.Query. The caller is responsible
// for MkdirAll-ing the result.
func (e *ResearchExecutor) resolveSessionDir(p Plan) (string, error) {
	root := strings.TrimSpace(e.SessionDir)
	if root == "" {
		return "", fmt.Errorf("research executor: empty SessionDir")
	}
	sess := strings.TrimSpace(p.ID)
	if sess == "" {
		sess = sanitizeID(p.Query)
	}
	if sess == "" {
		sess = "session"
	}
	return filepath.Join(root, sess), nil
}

// sanitizeID maps a free-form string to a path-safe slug. Keeps
// [A-Za-z0-9._-]; collapses everything else to '-'. An empty input
// returns "" so callers can apply their own default.
func sanitizeID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.' || r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	// Collapse consecutive dashes so runs of non-allowed chars (e.g.
	// "//") don't produce double-dashes in the slug.
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	// Trim leading/trailing dashes so path segments look clean.
	out = strings.Trim(out, "-")
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// BuildCriteria implements Executor. One AC per Claim — each AC's
// VerifyFunc fetches the cited URL and confirms the claim text is
// supported by the page. Descent runs its standard ladder over this
// AC set.
func (e *ResearchExecutor) BuildCriteria(t Task, d Deliverable) []plan.AcceptanceCriterion {
	rd, ok := d.(ResearchDeliverable)
	if !ok {
		return nil
	}
	out := make([]plan.AcceptanceCriterion, 0, len(rd.Report.Claims))
	for _, c := range rd.Report.Claims {
		c := c // capture per-iteration
		fetcher := e.Fetcher
		out = append(out, plan.AcceptanceCriterion{
			ID:          c.ID,
			Description: fmt.Sprintf("source supports claim: %s", truncate(c.Text, 80)),
			VerifyFunc: func(ctx context.Context) (bool, string) {
				return research.VerifyClaim(ctx, c, fetcher)
			},
		})
	}
	return out
}

// BuildRepairFunc implements Executor. Research-specific repair
// (re-search for better sources for the failing claim) will land
// with the subagent fan-out task; today descent uses T7 refactor-for-
// verifiability or T8 soft-pass when we return nil here.
func (e *ResearchExecutor) BuildRepairFunc(p Plan) RepairFunc {
	return nil
}

// BuildEnvFixFunc implements Executor. Classifies a failure as
// transient (timeout / 5xx / "connection refused") — in that case
// descent may retry at T5 before burning a real repair attempt.
func (e *ResearchExecutor) BuildEnvFixFunc() EnvFixFunc {
	return func(ctx context.Context, rootCause, stderr string) bool {
		low := strings.ToLower(rootCause + " " + stderr)
		for _, transient := range []string{
			"timeout", "timed out", "i/o timeout",
			"connection refused", "connection reset",
			"temporary failure", "no such host",
			" 502 ", " 503 ", " 504 ",
		} {
			if strings.Contains(low, transient) {
				return true
			}
		}
		return false
	}
}

// ResearchDeliverable is what ResearchExecutor returns. Callers that
// need the typed payload (the CLI pretty-printer, the JSON dumper)
// type-assert on this concrete type.
type ResearchDeliverable struct {
	Report  research.Report
	Sources []research.Source
}

// Summary implements Deliverable.
func (d ResearchDeliverable) Summary() string {
	return fmt.Sprintf("research: %d claims, %d sources",
		len(d.Report.Claims), len(d.Sources))
}

// Size implements Deliverable.
func (d ResearchDeliverable) Size() int { return len(d.Report.Body) }

// Compile-time interface check.
var _ Executor = (*ResearchExecutor)(nil)

// ---- Helpers ----------------------------------------------------------------

// DeterministicSynthesize produces a report body that lists each
// sub-question with its top sentences and a small Sources section.
// Deterministic — given the same SubQuestionAnswers, it always
// returns the same body. Callers swap this for an LLM-backed
// synthesizer by setting ResearchExecutor.Synthesize.
func DeterministicSynthesize(_ context.Context, query string, answers []research.SubQuestionAnswer) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Research report: %s\n\n", query)
	for _, a := range answers {
		fmt.Fprintf(&b, "## %s\n\n", a.Question.Text)
		if len(a.Sentences) == 0 {
			b.WriteString("_No sentences extracted._\n\n")
			continue
		}
		for _, s := range a.Sentences {
			fmt.Fprintf(&b, "- %s\n", s)
		}
		if len(a.Sources) > 0 {
			b.WriteString("\nSources:\n")
			for _, s := range a.Sources {
				if s.Title != "" {
					fmt.Fprintf(&b, "- [%s](%s)\n", s.Title, s.URL)
				} else {
					fmt.Fprintf(&b, "- %s\n", s.URL)
				}
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// collectURLs returns the candidate URLs for one sub-question:
// per-hint matches first, then the global list. Order preserved;
// duplicates removed.
func collectURLs(sq research.SubQuestion, byHint map[string][]string, global []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, u)
	}
	if byHint != nil {
		for _, h := range sq.Hints {
			for _, u := range byHint[h] {
				add(u)
			}
		}
	}
	for _, u := range global {
		add(u)
	}
	return out
}

// overlapScore returns a rough keyword overlap score between query
// and body text — used to pick the best source per sub-question.
// Stdlib only, via a simple token-set jaccard-ish measure. Negative
// return is impossible; callers start bestScore at -1 so any body
// beats "no body seen yet".
func overlapScore(query, body string) float64 {
	q := researchTokenSet(query)
	if len(q) == 0 {
		return 0
	}
	b := researchTokenSet(body)
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

// topSentences returns the top-N sentences from body ranked by token
// overlap with query. Used to pick claim-sized excerpts per
// sub-question. Uses a simple regex-free sentence splitter on .!?
// because spec says stdlib-only and this is enough signal at the MVP
// level. An LLM synthesizer (future seam) replaces this whole path.
func topSentences(body, query string, n int) []string {
	text := research.StripHTMLPublic(body)
	if text == "" || n <= 0 {
		return nil
	}
	sents := splitSentences(text)
	if len(sents) == 0 {
		return nil
	}
	q := researchTokenSet(query)
	type scored struct {
		s     string
		score float64
	}
	ranked := make([]scored, 0, len(sents))
	for _, s := range sents {
		st := strings.TrimSpace(s)
		if len(st) < 20 || len(st) > 400 {
			continue
		}
		ts := researchTokenSet(st)
		matched := 0
		for t := range q {
			if ts[t] {
				matched++
			}
		}
		if matched == 0 {
			continue
		}
		ranked = append(ranked, scored{s: st, score: float64(matched)})
	}
	// Simple insertion sort on ranked; small N so O(k^2) is fine.
	for i := 1; i < len(ranked); i++ {
		for j := i; j > 0 && ranked[j].score > ranked[j-1].score; j-- {
			ranked[j], ranked[j-1] = ranked[j-1], ranked[j]
		}
	}
	if len(ranked) > n {
		ranked = ranked[:n]
	}
	out := make([]string, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, r.s)
	}
	return out
}

// splitSentences is a minimal sentence splitter on ., !, ? boundaries.
// Not I18N-aware; adequate for the MVP's keyword-based claim
// extraction.
func splitSentences(text string) []string {
	text = strings.ReplaceAll(text, "\n", " ")
	var out []string
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		c := text[i]
		b.WriteByte(c)
		if c == '.' || c == '!' || c == '?' {
			// Require a following space (or end of string) to treat
			// the boundary as real — avoids splitting inside URLs
			// and abbreviations like "e.g.".
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

// researchTokenSet mirrors research.tokenSet behavior via the public
// helper. We could widen research.tokenSet to exported, but keeping
// it package-private keeps the API surface small; a tiny wrapper is
// cheaper than the name churn.
func researchTokenSet(s string) map[string]bool {
	return research.TokenSetPublic(s)
}

// extractTitle returns the first <title>…</title> content from body
// if present, otherwise empty. Best-effort and tolerant of missing
// tags. Keeps the HTMLish heuristic here (rather than in research/)
// so the research package stays focused on verification primitives.
func extractTitle(body string) string {
	low := strings.ToLower(body)
	start := strings.Index(low, "<title")
	if start < 0 {
		return ""
	}
	// Find the end of the opening tag.
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

// truncate returns s clipped to at most n runes, appending "..." when
// the clip was necessary. Used for AC descriptions so a verbose
// claim doesn't blow out dashboard formatting.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
