// export_delta.go — CS-4 session-end memory-delta support.
//
// ExportDelta returns all memories created since the supplied cutoff,
// in ascending creation order. CloudSwarm calls this at session end so
// the supervisor can persist the newly-written rows back to its
// per-task memory store for the next session.

package membus

import (
	"context"
	"fmt"
	"time"
)

// ExportDelta returns every row in stoke_memory_bus whose created_at
// is strictly greater than since, in ascending creation order. Rows
// are copied whole (no filtering) — ScopeAlways, ScopeSession,
// ScopeWorker, everything. Callers that only want a subset filter the
// returned slice in-process.
//
// Returns (nil, nil) when the bus DB is closed, so CloudSwarm's
// supervisor doesn't have to branch on nil errors separately from
// empty-delta results.
func (b *Bus) ExportDelta(ctx context.Context, since time.Time) ([]Memory, error) {
	if b == nil || b.db == nil {
		return nil, nil
	}
	// Use RFC3339Nano so the WHERE clause matches the shape persisted
	// by the writer goroutine (see flushBatch — created_at is stored
	// via ts.Format(time.RFC3339Nano)).
	cutoff := since.UTC().Format(time.RFC3339Nano)
	const q = `
		SELECT id, created_at, expires_at, scope, scope_target,
		       session_id, step_id, task_id, author, key, content,
		       content_hash, tags, metadata, read_count
		FROM stoke_memory_bus
		WHERE created_at > ?
		ORDER BY id ASC`
	rows, err := b.db.QueryContext(ctx, q, cutoff)
	if err != nil {
		return nil, fmt.Errorf("membus.ExportDelta: query: %w", err)
	}
	defer rows.Close()

	var out []Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("membus.ExportDelta: scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("membus.ExportDelta: iterate: %w", err)
	}
	return out, nil
}

// ExportDeltaSince is a convenience wrapper for the common case where
// the caller wants everything written after the bus was opened. Uses
// a background context so it never blocks a session-end path on ctx
// cancellation. Returns an empty slice (not nil) when there's nothing
// to report so JSON-marshal produces "[]" rather than "null".
func (b *Bus) ExportDeltaSince(since time.Time) []Memory {
	ctx, cancel := emptyCtx()
	defer cancel()
	rows, err := b.ExportDelta(ctx, since)
	if err != nil || rows == nil {
		return []Memory{}
	}
	return rows
}

// emptyCtx returns a fresh background context + cancel — extracted so
// tests can swap it if they ever want to exercise cancellation.
var emptyCtx = func() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

