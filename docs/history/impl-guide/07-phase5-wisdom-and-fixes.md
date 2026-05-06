# 07 — Phase 5: Wisdom Migration, Package Audit, Cleanup

This is the final phase. It tackles three things:

1. **Wisdom store migration to SQLite** — currently in-memory only, with a 500-character cap on prompts. Cross-session persistence is required.
2. **Package audit** — produce `PACKAGE-AUDIT.md` tagging all 103 packages CORE/HELPFUL/DEPRECATED so Eric can decide what to delete in a future cleanup.
3. **Prompt cache alignment audit** — verify every API call from Stoke is structured for cache hits.

## Step 1: Wisdom SQLite migration

### Current state

`internal/wisdom/store.go` has an in-memory store. Find it:

```bash
ls internal/wisdom/
cat internal/wisdom/store.go | head -50
```

There should be a `Store` interface with methods like `Add`, `Get`, `Search`, `List`, and an in-memory implementation. The new SQLite implementation must satisfy the same interface.

### Schema

**File:** `internal/wisdom/sqlite_schema.go`

```go
package wisdom

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS wisdom_learnings (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id         TEXT,
    mission_id      TEXT,
    category        TEXT NOT NULL,
    description     TEXT NOT NULL,
    file_path       TEXT,
    failure_pattern TEXT,
    skill_match     TEXT,
    use_count       INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_wisdom_category ON wisdom_learnings(category);
CREATE INDEX IF NOT EXISTS idx_wisdom_failure_pattern ON wisdom_learnings(failure_pattern);
CREATE INDEX IF NOT EXISTS idx_wisdom_skill_match ON wisdom_learnings(skill_match);
CREATE INDEX IF NOT EXISTS idx_wisdom_task ON wisdom_learnings(task_id);
CREATE INDEX IF NOT EXISTS idx_wisdom_mission ON wisdom_learnings(mission_id);

CREATE VIRTUAL TABLE IF NOT EXISTS wisdom_fts USING fts5(
    description,
    content='wisdom_learnings',
    content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS wisdom_ai AFTER INSERT ON wisdom_learnings BEGIN
    INSERT INTO wisdom_fts(rowid, description) VALUES (new.id, new.description);
END;

CREATE TRIGGER IF NOT EXISTS wisdom_ad AFTER DELETE ON wisdom_learnings BEGIN
    DELETE FROM wisdom_fts WHERE rowid = old.id;
END;

CREATE TRIGGER IF NOT EXISTS wisdom_au AFTER UPDATE ON wisdom_learnings BEGIN
    UPDATE wisdom_fts SET description = new.description WHERE rowid = old.id;
END;
`
```

### SQLite store implementation

**File:** `internal/wisdom/sqlite.go`

```go
package wisdom

import (
    "database/sql"
    "fmt"
    "sync"
    "time"

    _ "modernc.org/sqlite"
)

// SQLiteStore is the persistent wisdom store backed by SQLite + FTS5.
type SQLiteStore struct {
    db *sql.DB
    mu sync.Mutex
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
    db, err := sql.Open("sqlite", path)
    if err != nil {
        return nil, err
    }
    if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
        return nil, err
    }
    if _, err := db.Exec(sqliteSchema); err != nil {
        return nil, err
    }
    return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Close() error {
    return s.db.Close()
}

// Add inserts a new learning. Idempotent on (failure_pattern, file_path) tuple
// — if a duplicate exists, increments use_count instead.
func (s *SQLiteStore) Add(l Learning) error {
    s.mu.Lock()
    defer s.mu.Unlock()

    now := time.Now().Format(time.RFC3339Nano)

    // Idempotency check
    if l.FailurePattern != "" {
        var existingID int64
        err := s.db.QueryRow(`
            SELECT id FROM wisdom_learnings
            WHERE failure_pattern = ? AND COALESCE(file_path, '') = COALESCE(?, '')
            LIMIT 1
        `, l.FailurePattern, l.FilePath).Scan(&existingID)
        if err == nil {
            _, err := s.db.Exec(`
                UPDATE wisdom_learnings
                SET use_count = use_count + 1, updated_at = ?
                WHERE id = ?
            `, now, existingID)
            return err
        }
    }

    _, err := s.db.Exec(`
        INSERT INTO wisdom_learnings
        (task_id, mission_id, category, description, file_path, failure_pattern, skill_match, use_count, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `, l.TaskID, l.MissionID, l.Category, l.Description, l.FilePath, l.FailurePattern, l.SkillMatch, 0, now, now)
    return err
}

