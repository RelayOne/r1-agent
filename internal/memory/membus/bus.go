// Package membus — bus.go
//
// MEMBUS-core: scoped live worker-to-worker memory bus.
//
// This is the core slice of specs/memory-bus.md. It provides:
//
//   - WriteMemory(ctx, scope, content)  → UPSERT via writer goroutine + event
//   - ReadMemories(ctx, scope)          → SQLite SELECT
//   - Remember / Recall                 → richer request-struct variants
//
// Writer-goroutine throughput pattern (spec §5, TASK 5 in work-stoke.md):
//
// SQLite WAL permits exactly one writer at a time. N goroutines each calling
// db.Exec directly collapse to a serialized single-threaded pipeline — every
// INSERT pays the WAL journal fsync independently and throughput flatlines
// below 1k/sec. This package converts that into a single writer goroutine
// draining a buffered channel: up to 256 queued writes (or a 5 ms tick, or
// a Close signal, whichever fires first) are committed in one BEGIN IMMEDIATE
// transaction, amortizing the fsync over the whole batch. On commodity SSD
// this sustains ≥20k inserts/sec; see BenchmarkBus_WriterGoroutine_20kPerSec.
//
// Intentionally deferred to follow-up passes:
//
//   - Full tier hierarchy (worker/session-step/all-sessions/global/always
//     all share the same table here but only the primary scope filter is
//     applied; the multi-scope visibility matrix in spec §7 ships later).
//   - Retention policy wiring (expires_at is stored but not enforced yet).
//   - Cross-process read-only cursor, UDS wake signal, fsnotify fallback.
//   - Recall read_count increments routed through the writer (spec §5.4 step
//     3); the current slice leaves read_count at zero.
//
// Event emission: when an event bus is configured, Remember publishes a
// `memory.stored` event carrying {scope, scope_target, key, content_hash}.
// The raw content is never on the event to preserve the privacy contract in
// spec §9. If no event bus is wired, writes still persist — the event is a
// best-effort pub/sub signal, not a durability channel.
package membus

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
)

// Scope controls who sees a memory via Recall. Exactly one value per row.
// See spec §4 for the full semantics. This core slice stores the scope but
// does not yet enforce the full cross-scope visibility matrix on Recall.
type Scope string

const (
	// ScopeSession — visible to every worker in the same session_id.
	ScopeSession Scope = "session"

	// ScopeSessionStep — visible only to workers whose (session_id, step_id)
	// matches scope_target.
	ScopeSessionStep Scope = "session_step"

	// ScopeWorker — visible only to the worker whose TaskID matches
	// scope_target. Self-notes between turns.
	ScopeWorker Scope = "worker"

	// ScopeAllSessions — visible to every worker in every session of the
	// current R1 instance.
	ScopeAllSessions Scope = "all_sessions"

	// ScopeGlobal — visible across R1 instances on the same machine.
	ScopeGlobal Scope = "global"

	// ScopeAlways — every Recall returns this, regardless of filters.
	// OPERATOR-ONLY per spec; workers must not write to this scope.
	ScopeAlways Scope = "always"
)

// ValidScope reports whether s is one of the 6 known scopes.
func ValidScope(s Scope) bool {
	switch s {
	case ScopeSession, ScopeSessionStep, ScopeWorker,
		ScopeAllSessions, ScopeGlobal, ScopeAlways:
		return true
	}
	return false
}

// ErrScopeForbidden is returned when a worker attempts to write to
// ScopeAlways (which is operator/system only).
var ErrScopeForbidden = fmt.Errorf("memory: scope forbidden for author")

// MaxContentBytes caps the size of a single memory payload. Matches the
// 100 KB parity with V-115 in spec §6.
const MaxContentBytes = 100 * 1024

// EventTypeMemoryStored is the event type published on Remember/WriteMemory.
const EventTypeMemoryStored bus.EventType = "memory.stored"

