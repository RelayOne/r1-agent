// Package memory — scope.go
//
// specs/memory-full-stack.md §5 primitive: the memory
// hierarchy (global / repo / task) with a deterministic
// task > repo > global specificity ordering. Retrieval,
// consolidation, and hygiene all need a single source of
// truth for which rows belong to which hierarchy bucket
// and which bucket wins on contradiction — this file is
// that source of truth.
//
// This is the *hierarchy* dimension of scope, deliberately
// typed separately from the bus/visibility scope defined
// in bus.go (which is session/worker/all_sessions/global/
// always/session_step). A memory row ends up with two
// orthogonal labels in the full stack:
//
//	HierScope  (this file)  — which project/task context
//	                          the fact is about
//	Scope      (bus.go)     — which worker(s) / session(s)
//	                          can see it at runtime
//
// Keeping the two type names distinct means callers never
// accidentally pass a bus scope where a hierarchy scope
// belongs and vice versa — the compiler catches it.
//
// Scope of this file (checklist items 14-17 of the spec):
//
//   - HierScope enum with the four canonical values
//     (Global / Repo / Task / Auto) plus parsing +
//     validation helpers that keep SQL predicates and CLI
//     flags consistent.
//   - RepoHash() — deterministic 16-char SHA256 prefix of
//     the toplevel repo path, with a documented fallback
//     to the process CWD when we're not inside a git work
//     tree. The hash is what backfills `scope_id` for
//     repo-scoped memories; CWD fallback is what keeps
//     air-gapped CI + dev-laptop use working.
//   - PredicateFor(scope, repoHash, taskID) returns a
//     parameterized SQL fragment + arg slice. Callers
//     append the fragment to their WHERE clause; args line
//     up 1:1 with the fragment's positional bind markers.
//     Auto resolves to a UNION over the three concrete
//     scopes so retrieval gets the task's rows AND the
//     repo's rows AND the global rows in one query.
//   - Specificity(item) returns 3/2/1 for task/repo/global
//     and 0 for items with no hierarchy bucket yet — used
//     as a tie-break in ORDER BY and in the contradiction
//     rule "more-specific scope wins".
//
// The primitive is storage-agnostic: nothing in this file
// opens a database or runs a query. The SQL fragment is a
// string + []any that Store.Query (a separate, not-yet-
// written file) will splice into its own SELECT. This
// keeps hierarchy logic trivially unit-testable without
// dragging in sqlite wiring.
package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// HierScope classifies a memory row's project/task reach.
//
// Values are the exact strings the spec's §3 schema uses
// in the `scope` TEXT column, so a HierScope round-trips
// straight to SQL without translation.
type HierScope string

const (
	// HierGlobal: applies across every repo + every task.
	// Lowest specificity (1). Used for user preferences,
	// agent-performance priors, and meta-rules the
	// operator wants everywhere.
	HierGlobal HierScope = "global"

	// HierRepo: scoped to one repo (keyed by RepoHash).
	// Specificity 2. Used for codebase facts, gotchas the
	// team hit in this project, false-positive
	// fingerprints for this repo's verifier.
	HierRepo HierScope = "repo"

	// HierTask: scoped to a single task / session.
	// Specificity 3 — the most specific bucket, wins all
	// contradiction tie-breaks. Used for per-session
	// episodic rows the consolidator has not yet rolled
	// up into the repo bucket.
	HierTask HierScope = "task"

	// HierAuto: caller delegation — "give me everything
	// relevant to this task, repo-scoped rows, and
	// globals". PredicateFor expands Auto into a UNION
	// over the three concrete buckets. Writes never use
	// Auto; storage rejects Auto on Put.
	HierAuto HierScope = "auto"
)

// Valid reports whether s is one of the four declared
// hierarchy buckets. Anything else (empty string, typo,
// an ad-hoc value from an old DB) returns false.
func (s HierScope) Valid() bool {
	switch s {
	case HierGlobal, HierRepo, HierTask, HierAuto:
		return true
	}
	return false
}