// Search returns learnings matching the query via FTS5.
func (s *SQLiteStore) Search(query string, limit int) ([]Learning, error) {
    if limit == 0 {
        limit = 20
    }
    rows, err := s.db.Query(`
        SELECT l.id, l.task_id, l.mission_id, l.category, l.description, l.file_path,
               l.failure_pattern, l.skill_match, l.use_count, l.created_at, l.updated_at
        FROM wisdom_learnings l
        JOIN wisdom_fts f ON f.rowid = l.id
        WHERE wisdom_fts MATCH ?
        ORDER BY l.use_count DESC, rank
        LIMIT ?
    `, query, limit)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    return scanLearnings(rows)
}

// ListByCategory returns learnings of a given category, most recently used first.
func (s *SQLiteStore) ListByCategory(category string, limit int) ([]Learning, error) {
    if limit == 0 {
        limit = 100
    }
    rows, err := s.db.Query(`
        SELECT id, task_id, mission_id, category, description, file_path,
               failure_pattern, skill_match, use_count, created_at, updated_at
        FROM wisdom_learnings
        WHERE category = ?
        ORDER BY use_count DESC, updated_at DESC
        LIMIT ?
    `, category, limit)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    return scanLearnings(rows)
}

func scanLearnings(rows *sql.Rows) ([]Learning, error) {
    var out []Learning
    for rows.Next() {
        var l Learning
        var taskID, missionID, filePath, failurePattern, skillMatch sql.NullString
        if err := rows.Scan(&l.ID, &taskID, &missionID, &l.Category, &l.Description,
            &filePath, &failurePattern, &skillMatch, &l.UseCount, &l.CreatedAt, &l.UpdatedAt); err != nil {
            return nil, err
        }
        l.TaskID = taskID.String
        l.MissionID = missionID.String
        l.FilePath = filePath.String
        l.FailurePattern = failurePattern.String
        l.SkillMatch = skillMatch.String
        out = append(out, l)
    }
    return out, rows.Err()
}

// Learning is the public type. Update the existing wisdom.Learning struct to
// match this shape if it doesn't already.
type Learning struct {
    ID             int64
    TaskID         string
    MissionID      string
    Category       string  // gotcha|decision|pattern|failure
    Description    string
    FilePath       string
    FailurePattern string
    SkillMatch     string
    UseCount       int
    CreatedAt      string
    UpdatedAt      string
}
```

### Wire it in

In `internal/app/app.go` where wisdom is initialized:

```go
// Replace the in-memory store init with SQLite
wisdomPath := filepath.Join(stokeDir, "wisdom.db")
wisdomStore, err := wisdom.NewSQLiteStore(wisdomPath)
if err != nil {
    log.Printf("[wisdom] sqlite init failed: %v (falling back to in-memory)", err)
    wisdomStore = wisdom.NewMemoryStore()
}
cfg.Wisdom = wisdomStore
```

The existing in-memory implementation should be **kept** as `NewMemoryStore` and used as a fallback when SQLite init fails. Both must implement the same `Store` interface.

### Removing the 500-character cap

Find the cap:

```bash
grep -rn "500" internal/wisdom/
```

Remove or raise the cap. Wisdom entries should hold full descriptions; the FTS5 index handles search quality regardless of length.

---

## Step 2: Package audit

### Goal

Create `PACKAGE-AUDIT.md` at the repo root with a row for every package in `internal/`. Each package gets one of three tags:

- **CORE** — currently providing essential functionality the orchestrator depends on
- **HELPFUL** — provides value but could potentially be simplified or absorbed into another package
- **DEPRECATED** — appears to be unused, dead code, or made obsolete by other packages

### Procedure

```bash
# 1. List all packages
find internal -type d -mindepth 1 -maxdepth 2 | sort > /tmp/packages.txt

