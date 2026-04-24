package eventlog

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/oklog/ulid/v2"

	"github.com/RelayOne/r1/internal/bus"
)

//go:embed schema.sql
var schemaSQL string

// Log is the durable, hash-chained event store. Callers construct a Log via
// Open and must call Close when finished.
type Log struct {
	db *sql.DB

	// mu serializes Append so that the head-row read + INSERT + head-row
	// UPDATE form a single logical sequence even under concurrent writers
	// in the same process. BEGIN IMMEDIATE guards against other DB
	// handles; mu avoids SQLITE_BUSY churn from in-process concurrency.
	mu sync.Mutex

	// ulidMu protects ulidEntropy, which is reused across Append calls.
	ulidMu      sync.Mutex
	ulidEntropy *ulid.MonotonicEntropy
}

// ChainBrokenError is returned by Verify when the stored parent_hash does not
// match the hash recomputed from the previous row's (id, payload,
// parent_hash) tuple.
type ChainBrokenError struct {
	Sequence uint64
	Expected string
	Got      string
}

// Error implements error.
func (e *ChainBrokenError) Error() string {
	return fmt.Sprintf("eventlog: chain broken at sequence %d: expected parent_hash=%s, got %s",
		e.Sequence, e.Expected, e.Got)
}

// Open opens (or creates) the SQLite database at dbPath, runs the embedded
// DDL, and applies the WAL/synchronous/busy_timeout pragmas.
func Open(dbPath string) (*Log, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("eventlog: dbPath must not be empty")
	}
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("eventlog: mkdir %q: %w", dir, err)
		}
	}
	// The query-string pragmas ensure each new connection in the pool gets
	// the right settings (sqlite pragmas are per-connection). We also
	// execute the schema (which contains PRAGMA statements) on the first
	// connection below.
	dsn := dbPath + "?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_foreign_keys=ON"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open %q: %w", dbPath, err)
	}
	// SQLite with WAL handles concurrent readers well, but serial writers
	// only. Leave the default pool behaviour.
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("eventlog: ping %q: %w", dbPath, err)
	}
	// Run the embedded DDL (CREATE TABLE IF NOT EXISTS etc.).
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("eventlog: apply schema: %w", err)
	}

	l := &Log{db: db}
	l.ulidEntropy = ulid.Monotonic(newEntropySource(), 0)
	return l, nil
}

// Close closes the DB handle. Safe to call multiple times.
func (l *Log) Close() error {
	if l == nil || l.db == nil {
		return nil
	}
	err := l.db.Close()
	l.db = nil
	return err
}

// headRow returns the current chain head (id, hash, sequence) from the
// single-row event_chain_head table. Uses the provided Querier (either
// *sql.DB or *sql.Tx) so it can run inside or outside a transaction.
func (l *Log) headRow(ctx context.Context, q querier) (id, hash string, seq uint64, err error) {
	row := q.QueryRowContext(ctx, `SELECT head_id, head_hash, head_seq FROM event_chain_head WHERE id = 1`)
	if err = row.Scan(&id, &hash, &seq); err != nil {
		return "", "", 0, fmt.Errorf("eventlog: read head: %w", err)
	}
	return id, hash, seq, nil
}

// querier is the subset of *sql.DB / *sql.Tx used by headRow.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Append writes ev to the log, computing its hash chain entry, ULID, and
// sequence atomically. Populates ev.ID, ev.Sequence, and ev.Timestamp in
// place on success. bus.Event has no ParentHash field; the parent hash is
// stored in the events row and is recoverable via Verify.
func (l *Log) Append(ev *bus.Event) error {
	if ev == nil {
		return fmt.Errorf("eventlog: nil event")
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		err := l.appendOnce(ev)
		if err == nil {
			return nil
		}
		if !isBusy(err) {
			return err
		}
		lastErr = err
		backoff := time.Duration(10<<attempt) * time.Millisecond
		time.Sleep(backoff)
	}
	return fmt.Errorf("eventlog: busy after 3 retries: %w", lastErr)
}