// RememberRequest is the input to Bus.Remember. Fields beyond Scope+Content
// are optional and default sensibly; WriteMemory is a convenience wrapper
// that populates only the required fields.
type RememberRequest struct {
	Scope       Scope
	ScopeTarget string
	SessionID   string
	StepID      string
	TaskID      string
	Author      string // "worker:<id>" | "operator" | "system"
	Key         string // dedup discriminator; auto-hash of Content if empty
	Content     string
	Tags        []string
	Metadata    map[string]string
	ExpiresAt   *time.Time
}

// RecallRequest filters a Recall by scope and (optionally) dedup key.
// Full multi-scope visibility matching (§7) is deferred; this core slice
// filters by a single primary Scope.
type RecallRequest struct {
	Scope       Scope
	ScopeTarget string
	Key         string // optional exact-match filter
	Limit       int    // default 256
}

// Memory is one row returned by Recall.
type Memory struct {
	ID          int64
	CreatedAt   time.Time
	ExpiresAt   *time.Time
	Scope       Scope
	ScopeTarget string
	SessionID   string
	StepID      string
	TaskID      string
	Author      string
	Key         string
	Content     string
	ContentHash string
	Tags        []string
	Metadata    map[string]string
	ReadCount   int64
}

// writeRequest is one enqueued Remember flowing through the writer
// goroutine. Each request carries a private done channel that the writer
// goroutine signals (exactly once) with the commit outcome, so Remember
// stays synchronous from the caller's perspective: the caller blocks until
// its row has either committed or failed to commit.
type writeRequest struct {
	// req is the originating RememberRequest. The writer needs the full
	// shape to emit the post-commit `memory.stored` event and the
	// `memory_stored` ledger node with the same attribution the caller
	// supplied.
	req RememberRequest

	// Derived-once fields the writer uses inside the transaction. These
	// are computed on the caller's goroutine so the writer does no sha256
	// or JSON marshalling inside the hot BEGIN-COMMIT window.
	key          string
	contentHash  string
	tagsJSON     string
	metadataJSON string
	createdAt    time.Time
	expiresArg   any // nil or string (RFC3339Nano)

	// done is a 1-buffered channel the writer sends a single commit
	// outcome on. Buffered so the writer never blocks on a slow caller.
	done chan error
}

// Bus is the scoped memory bus. Zero value is not usable; call NewBus.
// A nil *Bus is safe: Remember / Recall / WriteMemory / ReadMemories
// short-circuit on a nil receiver so the rollout flag can pass `nil` to
// callers without nil-check boilerplate at every site.
type Bus struct {
	db       *sql.DB
	eventBus *bus.Bus      // optional; may be nil
	ledger   LedgerEmitter // optional; may be nil

	// writes is the ingress for the writer goroutine. Buffered at 2048
	// so bursty producer fan-out absorbs without blocking senders in the
	// common case; once full, Remember blocks (backpressure) until the
	// writer drains a slot.
	writes chan writeRequest

	// stop is closed by Close to signal writerLoop to flush and exit.
	stop chan struct{}

	// closeOnce guards Close so repeat calls are safe (defer b.Close()
	// in tests alongside a main-path explicit Close wouldn't double-close
	// the stop channel).
	closeOnce sync.Once

	// wg waits for writerLoop to fully drain and return.
	wg sync.WaitGroup
}

// LedgerEmitter is the narrow shape the memory bus uses to publish
// provenance nodes (memory_stored / memory_recalled) to a ledger. It
// intentionally does not import internal/ledger/nodes — callers marshal a
// pre-built NodeTyper payload to JSON and pass it in via content. Any
// implementation that satisfies EmitNode (the concrete *ledger.Ledger does,
// through an adapter) can be wired in. Nil Ledger short-circuits silently
// so the public surface stays backwards compatible.
type LedgerEmitter interface {
	// EmitNode persists a node with the given type and schema version plus
	// a JSON-marshalable content struct. Returning an error does NOT fail
	// the memory write — the call site logs-and-swallows, because the
	// memory table is the system of record for the payload.
	EmitNode(ctx context.Context, nodeType string, schemaVersion int, createdBy string, content any) error
}

