package main

// ops_memory.go — UXMEM-core: read/write operator surface for the scoped
// memory bus (internal/memory/membus). This ships the first two verbs of
// spec-7 §D.7 — `list` and `add` — layered directly on membus.Bus so no
// new persistence code is introduced here.
//
//	r1 memory list [--db PATH] [--scope S] [--session SID]
//	                  [--limit N] [--json]
//	r1 memory add  [--db PATH] --scope S [--session SID]
//	                  [--task TID] [--step STEPID] [--key KEY] "content"
//
// Design:
//
//   - `memory` is a dispatcher: the sub-verb is os.Args[2] (or the
//     first positional arg passed to runMemoryCmd). Unknown / missing
//     sub-verbs print usage and exit 2, matching the ctl_cmd.go style.
//   - DB path: explicit --db wins, else <cwd>/.stoke/memory.db. We do
//     NOT reuse events.db — the memory bus lives in its own file per
//     spec 7 D.0 ("new SQLite DB under .stoke/memory.db"). For `list`
//     we tolerate a missing DB by printing "no memories" (the bus is
//     still opt-in), consistent with how `ops_tasks.go` treats an
//     empty DB. For `add` a missing DB is created via NewBus (the
//     migration is idempotent).
//   - Writer attribution: `add` tags the author as "operator" so the
//     ScopeAlways guard in membus.validateRemember does not fire
//     (operators are explicitly allowed to write ScopeAlways).
//
// Exit codes (both verbs):
//
//	0 — success (zero rows on `list` is still success)
//	1 — runtime failure (db open, query, write)
//	2 — usage error (bad flag, missing required arg, unknown sub-verb)

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/RelayOne/r1/internal/memory/membus"
)

// memoryUsage is emitted when runMemoryCmd receives an unknown / missing
// sub-verb. Keep it short — operators piping `r1 memory` into grep
// shouldn't have to wade through a man page.
const memoryUsage = `usage: r1 memory <verb> [flags]

verbs:
  list    list memories stored in the memory bus
  add     append a memory to the bus

See 'r1 memory <verb> -h' for per-verb flags.
`

// runMemoryCmd is the dispatcher entrypoint. It reads the sub-verb from
// args[0] and routes to the concrete runner. Returns an exit code so
// main.go composes it the same way as runEventsCmd / runTasksCmd.
func runMemoryCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, memoryUsage)
		return 2
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list":
		return runMemoryListCmd(rest, stdout, stderr)
	case "add":
		return runMemoryAddCmd(rest, stdout, stderr)
	case "-h", "--help", "help":
		_, _ = io.WriteString(stdout, memoryUsage)
		return 0
	default:
		fmt.Fprintf(stderr, "memory: unknown verb %q\n\n", verb)
		_, _ = io.WriteString(stderr, memoryUsage)
		return 2
	}
}

// resolveMemoryDB returns the absolute path to the memory bus SQLite
// database a verb invocation should target. Explicit --db wins;
// otherwise default to <cwd>/.stoke/memory.db. Mirrors resolveEventsDB
// in ops_events.go — identical heuristic, different filename.
func resolveMemoryDB(explicit string) string {
	if explicit != "" {
		return explicit
	}
	// LINT-ALLOW chdir-cli-entry: r1 ops memory subcommand; cwd is the memory.db discovery anchor when --explicit is unset.
	cwd, err := os.Getwd()
	if err != nil {
		// Surface the failure via the later Stat / Open; keep
		// the fallback behaviour predictable.
		cwd = "."
	}
	return filepath.Join(cwd, ".stoke", "memory.db")
}

// openMemoryBus opens (or creates when create is true) the SQLite
// database at path and wraps it in a membus.Bus. Returns the *sql.DB
// separately so the caller can close it — membus.Bus.Close() is a
// no-op by design (the handle is meant to be shared).
//
// When create is false and the file does not exist, the function
// returns (nil, nil, nil) so `list` can render "no memories" instead
// of an error (matches the empty-DB semantics of ops_tasks).
func openMemoryBus(path string, create bool) (*membus.Bus, *sql.DB, error) {
	if !create {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return nil, nil, nil
			}
			return nil, nil, fmt.Errorf("stat %s: %w", path, err)
		}
	} else {
		// Ensure the parent directory exists so NewBus doesn't
		// fail with ENOENT on first `add` against a fresh repo.
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
		}
	}
	// _txlock=immediate is required for the membus writer goroutine so
	// BEGIN starts with a RESERVED lock and doesn't race upgrades against
	// concurrent readers. See internal/memory/membus/bus.go flushBatch.
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate", path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	bus, err := membus.NewBus(db, membus.Options{})
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("membus.NewBus: %w", err)
	}
	return bus, db, nil
}

// ---------------------------------------------------------------------------
// `r1 memory list`
// ---------------------------------------------------------------------------