// ParseHierScope maps a string (CLI flag, DB column,
// config value) to a HierScope. Unknown values return the
// zero HierScope and an error so validation at the edge
// is explicit instead of silently falling back to global.
func ParseHierScope(s string) (HierScope, error) {
	sc := HierScope(strings.ToLower(strings.TrimSpace(s)))
	if !sc.Valid() {
		return HierScope(""), fmt.Errorf("memory: unknown hierarchy scope %q (want one of global, repo, task, auto)", s)
	}
	return sc, nil
}

// Specificity returns the ordering rank used as the tie-
// break in retrieval + the "more-specific wins" rule in
// contradiction resolution:
//
//	HierTask   → 3
//	HierRepo   → 2
//	HierGlobal → 1
//	everything else (unset, Auto, garbage) → 0
//
// Auto never reaches Specificity in practice — a row's
// persisted bucket is always one of the three concrete
// values — but returning 0 for Auto keeps callers that
// forget to resolve first from accidentally ranking Auto
// above Global.
func Specificity(s HierScope) int {
	switch s {
	case HierTask:
		return 3
	case HierRepo:
		return 2
	case HierGlobal:
		return 1
	case HierAuto:
		return 0
	}
	return 0
}

// SpecificityOf is a convenience that reads HierScope off
// an Item so ORDER BY comparators can say
// `SpecificityOf(a) > SpecificityOf(b)` without first
// pulling the field out by hand.
func SpecificityOf(it Item) int {
	return Specificity(it.HierScope)
}

// RepoHash returns the 16-character lowercase-hex SHA256
// prefix of the current repo's git toplevel path. That
// prefix is the canonical `scope_id` value for repo-
// scoped memories — stable across worktrees + checkouts
// of the same repo, unique across unrelated repos.
//
// Fallback ladder (documented, because this runs in CI
// sandboxes + dev laptops that may or may not be inside
// a git work tree):
//
//  1. `git rev-parse --show-toplevel` succeeds → hash its
//     output (trimmed of trailing newline). This is the
//     normal path.
//  2. git fails or is not installed → fall back to the
//     current working directory. This keeps every
//     air-gapped path (`go test` inside a sandbox that
//     redacts git, CI container without the git binary,
//     downloaded tarball of the source) producing a
//     stable, deterministic scope_id instead of a panic.
//  3. os.Getwd fails → last-resort fallback is the fixed
//     string "unknown"; we still emit a hash so the
//     caller gets a valid 16-char scope_id rather than "".
//
// The 16-char prefix length matches the spec's `scope_id
// TEXT` default width and the contentid/ package's
// prefix convention for content-addressed IDs elsewhere
// in stoke.
func RepoHash() string {
	return RepoHashAt(context.Background(), "")
}

// RepoHashAt is the ctx- + dir-aware form of RepoHash.
// Callers inside worktree code already have a context +
// a specific directory they want to resolve against;
// they should use this form. The zero-arg RepoHash()
// uses an empty dir (meaning "run git in the current
// process cwd") and a background context.
//
// Exposed separately so tests can drive the fallback
// ladder deterministically.
func RepoHashAt(ctx context.Context, dir string) string {
	if top, ok := gitToplevel(ctx, dir); ok {
		return hash16(top)
	}
	if cwd, err := os.Getwd(); err == nil {
		return hash16(cwd)
	}
	return hash16("unknown")
}

func gitToplevel(ctx context.Context, dir string) (string, bool) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	if dir != "" {
		cmd.Dir = dir
	}
	// Don't inherit stderr — a repo-less directory spews
	// "fatal: not a git repository" which would pollute
	// the parent process's logs. The exit code is all we
	// need.
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return "", false
	}
	return top, true
}

