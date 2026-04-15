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

	// Index: artifact name → session that produces it. Keyed by the
	// NORMALIZED form (lowercase, whitespace collapsed) so the
	// resolver can fuzzy-match prose-y planner emissions like "Auth
	// foundation from S2" against producer outputs like "auth
	// foundation".
	producers := map[string]string{}
	producerKeys := []string{} // ordered for deterministic substring fallback
	for _, s := range sow.Sessions {
		for _, out := range s.Outputs {
			key := normalizeArtifact(out)
			if key == "" {
				continue
			}
			if _, seen := producers[key]; !seen {
				producers[key] = s.ID
				producerKeys = append(producerKeys, key)
			}
		}
	}

	// resolveProducer returns the session that produces the named
	// artifact. Tries (1) exact normalized match, (2) input fully
	// CONTAINS some producer key (planner wrote "X from S2" where the
	// producer emitted "X"), (3) producer key fully CONTAINS the input
	// (planner wrote "auth" where producer emitted "auth foundation").
	// Returns "" when no producer matches.
	resolveProducer := func(in string) (string, string) {
		key := normalizeArtifact(in)
		if key == "" {
			return "", ""
		}
		if id, ok := producers[key]; ok {
			return id, key
		}
		for _, pk := range producerKeys {
			if strings.Contains(key, pk) {
				return producers[pk], pk
			}
		}
		for _, pk := range producerKeys {
			if strings.Contains(pk, key) {
				return producers[pk], pk
			}
		}
		return "", ""
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
		// Preempt sessions skip all INFERRED edges. Explicit
		// Inputs/Outputs still apply — a preempt session that really
		// does depend on a specific artifact can say so — but file-
		// scope overlap and declaration-order fallback do not block
		// it. This is how fix sessions promoted mid-run get to race
		// their parents instead of waiting behind them.
		if s.Preempt {
			for _, in := range s.Inputs {
				prodID, matched := resolveProducer(in)
				if prodID == "" {
					continue
				}
				if order[prodID] < order[s.ID] {
					addEdge(prodID, s.ID, "Inputs["+strings.TrimSpace(in)+"] → "+matched+" of "+prodID+" (preempt)")
				}
			}
			continue
		}
		// Layer 1: explicit I/O edges.
		hasExplicitIO := len(s.Inputs) > 0
		for _, in := range s.Inputs {
			prodID, matched := resolveProducer(in)
			if prodID == "" {
				continue
			}
			if order[prodID] < order[s.ID] {
				addEdge(prodID, s.ID, "Inputs["+strings.TrimSpace(in)+"] → "+matched+" of "+prodID)
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

// normalizeArtifact lowercases + collapses internal whitespace so the
// DAG resolver can treat "Auth foundation" and "auth   foundation\n"
// as the same artifact name. Used for matching session Inputs against
// session Outputs.
func normalizeArtifact(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
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
		// Prefix overlap, in BOTH directions. normalizePath has
		// already stripped any trailing slash via filepath.Clean,
		// so we can't gate on HasSuffix("/") — that was the
		// codex-review P1 here. Instead, treat any path as a
		// candidate prefix and require a directory-boundary match
		// (full prefix + the next character is `/`) so partial
		// component matches like "app" vs "apps" don't false-fire.
		for lk := range large {
			if hasDirPrefix(lk, k) || hasDirPrefix(k, lk) {
				return true
			}
		}
	}
	return false
}

// hasDirPrefix returns true when prefix is a directory-bounded prefix
// of path. Both inputs are normalized (no trailing slash). A prefix
// matches when path == prefix exactly (already handled by the equality
// check at the call site) or when path starts with prefix + "/".
func hasDirPrefix(path, prefix string) bool {
	if path == prefix || prefix == "" {
		return false
	}
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	return path[len(prefix)] == '/'
}