// Options configures a new Bus.
type Options struct {
	// EventBus, when non-nil, receives a `memory.stored` event after each
	// successful Remember / WriteMemory. Publish failures are logged but do
	// not fail the underlying write — the bus is a best-effort pub/sub.
	EventBus *bus.Bus

	// Ledger, when non-nil, receives a memory_stored / memory_recalled
	// node on every successful Remember / Recall. Failures are swallowed
	// silently: the memory bus row is the durable record.
	Ledger LedgerEmitter
}

// NewBus runs the idempotent bus migration and PRAGMA baseline on db,
// starts the writer goroutine, and returns a ready-to-use *Bus. Safe to
// call on a db that other packages have already opened (the table +
// indices are namespaced to stoke_memory_bus; PRAGMA apply is idempotent).
//
// The writer goroutine keeps running until Close is called; callers that
// drop the *Bus handle without Close will leak the goroutine. Because the
// underlying *sql.DB is typically shared with other packages (wisdom,
// event log) NewBus does NOT take ownership of the handle — its Close
// only stops the writer, it does not close the db.
func NewBus(db *sql.DB, opts Options) (*Bus, error) {
	if db == nil {
		return nil, fmt.Errorf("memory: NewBus: nil db")
	}
	if err := applyBusPragmas(db); err != nil {
		return nil, err
	}
	if err := migrateBus(db); err != nil {
		return nil, err
	}
	b := &Bus{
		db:       db,
		eventBus: opts.EventBus,
		ledger:   opts.Ledger,
		// 2048-deep channel per task brief. Sized to absorb the tail of a
		// fan-out burst without blocking producers while the writer
		// flushes a 256-row batch.
		writes: make(chan writeRequest, 2048),
		stop:   make(chan struct{}),
	}
	b.wg.Add(1)
	go b.writerLoop()
	return b, nil
}

// Close signals the writer goroutine to flush any in-flight batch and
// exit, then waits for it. Idempotent — safe to call more than once. The
// underlying *sql.DB is NOT closed here because the handle is typically
// shared with other packages. Callers that own the db are responsible for
// closing it after Close returns.
func (b *Bus) Close() error {
	if b == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		close(b.stop)
		b.wg.Wait()
	})
	return nil
}

// DB returns the underlying *sql.DB handle so sibling packages (notably
// internal/retention) can run maintenance SQL — TTL sweeps and session-end
// wipes — directly against the stoke_memory_bus table without having to
// thread a second handle through every caller. Returns nil for a nil *Bus
// so the accessor remains safe under the nil-bus short-circuits above.
//
// This is a deliberate, minimal escape hatch. Callers that need to issue
// INSERTs should continue to go through Remember/WriteMemory so the
// memory.stored event fires; DB() is for housekeeping DELETEs only.
func (b *Bus) DB() *sql.DB {
	if b == nil {
		return nil
	}
	return b.db
}

// ---------------------------------------------------------------------------
// Public API — primary surface
// ---------------------------------------------------------------------------

// WriteMemory is the convenience wrapper requested by the task brief:
// "WriteMemory(scope, content)". It UPSERTs a row scoped to the given scope
// with an auto-derived key (SHA-256 prefix of the content). Callers that
// need to control the dedup key, session/task attribution, tags, metadata,
// or expires_at should call Remember directly.
func (b *Bus) WriteMemory(ctx context.Context, scope Scope, content string) error {
	if b == nil {
		return nil
	}
	return b.Remember(ctx, RememberRequest{
		Scope:   scope,
		Content: content,
		Author:  "system",
	})
}

// ReadMemories is the convenience wrapper requested by the task brief:
// "ReadMemories(scope)". It returns up to 256 non-expired rows matching the
// given scope, sorted oldest-first.
func (b *Bus) ReadMemories(ctx context.Context, scope Scope) ([]Memory, error) {
	if b == nil {
		return nil, nil
	}
	return b.Recall(ctx, RecallRequest{Scope: scope})
}