# 2. For each package, count its callers from outside the package
for pkg in $(cat /tmp/packages.txt); do
    short=$(echo $pkg | sed 's|internal/||')
    callers=$(grep -rn "ericmacdougall/stoke/$pkg" internal/ cmd/ 2>/dev/null | grep -v "^$pkg" | grep -v "_test.go" | wc -l)
    files=$(find $pkg -name "*.go" -not -name "*_test.go" | wc -l)
    lines=$(find $pkg -name "*.go" -not -name "*_test.go" -exec cat {} \; | wc -l)
    echo "$short|$callers|$files|$lines"
done
```

For packages with zero or near-zero outside callers, dig deeper: read the package doc comment, look at what its types do, check git log for recent activity. A package with zero callers is a strong DEPRECATED candidate but might be a public API surface that's externally referenced — verify before tagging.

### `PACKAGE-AUDIT.md` template

```markdown
# Stoke Package Audit

Generated: <date>
Total packages: <count>

## Methodology

Each package was assessed against three criteria:
1. **External callers** — how many places outside the package import it
2. **Functional uniqueness** — does any other package provide similar functionality
3. **Strategic alignment** — does it support Stoke's positioning as the anti-deception harness

Packages tagged **CORE** are essential to current operation. **HELPFUL** packages
provide value but could potentially be merged or simplified. **DEPRECATED**
packages appear unused or made obsolete by newer packages.

**Note:** No packages were deleted as part of this audit. Eric will make deletion
calls based on actual benchmark results.

## Summary

- CORE: __ packages
- HELPFUL: __ packages
- DEPRECATED: __ packages

## Detailed listing

| Package | Tag | Callers | LOC | Notes |
|---|---|---|---|---|
| internal/agent | CORE | 12 | 1234 | Orchestrator agent loop, central. |
| internal/agentloop | CORE | 3 | 800 | New native harness loop (Phase 4). |
| internal/app | CORE | 8 | 2100 | Application bootstrap, config, lifecycle. |
| ... | ... | ... | ... | ... |
| internal/lifecycle | DEPRECATED | 0 | 350 | Hook system replaced by internal/hub in Phase 3. Adapter exists for backward compat. Safe to remove after one release cycle. |
| ... | ... | ... | ... | ... |

## Deprecated package notes

For each DEPRECATED package, document:
- What it was for
- What replaces it
- Any external references to consider
- Suggested removal timeline

### internal/lifecycle

**Replaced by:** internal/hub (Phase 3)
**Was for:** Lifecycle hook registration (5-tier)
**External references:** None found
**Suggested removal:** After hub has been in production for 30 days without regression
```

### Building the audit

Use this prompt for the audit session (if a human or another AI agent is helping):

```
For each package in internal/, determine:
1. The number of distinct external callers (places outside the package that import it)
2. What unique functionality it provides
3. Whether any newer package supersedes it
4. Whether removing it would break the build or any tests

Tag each package CORE / HELPFUL / DEPRECATED based on the criteria above. Write
the result to PACKAGE-AUDIT.md following the template format.
```

### CLI command

Add `stoke audit` to `cmd/r1/main.go`:

```go
case "audit":
    return runAuditCmd()
```

```go
func runAuditCmd() error {
    // Walk internal/, count callers, output PACKAGE-AUDIT.md
    // (concrete implementation similar to the bash script above, in Go)
    // Output format matches the template above.
    return nil
}
```

---

## Step 3: Prompt cache alignment audit

### Why this matters

Research [P69]: prompt caching reduces input token cost by 70–82% in 20-turn sessions when correctly aligned. Misalignment is silent — cache writes happen but cache reads never hit, so you pay 25% extra for the writes and never get the discount. The audit ensures every API call from Stoke is structured correctly.

### Audit checklist

For every place in the codebase that constructs an Anthropic API request (search for `messages.Create`, `client.Messages`, `provider.Send`), verify:

1. **Tools are sorted alphabetically** before being added to the request
2. **System prompt has cache_control** on the static portion (the role definition + safety policy + skill block)
3. **Dynamic system content** (working dir, timestamp, env metadata) sits AFTER the cache_control breakpoint, not before
4. **Conversation history** uses the incremental cache breakpoint pattern: mark the second-to-last user message's first content block with cache_control
5. **Maximum 4 cache breakpoints** per request — count them
6. **Tool definitions include all fields verbatim** (no per-call mutation that would bust cache)

### Audit script

```bash
# Find all places that build messages requests
grep -rn "messages.Create\|MessagesRequest\|provider.Send\|provider.Chat" internal/ cmd/ | grep -v "_test.go" > /tmp/api_call_sites.txt