// runMemoryListCmd implements `r1 memory list`.
func runMemoryListCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "path to memory.db (default: <cwd>/.stoke/memory.db)")
	scope := fs.String("scope", "", "filter to this scope (session|session_step|worker|all_sessions|global|always)")
	session := fs.String("session", "", "filter to rows whose session_id matches")
	limit := fs.Int("limit", 50, "max rows to return (0 = 256 server default)")
	asJSON := fs.Bool("json", false, "emit one JSON object per memory (NDJSON) instead of a table")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *limit < 0 {
		fmt.Fprintln(stderr, "memory list: --limit must be >= 0")
		return 2
	}
	if *scope != "" && !membus.ValidScope(membus.Scope(*scope)) {
		fmt.Fprintf(stderr, "memory list: invalid --scope %q\n", *scope)
		return 2
	}

	resolved := resolveMemoryDB(*dbPath)
	bus, db, err := openMemoryBus(resolved, false)
	if err != nil {
		fmt.Fprintf(stderr, "memory list: %v\n", err)
		return 1
	}
	if bus == nil {
		// DB does not exist yet — no memories have been written. This
		// is success, not failure.
		if !*asJSON {
			fmt.Fprintln(stdout, "no memories")
		}
		return 0
	}
	defer db.Close()

	ctx := context.Background()
	rows, err := bus.Recall(ctx, membus.RecallRequest{
		Scope: membus.Scope(*scope),
		Limit: *limit,
	})
	if err != nil {
		fmt.Fprintf(stderr, "memory list: %v\n", err)
		return 1
	}

	// Apply the session filter client-side — membus.RecallRequest does
	// not expose session_id directly in the core slice. For a fresh
	// MVP verb this is fine; the full-stack implementation plumbs it
	// through.
	if *session != "" {
		filtered := rows[:0]
		for _, r := range rows {
			if r.SessionID == *session {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}

	if *asJSON {
		return emitMemoryListJSON(stdout, stderr, rows)
	}
	return renderMemoryListTable(stdout, rows)
}

func renderMemoryListTable(stdout io.Writer, rows []membus.Memory) int {
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "no memories")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSCOPE\tSESSION\tAUTHOR\tKEY\tCREATED\tCONTENT")
	for _, r := range rows {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID,
			string(r.Scope),
			orDash(r.SessionID),
			orDash(r.Author),
			orDash(r.Key),
			r.CreatedAt.UTC().Format(time.RFC3339),
			excerpt(r.Content, 60),
		)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(stdout, "memory list: tabwriter flush: %v\n", err)
		return 1
	}
	return 0
}

func emitMemoryListJSON(stdout, stderr io.Writer, rows []membus.Memory) int {
	enc := json.NewEncoder(stdout)
	for _, r := range rows {
		out := memoryJSONRow{
			ID:          r.ID,
			CreatedAt:   r.CreatedAt.UTC().Format(time.RFC3339Nano),
			Scope:       string(r.Scope),
			ScopeTarget: r.ScopeTarget,
			SessionID:   r.SessionID,
			StepID:      r.StepID,
			TaskID:      r.TaskID,
			Author:      r.Author,
			Key:         r.Key,
			Content:     r.Content,
			ContentHash: r.ContentHash,
			Tags:        r.Tags,
		}
		if r.ExpiresAt != nil {
			out.ExpiresAt = r.ExpiresAt.UTC().Format(time.RFC3339Nano)
		}
		if err := enc.Encode(out); err != nil {
			fmt.Fprintf(stderr, "memory list: encode: %v\n", err)
			return 1
		}
	}
	return 0
}

// memoryJSONRow is the wire shape of a `list --json` row. Keeps the
// field names snake_case and stable so downstream tooling can depend on
// it without importing internal packages.
type memoryJSONRow struct {
	ID          int64    `json:"id"`
	CreatedAt   string   `json:"created_at"`
	ExpiresAt   string   `json:"expires_at,omitempty"`
	Scope       string   `json:"scope"`
	ScopeTarget string   `json:"scope_target,omitempty"`
	SessionID   string   `json:"session_id,omitempty"`
	StepID      string   `json:"step_id,omitempty"`
	TaskID      string   `json:"task_id,omitempty"`
	Author      string   `json:"author,omitempty"`
	Key         string   `json:"key"`
	Content     string   `json:"content"`
	ContentHash string   `json:"content_hash"`
	Tags        []string `json:"tags,omitempty"`
}

// ---------------------------------------------------------------------------
// `r1 memory add`
// ---------------------------------------------------------------------------