// Remember writes a memory row with full attribution. UPSERTs on conflict
// against (scope, scope_target, key) per spec §3. The call is synchronous
// from the caller's perspective — it returns only after the writer
// goroutine has committed (or failed to commit) the batch containing this
// request.
//
// On successful commit, publishes a `memory.stored` event to the configured
// event bus (if any) and emits a `memory_stored` ledger node (if a Ledger
// is configured). Both are best-effort: failures on those paths do not
// bubble back to the caller — the SQLite row is the durable record.
func (b *Bus) Remember(ctx context.Context, req RememberRequest) error {
	if b == nil {
		return nil
	}
	if err := validateRemember(req); err != nil {
		return err
	}
	now := time.Now().UTC()
	contentHash := computeContentHash(req.Content)
	key := req.Key
	if key == "" {
		// Auto-derive the dedup key from the content hash. This means two
		// identical-content writes in the same (scope, scope_target)
		// collapse to a single row, which is the right default for the
		// naive WriteMemory caller. Callers that want N distinct rows pass
		// an explicit Key.
		key = contentHash[:16]
	}

	tagsJSON, err := marshalTags(req.Tags)
	if err != nil {
		return err
	}
	metadataJSON, err := marshalMetadata(req.Metadata)
	if err != nil {
		return err
	}
	var expiresArg any
	if req.ExpiresAt != nil {
		expiresArg = req.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}

	// Buffered so the writer can signal completion even if the caller has
	// already advanced (pathological: a ctx.Done after enqueue). Also lets
	// the writer fan out the outcome across a whole batch without
	// head-of-line blocking.
	w := writeRequest{
		req:          req,
		key:          key,
		contentHash:  contentHash,
		tagsJSON:     tagsJSON,
		metadataJSON: metadataJSON,
		createdAt:    now,
		expiresArg:   expiresArg,
		done:         make(chan error, 1),
	}

	// Enqueue. Backpressure on a saturated channel: block until the
	// writer drains a slot or the ctx/stop fires. This preserves the
	// "my write must commit before I return" contract.
	select {
	case b.writes <- w:
	case <-ctx.Done():
		return ctx.Err()
	case <-b.stop:
		return fmt.Errorf("memory: bus closed")
	}

	// Wait for the writer's commit outcome for this specific row. The
	// writer signals every request in the batch after the tx commits.
	select {
	case err := <-w.done:
		if err != nil {
			return fmt.Errorf("memory: upsert: %w", err)
		}
		// Best-effort pub/sub and ledger emission, driven on the caller
		// goroutine (after commit) so the writer's hot path stays tight.
		// Failures are observable via subscriber logs but do not fail the
		// already-committed write.
		b.publishStored(req, key, contentHash)
		b.emitMemoryStored(ctx, req, key, contentHash, now)
		return nil
	case <-ctx.Done():
		// The row may still commit (we can't un-enqueue), but the
		// caller's ctx has fired; report that.
		return ctx.Err()
	}
}

// writerLoop drains b.writes, batching up to 256 rows or 5 ms worth of
// incoming traffic (whichever comes first) into a single BEGIN IMMEDIATE
// transaction. Exits after flushing any in-flight batch when b.stop is
// closed.
//
// The loop is structured to maximise batch fill without padding latency:
// once it accepts the first write it performs a bounded non-blocking
// drain of any additional writes already waiting in the channel, then
// flushes immediately. The 5 ms ticker is a safety net for the single-
// producer "one write every few seconds" edge case — it is not the
// normal flush trigger. With producers blocked on done-channels (the
// common pattern: Remember waits for commit), the drain step scoops up
// every sibling producer whose request landed while the writer was in
// the previous transaction, keeping the batch size high under fan-out
// without waiting on the ticker.
func (b *Bus) writerLoop() {
	defer b.wg.Done()

	const batchCap = 256
	batch := make([]writeRequest, 0, batchCap)

	// 5 ms tick is a safety net for sparse writers that never fill the
	// channel. Under fan-out the non-blocking drain below typically
	// flushes long before any tick fires.
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	// drainAvailable scoops every write currently sitting in the channel
	// (up to batchCap) without blocking. Called immediately after the
	// first write in a batch lands so a fan-out burst commits as one tx
	// rather than one-row-per-batch-commit.
	drainAvailable := func() {
		for len(batch) < batchCap {
			select {
			case w := <-b.writes:
				batch = append(batch, w)
			default:
				return
			}
		}
	}

	for {
		select {
		case w := <-b.writes:
			batch = append(batch, w)
			drainAvailable()
			b.flushBatch(batch)
			batch = batch[:0]
		case <-ticker.C:
			if len(batch) > 0 {
				b.flushBatch(batch)
				batch = batch[:0]
			}
		case <-b.stop:
			// Drain any still-queued requests before flushing the final
			// batch. Producers that have already sent into b.writes have
			// the right to see their commit outcome — the "my write
			// committed" contract spans the stop edge.
			for {
				drainAvailable()
				if len(batch) == 0 {
					return
				}
				b.flushBatch(batch)
				batch = batch[:0]
			}
		}
	}
}