# For each site, manually inspect using:
# 1. Is the request constructed in a function that's called many times?
# 2. Does the request include tool definitions sorted alphabetically?
# 3. Does the system prompt have cache_control?
# 4. Are messages reused from a stable state, or rebuilt every call?
```

For every site that fails the checklist, fix it. Document the fix in `STOKE-IMPL-NOTES.md`.

### Verify with Anthropic API

After fixes, run a test mission that makes 5+ API calls in sequence. In the response, look at `usage.cache_read_input_tokens`. By the third call, this should be > 80% of `usage.input_tokens`. If it's 0 or low, the cache is busted.

---

## Step 4: Final cleanup and documentation

### Architecture docs

Create these files at the repo root:

1. **`docs/architecture/skill-pipeline.md`** — explain how skills are loaded, selected, injected. Reference `internal/skill/` and `internal/skillselect/`.
2. **`docs/architecture/hub.md`** — explain the event bus, three modes, six transports, audit log. Reference `internal/hub/`.
3. **`docs/architecture/agentloop.md`** — explain the native harness, tool execution, prompt caching. Reference `internal/tools/` and `internal/agentloop/`.
4. **`docs/architecture/wizard.md`** — explain the wizard, detect-then-confirm pattern, maturity scoring. Reference `internal/wizard/`.

Each doc should be 100–300 lines. Include a Mermaid diagram if helpful.

### CLAUDE.md update

Stoke's own `CLAUDE.md` (or `AGENTS.md`) should be updated to reflect the new packages and commands. Keep it under 200 lines following the patterns in research [CMD]:
- Commands block first (build, test, lint commands)
- Architecture map (which package does what)
- Gotchas (the things-that-will-bite-you list)
- Workflow rules (commit format, branch naming)

### STOKE-IMPL-NOTES.md final entry

Append a final summary:

```markdown
## Phase 5 complete — <date>

- Wisdom store migrated to SQLite at .stoke/wisdom.db
- 500-char cap removed
- Package audit complete: PACKAGE-AUDIT.md generated
  - CORE: __ packages
  - HELPFUL: __ packages
  - DEPRECATED: __ packages
- Prompt cache alignment audit:
  - __ call sites reviewed
  - __ fixes applied
  - Cache hit rate verified at __% on test mission
- Architecture docs added in docs/architecture/
- CLAUDE.md updated

Open questions for Eric:
- ...

Recommended follow-ups:
- Run benchmarks (Phase 9) once the missing research prompts return
- Consider deleting DEPRECATED packages after a 30-day soak
- Run `stoke wizard --research` against a few existing projects to validate
  the AI-powered convergence path
```

---

## Validation gate for Phase 5

1. `go vet ./...` clean, full test suite passes
2. `go build ./cmd/r1` succeeds
3. `stoke audit` produces `PACKAGE-AUDIT.md` with all packages tagged
4. Wisdom learnings persist across `stoke` invocations: add a learning, exit, restart, query, learning is still there
5. Cache hit rate audit passes (cache_read_input_tokens > 80% by turn 3 in a multi-turn session)
6. All architecture docs exist in `docs/architecture/`
7. Final entry in `STOKE-IMPL-NOTES.md`

## You are done. Stoke now has:

- Wired skill registry (Phase 1)
- Auto-detecting wizard (Phase 2)
- Unified event bus (Phase 3)
- Native harness independence (Phase 4)
- Persistent wisdom + package audit + cache alignment (Phase 5)

## If you have time, also see:

- `08-skill-library-extraction.md` — how to convert the 61 engineering research files into `.stoke/skills/` content
- `09-validation-gates.md` — full validation gate reference and benchmark framework outline