func hash16(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

// PredicateFor builds the `WHERE`-clause fragment + arg
// slice for a hierarchy-filtered query. The fragment is
// a single parenthesized expression the caller AND-joins
// into its own WHERE; args line up with the positional
// bind markers left-to-right.
//
// Expansion rules (per spec §5):
//
//	HierGlobal: one equality predicate, single bind.
//	HierRepo:   scope + scope_id equality, two binds.
//	HierTask:   scope + scope_id equality, two binds.
//	HierAuto:   task > repo > global UNION. The fragment
//	            matches any row whose (scope, scope_id)
//	            tuple fits any of the three concrete
//	            buckets. Missing-piece handling: if
//	            taskID is "", the task branch is omitted
//	            (so repo-plus-global is still a valid
//	            Auto expansion during planning, before a
//	            task ID is assigned). If repoHash is "",
//	            the repo branch is omitted (so Auto from
//	            a non-git dir still returns global rows).
//
// An unknown scope returns an error so callers don't
// silently build an empty WHERE clause and pull back the
// whole table.
//
// Args are emitted as []any (instead of []string)
// because that's the shape database/sql expects
// downstream; no runtime cost versus []string.
func PredicateFor(scope HierScope, repoHash, taskID string) (string, []any, error) {
	switch scope {
	case HierGlobal:
		return "(scope = ?)", []any{string(HierGlobal)}, nil
	case HierRepo:
		if repoHash == "" {
			return "", nil, fmt.Errorf("memory: HierRepo predicate requires non-empty repoHash")
		}
		return "(scope = ? AND scope_id = ?)", []any{string(HierRepo), repoHash}, nil
	case HierTask:
		if taskID == "" {
			return "", nil, fmt.Errorf("memory: HierTask predicate requires non-empty taskID")
		}
		return "(scope = ? AND scope_id = ?)", []any{string(HierTask), taskID}, nil
	case HierAuto:
		// UNION: global is unconditional; repo + task
		// branches only appear when their ID is present.
		// Build the fragment piecewise so the emitted SQL
		// stays tight (no always-true tautologies).
		parts := []string{"scope = 'global'"}
		var args []any
		if repoHash != "" {
			parts = append(parts, "(scope = 'repo' AND scope_id = ?)")
			args = append(args, repoHash)
		}
		if taskID != "" {
			parts = append(parts, "(scope = 'task' AND scope_id = ?)")
			args = append(args, taskID)
		}
		return "(" + strings.Join(parts, " OR ") + ")", args, nil
	default:
		return "", nil, fmt.Errorf("memory: PredicateFor: unknown scope %q", scope)
	}
}

// ResolveConflict picks the more-specific of two items
// when they've been flagged as contradicting each other
// (per spec §5: "task beats repo beats global"). Returns
// the winner + a boolean reporting whether the tie was
// broken on specificity alone — false means both items
// are at the same hierarchy bucket and the caller needs
// a different tie-break (confidence, recency, …).
//
// This is the runtime half of the specificity rule — the
// SQL half lives in PredicateFor + Specificity. Keeping
// both halves in this file means the spec's
// task > repo > global promise has exactly one source of
// truth.
func ResolveConflict(a, b Item) (winner Item, brokenBySpecificity bool) {
	sa := SpecificityOf(a)
	sb := SpecificityOf(b)
	switch {
	case sa > sb:
		return a, true
	case sb > sa:
		return b, true
	}
	// Same bucket — caller must break the tie.
	return a, false
}

// SortBySpecificity stable-sorts items high-to-low on
// hierarchy specificity. Ties preserve input order so
// the caller's prior ORDER BY (importance, recency, …)
// still dominates within a bucket.
//
// Returned slice is a new slice; the input is not
// mutated. This keeps callers that pass a query result
// straight through from observing surprise reordering
// on the original slice.
func SortBySpecificity(items []Item) []Item {
	out := make([]Item, len(items))
	copy(out, items)
	sort.SliceStable(out, func(i, j int) bool {
		return SpecificityOf(out[i]) > SpecificityOf(out[j])
	})
	return out
}
