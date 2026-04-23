// research.go adds the claim-gated research primitives used by the
// ResearchExecutor (internal/executor/research.go). It coexists in
// the research package alongside the FTS5 persistence store
// (store.go) — the two serve different layers: Store is persistent
// cross-session research recall; Report / Claim / Planner here are
// the per-task artifacts that the executor produces and verifies.
//
// The types here are deliberately stdlib-only:
//   - Report / Claim / Source describe the deliverable a research
//     task returns.
//   - Planner drives query decomposition via a pluggable Decomposer
//     interface. The MVP HeuristicDecomposer is regex + keyword-based
//     and covers "X vs Y" comparisons and comma-separated lists.
//   - SubQuestionAnswer is the container passed to the executor's
//     Synthesize function so synthesis can be swapped for an LLM in
//     a future cycle without breaking callers.
//
// The seam for a future LLM planner is the Decomposer interface:
// swap HeuristicDecomposer for an LLMDecomposer that calls the judge
// provider and returns the same SubQuestion slice.

package research

import (
	"regexp"
	"strings"
)

// Report is the full deliverable produced by a research task. The
// Body is prose ready for display; Claims are the extracted
// propositions each paired with a cited SourceURL; Sources is the
// deduplicated list of all URLs referenced by any Claim. The descent
// engine verifies Report by running VerifyClaim over each Claim.
type Report struct {
	Query   string   `json:"query"`
	Body    string   `json:"body"`
	Claims  []Claim  `json:"claims"`
	Sources []Source `json:"sources"`
}

// Claim is a single verifiable proposition with a cited source. The
// ID is stable across the report's lifetime (used as the
// AcceptanceCriterion ID so descent retries map to the same claim).
type Claim struct {
	ID        string `json:"id"`         // stable identifier, e.g. "C-1"
	Text      string `json:"text"`       // one-sentence claim
	SourceURL string `json:"source_url"` // URL the verifier will fetch
}

// Source is a deduplicated reference entry. Title is best-effort from
// whatever the fetcher returned; may be empty when the provider did
// not supply one.
type Source struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

// SubQuestion is one decomposed leg of the primary query. Hints are
// free-form keywords the synthesizer may use to shape follow-up
// searches; the MVP Heuristic decomposer sets them to the split tokens.
type SubQuestion struct {
	ID    string   `json:"id"`
	Text  string   `json:"text"`
	Hints []string `json:"hints,omitempty"`
}

// SubQuestionAnswer pairs a SubQuestion with the sources and top
// sentences the executor surfaced while answering it. This is the
// structure the executor hands to its Synthesize function so a future
// LLM synthesizer can be dropped in without the caller changing
// shape.
type SubQuestionAnswer struct {
	Question  SubQuestion
	Sources   []Source
	Sentences []string // top-ranked sentences extracted from the highest-ranked source
}

// Decomposer turns a natural-language query into zero-or-more
// SubQuestions. The MVP implementation is HeuristicDecomposer; a
// future LLM-backed decomposer satisfies the same interface and slots
// into Planner without further changes.
type Decomposer interface {
	Decompose(query string) []SubQuestion
}

// Planner owns the decomposition phase of a research task. Today it
// holds only a Decomposer; future versions will carry source-type
// policies and retrieval budgets.
type Planner struct {
	Decomposer Decomposer
}

// NewPlanner returns a Planner wired with the default heuristic
// decomposer. Callers who need the LLM path construct a Planner
// directly with an LLM-backed Decomposer.
func NewPlanner() *Planner {
	return &Planner{Decomposer: HeuristicDecomposer{}}
}

// Plan turns a query into a slice of SubQuestions. The returned slice
// is never empty: when decomposition yields nothing, Plan falls back
// to a single SubQuestion equal to the input (so the caller always
// has something to search for).
func (p *Planner) Plan(query string) []SubQuestion {
	q := strings.TrimSpace(query)
	if q == "" {
		return []SubQuestion{{ID: "SQ-1", Text: query}}
	}
	d := p.Decomposer
	if d == nil {
		d = HeuristicDecomposer{}
	}
	out := d.Decompose(q)
	if len(out) == 0 {
		out = []SubQuestion{{ID: "SQ-1", Text: q}}
	}
	return out
}