// appendOnce performs a single Append attempt inside a transaction. Returns
// a SQLITE_BUSY-flagged error when the caller should retry.
func (l *Log) appendOnce(ev *bus.Event) error {
	ctx := context.Background()
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("eventlog: begin: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	prevID, prevHash, prevSeq, err := l.headRow(ctx, tx)
	if err != nil {
		return err
	}

	// Assign identity.
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	} else {
		ev.Timestamp = ev.Timestamp.UTC()
	}
	if ev.ID == "" {
		ev.ID = l.mintULID(ev.Timestamp)
	}
	ev.Sequence = prevSeq + 1

	// Canonical payload: the spec calls for canonical JSON of the payload
	// struct. bus.Event.Payload is already json.RawMessage, so we
	// canonicalize it (re-parse + sort keys). Empty payload stays empty.
	var payloadBytes []byte
	if len(ev.Payload) > 0 {
		payloadBytes, err = Marshal(ev.Payload)
		if err != nil {
			return fmt.Errorf("eventlog: canonical payload: %w", err)
		}
	}

	// Hash chain: sha256(prev_id || payload || prev_hash). prev values
	// are empty strings at the root.
	sum := sha256.New()
	sum.Write([]byte(prevID))
	sum.Write(payloadBytes)
	sum.Write([]byte(prevHash))
	parentHash := hex.EncodeToString(sum.Sum(nil))

	// sessionID column: bus.Event has no dedicated SessionID field.
	// resume_cmd.go treats LoopID as the session identifier, so we mirror
	// that here. If callers need distinct per-scope lookups they can query
	// loop_id/mission_id/task_id directly.
	sessionID := ev.Scope.LoopID

	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (
			id, sequence, type, session_id, mission_id, task_id, loop_id,
			timestamp, emitter_id, payload, parent_hash, causal_ref
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		ev.ID,
		ev.Sequence,
		string(ev.Type),
		nullable(sessionID),
		nullable(ev.Scope.MissionID),
		nullable(ev.Scope.TaskID),
		nullable(ev.Scope.LoopID),
		ev.Timestamp.Format(time.RFC3339Nano),
		nullable(ev.EmitterID),
		payloadBytes,
		parentHash,
		nullable(ev.CausalRef),
	)
	if err != nil {
		return fmt.Errorf("eventlog: insert: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE event_chain_head
		   SET head_id = ?, head_hash = ?, head_seq = ?
		 WHERE id = 1
	`, ev.ID, parentHash, ev.Sequence)
	if err != nil {
		return fmt.Errorf("eventlog: update head: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("eventlog: commit: %w", err)
	}
	tx = nil
	return nil
}

// nullable returns a driver.Value that stores NULL when s is empty, so the
// session_id / mission_id / task_id / loop_id columns contain NULL instead
// of the empty string. This keeps the indexes compact and lets NULL checks
// in ReplaySession do the right thing.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// mintULID returns a new ULID string with a timestamp derived from ev.
// Monotonic entropy ensures that two ULIDs minted in the same millisecond
// remain strictly increasing.
func (l *Log) mintULID(ts time.Time) string {
	l.ulidMu.Lock()
	defer l.ulidMu.Unlock()
	id, err := ulid.New(ulid.Timestamp(ts), l.ulidEntropy)
	if err != nil {
		// Monotonic entropy can overflow within a single ms; fall back to
		// fresh entropy for this call. Fresh entropy means we lose strict
		// monotonicity only if the caller minted >2^80 IDs in a ms, which
		// we won't.
		id = ulid.MustNew(ulid.Timestamp(ts), newEntropySource())
	}
	return id.String()
}

// newEntropySource returns a reader suitable for ulid.Monotonic. It uses
// crypto/rand so the entropy is high-quality even under test load.
func newEntropySource() *entropyReader {
	return &entropyReader{}
}

// entropyReader is an io.Reader that fills its input with crypto-random
// bytes. Stateless; safe for concurrent use.
type entropyReader struct{}

// Read implements io.Reader.
func (r *entropyReader) Read(p []byte) (int, error) {
	return rand.Read(p)
}

// isBusy returns true if err indicates SQLITE_BUSY or SQLITE_LOCKED. We use
// string matching because the mattn/go-sqlite3 error type isn't easily
// type-asserted from outside the driver without pulling the driver-specific
// error constant — and the strings are stable across driver versions.
func isBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked") ||
		strings.Contains(msg, "sqlite_busy") ||
		strings.Contains(msg, "busy")
}

// ReadFrom yields every event with sequence >= from, in ascending sequence
// order. Cancellable via ctx (the iterator terminates on ctx.Done()).
// Internally batches LIMIT 1000 rows at a time to bound memory.
func (l *Log) ReadFrom(ctx context.Context, from uint64) iter.Seq2[bus.Event, error] {
	return func(yield func(bus.Event, error) bool) {
		cur := from
		for {
			if err := ctx.Err(); err != nil {
				yield(bus.Event{}, err)
				return
			}
			lastSeq, hadRows, stop, err := l.readFromPage(ctx, cur, yield)
			if err != nil {
				yield(bus.Event{}, err)
				return
			}
			if stop || !hadRows {
				return
			}
			cur = lastSeq + 1
		}
	}
}

// readFromPage fetches one LIMIT-1000 page starting at cur, yielding
// each event. Returns (lastSeq, hadRows, stop, err) where stop=true means
// the consumer returned false from yield (abort iteration).
// Extracted so rows.Close() can live on a defer rather than needing
// explicit close in every error branch (triggers sqlclosecheck).
func (l *Log) readFromPage(ctx context.Context, cur uint64, yield func(bus.Event, error) bool) (lastSeq uint64, hadRows bool, stop bool, err error) {
	rows, err := l.db.QueryContext(ctx, `
		SELECT id, sequence, type, mission_id, task_id, loop_id,
		       timestamp, emitter_id, payload, causal_ref
		  FROM events
		 WHERE sequence >= ?
		 ORDER BY sequence ASC
		 LIMIT 1000
	`, cur)
	if err != nil {
		return 0, false, false, fmt.Errorf("eventlog: query: %w", err)
	}
	defer rows.Close()
	lastSeq = cur
	for rows.Next() {
		hadRows = true
		ev, scanErr := scanEvent(rows)
		if scanErr != nil {
			return 0, false, false, scanErr
		}
		lastSeq = ev.Sequence
		if !yield(ev, nil) {
			return lastSeq, hadRows, true, nil
		}
	}
	if rerr := rows.Err(); rerr != nil {
		return 0, false, false, fmt.Errorf("eventlog: iterate: %w", rerr)
	}
	return lastSeq, hadRows, false, nil
}

// ReplaySession yields every event whose session_id, mission_id, task_id,
// or loop_id matches sessionID, in ascending sequence order. Matches the
// heuristic in cmd/stoke/resume_cmd.go's eventMatchesSession.
func (l *Log) ReplaySession(ctx context.Context, sessionID string) iter.Seq2[bus.Event, error] {
	return func(yield func(bus.Event, error) bool) {
		if sessionID == "" {
			return
		}
		lastSeq := uint64(0)
		firstBatch := true
		for {
			if err := ctx.Err(); err != nil {
				yield(bus.Event{}, err)
				return
			}
			var (
				rows *sql.Rows
				err  error
			)
			if firstBatch {
				rows, err = l.db.QueryContext(ctx, `
					SELECT id, sequence, type, mission_id, task_id, loop_id,
					       timestamp, emitter_id, payload, causal_ref
					  FROM events
					 WHERE (session_id = ? OR mission_id = ? OR loop_id = ? OR task_id = ?)
					 ORDER BY sequence ASC
					 LIMIT 1000
				`, sessionID, sessionID, sessionID, sessionID)
			} else {
				rows, err = l.db.QueryContext(ctx, `
					SELECT id, sequence, type, mission_id, task_id, loop_id,
					       timestamp, emitter_id, payload, causal_ref
					  FROM events
					 WHERE sequence > ?
					   AND (session_id = ? OR mission_id = ? OR loop_id = ? OR task_id = ?)
					 ORDER BY sequence ASC
					 LIMIT 1000
				`, lastSeq, sessionID, sessionID, sessionID, sessionID)
			}
			if err != nil {
				yield(bus.Event{}, fmt.Errorf("eventlog: query session: %w", err))
				return
			}
			hadRows := false
			for rows.Next() {
				hadRows = true
				ev, err := scanEvent(rows)
				if err != nil {
					rows.Close()
					yield(bus.Event{}, err)
					return
				}
				lastSeq = ev.Sequence
				if !yield(ev, nil) {
					rows.Close()
					return
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				yield(bus.Event{}, fmt.Errorf("eventlog: iterate session: %w", err))
				return
			}
			rows.Close()
			firstBatch = false
			if !hadRows {
				return
			}
		}
	}
}

// scanEvent reads one row from the SELECT used by ReadFrom/ReplaySession
// and returns a bus.Event. Never returns the stored parent_hash (bus.Event
// has no field for it).
func scanEvent(rows *sql.Rows) (bus.Event, error) {
	var (
		ev        bus.Event
		typeStr   string
		mission   sql.NullString
		task      sql.NullString
		loop      sql.NullString
		tsStr     string
		emitter   sql.NullString
		payload   []byte
		causalRef sql.NullString
	)
	if err := rows.Scan(
		&ev.ID, &ev.Sequence, &typeStr,
		&mission, &task, &loop,
		&tsStr, &emitter, &payload, &causalRef,
	); err != nil {
		return bus.Event{}, fmt.Errorf("eventlog: scan: %w", err)
	}
	ev.Type = bus.EventType(typeStr)
	ev.Scope.MissionID = mission.String
	ev.Scope.TaskID = task.String
	ev.Scope.LoopID = loop.String
	if emitter.Valid {
		ev.EmitterID = emitter.String
	}
	if causalRef.Valid {
		ev.CausalRef = causalRef.String
	}
	if len(payload) > 0 {
		// Copy into a json.RawMessage so callers can re-marshal without
		// worrying about aliasing the driver's buffer.
		cp := make([]byte, len(payload))
		copy(cp, payload)
		ev.Payload = cp
	}
	t, err := time.Parse(time.RFC3339Nano, tsStr)
	if err != nil {
		return bus.Event{}, fmt.Errorf("eventlog: parse timestamp %q: %w", tsStr, err)
	}
	ev.Timestamp = t
	return ev, nil
}

// Verify walks the chain in ascending sequence order and recomputes each
// row's parent_hash. Returns *ChainBrokenError on the first mismatch, nil on
// a clean log.
func (l *Log) Verify(ctx context.Context) error {
	rows, err := l.db.QueryContext(ctx, `
		SELECT sequence, id, payload, parent_hash
		  FROM events
		 ORDER BY sequence ASC
	`)
	if err != nil {
		return fmt.Errorf("eventlog: verify query: %w", err)
	}
	defer rows.Close()

	var prevID, prevHash string
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var (
			seq     uint64
			rowID   string
			payload []byte
			pHash   string
		)
		if err := rows.Scan(&seq, &rowID, &payload, &pHash); err != nil {
			return fmt.Errorf("eventlog: verify scan: %w", err)
		}
		sum := sha256.New()
		sum.Write([]byte(prevID))
		sum.Write(payload)
		sum.Write([]byte(prevHash))
		expected := hex.EncodeToString(sum.Sum(nil))
		if expected != pHash {
			return &ChainBrokenError{Sequence: seq, Expected: expected, Got: pHash}
		}
		prevID = rowID
		prevHash = pHash
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("eventlog: verify iterate: %w", err)
	}
	return nil
}

// ErrClosed is returned when the caller uses a Log after Close.
var ErrClosed = errors.New("eventlog: closed")

// SessionIDs is the de-duplicated set of scope identifiers observed in
// the event log, grouped by the column they were read from. Each slice
// is sorted ascending and contains only non-empty values. Empty slices
// are returned when no events carry the corresponding column.
type SessionIDs struct {
	Sessions []string `json:"sessions"`
	Missions []string `json:"missions"`
	Loops    []string `json:"loops"`
}

// ListSessions returns every distinct non-empty session_id, mission_id,
// and loop_id across the events table. Each set is returned in a
// separate slice so callers can render them by kind. The query hits
// three indexed columns and streams results, so it is safe to call on
// large logs.
//
// This satisfies spec event-log-proper.md item 22 and powers the
// `stoke eventlog list-sessions` CLI verb.
func (l *Log) ListSessions(ctx context.Context) (SessionIDs, error) {
	if l == nil || l.db == nil {
		return SessionIDs{}, ErrClosed
	}
	out := SessionIDs{}
	cols := []struct {
		col string
		dst *[]string
	}{
		{"session_id", &out.Sessions},
		{"mission_id", &out.Missions},
		{"loop_id", &out.Loops},
	}
	for _, c := range cols {
		if err := l.listDistinctInto(ctx, c.col, c.dst); err != nil {
			return SessionIDs{}, err
		}
	}
	return out, nil
}

// listDistinctInto appends all distinct non-empty values of column
// `col` into *dst. Extracted so rows.Close() can live on a defer and
// satisfies sqlclosecheck.
func (l *Log) listDistinctInto(ctx context.Context, col string, dst *[]string) error {
	// #nosec G202 — column name is a constant literal from the
	// caller's struct, not user input.
	q := "SELECT DISTINCT " + col + " FROM events WHERE " + col + " IS NOT NULL AND " + col + " <> '' ORDER BY " + col + " ASC"
	rows, err := l.db.QueryContext(ctx, q)
	if err != nil {
		return fmt.Errorf("eventlog: list %s: %w", col, err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return fmt.Errorf("eventlog: scan %s: %w", col, err)
		}
		*dst = append(*dst, s)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("eventlog: iterate %s: %w", col, err)
	}
	return nil
}
