// Package plan — session_dag.go
//
// Builds the inter-session dependency graph that the parallel session
// runner consumes. The SOW schema already models Session.Inputs and
// Session.Outputs — strings that name artifacts produced or consumed
// by a session. BuildSessionDAG turns those into concrete sessionID →
// sessionID edges so the runner can launch independent sessions
// concurrently.
//
// Three layers of edge inference, in descending confidence order:
//
//  1. Explicit I/O edges. Session B.Inputs references an artifact that
//     exists in session A.Outputs → A → B. Highest confidence; this is
//     what the planner wrote down.
//
//  2. File-scope overlap. When B's tasks declare files that overlap A's
//     task file list, B probably depends on A. We only infer this edge
//     when neither session has declared explicit I/O — once a planner
//     has emitted I/O, we trust it to be authoritative.
//
//  3. Declaration-order fallback. When a session has no Inputs and no
//     file-scope overlap with any prior session, it is still given an
//     implicit dependency on the prior session. This is the safe
//     default for SOWs whose planner didn't populate I/O: behavior
//     matches the legacy sequential runner exactly.
//
// The resulting DAG is acyclic by construction — edges only ever point
// from earlier-declared sessions to later-declared ones. A cycle would
// require a session to depend on one declared after it, which the
// inference rules never produce.
package plan

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SessionDAG is the adjacency representation consumed by RunParallel.
// Deps[sessionID] is the set of sessionIDs that must complete before
// sessionID is eligible to run. Missing from the map == no deps (a
// root session that can launch immediately).
type SessionDAG struct {
	Deps    map[string][]string
	Reasons map[string]map[string]string // Deps[to][from] → why this edge exists
}

// Blockers returns the unresolved deps of sessionID given the set of
// completed session IDs. When the return is empty the session is ready.
func (g *SessionDAG) Blockers(sessionID string, completed map[string]bool) []string {
	if g == nil {
		return nil
	}
	var out []string
	for _, d := range g.Deps[sessionID] {
		if !completed[d] {
			out = append(out, d)
		}
	}
	return out
}

// BuildSessionDAG constructs the DAG from a SOW. DAG construction is
// deterministic — given the same SOW it always produces the same
// adjacency + reasons, which makes debugging reproducible.
func BuildSessionDAG(sow *SOW) *SessionDAG {
	g := &SessionDAG{
		Deps:    map[string][]string{},
		Reasons: map[string]map[string]string{},
	}
	if sow == nil || len(sow.Sessions) == 0 {
		return g
	}

	// Index: artifact name → session that produces it.
	producers := map[string]string{}
	for _, s := range sow.Sessions {
		for _, out := range s.Outputs {
			out = strings.TrimSpace(out)
			if out == "" {
				continue
			}
			// When two sessions claim to produce the same artifact,
			// the FIRST one wins. Subsequent claims are ignored so a
			// buggy planner duplicating Outputs doesn't create cycles.
			if _, seen := producers[out]; !seen {
				producers[out] = s.ID
			}
		}
	}

	// Index: session index by ID, used to enforce "earlier declared"
	// ordering for file-scope-inferred edges.
	order := map[string]int{}
	for i, s := range sow.Sessions {
		order[s.ID] = i
	}

	// Index: normalized declared file paths per session.
	fileScope := map[string]map[string]bool{}
	for _, s := range sow.Sessions {
		set := map[string]bool{}
		for _, t := range s.Tasks {
			for _, f := range t.Files {
				set[normalizePath(f)] = true
			}
		}
		fileScope[s.ID] = set
	}

	addEdge := func(from, to, reason string) {
		if from == to {
			return
		}
		// Skip duplicate edges — Deps is a set semantically.
		for _, existing := range g.Deps[to] {
			if existing == from {
				// Keep the first reason; later inference layers are
				// lower confidence and shouldn't overwrite explicit
				// I/O reasons.
				return
			}
		}
		g.Deps[to] = append(g.Deps[to], from)
		if g.Reasons[to] == nil {
			g.Reasons[to] = map[string]string{}
		}
		g.Reasons[to][from] = reason
	}

	for i, s := range sow.Sessions {
		// Layer 1: explicit I/O edges.
		hasExplicitIO := len(s.Inputs) > 0
		for _, in := range s.Inputs {
			in = strings.TrimSpace(in)
			if in == "" {
				continue
			}
			if prodID, ok := producers[in]; ok {
				if order[prodID] < order[s.ID] {
					addEdge(prodID, s.ID, "Inputs["+in+"] → Outputs of "+prodID)
				}
			}
		}

		// Layer 2: file-scope overlap. Only inferred when this session
		// did not declare explicit Inputs. Overlap with an earlier
		// session's declared file set → implicit dep.
		if !hasExplicitIO {
			mine := fileScope[s.ID]
			if len(mine) > 0 {
				for j := 0; j < i; j++ {
					other := sow.Sessions[j]
					otherScope := fileScope[other.ID]
					if overlaps(mine, otherScope) {
						addEdge(other.ID, s.ID, "file-scope overlap with "+other.ID)
					}
				}
			}
		}

		// Layer 3: declaration-order fallback. If this session has no
		// deps yet from layers 1 or 2, serialize it behind the
		// immediately prior session. This preserves exact
		// legacy-sequential behavior when a SOW has no I/O + no
		// file-scope hints.
		if i > 0 && len(g.Deps[s.ID]) == 0 {
			prev := sow.Sessions[i-1].ID
			addEdge(prev, s.ID, "declaration-order fallback (no Inputs, no file-scope overlap)")
		}
	}

	return g
}

// RootSessions returns the set of session IDs with zero dependencies.
// These are the initial work items for the parallel runner.
func (g *SessionDAG) RootSessions(sow *SOW) []string {
	var roots []string
	for _, s := range sow.Sessions {
		if len(g.Deps[s.ID]) == 0 {
			roots = append(roots, s.ID)
		}
	}
	return roots
}

// Summary renders a one-line-per-session report of the computed DAG.
// Printed at session-scheduler startup so the operator can eyeball
// the inferred dependencies before the run.
func (g *SessionDAG) Summary(sow *SOW) string {
	var b strings.Builder
	b.WriteString("Session DAG:\n")
	for _, s := range sow.Sessions {
		deps := g.Deps[s.ID]
		if len(deps) == 0 {
			fmt.Fprintf(&b, "  %s: (root)\n", s.ID)
			continue
		}
		reasons := g.Reasons[s.ID]
		var parts []string
		for _, d := range deps {
			parts = append(parts, fmt.Sprintf("%s [%s]", d, reasons[d]))
		}
		fmt.Fprintf(&b, "  %s ← %s\n", s.ID, strings.Join(parts, ", "))
	}
	return b.String()
}

func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	p = filepath.Clean(p)
	return p
}

func overlaps(a, b map[string]bool) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	// Iterate the smaller set for cheaper checks.
	small, large := a, b
	if len(b) < len(a) {
		small, large = b, a
	}
	for k := range small {
		if large[k] {
			return true
		}
		// Prefix overlap: "apps/web/" in small and "apps/web/app/page.tsx"
		// in large, or vice versa. Tasks often declare directory-level
		// scope for one session and file-level for another; the prefix
		// check catches the common case.
		if strings.HasSuffix(k, "/") {
			for lk := range large {
				if strings.HasPrefix(lk, k) {
					return true
				}
			}
		}
	}
	return false
}