// runMemoryAddCmd implements `r1 memory add`. Content is the first
// positional arg after flags; if absent, we read stdin so callers can
// pipe (e.g. `cat note.md | r1 memory add --scope session`).
func runMemoryAddCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "path to memory.db (default: <cwd>/.stoke/memory.db)")
	scope := fs.String("scope", "", "scope to write to (required; one of session|session_step|worker|all_sessions|global|always)")
	session := fs.String("session", "", "session_id attribution (optional)")
	step := fs.String("step", "", "step_id attribution (optional)")
	task := fs.String("task", "", "task_id attribution (optional)")
	scopeTarget := fs.String("scope-target", "", "scope_target (optional; defaults to empty, matches '' in Recall)")
	key := fs.String("key", "", "dedup key (optional; defaults to SHA256 prefix of content)")
	tagsCSV := fs.String("tags", "", "comma-separated tag list (optional)")
	asJSON := fs.Bool("json", false, "emit a JSON object with the written row's identity instead of a human line")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *scope == "" {
		fmt.Fprintln(stderr, "memory add: --scope is required")
		return 2
	}
	if !membus.ValidScope(membus.Scope(*scope)) {
		fmt.Fprintf(stderr, "memory add: invalid --scope %q\n", *scope)
		return 2
	}

	content, code := readAddContent(fs.Args(), os.Stdin, stderr)
	if code != 0 {
		return code
	}

	resolved := resolveMemoryDB(*dbPath)
	bus, db, err := openMemoryBus(resolved, true)
	if err != nil {
		fmt.Fprintf(stderr, "memory add: %v\n", err)
		return 1
	}
	defer db.Close()

	var tags []string
	if s := strings.TrimSpace(*tagsCSV); s != "" {
		for _, t := range strings.Split(s, ",") {
			if trimmed := strings.TrimSpace(t); trimmed != "" {
				tags = append(tags, trimmed)
			}
		}
	}

	ctx := context.Background()
	req := membus.RememberRequest{
		Scope:       membus.Scope(*scope),
		ScopeTarget: *scopeTarget,
		SessionID:   *session,
		StepID:      *step,
		TaskID:      *task,
		Author:      "operator",
		Key:         *key,
		Content:     content,
		Tags:        tags,
	}
	if err = bus.Remember(ctx, req); err != nil {
		fmt.Fprintf(stderr, "memory add: %v\n", err)
		return 1
	}

	// Recall the just-written row so we can echo its id + content_hash.
	// UPSERT semantics mean the key-matching row is the one we want.
	got, err := bus.Recall(ctx, membus.RecallRequest{
		Scope:       membus.Scope(*scope),
		ScopeTarget: *scopeTarget,
		Key:         req.Key, // empty → content-hash key already derived inside Remember; Recall falls through to scope match
	})
	if err != nil {
		// Write succeeded; read-back failure is surfaced but we still
		// return 0 so callers pipelining `add | next` don't see a
		// false negative.
		fmt.Fprintf(stderr, "memory add: warning: readback failed: %v\n", err)
	}

	var wrote membus.Memory
	if len(got) > 0 {
		// Prefer the newest row matching scope (highest id).
		wrote = got[len(got)-1]
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		out := memoryAddJSON{
			OK:          true,
			ID:          wrote.ID,
			Scope:       string(req.Scope),
			ScopeTarget: req.ScopeTarget,
			Key:         wrote.Key,
			ContentHash: wrote.ContentHash,
		}
		if err := enc.Encode(out); err != nil {
			fmt.Fprintf(stderr, "memory add: encode: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "wrote memory id=%d scope=%s key=%s\n",
		wrote.ID, string(req.Scope), orDash(wrote.Key))
	return 0
}

// memoryAddJSON is the `add --json` wire shape. Echoes the stored
// row's identity so callers can pipe the id into follow-on verbs.
type memoryAddJSON struct {
	OK          bool   `json:"ok"`
	ID          int64  `json:"id"`
	Scope       string `json:"scope"`
	ScopeTarget string `json:"scope_target,omitempty"`
	Key         string `json:"key"`
	ContentHash string `json:"content_hash"`
}

// readAddContent picks the content for a `memory add` invocation from
// either the first positional arg or stdin. Returns (content, 0) on
// success or ("", exitCode) on usage/IO failure.
func readAddContent(positional []string, stdin io.Reader, stderr io.Writer) (string, int) {
	if len(positional) > 0 {
		joined := strings.Join(positional, " ")
		if joined == "" {
			fmt.Fprintln(stderr, "memory add: empty content")
			return "", 2
		}
		return joined, 0
	}
	if stdin == nil {
		fmt.Fprintln(stderr, "memory add: no content (pass as arg or pipe via stdin)")
		return "", 2
	}
	buf, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "memory add: read stdin: %v\n", err)
		return "", 1
	}
	content := strings.TrimRight(string(buf), "\n")
	if content == "" {
		fmt.Fprintln(stderr, "memory add: no content (pass as arg or pipe via stdin)")
		return "", 2
	}
	return content, 0
}

// excerpt returns s truncated to n bytes with an ellipsis when it
// overflows. Newlines are replaced with spaces so the table row stays
// on one line. Kept inline here rather than in a shared util — the
// only other callsite uses tabwriter differently.
func excerpt(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