// flushBatch commits a slice of writeRequests in a single transaction and
// signals each request's done channel with the per-row outcome. The DSN
// used to open the underlying *sql.DB should carry `_txlock=immediate` so
// BeginTx returns with a RESERVED lock already held; without it the
// transaction starts DEFERRED and upgrades on the first write, which can
// race a concurrent reader and surface `database is locked`.
//
// On transaction error (Begin / Exec / Commit) every request in the batch
// gets the same error on its done channel. That mirrors the all-or-nothing
// semantics of the underlying BEGIN IMMEDIATE commit.
func (b *Bus) flushBatch(reqs []writeRequest) {
	if len(reqs) == 0 {
		return
	}
	ctx := context.Background()
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		for _, r := range reqs {
			r.done <- fmt.Errorf("begin tx: %w", err)
		}
		return
	}
	const upsertSQL = `
INSERT INTO stoke_memory_bus (
    created_at, expires_at, scope, scope_target,
    session_id, step_id, task_id, author, key,
    content, content_hash, tags, metadata
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (scope, scope_target, key) DO UPDATE SET
    content      = excluded.content,
    content_hash = excluded.content_hash,
    tags         = excluded.tags,
    metadata     = excluded.metadata,
    expires_at   = excluded.expires_at
`
	var execErr error
	for _, r := range reqs {
		if _, err := tx.ExecContext(ctx, upsertSQL,
			r.createdAt.Format(time.RFC3339Nano),
			r.expiresArg,
			string(r.req.Scope),
			r.req.ScopeTarget,
			r.req.SessionID,
			r.req.StepID,
			r.req.TaskID,
			r.req.Author,
			r.key,
			r.req.Content,
			r.contentHash,
			r.tagsJSON,
			r.metadataJSON,
		); err != nil {
			execErr = err
			break
		}
	}
	if execErr != nil {
		_ = tx.Rollback()
		for _, r := range reqs {
			r.done <- execErr
		}
		return
	}
	if err := tx.Commit(); err != nil {
		for _, r := range reqs {
			r.done <- fmt.Errorf("commit tx: %w", err)
		}
		return
	}
	for _, r := range reqs {
		r.done <- nil
	}
}

// Recall returns rows matching the primary scope filter. Expired rows are
// filtered out server-side via the expires_at column. Sorted oldest-first
// (id ASC) so callers paging forward see a stable, monotonic cursor.
//
// Full multi-scope visibility (spec §7) is intentionally NOT implemented
// here — this core slice applies exact-scope-match only. The richer matcher
// can be layered on via RecallRequest fields without breaking callers.
func (b *Bus) Recall(ctx context.Context, req RecallRequest) ([]Memory, error) {
	if b == nil {
		return nil, nil
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 256
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	var (
		where strings.Builder
		args  []any
	)
	where.WriteString(`(expires_at IS NULL OR expires_at > ?)`)
	args = append(args, now)

	if req.Scope != "" {
		where.WriteString(` AND scope = ?`)
		args = append(args, string(req.Scope))
	}
	if req.ScopeTarget != "" {
		where.WriteString(` AND scope_target = ?`)
		args = append(args, req.ScopeTarget)
	}
	if req.Key != "" {
		where.WriteString(` AND key = ?`)
		args = append(args, req.Key)
	}

	query := `
SELECT id, created_at, expires_at, scope, scope_target,
       session_id, step_id, task_id, author, key,
       content, content_hash, tags, metadata, read_count
  FROM stoke_memory_bus
 WHERE ` + where.String() + `
 ORDER BY id ASC
 LIMIT ?`
	args = append(args, limit)

	rows, err := b.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("memory: recall query: %w", err)
	}
	defer rows.Close()

	var out []Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory: recall iterate: %w", err)
	}
	// Best-effort provenance emit. One memory_recalled node per returned
	// row so auditors can trace which exact hashes a worker read. Failures
	// are silently swallowed — the memory bus row itself is the system of
	// record.
	b.emitMemoryRecalled(ctx, req, out)
	return out, nil
}