// HeuristicDecomposer is the stdlib-only MVP. It recognises three
// shapes and otherwise returns the input verbatim:
//
//  1. "X vs Y" (or "X versus Y")    → one SubQuestion per operand
//  2. Comma-separated enumerations  → one SubQuestion per item
//  3. "A and B" conjunctions        → one SubQuestion per conjunct
//
// All detection is case-insensitive. The decomposer intentionally
// errs on the side of NOT splitting: a complex open-ended query
// returns a single SubQuestion rather than a chaotic decomposition.
type HeuristicDecomposer struct{}

// versusSplitter matches " vs ", " v. ", " versus " — surrounded by
// spaces so "versus" embedded in a longer word (unlikely but possible)
// does not trigger.
var versusSplitter = regexp.MustCompile(`(?i)\s+(?:vs\.?|versus)\s+`)

// andSplitter matches " and " as a conjunction. Bounded by spaces so
// it doesn't split words like "sand" or "bandwidth".
var andSplitter = regexp.MustCompile(`(?i)\s+and\s+`)

// Decompose implements the Decomposer interface.
func (HeuristicDecomposer) Decompose(query string) []SubQuestion {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}

	// 1. versus split
	if versusSplitter.MatchString(q) {
		parts := versusSplitter.Split(q, -1)
		out := make([]SubQuestion, 0, len(parts))
		for i, p := range parts {
			txt := strings.TrimSpace(p)
			if txt == "" {
				continue
			}
			out = append(out, SubQuestion{
				ID:    subQID(i + 1),
				Text:  "What is " + txt + "?",
				Hints: []string{txt},
			})
		}
		if len(out) >= 2 {
			return out
		}
	}

	// 2. comma-separated list (only when there are at least two
	// commas, to avoid splitting "Rome, Italy" into pieces)
	if strings.Count(q, ",") >= 2 {
		// Accept " and " or ", and " before the last item ("A, B, and C").
		trimmed := strings.TrimSuffix(strings.TrimSuffix(q, "."), "?")
		parts := splitCommaList(trimmed)
		if len(parts) >= 2 {
			out := make([]SubQuestion, 0, len(parts))
			for i, p := range parts {
				txt := strings.TrimSpace(p)
				if txt == "" {
					continue
				}
				out = append(out, SubQuestion{
					ID:    subQID(i + 1),
					Text:  "What is " + txt + "?",
					Hints: []string{txt},
				})
			}
			if len(out) >= 2 {
				return out
			}
		}
	}

	// 3. "A and B" conjunction — only when there's exactly ONE " and "
	// so we don't mis-split sentences like "A, B, and C".
	if andSplitter.MatchString(q) {
		parts := andSplitter.Split(q, -1)
		if len(parts) == 2 {
			out := make([]SubQuestion, 0, 2)
			for i, p := range parts {
				txt := strings.TrimSpace(p)
				if txt == "" {
					continue
				}
				out = append(out, SubQuestion{
					ID:    subQID(i + 1),
					Text:  "What is " + txt + "?",
					Hints: []string{txt},
				})
			}
			if len(out) == 2 {
				return out
			}
		}
	}

	// Fallback: single sub-question equal to the input.
	return []SubQuestion{{ID: "SQ-1", Text: q, Hints: []string{q}}}
}

// splitCommaList splits "A, B, and C" / "A, B, C" into ["A","B","C"].
// The final "and" before the last element is tolerated and stripped.
func splitCommaList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		// Strip leading "and " on the last element.
		if low := strings.ToLower(p); strings.HasPrefix(low, "and ") {
			p = strings.TrimSpace(p[4:])
		}
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// subQID returns a stable ID for the Nth sub-question ("SQ-1" for N=1).
func subQID(n int) string {
	return "SQ-" + itoa(n)
}

// itoa is the trivial int-to-string; we avoid strconv to keep the
// import list minimal and signal-clear in this MVP file. Small-int
// only; callers pass 1..20.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	s := string(buf[i:])
	if neg {
		s = "-" + s
	}
	return s
}