// ---------------------------------------------------------------------------
// Internals
// ---------------------------------------------------------------------------

// validateRemember enforces the minimal invariants the core slice can
// cheaply check. The spec lists additional rules (e.g., ScopeSessionStep
// requires StepID); those ship with the full Recall visibility matrix.
func validateRemember(req RememberRequest) error {
	if !ValidScope(req.Scope) {
		return fmt.Errorf("memory: invalid scope %q", req.Scope)
	}
	if req.Content == "" {
		return fmt.Errorf("memory: empty content")
	}
	if len(req.Content) > MaxContentBytes {
		return fmt.Errorf("memory: content exceeds %d bytes", MaxContentBytes)
	}
	if req.Scope == ScopeAlways && strings.HasPrefix(req.Author, "worker:") {
		return ErrScopeForbidden
	}
	return nil
}

func computeContentHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func marshalTags(tags []string) (string, error) {
	if len(tags) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return "", fmt.Errorf("memory: marshal tags: %w", err)
	}
	return string(b), nil
}

func marshalMetadata(m map[string]string) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("memory: marshal metadata: %w", err)
	}
	return string(b), nil
}

func scanMemory(rows *sql.Rows) (Memory, error) {
	var (
		m             Memory
		scopeStr      string
		createdAtStr  string
		expiresAtNull sql.NullString
		tagsJSON      string
		metaJSON      string
	)
	if err := rows.Scan(
		&m.ID,
		&createdAtStr,
		&expiresAtNull,
		&scopeStr,
		&m.ScopeTarget,
		&m.SessionID,
		&m.StepID,
		&m.TaskID,
		&m.Author,
		&m.Key,
		&m.Content,
		&m.ContentHash,
		&tagsJSON,
		&metaJSON,
		&m.ReadCount,
	); err != nil {
		return Memory{}, fmt.Errorf("memory: scan: %w", err)
	}
	m.Scope = Scope(scopeStr)
	t, err := time.Parse(time.RFC3339Nano, createdAtStr)
	if err != nil {
		return Memory{}, fmt.Errorf("memory: parse created_at %q: %w", createdAtStr, err)
	}
	m.CreatedAt = t
	if expiresAtNull.Valid && expiresAtNull.String != "" {
		et, err := time.Parse(time.RFC3339Nano, expiresAtNull.String)
		if err != nil {
			return Memory{}, fmt.Errorf("memory: parse expires_at %q: %w", expiresAtNull.String, err)
		}
		m.ExpiresAt = &et
	}
	if tagsJSON != "" {
		if err := json.Unmarshal([]byte(tagsJSON), &m.Tags); err != nil {
			return Memory{}, fmt.Errorf("memory: unmarshal tags: %w", err)
		}
	}
	if metaJSON != "" {
		if err := json.Unmarshal([]byte(metaJSON), &m.Metadata); err != nil {
			return Memory{}, fmt.Errorf("memory: unmarshal metadata: %w", err)
		}
	}
	return m, nil
}

// memoryStoredPayload is the JSON payload attached to memory.stored events.
// Raw content is intentionally absent — the content_hash is the ledger-safe
// reference and preserves the privacy contract from spec §9.
type memoryStoredPayload struct {
	Scope       string `json:"scope"`
	ScopeTarget string `json:"scope_target,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	StepID      string `json:"step_id,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	Author      string `json:"author,omitempty"`
	Key         string `json:"key"`
	ContentHash string `json:"content_hash"`
}

// publishStored emits a memory.stored event. Best-effort: logs the error to
// the caller via a wrapped return when the Publish itself fails, but never
// fails the write path (the memory is already durable in SQLite).
func (b *Bus) publishStored(req RememberRequest, key, contentHash string) {
	if b.eventBus == nil {
		return
	}
	payload := memoryStoredPayload{
		Scope:       string(req.Scope),
		ScopeTarget: req.ScopeTarget,
		SessionID:   req.SessionID,
		StepID:      req.StepID,
		TaskID:      req.TaskID,
		Author:      req.Author,
		Key:         key,
		ContentHash: contentHash,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	evt := bus.Event{
		Type:      EventTypeMemoryStored,
		EmitterID: "memory.bus",
		Scope: bus.Scope{
			LoopID: req.SessionID,
			TaskID: req.TaskID,
		},
		Payload: raw,
	}
	_ = b.eventBus.Publish(evt)
}

// memoryStoredNode mirrors nodes.MemoryStored verbatim — we duplicate the
// struct here (with identical JSON tags) so the membus package stays free of
// a dependency on internal/ledger/nodes. The ledger-side register uses the
// same shape, so JSON payloads round-trip cleanly.
type memoryStoredNode struct {
	Scope       string    `json:"scope"`
	ScopeTarget string    `json:"scope_target,omitempty"`
	Key         string    `json:"key"`
	ContentHash string    `json:"content_hash"`
	MemoryType  string    `json:"memory_type,omitempty"`
	WrittenBy   string    `json:"written_by"`
	CreatedAt   time.Time `json:"created_at"`
	Version     int       `json:"schema_version"`
}

type memoryRecalledNode struct {
	Scope       string    `json:"scope"`
	Key         string    `json:"key"`
	ContentHash string    `json:"content_hash"`
	RecalledBy  string    `json:"recalled_by"`
	CreatedAt   time.Time `json:"created_at"`
	Version     int       `json:"schema_version"`
}

// emitMemoryStored writes a memory_stored provenance node for a successful
// Remember. Nil-ledger, marshal-failure, and EmitNode errors are all
// swallowed: the SQLite row is durable and the ledger is best-effort.
func (b *Bus) emitMemoryStored(ctx context.Context, req RememberRequest, key, contentHash string, createdAt time.Time) {
	if b.ledger == nil {
		return
	}
	memoryType := ""
	if req.Metadata != nil {
		memoryType = req.Metadata["memory_type"]
	}
	node := memoryStoredNode{
		Scope:       string(req.Scope),
		ScopeTarget: req.ScopeTarget,
		Key:         key,
		ContentHash: contentHash,
		MemoryType:  memoryType,
		WrittenBy:   req.Author,
		CreatedAt:   createdAt,
		Version:     1,
	}
	_ = b.ledger.EmitNode(ctx, "memory_stored", 1, req.Author, node)
}

// emitMemoryRecalled writes one memory_recalled provenance node per row
// returned by a Recall. Silent on every failure path (see emitMemoryStored).
func (b *Bus) emitMemoryRecalled(ctx context.Context, req RecallRequest, rows []Memory) {
	if b.ledger == nil || len(rows) == 0 {
		return
	}
	now := time.Now().UTC()
	for _, m := range rows {
		scope := string(req.Scope)
		if scope == "" {
			scope = string(m.Scope)
		}
		key := req.Key
		if key == "" {
			key = m.Key
		}
		node := memoryRecalledNode{
			Scope:       scope,
			Key:         key,
			ContentHash: m.ContentHash,
			RecalledBy:  m.Author,
			CreatedAt:   now,
			Version:     1,
		}
		_ = b.ledger.EmitNode(ctx, "memory_recalled", 1, m.Author, node)
	}
}
